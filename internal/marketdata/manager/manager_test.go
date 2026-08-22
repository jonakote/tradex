package manager

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newTestManager(t *testing.T, providers ...*fakeProvider) *Manager {
	t.Helper()
	names := make([]string, len(providers))
	for i, p := range providers {
		names[i] = p.name
	}

	reg, err := NewRegistry(RegistryConfig{DefaultProviders: names})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range providers {
		if err := reg.Register(p); err != nil {
			t.Fatal(err)
		}
	}

	provConfigs := make(map[string]ProviderConfig)
	for _, p := range providers {
		provConfigs[p.name] = ProviderConfig{
			Name:             p.name,
			Enabled:          true,
			RateLimit:        RateLimitConfig{RequestsPerMinute: 100, CreditsPerDay: 10000},
			CircuitThreshold: 3,
			CircuitCooldown:  5 * time.Second,
		}
	}

	cfg := ManagerConfig{
		Cache:     DefaultCacheConfig(),
		Stale:     DefaultStaleConfig(),
		Providers: provConfigs,
		Registry:  RegistryConfig{DefaultProviders: names},
	}

	cache := NewMemoryCache(1000, time.Now)
	mgr, err := NewManager(cfg, reg, cache, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	return mgr
}

func TestManagerGetCandlesCacheHit(t *testing.T) {
	provider := &fakeProvider{
		name: "alpha",
		candles: []Candle{
			{Symbol: "EUR/USD", Timeframe: Timeframe1h, OpenTime: time.Now(), Open: 1.08, High: 1.09, Low: 1.07, Close: 1.085, Provider: "alpha"},
		},
	}
	mgr := newTestManager(t, provider)
	defer mgr.Close()

	req := CandleRequest{Symbol: "EUR/USD", Timeframe: Timeframe1h, Limit: 5}

	// First call: cache miss, hits provider
	candles, err := mgr.GetCandles(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(candles) != 1 {
		t.Fatalf("expected 1 candle, got %d", len(candles))
	}
	if provider.callCount != 1 {
		t.Fatalf("expected 1 provider call, got %d", provider.callCount)
	}

	// Second call: should hit cache, no additional provider call
	candles, err = mgr.GetCandles(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(candles) != 1 {
		t.Fatalf("expected 1 candle, got %d", len(candles))
	}
	if provider.callCount != 1 {
		t.Fatalf("expected still 1 provider call (cache hit), got %d", provider.callCount)
	}

	stats := mgr.Stats()
	if stats.CacheHits != 1 {
		t.Fatalf("expected 1 cache hit, got %d", stats.CacheHits)
	}
	if stats.CacheMisses != 1 {
		t.Fatalf("expected 1 cache miss, got %d", stats.CacheMisses)
	}
}

func TestManagerGetCandlesDeduplication(t *testing.T) {
	var callCount int32
	provider := &fakeProvider{
		name: "alpha",
		candles: []Candle{
			{Symbol: "EUR/USD", Timeframe: Timeframe1h, OpenTime: time.Now(), Open: 1.08, High: 1.09, Low: 1.07, Close: 1.085, Provider: "alpha"},
		},
	}
	// Override GetCandles to track with atomic counter and add delay
	slowProvider := &slowFakeProvider{
		fakeProvider: provider,
		delay:        50 * time.Millisecond,
		callCount:    &callCount,
	}

	names := []string{"alpha"}
	reg, _ := NewRegistry(RegistryConfig{DefaultProviders: names})
	reg.Register(slowProvider)

	cfg := ManagerConfig{
		Cache: DefaultCacheConfig(),
		Stale: DefaultStaleConfig(),
		Providers: map[string]ProviderConfig{
			"alpha": {Name: "alpha", Enabled: true, RateLimit: RateLimitConfig{RequestsPerMinute: 100, CreditsPerDay: 10000}, CircuitThreshold: 5, CircuitCooldown: 5 * time.Second},
		},
		Registry: RegistryConfig{DefaultProviders: names},
	}
	cache := NewMemoryCache(1000, time.Now)
	mgr, _ := NewManager(cfg, reg, cache, time.Now)
	defer mgr.Close()

	req := CandleRequest{Symbol: "EUR/USD", Timeframe: Timeframe1h, Limit: 5}

	// Launch 5 concurrent requests for the same data
	var wg sync.WaitGroup
	results := make([][]Candle, 5)
	errs := make([]error, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx], errs[idx] = mgr.GetCandles(context.Background(), req)
		}(i)
	}
	wg.Wait()

	// Only ONE provider call should have been made
	if atomic.LoadInt32(&callCount) != 1 {
		t.Fatalf("expected 1 provider call (dedup), got %d", callCount)
	}

	// All results should be valid
	for i, err := range errs {
		if err != nil {
			t.Fatalf("request %d failed: %v", i, err)
		}
		if len(results[i]) != 1 {
			t.Fatalf("request %d: expected 1 candle, got %d", i, len(results[i]))
		}
	}
}

func TestManagerGetCandlesFailover(t *testing.T) {
	failing := &fakeProvider{
		name:      "primary",
		supported: map[string]bool{"EUR/USD": true},
		err:       errors.New("primary down"),
	}
	backup := &fakeProvider{
		name:      "secondary",
		supported: map[string]bool{"EUR/USD": true},
		candles: []Candle{
			{Symbol: "EUR/USD", Timeframe: Timeframe1h, OpenTime: time.Now(), Open: 1.08, High: 1.09, Low: 1.07, Close: 1.085, Provider: "secondary"},
		},
	}

	mgr := newTestManager(t, failing, backup)
	defer mgr.Close()

	req := CandleRequest{Symbol: "EUR/USD", Timeframe: Timeframe1h, Limit: 5}
	candles, err := mgr.GetCandles(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(candles) != 1 || candles[0].Provider != "secondary" {
		t.Fatalf("expected secondary provider result, got %+v", candles)
	}

	stats := mgr.Stats()
	if stats.Failovers != 1 {
		t.Fatalf("expected 1 failover, got %d", stats.Failovers)
	}
	if stats.ProviderErrors != 1 {
		t.Fatalf("expected 1 provider error, got %d", stats.ProviderErrors)
	}
}

func TestManagerGetCandlesCircuitBreaker(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	failing := &fakeProvider{
		name:      "primary",
		supported: map[string]bool{"EUR/USD": true},
		err:       errors.New("always fails"),
	}
	backup := &fakeProvider{
		name:      "secondary",
		supported: map[string]bool{"EUR/USD": true},
		candles: []Candle{
			{Symbol: "EUR/USD", Timeframe: Timeframe1h, OpenTime: now, Open: 1.08, High: 1.09, Low: 1.07, Close: 1.085, Provider: "secondary"},
		},
	}

	names := []string{"primary", "secondary"}
	reg, _ := NewRegistry(RegistryConfig{DefaultProviders: names})
	reg.Register(failing)
	reg.Register(backup)

	cfg := ManagerConfig{
		Cache: DefaultCacheConfig(),
		Stale: DefaultStaleConfig(),
		Providers: map[string]ProviderConfig{
			"primary":   {Name: "primary", Enabled: true, RateLimit: RateLimitConfig{RequestsPerMinute: 100, CreditsPerDay: 10000}, CircuitThreshold: 2, CircuitCooldown: 10 * time.Second},
			"secondary": {Name: "secondary", Enabled: true, RateLimit: RateLimitConfig{RequestsPerMinute: 100, CreditsPerDay: 10000}, CircuitThreshold: 5, CircuitCooldown: 10 * time.Second},
		},
		Registry: RegistryConfig{DefaultProviders: names},
	}
	cache := NewMemoryCache(1000, func() time.Time { return now })
	mgr, _ := NewManager(cfg, reg, cache, func() time.Time { return now })
	defer mgr.Close()

	// First two calls fail primary, trip the circuit after 2 failures
	// Each also fails over to secondary successfully
	for i := 0; i < 2; i++ {
		// Need fresh cache key each time to bypass cache
		r := CandleRequest{Symbol: "EUR/USD", Timeframe: Timeframe1h, Limit: i + 1}
		_, err := mgr.GetCandles(context.Background(), r)
		if err != nil {
			t.Fatalf("call %d failed: %v", i, err)
		}
	}

	// After 2 failures, primary's circuit should be open
	circuit := mgr.circuits["primary"]
	if circuit.getState() != CircuitOpen {
		t.Fatalf("expected OPEN, got %s", circuit.getState())
	}

	// Next call should skip primary entirely (circuit open) and go straight to secondary
	failing.callCount = 0
	r := CandleRequest{Symbol: "EUR/USD", Timeframe: Timeframe1h, Limit: 99}
	_, err := mgr.GetCandles(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
}

func TestManagerGetCandlesRateLimited(t *testing.T) {
	provider := &fakeProvider{
		name: "limited",
		candles: []Candle{
			{Symbol: "EUR/USD", Timeframe: Timeframe1h, OpenTime: time.Now(), Open: 1.08, High: 1.09, Low: 1.07, Close: 1.085, Provider: "limited"},
		},
	}

	names := []string{"limited"}
	reg, _ := NewRegistry(RegistryConfig{DefaultProviders: names})
	reg.Register(provider)

	cfg := ManagerConfig{
		Cache: DefaultCacheConfig(),
		Stale: DefaultStaleConfig(),
		Providers: map[string]ProviderConfig{
			"limited": {Name: "limited", Enabled: true, RateLimit: RateLimitConfig{RequestsPerMinute: 1, CreditsPerDay: 100}, CircuitThreshold: 5, CircuitCooldown: 10 * time.Second},
		},
		Registry: RegistryConfig{DefaultProviders: names},
	}
	cache := NewMemoryCache(1000, time.Now)
	mgr, _ := NewManager(cfg, reg, cache, time.Now)
	defer mgr.Close()

	// First request uses the 1 allowed per minute
	r1 := CandleRequest{Symbol: "EUR/USD", Timeframe: Timeframe1h, Limit: 1}
	_, err := mgr.GetCandles(context.Background(), r1)
	if err != nil {
		t.Fatal(err)
	}

	// Second request (different cache key) should be rate limited
	r2 := CandleRequest{Symbol: "EUR/USD", Timeframe: Timeframe1h, Limit: 2}
	_, err = mgr.GetCandles(context.Background(), r2)
	if err == nil {
		t.Fatal("expected rate limit error")
	}

	stats := mgr.Stats()
	if stats.RateLimited < 1 {
		t.Fatalf("expected rate_limited >= 1, got %d", stats.RateLimited)
	}
}

func TestManagerGetQuote(t *testing.T) {
	provider := &fakeProvider{
		name:  "alpha",
		quote: Quote{Symbol: "XAU/USD", Bid: 1950, Ask: 1950.5, Price: 1950.25, Provider: "alpha", FetchedAt: time.Now()},
	}
	mgr := newTestManager(t, provider)
	defer mgr.Close()

	quote, err := mgr.GetQuote(context.Background(), QuoteRequest{Symbol: "XAU/USD"})
	if err != nil {
		t.Fatal(err)
	}
	if quote.Symbol != "XAU/USD" || quote.Bid != 1950 {
		t.Fatalf("unexpected: %+v", quote)
	}

	// Second call should be cached
	quote, err = mgr.GetQuote(context.Background(), QuoteRequest{Symbol: "XAU/USD"})
	if err != nil {
		t.Fatal(err)
	}
	if provider.callCount != 1 {
		t.Fatalf("expected 1 call (cache hit), got %d", provider.callCount)
	}
}

func TestManagerProviderStatus(t *testing.T) {
	provider := &fakeProvider{
		name:  "alpha",
		quota: ProviderQuota{Provider: "alpha", CreditsRemaining: 790, DailyLimit: 800},
	}
	mgr := newTestManager(t, provider)
	defer mgr.Close()

	status := mgr.ProviderStatus()
	if len(status) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(status))
	}
	if status[0].Name != "alpha" {
		t.Fatalf("unexpected name: %s", status[0].Name)
	}
	if status[0].Circuit != CircuitClosed {
		t.Fatalf("expected CLOSED, got %s", status[0].Circuit)
	}
}

func TestManagerRejectsInvalidRequest(t *testing.T) {
	provider := &fakeProvider{name: "alpha"}
	mgr := newTestManager(t, provider)
	defer mgr.Close()

	_, err := mgr.GetCandles(context.Background(), CandleRequest{})
	if err == nil {
		t.Fatal("accepted empty request")
	}
}

func TestManagerNoProviders(t *testing.T) {
	reg, _ := NewRegistry(RegistryConfig{DefaultProviders: []string{"missing"}})
	cache := NewMemoryCache(100, time.Now)
	cfg := DefaultManagerConfig()
	mgr, _ := NewManager(cfg, reg, cache, time.Now)
	defer mgr.Close()

	_, err := mgr.GetCandles(context.Background(), CandleRequest{Symbol: "EUR/USD", Timeframe: Timeframe1h, Limit: 5})
	if err == nil {
		t.Fatal("expected error with no registered providers")
	}
}

// ---------- Helpers ----------

// slowFakeProvider wraps fakeProvider with a delay for dedup testing.
type slowFakeProvider struct {
	*fakeProvider
	delay     time.Duration
	callCount *int32
}

func (s *slowFakeProvider) GetCandles(ctx context.Context, req CandleRequest) ([]Candle, error) {
	atomic.AddInt32(s.callCount, 1)
	time.Sleep(s.delay)
	return s.fakeProvider.GetCandles(ctx, req)
}

func (s *slowFakeProvider) Name() string            { return s.fakeProvider.Name() }
func (s *slowFakeProvider) Supports(i Instrument) bool { return s.fakeProvider.Supports(i) }
func (s *slowFakeProvider) GetQuote(ctx context.Context, req QuoteRequest) (Quote, error) {
	return s.fakeProvider.GetQuote(ctx, req)
}
func (s *slowFakeProvider) RemainingQuota() ProviderQuota { return s.fakeProvider.RemainingQuota() }
