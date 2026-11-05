package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// CircuitState for provider health tracking.
type CircuitState string

const (
	CircuitClosed   CircuitState = "CLOSED"
	CircuitOpen     CircuitState = "OPEN"
	CircuitHalfOpen CircuitState = "HALF_OPEN"
)

// providerCircuit tracks health for a single provider.
type providerCircuit struct {
	mu                  sync.Mutex
	state               CircuitState
	consecutiveFailures int
	threshold           int
	cooldown            time.Duration
	openedAt            time.Time
	lastFailureAt       time.Time
	lastSuccessAt       time.Time
}

func newProviderCircuit(threshold int, cooldown time.Duration) *providerCircuit {
	if threshold <= 0 {
		threshold = 5
	}
	if cooldown <= 0 {
		cooldown = 60 * time.Second
	}
	return &providerCircuit{
		state:     CircuitClosed,
		threshold: threshold,
		cooldown:  cooldown,
	}
}

func (c *providerCircuit) allow(now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch c.state {
	case CircuitClosed, CircuitHalfOpen:
		return true
	case CircuitOpen:
		if now.After(c.openedAt.Add(c.cooldown)) {
			c.state = CircuitHalfOpen
			return true
		}
		return false
	}
	return true
}

func (c *providerCircuit) recordSuccess(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state = CircuitClosed
	c.consecutiveFailures = 0
	c.lastSuccessAt = now
}

func (c *providerCircuit) recordFailure(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.consecutiveFailures++
	c.lastFailureAt = now
	if c.state == CircuitHalfOpen || c.consecutiveFailures >= c.threshold {
		c.state = CircuitOpen
		c.openedAt = now
	}
}

func (c *providerCircuit) getState() CircuitState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// ---------- Manager ----------

// Manager is the central Market Data Manager.
// All market data requests flow through this orchestrator.
// Strategies and bot components never call providers directly.
type Manager struct {
	registry *Registry
	cache    Cache
	dedup    *Deduplicator
	limiters *RateLimiterRegistry
	circuits map[string]*providerCircuit
	config   ManagerConfig
	clock    func() time.Time

	// Observability
	mu    sync.Mutex
	stats ManagerStats
}

// ManagerStats tracks operational metrics.
type ManagerStats struct {
	CacheHits      int64 `json:"cache_hits"`
	CacheMisses    int64 `json:"cache_misses"`
	DedupShared    int64 `json:"dedup_shared"`
	ProviderCalls  int64 `json:"provider_calls"`
	ProviderErrors int64 `json:"provider_errors"`
	RateLimited    int64 `json:"rate_limited"`
	CircuitOpen    int64 `json:"circuit_open"`
	Failovers      int64 `json:"failovers"`
}

// NewManager creates a MarketDataManager with all layers wired together.
func NewManager(cfg ManagerConfig, registry *Registry, cache Cache, clock func() time.Time) (*Manager, error) {
	if registry == nil {
		return nil, errors.New("manager requires a provider registry")
	}
	if cache == nil {
		return nil, errors.New("manager requires a cache")
	}
	if clock == nil {
		clock = time.Now
	}

	// Build circuit breakers per provider
	circuits := make(map[string]*providerCircuit)
	for name, pcfg := range cfg.Providers {
		if pcfg.Enabled {
			circuits[name] = newProviderCircuit(pcfg.CircuitThreshold, pcfg.CircuitCooldown)
		}
	}

	return &Manager{
		registry: registry,
		cache:    cache,
		dedup:    NewDeduplicator(),
		limiters: NewRateLimiterRegistry(cfg.Providers, clock),
		circuits: circuits,
		config:   cfg,
		clock:    clock,
	}, nil
}

// GetCandles is the primary entry point for candle data.
// It implements the full decision flow from the strategy document.
func (m *Manager) GetCandles(ctx context.Context, req CandleRequest) ([]Candle, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}

	cacheKey := req.CacheKey()

	// Step 1: Check cache
	cached, err := GetCachedCandles(ctx, m.cache, cacheKey)
	if err == nil && cached != nil {
		m.recordCacheHit()
		return cached, nil
	}
	m.recordCacheMiss()

	// Step 2: Deduplication — only one provider call for identical requests
	result := m.dedup.Do(cacheKey, func() ([]byte, error) {
		candles, err := m.fetchFromProviders(ctx, req)
		if err != nil {
			return nil, err
		}
		data, _ := json.Marshal(candles)
		return data, nil
	})

	if result.Shared {
		m.mu.Lock()
		m.stats.DedupShared++
		m.mu.Unlock()
	}

	if result.Err != nil {
		return nil, result.Err
	}

	var candles []Candle
	if err := json.Unmarshal(result.Data, &candles); err != nil {
		return nil, fmt.Errorf("unmarshal dedup result: %w", err)
	}

	// Step 6: Store in cache
	ttl := m.config.Cache.TTLForTimeframe(req.Timeframe)
	_ = CacheCandles(ctx, m.cache, cacheKey, candles, ttl)

	return candles, nil
}

// GetQuote is the primary entry point for real-time quotes.
func (m *Manager) GetQuote(ctx context.Context, req QuoteRequest) (Quote, error) {
	if req.Symbol == "" {
		return Quote{}, errors.New("quote request requires symbol")
	}

	cacheKey := req.CacheKey()

	// Step 1: Check cache
	cached, found, err := GetCachedQuote(ctx, m.cache, cacheKey)
	if err == nil && found {
		m.recordCacheHit()
		return cached, nil
	}
	m.recordCacheMiss()

	// Step 2: Deduplication
	result := m.dedup.Do(cacheKey, func() ([]byte, error) {
		quote, err := m.fetchQuoteFromProviders(ctx, req)
		if err != nil {
			return nil, err
		}
		data, _ := json.Marshal(quote)
		return data, nil
	})

	if result.Shared {
		m.mu.Lock()
		m.stats.DedupShared++
		m.mu.Unlock()
	}

	if result.Err != nil {
		return Quote{}, result.Err
	}

	var quote Quote
	if err := json.Unmarshal(result.Data, &quote); err != nil {
		return Quote{}, fmt.Errorf("unmarshal dedup result: %w", err)
	}

	// Cache
	_ = CacheQuote(ctx, m.cache, cacheKey, quote, m.config.Cache.QuoteTTL)

	return quote, nil
}

// fetchFromProviders tries each provider in priority order.
func (m *Manager) fetchFromProviders(ctx context.Context, req CandleRequest) ([]Candle, error) {
	providers := m.registry.ProvidersFor(req.Symbol)
	if len(providers) == 0 {
		return nil, fmt.Errorf("no providers registered for %s", req.Symbol)
	}

	var lastErr error
	for i, provider := range providers {
		if i > 0 {
			m.mu.Lock()
			m.stats.Failovers++
			m.mu.Unlock()
		}

		candles, err := m.tryProvider(ctx, provider, req)
		if err == nil {
			return candles, nil
		}
		lastErr = err
	}

	return nil, fmt.Errorf("all providers failed for %s: %w", req.Symbol, lastErr)
}

// tryProvider attempts a single provider with rate limit, circuit, and error handling.
func (m *Manager) tryProvider(ctx context.Context, provider MarketDataProvider, req CandleRequest) ([]Candle, error) {
	name := provider.Name()
	now := m.clock()

	// Step 3: Check circuit breaker
	circuit := m.circuits[name]
	if circuit != nil && !circuit.allow(now) {
		m.mu.Lock()
		m.stats.CircuitOpen++
		m.mu.Unlock()
		return nil, fmt.Errorf("provider %s circuit is open", name)
	}

	// Step 4: Check rate limit
	if !m.limiters.AllowProvider(name) {
		m.mu.Lock()
		m.stats.RateLimited++
		m.mu.Unlock()
		return nil, fmt.Errorf("provider %s rate limited", name)
	}

	// Step 5: Make the request
	m.mu.Lock()
	m.stats.ProviderCalls++
	m.mu.Unlock()

	candles, err := provider.GetCandles(ctx, req)
	if err != nil {
		m.handleProviderError(name, err)
		return nil, err
	}

	// Success — reset circuit
	if circuit != nil {
		circuit.recordSuccess(m.clock())
	}

	return candles, nil
}

// fetchQuoteFromProviders tries each provider for a quote.
func (m *Manager) fetchQuoteFromProviders(ctx context.Context, req QuoteRequest) (Quote, error) {
	providers := m.registry.ProvidersFor(req.Symbol)
	if len(providers) == 0 {
		return Quote{}, fmt.Errorf("no providers registered for %s", req.Symbol)
	}

	var lastErr error
	for i, provider := range providers {
		if i > 0 {
			m.mu.Lock()
			m.stats.Failovers++
			m.mu.Unlock()
		}

		quote, err := m.tryQuoteProvider(ctx, provider, req)
		if err == nil {
			return quote, nil
		}
		lastErr = err
	}

	return Quote{}, fmt.Errorf("all providers failed for %s: %w", req.Symbol, lastErr)
}

func (m *Manager) tryQuoteProvider(ctx context.Context, provider MarketDataProvider, req QuoteRequest) (Quote, error) {
	name := provider.Name()
	now := m.clock()

	circuit := m.circuits[name]
	if circuit != nil && !circuit.allow(now) {
		m.mu.Lock()
		m.stats.CircuitOpen++
		m.mu.Unlock()
		return Quote{}, fmt.Errorf("provider %s circuit is open", name)
	}

	if !m.limiters.AllowProvider(name) {
		m.mu.Lock()
		m.stats.RateLimited++
		m.mu.Unlock()
		return Quote{}, fmt.Errorf("provider %s rate limited", name)
	}

	m.mu.Lock()
	m.stats.ProviderCalls++
	m.mu.Unlock()

	quote, err := provider.GetQuote(ctx, req)
	if err != nil {
		m.handleProviderError(name, err)
		return Quote{}, err
	}

	if circuit != nil {
		circuit.recordSuccess(m.clock())
	}
	return quote, nil
}

// handleProviderError classifies the error and updates circuit/rate limiter.
func (m *Manager) handleProviderError(providerName string, err error) {
	m.mu.Lock()
	m.stats.ProviderErrors++
	m.mu.Unlock()

	now := m.clock()

	// Record failure in circuit breaker
	if circuit, ok := m.circuits[providerName]; ok {
		circuit.recordFailure(now)
	}

	// Check if it's a rate limit error
	var provErr *ProviderError
	if errors.As(err, &provErr) {
		if provErr.IsRateLimited() {
			limiter := m.limiters.Get(providerName)
			if limiter != nil {
				limiter.RecordRateLimit(provErr.RetryAfter)
			}
		}
	}
}

// Stats returns current operational metrics.
func (m *Manager) Stats() ManagerStats {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stats
}

// ProviderStatus returns the health state of all providers.
func (m *Manager) ProviderStatus() []ProviderStatusEntry {
	var entries []ProviderStatusEntry
	for _, provider := range m.registry.All() {
		name := provider.Name()
		entry := ProviderStatusEntry{
			Name:    name,
			Quota:   provider.RemainingQuota(),
			Circuit: CircuitClosed,
		}
		if circuit, ok := m.circuits[name]; ok {
			entry.Circuit = circuit.getState()
		}
		if limiter := m.limiters.Get(name); limiter != nil {
			entry.RemainingMinute = limiter.RemainingMinute()
			entry.RemainingDay = limiter.RemainingDay()
			total, rateLimited := limiter.Stats()
			entry.TotalRequests = total
			entry.RateLimitedCount = rateLimited
		}
		entries = append(entries, entry)
	}
	return entries
}

// ProviderStatusEntry summarizes one provider's operational state.
type ProviderStatusEntry struct {
	Name             string        `json:"name"`
	Circuit          CircuitState  `json:"circuit"`
	Quota            ProviderQuota `json:"quota"`
	RemainingMinute  int           `json:"remaining_minute"`
	RemainingDay     int64         `json:"remaining_day"`
	TotalRequests    int64         `json:"total_requests"`
	RateLimitedCount int64         `json:"rate_limited_count"`
}

// Close releases resources.
func (m *Manager) Close() error {
	return m.cache.Close()
}

// ---------- Helpers ----------

func (m *Manager) recordCacheHit() {
	m.mu.Lock()
	m.stats.CacheHits++
	m.mu.Unlock()
}

func (m *Manager) recordCacheMiss() {
	m.mu.Lock()
	m.stats.CacheMisses++
	m.mu.Unlock()
}
