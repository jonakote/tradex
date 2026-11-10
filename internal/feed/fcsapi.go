package feed

import (
	"bytes"
	"context"
	"encoding/json"
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

// FCSConfig configures the FCS API REST client.
type FCSConfig struct {
	APIKey   string        `json:"api_key"`
	BaseURL  string        `json:"base_url"`
	Symbols  []string      `json:"symbols"`
	Interval string        `json:"interval"` // "1h", "5m", "15m", "1d"
	Timeout  time.Duration `json:"timeout"`
}

func (c FCSConfig) Validate() error {
	if c.APIKey == "" {
		return fmt.Errorf("FCS API key is required")
	}
	if len(c.Symbols) == 0 {
		return fmt.Errorf("at least one FCS symbol is required")
	}
	if !validFCSInterval(c.Interval) {
		return fmt.Errorf("invalid FCS interval: must be 5m, 15m, 30m, 1h, 4h, 1d, 1w")
	}
	return nil
}

func (c FCSConfig) baseURL() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return "https://fcsapi.com/api-v3"
}

func (c FCSConfig) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 10 * time.Second
}

func validFCSInterval(s string) bool {
	switch s {
	case "5m", "15m", "30m", "1h", "4h", "1d", "1w":
		return true
	}
	return false
}

// FCSClient fetches OHLC candles from FCS API's forex/history endpoint.
// Works for forex, commodities (XAG/USD, BRENT), and indices.
type FCSClient struct {
	config FCSConfig
	client *http.Client
	mu     sync.Mutex
	last   map[string]time.Time
}

// NewFCSClient creates a new FCS API client.
func NewFCSClient(cfg FCSConfig) (*FCSClient, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &FCSClient{
		config: cfg,
		client: &http.Client{Timeout: cfg.timeout()},
		last:   make(map[string]time.Time),
	}, nil
}

// FCS API response structures
type fcsResponse struct {
	Status   bool            `json:"status"`
	Code     int             `json:"code"`
	Msg      string          `json:"msg"`
	Response json.RawMessage `json:"response"`
}

type fcsCandle struct {
	Open  interface{} `json:"o"`
	High  interface{} `json:"h"`
	Low   interface{} `json:"l"`
	Close interface{} `json:"c"`
	Time  interface{} `json:"t"`  // Unix timestamp or datetime
	TM    string      `json:"tm"` // datetime format "2024-01-15 10:00:00"
}

func unmarshalFCSCandles(raw json.RawMessage) ([]fcsCandle, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil, nil
	}
	if trimmed[0] == '[' {
		var list []fcsCandle
		if err := json.Unmarshal(trimmed, &list); err != nil {
			return nil, err
		}
		return list, nil
	}
	if trimmed[0] == '{' {
		var objMap map[string]fcsCandle
		if err := json.Unmarshal(trimmed, &objMap); err != nil {
			return nil, err
		}
		list := make([]fcsCandle, 0, len(objMap))
		for _, c := range objMap {
			list = append(list, c)
		}
		return list, nil
	}
	return nil, fmt.Errorf("unexpected json format for FCS candles: %s", string(trimmed))
}

// FetchCandles fetches recent candles for a single symbol.
// Tries /forex/history first, and falls back to /forex/latest if historical candles are unavailable.
func (c *FCSClient) FetchCandles(ctx context.Context, symbol string) ([]domain.Candle, error) {
	candles, err := c.fetchHistory(ctx, symbol)
	if err == nil && len(candles) > 0 {
		return c.deduplicate(symbol, candles), nil
	}

	// If history returned an error or no candles, attempt /forex/latest fallback
	latestCandles, errLatest := c.fetchLatest(ctx, symbol)
	if errLatest == nil && len(latestCandles) > 0 {
		return c.deduplicate(symbol, latestCandles), nil
	}

	if err != nil {
		return nil, err
	}
	return nil, errLatest
}

func (c *FCSClient) fetchHistory(ctx context.Context, symbol string) ([]domain.Candle, error) {
	// FCS uses symbols without slash for forex history: XAG/USD -> XAGUSD
	fcsSymbol := strings.ReplaceAll(symbol, "/", "")

	url := fmt.Sprintf("%s/forex/history?symbol=%s&period=%s&access_key=%s",
		c.config.baseURL(), fcsSymbol, c.config.Interval, c.config.APIKey)

	return c.doRequest(ctx, url, symbol)
}

func (c *FCSClient) fetchLatest(ctx context.Context, symbol string) ([]domain.Candle, error) {
	// FCS /forex/latest accepts both symbol formats (e.g. XAG/USD or XAGUSD)
	url := fmt.Sprintf("%s/forex/latest?symbol=%s&access_key=%s",
		c.config.baseURL(), symbol, c.config.APIKey)

	return c.doRequest(ctx, url, symbol)
}

func (c *FCSClient) doRequest(ctx context.Context, url, symbol string) ([]domain.Candle, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("FCS request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("FCS fetch %s: %w", symbol, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("FCS %s: status %d: %s", symbol, resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("FCS read %s: %w", symbol, err)
	}

	var data fcsResponse
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("FCS decode %s: %w", symbol, err)
	}

	if !data.Status {
		if data.Code == 213 {
			return nil, fmt.Errorf("FCS API rate limited (3 req/min on free tier)")
		}
		return nil, fmt.Errorf("FCS API error %s: [%d] %s", symbol, data.Code, data.Msg)
	}

	rawCandles, err := unmarshalFCSCandles(data.Response)
	if err != nil {
		return nil, fmt.Errorf("FCS decode %s: %w", symbol, err)
	}

	return c.parseCandles(symbol, rawCandles)
}

func (c *FCSClient) deduplicate(symbol string, candles []domain.Candle) []domain.Candle {
	c.mu.Lock()
	defer c.mu.Unlock()

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
	return newCandles
}

// FetchAllSymbols fetches candles for all configured symbols.
// FCS free tier: 3 req/min (spaced with delay).
func (c *FCSClient) FetchAllSymbols(ctx context.Context) ([]domain.Candle, error) {
	var all []domain.Candle
	for i, symbol := range c.config.Symbols {
		if i > 0 {
			// Space requests to respect the 3 req/min rate limit on FCS free accounts
			select {
			case <-ctx.Done():
				return all, ctx.Err()
			case <-time.After(21 * time.Second):
			}
		}
		candles, err := c.FetchCandles(ctx, symbol)
		if err != nil {
			fmt.Printf("[feed:fcs] error fetching %s: %v\n", symbol, err)
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
func (c *FCSClient) parseCandles(symbol string, raw []fcsCandle) ([]domain.Candle, error) {
	spec, ok := instrument.Lookup(symbol)
	if !ok {
		return nil, &UnknownInstrumentError{Symbol: symbol}
	}
	timeframe := IntervalDuration(c.config.Interval)
	if timeframe <= 0 {
		return nil, fmt.Errorf("feed: fcs unknown interval %q", c.config.Interval)
	}

	candles := make([]domain.Candle, 0, len(raw))
	for _, v := range raw {
		t := parseFCSTime(v.TM, v.Time)
		if t.IsZero() {
			continue
		}
		open := parseFCSFloat(v.Open)
		high := parseFCSFloat(v.High)
		low := parseFCSFloat(v.Low)
		close_ := parseFCSFloat(v.Close)

		// FCS timestamps a bar by its open.
		barOpen, barClose := resolveBarTimes(t, c.config.Interval)
		candle := domain.Candle{
			Source: "fcs", Symbol: symbol, Timeframe: timeframe,
			OpenTime: barOpen, CloseTime: barClose,
			Open:      domain.Price(spec.Ticks(open)),
			High:      domain.Price(spec.Ticks(high)),
			Low:       domain.Price(spec.Ticks(low)),
			Close:     domain.Price(spec.Ticks(close_)),
			TickCount: 1,
		}
		if err := candle.Validate(); err == nil {
			candles = append(candles, candle)
		}
	}
	return candles, nil
}

func parseFCSFloat(v interface{}) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case float32:
		return float64(val)
	case int:
		return float64(val)
	case int64:
		return float64(val)
	case string:
		f, _ := strconv.ParseFloat(val, 64)
		return f
	case json.Number:
		f, _ := val.Float64()
		return f
	}
	return 0
}

func parseFCSTime(tm string, tVal interface{}) time.Time {
	// Try datetime format first
	if tm != "" {
		if t, err := time.Parse("2006-01-02 15:04:05", tm); err == nil {
			return t.UTC()
		}
	}
	// Try unix timestamp
	switch v := tVal.(type) {
	case float64:
		if v > 0 {
			return time.Unix(int64(v), 0).UTC()
		}
	case int64:
		if v > 0 {
			return time.Unix(v, 0).UTC()
		}
	case int:
		if v > 0 {
			return time.Unix(int64(v), 0).UTC()
		}
	case string:
		if ts, err := strconv.ParseInt(v, 10, 64); err == nil && ts > 0 {
			return time.Unix(ts, 0).UTC()
		}
	}
	return time.Time{}
}

// DefaultFCSSymbols returns symbols to fetch from FCS that aren't available
// on Twelve Data free tier.
func DefaultFCSSymbols() []string {
	return []string{
		"XAG/USD", // Silver
	}
}
