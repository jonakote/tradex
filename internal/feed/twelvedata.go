// Package feed provides market data ingestion from external sources.
// The primary implementation uses Twelve Data's REST API for OHLC candles
// across forex, metals, commodities, and indices.
package feed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"tradex/internal/domain"
	"tradex/internal/instrument"
)

// TwelveDataConfig configures the Twelve Data REST client.
type TwelveDataConfig struct {
	APIKey   string        `json:"api_key"`
	BaseURL  string        `json:"base_url"`
	Symbols  []string      `json:"symbols"`
	Interval string        `json:"interval"`
	Timezone string        `json:"timezone"`
	Timeout  time.Duration `json:"timeout"`
}

// Validate checks required configuration.
func (c TwelveDataConfig) Validate() error {
	if c.APIKey == "" {
		return errors.New("Twelve Data API key is required")
	}
	if len(c.Symbols) == 0 {
		return errors.New("at least one symbol is required")
	}
	if !validInterval(c.Interval) {
		return errors.New("invalid interval: must be 1min, 5min, 15min, 30min, 1h, 2h, 4h, 1day")
	}
	return nil
}

func (c TwelveDataConfig) baseURL() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return "https://api.twelvedata.com"
}

func (c TwelveDataConfig) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 10 * time.Second
}

func (c TwelveDataConfig) timezone() string {
	if c.Timezone != "" {
		return c.Timezone
	}
	return "UTC"
}

func validInterval(s string) bool {
	switch s {
	case "1min", "5min", "15min", "30min", "45min", "1h", "2h", "4h", "8h", "1day", "1week":
		return true
	}
	return false
}

// TwelveDataClient fetches OHLC candles from Twelve Data's time_series endpoint.
type TwelveDataClient struct {
	config TwelveDataConfig
	client *http.Client
	mu     sync.Mutex
	last   map[string]time.Time // last candle time per symbol to avoid duplicates
}

// NewTwelveDataClient creates a new client with the given configuration.
func NewTwelveDataClient(cfg TwelveDataConfig) (*TwelveDataClient, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &TwelveDataClient{
		config: cfg,
		client: &http.Client{Timeout: cfg.timeout()},
		last:   make(map[string]time.Time),
	}, nil
}

// twelveDataResponse is the JSON structure from the time_series endpoint.
type twelveDataResponse struct {
	Meta   twelveDataMeta    `json:"meta"`
	Values []twelveDataValue `json:"values"`
	Status string            `json:"status"`
	Code   int               `json:"code"`
	Msg    string            `json:"message"`
}

type twelveDataMeta struct {
	Symbol   string `json:"symbol"`
	Interval string `json:"interval"`
	Currency string `json:"currency"`
	Exchange string `json:"exchange"`
	Type     string `json:"type"`
}

type twelveDataValue struct {
	Datetime string `json:"datetime"`
	Open     string `json:"open"`
	High     string `json:"high"`
	Low      string `json:"low"`
	Close    string `json:"close"`
	Volume   string `json:"volume"`
}

// FetchCandles fetches the most recent completed candles for a single symbol.
// It returns only new candles since the last fetch (deduplication by open time).
func (c *TwelveDataClient) FetchCandles(ctx context.Context, symbol string) ([]domain.Candle, error) {
	url := fmt.Sprintf("%s/time_series?symbol=%s&interval=%s&outputsize=5&timezone=%s&apikey=%s",
		c.config.baseURL(), symbol, c.config.Interval, c.config.timezone(), c.config.APIKey)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("twelve data request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("twelve data fetch %s: %w", symbol, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("twelve data %s: status %d: %s", symbol, resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("twelve data read %s: %w", symbol, err)
	}

	var data twelveDataResponse
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("twelve data decode %s: %w", symbol, err)
	}

	if data.Status == "error" {
		return nil, fmt.Errorf("twelve data API error %s: [%d] %s", symbol, data.Code, data.Msg)
	}

	candles, err := c.parseCandles(symbol, data)
	if err != nil {
		return nil, err
	}

	// Filter out already-seen candles
	c.mu.Lock()
	lastTime := c.last[symbol]
	var newCandles []domain.Candle
	var latestTime time.Time
	for _, candle := range candles {
		if candle.OpenTime.After(lastTime) {
			newCandles = append(newCandles, candle)
			if candle.OpenTime.After(latestTime) {
				latestTime = candle.OpenTime
			}
		}
	}
	if !latestTime.IsZero() {
		c.last[symbol] = latestTime
	}
	c.mu.Unlock()

	return newCandles, nil
}

// FetchAllSymbols fetches candles for all configured symbols.
// It respects the free tier rate limit (8 credits/minute) by spacing requests
// to ensure no more than 7 requests per minute (one credit each).
func (c *TwelveDataClient) FetchAllSymbols(ctx context.Context) ([]domain.Candle, error) {
	var all []domain.Candle
	for i, symbol := range c.config.Symbols {
		if i > 0 {
			// Free tier: 8 credits/min. Each time_series call = 1 credit.
			// Space at 10s apart = max 6 per minute, safely under limit.
			select {
			case <-ctx.Done():
				return all, ctx.Err()
			case <-time.After(10 * time.Second):
			}
		}
		candles, err := c.FetchCandles(ctx, symbol)
		if err != nil {
			// Log but continue with other symbols
			fmt.Printf("[feed] error fetching %s: %v\n", symbol, err)
			continue
		}
		all = append(all, candles...)
	}
	return all, nil
}

// parseCandles converts the vendor payload into validated domain candles.
//
// Prices convert to integer ticks once, here, at the verified instrument
// precision; an unresolved symbol or unknown interval fails closed (returns
// an error) rather than guessing a scale, matching resolve() in source.go.
func (c *TwelveDataClient) parseCandles(symbol string, data twelveDataResponse) ([]domain.Candle, error) {
	spec, ok := instrument.Lookup(symbol)
	if !ok {
		return nil, &UnknownInstrumentError{Symbol: symbol}
	}
	timeframe := IntervalDuration(c.config.Interval)
	if timeframe <= 0 {
		return nil, fmt.Errorf("feed: twelvedata unknown interval %q", c.config.Interval)
	}

	candles := make([]domain.Candle, 0, len(data.Values))
	for _, v := range data.Values {
		t, err := parseTwelveDataTime(v.Datetime)
		if err != nil {
			continue // skip unparseable entries
		}
		open, _ := strconv.ParseFloat(v.Open, 64)
		high, _ := strconv.ParseFloat(v.High, 64)
		low, _ := strconv.ParseFloat(v.Low, 64)
		close_, _ := strconv.ParseFloat(v.Close, 64)
		vol, _ := strconv.ParseFloat(v.Volume, 64)

		// Twelve Data's `datetime` is the bar's open.
		barOpen, barClose := resolveBarTimes(t, c.config.Interval)
		candle := domain.Candle{
			Source: "twelvedata", Symbol: symbol, Timeframe: timeframe,
			OpenTime: barOpen, CloseTime: barClose,
			Open:      domain.Price(spec.Ticks(open)),
			High:      domain.Price(spec.Ticks(high)),
			Low:       domain.Price(spec.Ticks(low)),
			Close:     domain.Price(spec.Ticks(close_)),
			TickCount: maxUint64(uint64(vol), 1),
		}
		if err := candle.Validate(); err == nil {
			candles = append(candles, candle)
		}
	}
	// Twelve Data returns newest first; reverse to chronological order
	for i, j := 0, len(candles)-1; i < j; i, j = i+1, j-1 {
		candles[i], candles[j] = candles[j], candles[i]
	}
	return candles, nil
}

// maxUint64 returns the larger of a and b. A bar with no reported volume
// still occupies time; TickCount must be positive for domain validation, so
// an absent or zero volume becomes 1 rather than inventing activity.
func maxUint64(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}

func parseTwelveDataTime(s string) (time.Time, error) {
	// Twelve Data returns "2024-01-15 10:00:00" format in the configured timezone
	s = strings.TrimSpace(s)
	if t, err := time.Parse("2006-01-02 15:04:05", s); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("cannot parse time: %q", s)
}

// Poller periodically fetches candles and sends them to a channel.
type Poller struct {
	client   *TwelveDataClient
	interval time.Duration
	output   chan domain.Candle
	done     chan struct{}
	log      io.Writer // optional destination for poll activity logs
}

// NewPoller creates a poller that fetches at the given interval and
// outputs candles to the returned channel.
func NewPoller(client *TwelveDataClient, pollInterval time.Duration) (*Poller, chan domain.Candle) {
	ch := make(chan domain.Candle, 128)
	return &Poller{
		client:   client,
		interval: pollInterval,
		output:   ch,
		done:     make(chan struct{}),
	}, ch
}

// SetLogWriter directs poll activity logs to w. Each poll attempt is logged
// with a UTC timestamp, outcome, candle count, and duration.
func (p *Poller) SetLogWriter(w io.Writer) {
	p.log = w
}

// logf writes one timestamped line to the configured log writer, if any.
func (p *Poller) logf(format string, args ...any) {
	if p.log == nil {
		return
	}
	fmt.Fprintf(p.log, "%s [twelvedata-poll] %s\n",
		time.Now().UTC().Format(time.RFC3339Nano), fmt.Sprintf(format, args...))
}

// Run starts the polling loop. It blocks until the context is cancelled.
func (p *Poller) Run(ctx context.Context) {
	// Initial fetch immediately
	p.fetchAndSend(ctx)

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.done:
			return
		case <-ticker.C:
			p.fetchAndSend(ctx)
		}
	}
}

func (p *Poller) fetchAndSend(ctx context.Context) {
	start := time.Now()
	candles, err := p.client.FetchAllSymbols(ctx)
	if err != nil {
		p.logf("poll failed after %s: %v", time.Since(start).Round(time.Millisecond), err)
		return
	}
	p.logf("poll ok in %s: %d candles", time.Since(start).Round(time.Millisecond), len(candles))
	for _, c := range candles {
		select {
		case p.output <- c:
		case <-ctx.Done():
			return
		}
	}
}

// Stop terminates the poller.
func (p *Poller) Stop() {
	select {
	case <-p.done:
	default:
		close(p.done)
	}
}

// DefaultSymbols returns the recommended initial symbol set for the free tier.
// Note: XAG/USD, BRENT, and futures (NQ) require paid Twelve Data plans.
// Use QQQ (Nasdaq 100 ETF) or IXIC (Nasdaq Composite index) for Nasdaq exposure.
func DefaultSymbols() []string {
	return []string{
		"EUR/USD", "GBP/USD", "USD/JPY", "AUD/USD", "USD/CAD", "NZD/USD",
		"XAU/USD",
		"QQQ", // Nasdaq 100 ETF (free tier)
	}
}
