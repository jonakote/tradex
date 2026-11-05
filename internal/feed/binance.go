package feed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// BinanceConfig configures the public Binance Spot REST market-data client.
// Kline and book-ticker endpoints are public and do not require an API key.
type BinanceConfig struct {
	APIKey   string        `json:"api_key,omitempty"`
	BaseURL  string        `json:"base_url,omitempty"`
	Symbols  []string      `json:"symbols"`
	Interval string        `json:"interval"`
	Limit    int           `json:"limit,omitempty"`
	Timeout  time.Duration `json:"timeout,omitempty"`
}

func (c BinanceConfig) Validate() error {
	if len(c.Symbols) == 0 {
		return errors.New("at least one Binance symbol is required")
	}
	if !validBinanceInterval(c.Interval) {
		return fmt.Errorf("invalid Binance interval %q", c.Interval)
	}
	if c.Limit < 0 || c.Limit > 1000 {
		return errors.New("Binance limit must be 0 (default) or between 1 and 1000")
	}
	return nil
}

func (c BinanceConfig) baseURL() string {
	if c.BaseURL != "" {
		return strings.TrimRight(c.BaseURL, "/")
	}
	return "https://api.binance.com"
}

func (c BinanceConfig) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 10 * time.Second
}

func (c BinanceConfig) limit() int {
	if c.Limit > 0 {
		return c.Limit
	}
	return 100
}

func validBinanceInterval(s string) bool {
	switch s {
	case "1s", "1m", "3m", "5m", "15m", "30m", "1h", "2h", "4h", "6h", "8h", "12h", "1d", "3d", "1w", "1M":
		return true
	}
	return false
}

// BinanceClient fetches completed Spot klines and public book-ticker quotes.
type BinanceClient struct {
	config BinanceConfig
	client *http.Client
	mu     sync.Mutex
	last   map[string]time.Time
}

// NewBinanceClient creates a public Binance Spot market-data client.
func NewBinanceClient(cfg BinanceConfig) (*BinanceClient, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &BinanceClient{
		config: cfg,
		client: &http.Client{Timeout: cfg.timeout()},
		last:   make(map[string]time.Time),
	}, nil
}

// FetchCandles fetches the configured interval for one symbol.
func (c *BinanceClient) FetchCandles(ctx context.Context, symbol string) ([]Candle, error) {
	return c.fetchCandles(ctx, symbol, c.config.Interval)
}

// FetchCandlesAt fetches the requested interval. It is used by the central
// market-data manager when a request differs from the client's default.
func (c *BinanceClient) FetchCandlesAt(ctx context.Context, symbol, interval string) ([]Candle, error) {
	return c.fetchCandles(ctx, symbol, interval)
}

func (c *BinanceClient) fetchCandles(ctx context.Context, symbol, interval string) ([]Candle, error) {
	if !validBinanceInterval(interval) {
		return nil, fmt.Errorf("binance unsupported interval %q", interval)
	}

	query := url.Values{}
	query.Set("symbol", binanceSymbol(symbol))
	query.Set("interval", interval)
	query.Set("limit", strconv.Itoa(c.config.limit()))

	data, err := c.doJSON(ctx, http.MethodGet, "/api/v3/klines", query)
	if err != nil {
		return nil, fmt.Errorf("binance fetch %s: %w", symbol, err)
	}

	var rows [][]json.RawMessage
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("binance decode klines %s: %w", symbol, err)
	}

	now := time.Now().UTC()
	candles := make([]Candle, 0, len(rows))
	for _, row := range rows {
		candle, closeTime, err := parseBinanceKline(symbol, interval, row)
		if err != nil {
			continue
		}
		// Binance includes the currently forming kline. Signal generation must
		// only use closed bars so an unfinished candle cannot repaint a signal.
		if closeTime.After(now) {
			continue
		}
		candles = append(candles, candle)
	}
	sort.Slice(candles, func(i, j int) bool { return candles[i].Time.Before(candles[j].Time) })

	// Return only candles not seen in an earlier poll, independently per
	// symbol and interval.
	key := binanceSymbol(symbol) + "\x00" + interval
	c.mu.Lock()
	lastTime := c.last[key]
	newCandles := make([]Candle, 0, len(candles))
	var latest time.Time
	for _, candle := range candles {
		if candle.Time.After(lastTime) {
			newCandles = append(newCandles, candle)
			if candle.Time.After(latest) {
				latest = candle.Time
			}
		}
	}
	if !latest.IsZero() {
		c.last[key] = latest
	}
	c.mu.Unlock()

	return newCandles, nil
}

// FetchAllSymbols fetches completed candles for every configured symbol.
func (c *BinanceClient) FetchAllSymbols(ctx context.Context) ([]Candle, error) {
	var all []Candle
	for _, symbol := range c.config.Symbols {
		select {
		case <-ctx.Done():
			return all, ctx.Err()
		default:
		}
		candles, err := c.FetchCandles(ctx, symbol)
		if err != nil {
			return all, err
		}
		all = append(all, candles...)
	}
	return all, nil
}

// FetchQuote fetches the public best bid/ask book ticker for a symbol.
func (c *BinanceClient) FetchQuote(ctx context.Context, symbol string) (Quote, error) {
	query := url.Values{}
	query.Set("symbol", binanceSymbol(symbol))
	data, err := c.doJSON(ctx, http.MethodGet, "/api/v3/ticker/bookTicker", query)
	if err != nil {
		return Quote{}, fmt.Errorf("binance quote %s: %w", symbol, err)
	}

	var ticker struct {
		BidPrice string `json:"bidPrice"`
		AskPrice string `json:"askPrice"`
	}
	if err := json.Unmarshal(data, &ticker); err != nil {
		return Quote{}, fmt.Errorf("binance decode quote %s: %w", symbol, err)
	}
	bid, err := strconv.ParseFloat(ticker.BidPrice, 64)
	if err != nil || bid <= 0 {
		return Quote{}, fmt.Errorf("binance quote %s: invalid bid", symbol)
	}
	ask, err := strconv.ParseFloat(ticker.AskPrice, 64)
	if err != nil || ask <= 0 {
		return Quote{}, fmt.Errorf("binance quote %s: invalid ask", symbol)
	}

	return Quote{
		Symbol: symbol,
		Bid:    bid,
		Ask:    ask,
		Price:  (bid + ask) / 2,
		Time:   time.Now().UTC(),
	}, nil
}

func (c *BinanceClient) doJSON(ctx context.Context, method, path string, query url.Values) ([]byte, error) {
	requestURL := c.config.baseURL() + path
	if len(query) > 0 {
		requestURL += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, requestURL, nil)
	if err != nil {
		return nil, err
	}
	if c.config.APIKey != "" {
		req.Header.Set("X-MBX-APIKEY", c.config.APIKey)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if readErr != nil {
		return nil, readErr
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

func parseBinanceKline(symbol, interval string, row []json.RawMessage) (Candle, time.Time, error) {
	if len(row) < 7 {
		return Candle{}, time.Time{}, errors.New("binance kline has fewer than 7 fields")
	}
	openMillis, err := parseBinanceInt(row[0])
	if err != nil {
		return Candle{}, time.Time{}, err
	}
	closeMillis, err := parseBinanceInt(row[6])
	if err != nil {
		return Candle{}, time.Time{}, err
	}
	values := make([]float64, 5)
	for i := range values {
		values[i], err = parseBinanceFloat(row[i+1])
		if err != nil {
			return Candle{}, time.Time{}, err
		}
	}
	// Binance timestamps a kline by its open and reports a close one
	// millisecond early (23:59:59.999 for a bar owning up to 24:00:00), so
	// the owned interval is derived from the open instead.
	open, close := resolveBarTimes(time.UnixMilli(openMillis), interval)
	candle := Candle{
		Symbol:    symbol,
		Interval:  interval,
		Open:      values[0],
		High:      values[1],
		Low:       values[2],
		Close:     values[3],
		Volume:    values[4],
		Time:      open,
		OpenTime:  open,
		CloseTime: close,
	}
	if err := candle.Validate(); err != nil {
		return Candle{}, time.Time{}, err
	}
	return candle, time.UnixMilli(closeMillis).UTC(), nil
}

func parseBinanceInt(raw json.RawMessage) (int64, error) {
	var value int64
	if err := json.Unmarshal(raw, &value); err == nil {
		return value, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return 0, err
	}
	return strconv.ParseInt(text, 10, 64)
}

func parseBinanceFloat(raw json.RawMessage) (float64, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strconv.ParseFloat(text, 64)
	}
	var value float64
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, err
	}
	return value, nil
}

func binanceSymbol(symbol string) string {
	return strings.ToUpper(strings.NewReplacer("/", "", "-", "", "_", "").Replace(strings.TrimSpace(symbol)))
}

// DefaultBinanceSymbols returns liquid Spot pairs suitable for the signal
// monitor's default crypto feed.
func DefaultBinanceSymbols() []string {
	return []string{"BTCUSDT", "ETHUSDT", "BNBUSDT", "SOLUSDT"}
}
