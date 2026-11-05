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
)

// Candle represents a completed OHLC bar from the feed.
type Candle struct {
	Symbol   string  `json:"symbol"`
	Interval string  `json:"interval"`
	Open     float64 `json:"open"`
	High     float64 `json:"high"`
	Low      float64 `json:"low"`
	Close    float64 `json:"close"`
	Volume   float64 `json:"volume"`
	// Time is the instant the bar OPENED. Every producer in this package
	// sets it that way, matching Binance, cTrader and Twelve Data, which all
	// timestamp a bar by its open.
	//
	// This was previously undefined, and consumers disagreed: the market
	// data manager read it as the open while the signal engine read it as
	// the close. The engine therefore labelled each bar with the previous
	// bar's window, stamping a pattern one whole bar before its data
	// existed and expiring the resulting order window before it opened.
	// Prefer OpenTime/CloseTime; Time is retained for compatibility.
	Time time.Time `json:"time"`
	// OpenTime and CloseTime are the bar's half-open interval
	// [OpenTime, CloseTime). CloseTime is always OpenTime plus the interval.
	OpenTime  time.Time `json:"open_time"`
	CloseTime time.Time `json:"close_time"`
}

// Bar returns the candle's open and close instants, deriving them from Time
// when a producer has not set them explicitly.
func (c Candle) Bar() (time.Time, time.Time) {
	if !c.OpenTime.IsZero() && !c.CloseTime.IsZero() {
		return c.OpenTime.UTC(), c.CloseTime.UTC()
	}
	return resolveBarTimes(c.Time, c.Interval)
}

// Validate checks that the candle has sensible values.
func (c Candle) Validate() error {
	if c.Symbol == "" || c.Interval == "" {
		return errors.New("candle requires symbol and interval")
	}
	if c.Open <= 0 || c.High <= 0 || c.Low <= 0 || c.Close <= 0 {
		return errors.New("candle OHLC must be positive")
	}
	if c.High < c.Low || c.High < c.Open || c.High < c.Close ||
		c.Low > c.Open || c.Low > c.Close {
		return errors.New("invalid candle geometry")
	}
	if c.Time.IsZero() {
		return errors.New("candle time is required")
	}
	return nil
}

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
// It returns only new candles since the last fetch (deduplication by time).
func (c *TwelveDataClient) FetchCandles(ctx context.Context, symbol string) ([]Candle, error) {
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
		c.last[symbol] = latestTime
	}
	c.mu.Unlock()

	return newCandles, nil
}

// FetchAllSymbols fetches candles for all configured symbols.
// It respects the free tier rate limit (8 credits/minute) by spacing requests
// to ensure no more than 7 requests per minute (one credit each).
func (c *TwelveDataClient) FetchAllSymbols(ctx context.Context) ([]Candle, error) {
	var all []Candle
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

func (c *TwelveDataClient) parseCandles(symbol string, data twelveDataResponse) ([]Candle, error) {
	candles := make([]Candle, 0, len(data.Values))
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
		candle := Candle{
			Symbol:    symbol,
			Interval:  c.config.Interval,
			Open:      open,
			High:      high,
			Low:       low,
			Close:     close_,
			Volume:    vol,
			Time:      barOpen,
			OpenTime:  barOpen,
			CloseTime: barClose,
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
	output   chan Candle
	done     chan struct{}
	log      io.Writer // optional destination for poll activity logs
}

// NewPoller creates a poller that fetches at the given interval and
// outputs candles to the returned channel.
func NewPoller(client *TwelveDataClient, pollInterval time.Duration) (*Poller, chan Candle) {
	ch := make(chan Candle, 128)
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
