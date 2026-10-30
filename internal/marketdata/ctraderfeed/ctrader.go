package ctraderfeed

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"tradex/internal/broker/ctrader"
	"tradex/internal/feed"
	"tradex/internal/marketdata/manager"
	"tradex/internal/marketdata/manager/providers"
)

// Symbol maps a TradeX symbol to the account-specific cTrader symbol ID and
// price scale. The symbol ID and digits must be verified from the target
// broker/account before enabling the feed.
type Symbol struct {
	SymbolID int64                  `json:"symbol_id"`
	Symbol   string                 `json:"symbol"`
	Type     manager.InstrumentType `json:"type"`
	Digits   int                    `json:"digits"`
}

// Config configures the optional direct cTrader Open API feed. Client ID,
// client secret, and token contents are read from the environment or token
// file, never serialized in this configuration.
type Config struct {
	Endpoint    string        `json:"endpoint"`
	Environment string        `json:"environment"`
	AccountID   string        `json:"account_id"`
	TokenFile   string        `json:"token_file"`
	Symbols     []Symbol      `json:"symbols"`
	Interval    string        `json:"interval"`
	Timeout     time.Duration `json:"timeout"`
}

func (c Config) Validate() error {
	if c.Environment != "demo" && c.Environment != "live" {
		return errors.New("cTrader feed environment must be demo or live")
	}
	if c.AccountID == "" {
		return errors.New("cTrader feed account ID is required")
	}
	if c.TokenFile == "" {
		return errors.New("cTrader feed token file is required")
	}
	if len(c.Symbols) == 0 {
		return errors.New("cTrader feed requires at least one symbol")
	}
	if _, ok := timeframe(c.Interval); !ok {
		return fmt.Errorf("unsupported cTrader feed interval %q", c.Interval)
	}
	seen := make(map[string]struct{}, len(c.Symbols))
	for _, symbol := range c.Symbols {
		if err := (providers.CTraderSymbol{
			SymbolID: symbol.SymbolID,
			Symbol:   symbol.Symbol,
			Type:     symbol.Type,
			Digits:   symbol.Digits,
		}).Validate(); err != nil {
			return fmt.Errorf("validate cTrader feed symbol %q: %w", symbol.Symbol, err)
		}
		if _, exists := seen[symbol.Symbol]; exists {
			return fmt.Errorf("duplicate cTrader feed symbol %q", symbol.Symbol)
		}
		seen[symbol.Symbol] = struct{}{}
	}
	return nil
}

func (c Config) endpoint() string {
	if c.Endpoint != "" {
		return c.Endpoint
	}
	if c.Environment == "live" {
		return "live.ctraderapi.com:5036"
	}
	return "demo.ctraderapi.com:5036"
}

// Feed adapts the direct cTrader Open API client to the legacy candle feed
// used by the signal monitor. It only retrieves market data.
type Feed struct {
	provider  *providers.CTraderProvider
	transport interface{ Close() error }
	config    Config
	now       func() time.Time

	mu   sync.Mutex
	last map[string]time.Time
}

type persistingTokenRefresher struct {
	client *ctrader.OAuthClient
	path   string
}

func (r persistingTokenRefresher) Refresh(ctx context.Context, token ctrader.OAuthToken) (ctrader.OAuthToken, error) {
	refreshed, err := r.client.Refresh(ctx, token)
	if err != nil {
		return ctrader.OAuthToken{}, err
	}
	if err := ctrader.WriteOAuthTokenFile(r.path, refreshed); err != nil {
		return ctrader.OAuthToken{}, fmt.Errorf("persist refreshed cTrader OAuth token: %w", err)
	}
	return refreshed, nil
}

// New creates an authenticated, read-only cTrader feed. The cTrader OAuth
// token is loaded from TokenFile and refreshed through the existing OAuth
// client/token manager. Rotated refresh tokens are atomically persisted.
func New(config Config, clock func() time.Time) (*Feed, error) {
	if clock == nil {
		clock = time.Now
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	clientID := os.Getenv("CTRADER_CLIENT_ID")
	clientSecret := os.Getenv("CTRADER_CLIENT_SECRET")
	if clientID == "" || clientSecret == "" {
		return nil, errors.New("CTRADER_CLIENT_ID and CTRADER_CLIENT_SECRET are required for cTrader feed")
	}
	oauthConfig, err := ctrader.OAuthConfigFromEnv()
	if err != nil {
		return nil, fmt.Errorf("load cTrader OAuth configuration: %w", err)
	}
	token, err := ctrader.ReadOAuthTokenFile(config.TokenFile)
	if err != nil {
		return nil, err
	}
	oauthClient, err := ctrader.NewOAuthClient(oauthConfig, http.DefaultClient, clock)
	if err != nil {
		return nil, err
	}
	tokenManager, err := ctrader.NewTokenManager(token, persistingTokenRefresher{client: oauthClient, path: config.TokenFile}, clock, 2*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("create cTrader token manager: %w", err)
	}
	accessToken := func(ctx context.Context) (string, error) {
		current, err := tokenManager.Current(ctx)
		if err != nil {
			return "", err
		}
		return current.AccessToken, nil
	}
	transport, err := providers.NewCTraderJSONTCPDataClient(providers.CTraderJSONTCPConfig{
		Endpoint:       config.endpoint(),
		ClientID:       clientID,
		ClientSecret:   clientSecret,
		AccountID:      config.AccountID,
		AccessToken:    accessToken,
		RequestTimeout: config.Timeout,
	}, clock)
	if err != nil {
		return nil, fmt.Errorf("create cTrader JSON/TCP client: %w", err)
	}
	providerSymbols := make([]providers.CTraderSymbol, 0, len(config.Symbols))
	for _, symbol := range config.Symbols {
		providerSymbols = append(providerSymbols, providers.CTraderSymbol{
			SymbolID: symbol.SymbolID,
			Symbol:   symbol.Symbol,
			Type:     symbol.Type,
			Digits:   symbol.Digits,
		})
	}
	provider, err := providers.NewCTraderProvider(transport, providers.CTraderConfig{
		AccountID:   config.AccountID,
		Environment: config.Environment,
		Symbols:     providerSymbols,
	}, clock)
	if err != nil {
		_ = transport.Close()
		return nil, fmt.Errorf("create cTrader market-data provider: %w", err)
	}
	return &Feed{
		provider:  provider,
		transport: transport,
		config:    config,
		now:       clock,
		last:      make(map[string]time.Time),
	}, nil
}

// FetchAllSymbols returns only candles newer than the last successful fetch
// for each configured symbol.
func (f *Feed) FetchAllSymbols(ctx context.Context) ([]feed.Candle, error) {
	if f == nil || f.provider == nil {
		return nil, errors.New("cTrader feed is not initialized")
	}
	var all []feed.Candle
	var firstErr error
	for _, symbol := range f.config.Symbols {
		candles, err := f.fetchCandles(ctx, symbol)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		all = append(all, candles...)
	}
	if firstErr != nil && len(all) == 0 {
		return nil, firstErr
	}
	return all, nil
}

func (f *Feed) fetchCandles(ctx context.Context, symbol Symbol) ([]feed.Candle, error) {
	tf, ok := timeframe(f.config.Interval)
	if !ok {
		return nil, fmt.Errorf("unsupported cTrader feed interval %q", f.config.Interval)
	}
	to := f.now().UTC()
	from := to.Add(-tf.Duration() * 20)
	result, err := f.provider.GetCandles(ctx, manager.CandleRequest{
		Symbol: symbol.Symbol, Timeframe: tf, From: from, To: to, Limit: 20,
	})
	if err != nil {
		return nil, fmt.Errorf("fetch cTrader candles for %s: %w", symbol.Symbol, err)
	}
	key := symbol.Symbol + "\x00" + f.config.Interval
	f.mu.Lock()
	last := f.last[key]
	converted := make([]feed.Candle, 0, len(result))
	var latest time.Time
	for _, candle := range result {
		if !candle.OpenTime.After(last) {
			continue
		}
		converted = append(converted, feed.Candle{
			Symbol:   symbol.Symbol,
			Interval: f.config.Interval,
			Open:     candle.Open,
			High:     candle.High,
			Low:      candle.Low,
			Close:    candle.Close,
			Volume:   candle.Volume,
			Time:     candle.OpenTime,
		})
		if candle.OpenTime.After(latest) {
			latest = candle.OpenTime
		}
	}
	if !latest.IsZero() {
		f.last[key] = latest
	}
	f.mu.Unlock()
	return converted, nil
}

// Close shuts down the underlying read-only cTrader session.
func (f *Feed) Close() error {
	if f == nil || f.transport == nil {
		return nil
	}
	return f.transport.Close()
}

func timeframe(interval string) (manager.Timeframe, bool) {
	switch interval {
	case "1m", "1min":
		return manager.Timeframe1m, true
	case "5m", "5min":
		return manager.Timeframe5m, true
	case "15m", "15min":
		return manager.Timeframe15m, true
	case "30m", "30min":
		return manager.Timeframe30m, true
	case "1h":
		return manager.Timeframe1h, true
	case "4h":
		return manager.Timeframe4h, true
	case "1day":
		return manager.Timeframe1d, true
	case "1week":
		return manager.Timeframe1w, true
	default:
		return "", false
	}
}
