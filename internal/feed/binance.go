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
	"time"

	"tradex/internal/domain"
	"tradex/internal/instrument"
)

// BinanceConfig configures the public Binance Spot kline source. The endpoint
// is public; an API key is optional and unused for market data.
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
		return ErrNoSymbols
	}
	if IntervalDuration(c.Interval) <= 0 {
		return fmt.Errorf("feed: unknown Binance interval %q", c.Interval)
	}
	if c.Limit < 0 || c.Limit > 1000 {
		return errors.New("feed: Binance limit must be 0 (default) or 1..1000")
	}
	for _, symbol := range c.Symbols {
		if _, err := resolve(binanceSymbol(symbol)); err != nil {
			return err
		}
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

// Binance is a Source over the public Spot kline endpoint.
type Binance struct {
	config BinanceConfig
	client *http.Client
	cursor *cursor
	now    func() time.Time
}

// NewBinance creates a public Binance Spot candle source.
func NewBinance(config BinanceConfig) (*Binance, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &Binance{
		config: config,
		client: &http.Client{Timeout: config.timeout()},
		cursor: newCursor(),
		now:    time.Now,
	}, nil
}

func (b *Binance) Name() string      { return "binance" }
func (b *Binance) Interval() string  { return b.config.Interval }
func (b *Binance) Symbols() []string { return b.config.Symbols }

// Poll fetches each configured symbol and returns only bars that have closed
// and were not yielded by an earlier call.
func (b *Binance) Poll(ctx context.Context) ([]domain.Candle, error) {
	var all []domain.Candle
	for _, symbol := range b.config.Symbols {
		if err := ctx.Err(); err != nil {
			return all, err
		}
		candles, err := b.fetch(ctx, symbol)
		if err != nil {
			return all, err
		}
		all = append(all, candles...)
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].OpenTime.Before(all[j].OpenTime) })
	return all, nil
}

func (b *Binance) fetch(ctx context.Context, symbol string) ([]domain.Candle, error) {
	canonical := binanceSymbol(symbol)
	spec, err := resolve(canonical)
	if err != nil {
		return nil, err
	}
	timeframe := IntervalDuration(b.config.Interval)

	query := url.Values{}
	query.Set("symbol", canonical)
	query.Set("interval", b.config.Interval)
	query.Set("limit", strconv.Itoa(b.config.limit()))
	payload, err := b.doJSON(ctx, "/api/v3/klines", query)
	if err != nil {
		return nil, fmt.Errorf("feed: binance fetch %s: %w", canonical, err)
	}

	var rows [][]json.RawMessage
	if err := json.Unmarshal(payload, &rows); err != nil {
		return nil, fmt.Errorf("feed: binance decode %s: %w", canonical, err)
	}

	now := b.now().UTC()
	candles := make([]domain.Candle, 0, len(rows))
	for _, row := range rows {
		candle, err := parseBinanceKline(row, canonical, spec, timeframe)
		if err != nil {
			continue
		}
		// Binance includes the forming kline. A signal from an unfinished
		// bar repaints as the bar develops, so only closed bars are used.
		if candle.CloseTime.After(now) {
			continue
		}
		candles = append(candles, candle)
	}
	return b.cursor.advance(canonical+"\x00"+b.config.Interval, candles), nil
}

// parseBinanceKline converts one kline row into a validated candle.
//
// Binance timestamps a kline by its OPEN and reports a close one millisecond
// early (23:59:59.999 for a bar owning up to 24:00:00), so the owned interval
// is derived from the open. Prices convert to integer ticks once, here.
func parseBinanceKline(row []json.RawMessage, symbol string,
	spec instrument.Spec, timeframe time.Duration) (domain.Candle, error) {

	if len(row) < 6 {
		return domain.Candle{}, errors.New("kline has fewer than 6 fields")
	}
	openMillis, err := parseBinanceInt(row[0])
	if err != nil {
		return domain.Candle{}, err
	}
	values := make([]float64, 5)
	for i := range values {
		if values[i], err = parseBinanceFloat(row[i+1]); err != nil {
			return domain.Candle{}, err
		}
	}

	openTime := time.UnixMilli(openMillis).UTC()
	volume := uint64(values[4])
	if volume == 0 {
		volume = 1
	}
	candle := domain.Candle{
		Source: "binance", Symbol: symbol, Timeframe: timeframe,
		OpenTime: openTime, CloseTime: openTime.Add(timeframe),
		Open:      domain.Price(spec.Ticks(values[0])),
		High:      domain.Price(spec.Ticks(values[1])),
		Low:       domain.Price(spec.Ticks(values[2])),
		Close:     domain.Price(spec.Ticks(values[3])),
		TickCount: volume,
	}
	if err := candle.Validate(); err != nil {
		return domain.Candle{}, err
	}
	return candle, nil
}

func (b *Binance) doJSON(ctx context.Context, path string, query url.Values) ([]byte, error) {
	requestURL := b.config.baseURL() + path
	if len(query) > 0 {
		requestURL += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	if b.config.APIKey != "" {
		request.Header.Set("X-MBX-APIKEY", b.config.APIKey)
	}
	response, err := b.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
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
	return strings.ToUpper(strings.NewReplacer("/", "", "-", "", "_", "").
		Replace(strings.TrimSpace(symbol)))
}

// DefaultBinanceSymbols returns liquid Spot pairs for the monitor's default
// crypto feed.
func DefaultBinanceSymbols() []string {
	return []string{"BTCUSDT", "ETHUSDT", "BNBUSDT", "SOLUSDT"}
}
