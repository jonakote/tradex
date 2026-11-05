// Package manager implements the Market Data Provider Manager.
//
// All market-data requests flow through the central MarketDataManager.
// Trading strategies and bot components never call providers directly.
// The manager handles caching, deduplication, rate limiting, provider
// selection, health tracking, circuit breaking, and retry/backoff.
package manager

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Timeframe represents a candle interval.
type Timeframe string

const (
	Timeframe1m  Timeframe = "1min"
	Timeframe5m  Timeframe = "5min"
	Timeframe15m Timeframe = "15min"
	Timeframe30m Timeframe = "30min"
	Timeframe1h  Timeframe = "1h"
	Timeframe4h  Timeframe = "4h"
	Timeframe1d  Timeframe = "1day"
	Timeframe1w  Timeframe = "1week"
)

// Valid returns true if the timeframe is recognized.
func (t Timeframe) Valid() bool {
	switch t {
	case Timeframe1m, Timeframe5m, Timeframe15m, Timeframe30m,
		Timeframe1h, Timeframe4h, Timeframe1d, Timeframe1w:
		return true
	}
	return false
}

// Duration returns the time.Duration equivalent.
func (t Timeframe) Duration() time.Duration {
	switch t {
	case Timeframe1m:
		return time.Minute
	case Timeframe5m:
		return 5 * time.Minute
	case Timeframe15m:
		return 15 * time.Minute
	case Timeframe30m:
		return 30 * time.Minute
	case Timeframe1h:
		return time.Hour
	case Timeframe4h:
		return 4 * time.Hour
	case Timeframe1d:
		return 24 * time.Hour
	case Timeframe1w:
		return 7 * 24 * time.Hour
	default:
		return 0
	}
}

// InstrumentType classifies what kind of instrument a symbol is.
type InstrumentType string

const (
	InstrumentForex     InstrumentType = "forex"
	InstrumentMetal     InstrumentType = "metal"
	InstrumentCommodity InstrumentType = "commodity"
	InstrumentIndex     InstrumentType = "index"
	InstrumentStock     InstrumentType = "stock"
	InstrumentCrypto    InstrumentType = "crypto"
)

// Instrument identifies a tradeable symbol with its type.
type Instrument struct {
	Symbol string         `json:"symbol"`
	Type   InstrumentType `json:"type"`
}

func (i Instrument) Validate() error {
	if i.Symbol == "" {
		return errors.New("instrument symbol is required")
	}
	switch i.Type {
	case InstrumentForex, InstrumentMetal, InstrumentCommodity,
		InstrumentIndex, InstrumentStock, InstrumentCrypto:
	default:
		return fmt.Errorf("unknown instrument type: %s", i.Type)
	}
	return nil
}

// Quote represents a normalized real-time price observation.
type Quote struct {
	Symbol    string    `json:"symbol"`
	Bid       float64   `json:"bid"`
	Ask       float64   `json:"ask"`
	Price     float64   `json:"price"`
	Timestamp time.Time `json:"timestamp"`
	Provider  string    `json:"provider"`
	FetchedAt time.Time `json:"fetched_at"`
}

// Age returns how old the quote is relative to the given time.
func (q Quote) Age(now time.Time) time.Duration {
	return now.Sub(q.FetchedAt)
}

// Candle represents a normalized OHLCV bar from any provider.
type Candle struct {
	Symbol    string    `json:"symbol"`
	Timeframe Timeframe `json:"timeframe"`
	OpenTime  time.Time `json:"open_time"`
	Open      float64   `json:"open"`
	High      float64   `json:"high"`
	Low       float64   `json:"low"`
	Close     float64   `json:"close"`
	Volume    float64   `json:"volume"`
	Provider  string    `json:"provider"`
	FetchedAt time.Time `json:"fetched_at"`
}

// Validate checks candle integrity.
func (c Candle) Validate() error {
	if c.Symbol == "" || !c.Timeframe.Valid() {
		return errors.New("candle requires symbol and valid timeframe")
	}
	if c.OpenTime.IsZero() {
		return errors.New("candle open_time is required")
	}
	if c.Open <= 0 || c.High <= 0 || c.Low <= 0 || c.Close <= 0 {
		return errors.New("candle OHLC must be positive")
	}
	if c.High < c.Low || c.High < c.Open || c.High < c.Close ||
		c.Low > c.Open || c.Low > c.Close {
		return errors.New("invalid candle geometry")
	}
	if c.Provider == "" {
		return errors.New("candle provider is required")
	}
	return nil
}

// CacheKey returns the deduplication/cache key for this candle.
func (c Candle) CacheKey() string {
	return fmt.Sprintf("candle:%s:%s:%d", c.Symbol, c.Timeframe, c.OpenTime.Unix())
}

// CandleRequest specifies what candles to fetch.
type CandleRequest struct {
	Symbol    string    `json:"symbol"`
	Timeframe Timeframe `json:"timeframe"`
	From      time.Time `json:"from"`
	To        time.Time `json:"to"`
	Limit     int       `json:"limit"` // 0 = provider default
}

// Validate checks the request is well-formed.
func (r CandleRequest) Validate() error {
	if r.Symbol == "" {
		return errors.New("candle request requires symbol")
	}
	if !r.Timeframe.Valid() {
		return errors.New("candle request requires valid timeframe")
	}
	if !r.From.IsZero() && !r.To.IsZero() && !r.To.After(r.From) {
		return errors.New("candle request 'to' must be after 'from'")
	}
	return nil
}

// CacheKey returns the deduplication key for this request.
func (r CandleRequest) CacheKey() string {
	if r.Limit > 0 && r.From.IsZero() {
		return fmt.Sprintf("candles:%s:%s:latest:%d", r.Symbol, r.Timeframe, r.Limit)
	}
	return fmt.Sprintf("candles:%s:%s:%d:%d", r.Symbol, r.Timeframe, r.From.Unix(), r.To.Unix())
}

// QuoteRequest specifies what quote to fetch.
type QuoteRequest struct {
	Symbol string `json:"symbol"`
}

func (r QuoteRequest) CacheKey() string {
	return fmt.Sprintf("quote:%s", r.Symbol)
}

// ProviderQuota reports a provider's current quota state.
type ProviderQuota struct {
	Provider         string `json:"provider"`
	CreditsUsed      int64  `json:"credits_used"`
	CreditsRemaining int64  `json:"credits_remaining"`
	DailyLimit       int64  `json:"daily_limit"`
	MinuteUsed       int64  `json:"minute_used"`
	MinuteLimit      int64  `json:"minute_limit"`
}

// MarketDataProvider is the abstraction that every data source must implement.
// Trading strategies never interact with providers directly.
type MarketDataProvider interface {
	// Name returns the provider identifier (e.g. "twelvedata", "fcs", "finnhub").
	Name() string

	// Supports returns true if this provider can serve data for the instrument.
	Supports(instrument Instrument) bool

	// GetCandles fetches OHLCV candles for the given request.
	GetCandles(ctx context.Context, req CandleRequest) ([]Candle, error)

	// GetQuote fetches the latest quote for a symbol.
	GetQuote(ctx context.Context, req QuoteRequest) (Quote, error)

	// RemainingQuota returns the provider's current quota state.
	RemainingQuota() ProviderQuota
}

// ProviderError wraps provider-specific errors with classification.
type ProviderError struct {
	Provider   string
	StatusCode int
	Message    string
	Retryable  bool
	RetryAfter time.Duration
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("%s: [%d] %s", e.Provider, e.StatusCode, e.Message)
}

// IsRateLimited returns true if this is a 429 error.
func (e *ProviderError) IsRateLimited() bool {
	return e.StatusCode == 429
}

// IsRetryable returns true if the request can be retried.
func (e *ProviderError) IsRetryable() bool {
	return e.Retryable
}
