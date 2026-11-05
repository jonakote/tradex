package manager

import (
	"errors"
	"time"
)

// RateLimitConfig defines the rate limits for a single provider.
type RateLimitConfig struct {
	RequestsPerSecond int   `json:"requests_per_second"`
	RequestsPerMinute int   `json:"requests_per_minute"`
	RequestsPerDay    int64 `json:"requests_per_day"`
	CreditsPerMinute  int   `json:"credits_per_minute"`
	CreditsPerDay     int64 `json:"credits_per_day"`
}

// ProviderConfig configures a single data provider.
type ProviderConfig struct {
	Name       string          `json:"name"`
	Enabled    bool            `json:"enabled"`
	RateLimit  RateLimitConfig `json:"rate_limit"`
	Timeout    time.Duration   `json:"timeout"`
	MaxRetries int             `json:"max_retries"`
	// CircuitBreaker
	CircuitThreshold int           `json:"circuit_threshold"` // failures before open
	CircuitCooldown  time.Duration `json:"circuit_cooldown"`  // time before half-open
}

func (c ProviderConfig) Validate() error {
	if c.Name == "" {
		return errors.New("provider config requires name")
	}
	if c.Timeout <= 0 {
		c.Timeout = 10 * time.Second
	}
	return nil
}

// CacheConfig defines TTL settings for different data types.
type CacheConfig struct {
	QuoteTTL     time.Duration `json:"quote_ttl"`
	Candle1mTTL  time.Duration `json:"candle_1m_ttl"`
	Candle5mTTL  time.Duration `json:"candle_5m_ttl"`
	Candle15mTTL time.Duration `json:"candle_15m_ttl"`
	Candle1hTTL  time.Duration `json:"candle_1h_ttl"`
	Candle4hTTL  time.Duration `json:"candle_4h_ttl"`
	Candle1dTTL  time.Duration `json:"candle_1d_ttl"`
	SymbolsTTL   time.Duration `json:"symbols_ttl"`
}

// DefaultCacheConfig returns recommended TTL defaults.
func DefaultCacheConfig() CacheConfig {
	return CacheConfig{
		QuoteTTL:     5 * time.Second,
		Candle1mTTL:  15 * time.Second,
		Candle5mTTL:  30 * time.Second,
		Candle15mTTL: 2 * time.Minute,
		Candle1hTTL:  5 * time.Minute,
		Candle4hTTL:  15 * time.Minute,
		Candle1dTTL:  60 * time.Minute,
		SymbolsTTL:   24 * time.Hour,
	}
}

// TTLForTimeframe returns the cache TTL for a given candle timeframe.
func (c CacheConfig) TTLForTimeframe(tf Timeframe) time.Duration {
	switch tf {
	case Timeframe1m:
		return c.Candle1mTTL
	case Timeframe5m:
		return c.Candle5mTTL
	case Timeframe15m:
		return c.Candle15mTTL
	case Timeframe30m:
		return c.Candle15mTTL // use 15m TTL for 30m
	case Timeframe1h:
		return c.Candle1hTTL
	case Timeframe4h:
		return c.Candle4hTTL
	case Timeframe1d, Timeframe1w:
		return c.Candle1dTTL
	default:
		return 5 * time.Minute
	}
}

// StaleConfig defines maximum acceptable age before data is considered stale.
type StaleConfig struct {
	MaxQuoteAge     time.Duration `json:"max_quote_age"`
	MaxCandle1mAge  time.Duration `json:"max_candle_1m_age"`
	MaxCandle5mAge  time.Duration `json:"max_candle_5m_age"`
	MaxCandle15mAge time.Duration `json:"max_candle_15m_age"`
	MaxCandle1hAge  time.Duration `json:"max_candle_1h_age"`
}

// DefaultStaleConfig returns recommended staleness thresholds.
func DefaultStaleConfig() StaleConfig {
	return StaleConfig{
		MaxQuoteAge:     10 * time.Second,
		MaxCandle1mAge:  2 * time.Minute,
		MaxCandle5mAge:  7 * time.Minute,
		MaxCandle15mAge: 20 * time.Minute,
		MaxCandle1hAge:  70 * time.Minute,
	}
}

// ManagerConfig is the top-level configuration for the MarketDataManager.
type ManagerConfig struct {
	Cache     CacheConfig               `json:"cache"`
	Stale     StaleConfig               `json:"stale"`
	Providers map[string]ProviderConfig `json:"providers"`
	Registry  RegistryConfig            `json:"registry"`
}

// DefaultManagerConfig returns a working default configuration.
func DefaultManagerConfig() ManagerConfig {
	return ManagerConfig{
		Cache: DefaultCacheConfig(),
		Stale: DefaultStaleConfig(),
		Providers: map[string]ProviderConfig{
			"binance": {
				Name:             "binance",
				Enabled:          true, // Public Spot market data requires no API key
				RateLimit:        RateLimitConfig{RequestsPerMinute: 600},
				Timeout:          10 * time.Second,
				MaxRetries:       2,
				CircuitThreshold: 5,
				CircuitCooldown:  30 * time.Second,
			},
			"ctrader": {
				Name:             "ctrader",
				Enabled:          false, // Enable only after cTrader demo data validation
				RateLimit:        RateLimitConfig{RequestsPerSecond: 5},
				Timeout:          10 * time.Second,
				MaxRetries:       1,
				CircuitThreshold: 3,
				CircuitCooldown:  30 * time.Second,
			},
			"twelvedata": {
				Name:             "twelvedata",
				Enabled:          false, // Disabled after switching to Finnhub
				RateLimit:        RateLimitConfig{RequestsPerMinute: 7, CreditsPerMinute: 8, CreditsPerDay: 800},
				Timeout:          10 * time.Second,
				MaxRetries:       2,
				CircuitThreshold: 5,
				CircuitCooldown:  60 * time.Second,
			},
			"fcs": {
				Name:             "fcs",
				Enabled:          true,
				RateLimit:        RateLimitConfig{RequestsPerMinute: 10, CreditsPerDay: 500},
				Timeout:          10 * time.Second,
				MaxRetries:       2,
				CircuitThreshold: 3,
				CircuitCooldown:  120 * time.Second,
			},
			"finnhub": {
				Name:             "finnhub",
				Enabled:          true, // Enabled with API key
				RateLimit:        RateLimitConfig{RequestsPerMinute: 60, CreditsPerDay: 86400},
				Timeout:          10 * time.Second,
				MaxRetries:       2,
				CircuitThreshold: 5,
				CircuitCooldown:  30 * time.Second,
			},
		},
		Registry: RegistryConfig{
			DefaultProviders: []string{"finnhub", "fcs"},
			SymbolRouting: []ProviderPriority{
				{Symbol: "BTCUSDT", Providers: []string{"binance"}},
				{Symbol: "BTC/USDT", Providers: []string{"binance"}},
				{Symbol: "ETHUSDT", Providers: []string{"binance"}},
				{Symbol: "ETH/USDT", Providers: []string{"binance"}},
				{Symbol: "BNBUSDT", Providers: []string{"binance"}},
				{Symbol: "BNB/USDT", Providers: []string{"binance"}},
				{Symbol: "SOLUSDT", Providers: []string{"binance"}},
				{Symbol: "SOL/USDT", Providers: []string{"binance"}},
				{Symbol: "EUR/USD", Providers: []string{"finnhub", "fcs", "twelvedata"}},
				{Symbol: "GBP/USD", Providers: []string{"finnhub", "fcs", "twelvedata"}},
				{Symbol: "USD/JPY", Providers: []string{"finnhub", "fcs", "twelvedata"}},
				{Symbol: "AUD/USD", Providers: []string{"finnhub", "fcs", "twelvedata"}},
				{Symbol: "USD/CAD", Providers: []string{"finnhub", "fcs", "twelvedata"}},
				{Symbol: "NZD/USD", Providers: []string{"finnhub", "fcs", "twelvedata"}},
				{Symbol: "XAG/USD", Providers: []string{"fcs", "twelvedata"}},
				{Symbol: "BRENT", Providers: []string{"fcs"}},
				{Symbol: "US100", Providers: []string{"fcs", "twelvedata"}},
				{Symbol: "XAU/USD", Providers: []string{"finnhub", "twelvedata", "fcs"}},
				{Symbol: "QQQ", Providers: []string{"finnhub", "twelvedata"}},
			},
		},
	}
}
