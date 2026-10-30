package feed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// FinnhubConfig configures the Finnhub REST client.
// Free tier: 60 API calls/minute.
type FinnhubConfig struct {
	APIKey   string        `json:"api_key"`
	BaseURL  string        `json:"base_url"`
	Symbols  []string      `json:"symbols"`
	Interval string        `json:"interval"` // "1min", "5min", "15min", "30min", "1h", "1day"
	Timeout  time.Duration `json:"timeout"`
	// ForexExchange prefixes forex symbols for the candle endpoint,
	// e.g. "EUR/USD" becomes "OANDA:EUR_USD". Empty disables the prefix.
	ForexExchange string `json:"forex_exchange"`
}

func (c FinnhubConfig) Validate() error {
	if c.APIKey == "" {
		return errors.New("Finnhub API key is required")
	}
	if len(c.Symbols) == 0 {
		return errors.New("at least one Finnhub symbol is required")
	}
	if !validFinnhubInterval(c.Interval) {
		return errors.New("invalid Finnhub interval: must be 1min, 5min, 15min, 30min, 1h, 1day")
	}
	return nil
}

func (c FinnhubConfig) baseURL() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return "https://finnhub.io/api/v1"
}

func (c FinnhubConfig) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 10 * time.Second
}

func (c FinnhubConfig) forexExchange() string {
	if c.ForexExchange != "" {
		return c.ForexExchange
	}
	return "OANDA"
}

func validFinnhubInterval(s string) bool {
	switch s {
	case "1min", "5min", "15min", "30min", "1h", "1day":
		return true
	}
	return false
}

// finnhubResolution maps a feed interval to Finnhub's resolution parameter.
func finnhubResolution(interval string) string {
	switch interval {
	case "1min":
		return "1"
	case "5min":
		return "5"
	case "15min":
		return "15"
	case "30min":
		return "30"
	case "1h":
		return "60"
	case "1day":
		return "D"
	}
	return ""
}

// finnhubSymbol converts a workspace symbol to Finnhub's format.
// Forex pairs use EXCHANGE:BASE_QUOTE (e.g. OANDA:EUR_USD);
// stocks/ETFs use the plain ticker.
func finnhubSymbol(symbol, forexExchange string) string {
	if i := strings.Index(symbol, "/"); i > 0 {
		return fmt.Sprintf("%s:%s_%s", forexExchange, symbol[:i], symbol[i+1:])
	}
	return symbol
}

// FinnhubClient fetches OHLC candles and quotes from Finnhub's REST API.
type FinnhubClient struct {
	config FinnhubConfig
	client *http.Client
	mu     sync.Mutex
	last   map[string]time.Time // last candle time per symbol to avoid duplicates
}

// NewFinnhubClient creates a new Finnhub client.
func NewFinnhubClient(cfg FinnhubConfig) (*FinnhubClient, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &FinnhubClient{
		config: cfg,
		client: &http.Client{Timeout: cfg.timeout()},
		last:   make(map[string]time.Time),
	}, nil
}

// finnhubCandleResponse is the JSON structure of /forex/candle and /stock/candle.
type finnhubCandleResponse struct {
	Status string    `json:"s"`     // "ok" or "no_data"
	T      []int64   `json:"t"`     // unix timestamps (seconds)
	O      []float64 `json:"o"`     // open
	H      []float64 `json:"h"`     // high
	L      []float64 `json:"l"`     // low
	C      []float64 `json:"c"`     // close
	V      []float64 `json:"v"`     // volume (may be absent for forex)
	Error  string    `json:"error"` // error message when status is not ok
}

// FetchCandles fetches recent candles for a single symbol.
// It returns only new candles since the last fetch (deduplication by time).
func (c *FinnhubClient) FetchCandles(ctx context.Context, symbol string) ([]Candle, error) {
	return c.fetchCandles(ctx, symbol, c.config.Interval)
}

// FetchCandlesAt fetches candles at the requested supported interval. The
// market-data manager can request several timeframes through one provider;
// using the client's configured interval here would silently return the wrong
// timeframe for every request that differs from the default.
func (c *FinnhubClient) FetchCandlesAt(ctx context.Context, symbol, interval string) ([]Candle, error) {
	return c.fetchCandles(ctx, symbol, interval)
}

func (c *FinnhubClient) fetchCandles(ctx context.Context, symbol, interval string) ([]Candle, error) {
	resolution := finnhubResolution(interval)
	if resolution == "" {
		return nil, fmt.Errorf("finnhub unsupported interval %q", interval)
	}

	// Request a window covering ~10 bars so gaps can be backfilled.
	tf := intervalToFinnhubWindow(interval)
	to := time.Now().Unix()
	from := to - int64(tf)*10

	// Forex pairs (contain "/") use the forex candle endpoint with an
	// exchange-prefixed symbol; plain tickers use the stock candle endpoint.
	endpoint := "stock/candle"
	finnSymbol := symbol
	if strings.Contains(symbol, "/") {
		endpoint = "forex/candle"
		finnSymbol = finnhubSymbol(symbol, c.config.forexExchange())
	}

	query := url.Values{}
	query.Set("symbol", finnSymbol)
	query.Set("resolution", resolution)
	query.Set("from", fmt.Sprintf("%d", from))
	query.Set("to", fmt.Sprintf("%d", to))
	query.Set("token", c.config.APIKey)
	requestURL := fmt.Sprintf("%s/%s?%s", c.config.baseURL(), endpoint, query.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("finnhub request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("finnhub fetch %s: %w", symbol, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("finnhub %s: rate limited (429)", symbol)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("finnhub %s: status %d: %s", symbol, resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("finnhub read %s: %w", symbol, err)
	}

	var data finnhubCandleResponse
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("finnhub decode %s: %w", symbol, err)
	}
	if data.Error != "" {
		return nil, fmt.Errorf("finnhub API error %s: %s", symbol, data.Error)
	}
	if data.Status != "ok" {
		return nil, fmt.Errorf("finnhub %s: no data available", symbol)
	}

	candles := c.parseCandles(symbol, interval, data)

	// Filter out already-seen candles
	c.mu.Lock()
	lastKey := symbol + "\x00" + interval
	lastTime := c.last[lastKey]
	var newCandles []Candle
	var latestTime time.Time
	for _, candle := range candles {
		if candle.Time.After(lastTime) {
			newCandles = append(newCandles, candle)
			if candle.Time.After(latestTime) {
				latestTime = candle.Time
			}
		}
	}
	if !latestTime.IsZero() {
		c.last[lastKey] = latestTime
	}
	c.mu.Unlock()

	return newCandles, nil
}

// FetchAllSymbols fetches candles for all configured symbols.
// Free tier allows 60 calls/min; requests are spaced conservatively.
func (c *FinnhubClient) FetchAllSymbols(ctx context.Context) ([]Candle, error) {
	var all []Candle
	for i, symbol := range c.config.Symbols {
		if i > 0 {
			select {
			case <-ctx.Done():
				return all, ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
		candles, err := c.FetchCandles(ctx, symbol)
		if err != nil {
			fmt.Printf("[feed:finnhub] error fetching %s: %v\n", symbol, err)
			continue
		}
		all = append(all, candles...)
	}
	return all, nil
}

// FetchQuote fetches the latest quote for a stock/ETF symbol via /quote.
func (c *FinnhubClient) FetchQuote(ctx context.Context, symbol string) (Quote, error) {
	finnSymbol := symbol
	if strings.Contains(symbol, "/") {
		finnSymbol = finnhubSymbol(symbol, c.config.forexExchange())
	}
	query := url.Values{}
	query.Set("symbol", finnSymbol)
	query.Set("token", c.config.APIKey)
	requestURL := fmt.Sprintf("%s/quote?%s", c.config.baseURL(), query.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return Quote{}, fmt.Errorf("finnhub request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return Quote{}, fmt.Errorf("finnhub quote %s: %w", symbol, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return Quote{}, fmt.Errorf("finnhub quote %s: status %d: %s", symbol, resp.StatusCode, string(body))
	}

	var data struct {
		Current float64 `json:"c"`
		High    float64 `json:"h"`
		Low     float64 `json:"l"`
		Open    float64 `json:"o"`
		Time    int64   `json:"t"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&data); err != nil {
		return Quote{}, fmt.Errorf("finnhub decode quote %s: %w", symbol, err)
	}
	if data.Current <= 0 {
		return Quote{}, fmt.Errorf("finnhub quote %s: no data available", symbol)
	}

	return Quote{
		Symbol:   symbol,
		Bid:      data.Current,
		Ask:      data.Current,
		Price:    data.Current,
		Time:     time.Unix(data.Time, 0).UTC(),
		Interval: c.config.Interval,
	}, nil
}

// Quote is a lightweight price observation returned by FetchQuote.
type Quote struct {
	Symbol   string
	Bid      float64
	Ask      float64
	Price    float64
	Time     time.Time
	Interval string
}

func (c *FinnhubClient) parseCandles(symbol, interval string, data finnhubCandleResponse) []Candle {
	candles := make([]Candle, 0, len(data.T))
	for i := range data.T {
		if i >= len(data.O) || i >= len(data.H) || i >= len(data.L) || i >= len(data.C) {
			continue
		}
		candle := Candle{
			Symbol:   symbol,
			Interval: interval,
			Open:     data.O[i],
			High:     data.H[i],
			Low:      data.L[i],
			Close:    data.C[i],
			Volume:   volAt(data.V, i),
			Time:     time.Unix(data.T[i], 0).UTC(),
		}
		if err := candle.Validate(); err == nil {
			candles = append(candles, candle)
		}
	}
	return candles
}

func volAt(v []float64, i int) float64 {
	if i < len(v) {
		return v[i]
	}
	return 0
}

// intervalToFinnhubWindow returns the approximate bar duration in seconds.
func intervalToFinnhubWindow(interval string) int64 {
	switch interval {
	case "1min":
		return 60
	case "5min":
		return 300
	case "15min":
		return 900
	case "30min":
		return 1800
	case "1h":
		return 3600
	case "1day":
		return 86400
	}
	return 3600
}

// DefaultFinnhubSymbols returns the recommended symbol set for the free tier.
// Forex pairs are routed through OANDA; QQQ provides Nasdaq 100 exposure.
func DefaultFinnhubSymbols() []string {
	return []string{
		"EUR/USD", "GBP/USD", "USD/JPY", "AUD/USD", "USD/CAD", "NZD/USD",
		"XAU/USD",
		"QQQ",
	}
}
