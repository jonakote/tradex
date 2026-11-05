package manager

import (
	"math"
	"math/rand"
	"sync"
	"time"
)

// RateLimiter enforces per-provider rate limits using a multi-window token bucket.
// Each provider has independent limits that are never shared or pooled.
type RateLimiter struct {
	mu     sync.Mutex
	config RateLimitConfig
	clock  func() time.Time

	// Token buckets for each window
	secondTokens int
	secondReset  time.Time

	minuteTokens int
	minuteReset  time.Time

	dayCredits int64
	dayReset   time.Time

	// Cooldown: when set, all requests blocked until this time
	cooldownUntil time.Time

	// Tracking
	totalRequests    int64
	rateLimitedCount int64
}

// NewRateLimiter creates a rate limiter for a single provider.
func NewRateLimiter(cfg RateLimitConfig, clock func() time.Time) *RateLimiter {
	if clock == nil {
		clock = time.Now
	}
	now := clock()
	return &RateLimiter{
		config:       cfg,
		clock:        clock,
		secondTokens: cfg.RequestsPerSecond,
		secondReset:  now.Add(time.Second),
		minuteTokens: maxInt(cfg.RequestsPerMinute, cfg.CreditsPerMinute),
		minuteReset:  now.Add(time.Minute),
		dayCredits:   maxInt64(cfg.RequestsPerDay, cfg.CreditsPerDay),
		dayReset:     startOfNextDay(now),
	}
}

// Allow returns true if a request is permitted right now.
// If not, it returns false and the caller should not make the request.
func (r *RateLimiter) Allow() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.clock()

	// Check cooldown
	if !r.cooldownUntil.IsZero() && now.Before(r.cooldownUntil) {
		r.rateLimitedCount++
		return false
	}
	r.cooldownUntil = time.Time{} // Clear expired cooldown

	// Refresh windows
	r.refreshWindows(now)

	// Check per-second
	if r.config.RequestsPerSecond > 0 && r.secondTokens <= 0 {
		r.rateLimitedCount++
		return false
	}

	// Check per-minute
	minuteLimit := maxInt(r.config.RequestsPerMinute, r.config.CreditsPerMinute)
	if minuteLimit > 0 && r.minuteTokens <= 0 {
		r.rateLimitedCount++
		return false
	}

	// Check per-day
	dayLimit := maxInt64(r.config.RequestsPerDay, r.config.CreditsPerDay)
	if dayLimit > 0 && r.dayCredits <= 0 {
		r.rateLimitedCount++
		return false
	}

	// Consume tokens
	if r.config.RequestsPerSecond > 0 {
		r.secondTokens--
	}
	if minuteLimit > 0 {
		r.minuteTokens--
	}
	if dayLimit > 0 {
		r.dayCredits--
	}

	r.totalRequests++
	return true
}

// WaitTime returns how long to wait before the next request is allowed.
// Returns 0 if a request is allowed immediately.
func (r *RateLimiter) WaitTime() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.clock()

	// Cooldown active
	if !r.cooldownUntil.IsZero() && now.Before(r.cooldownUntil) {
		return r.cooldownUntil.Sub(now)
	}

	r.refreshWindows(now)

	// Per-second exhausted
	if r.config.RequestsPerSecond > 0 && r.secondTokens <= 0 {
		return r.secondReset.Sub(now)
	}

	// Per-minute exhausted
	minuteLimit := maxInt(r.config.RequestsPerMinute, r.config.CreditsPerMinute)
	if minuteLimit > 0 && r.minuteTokens <= 0 {
		return r.minuteReset.Sub(now)
	}

	// Per-day exhausted
	dayLimit := maxInt64(r.config.RequestsPerDay, r.config.CreditsPerDay)
	if dayLimit > 0 && r.dayCredits <= 0 {
		return r.dayReset.Sub(now)
	}

	return 0
}

// RecordRateLimit is called when a 429 response is received.
// It sets a cooldown period and optionally uses the Retry-After value.
func (r *RateLimiter) RecordRateLimit(retryAfter time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.clock()
	r.rateLimitedCount++

	if retryAfter > 0 {
		r.cooldownUntil = now.Add(retryAfter)
	} else {
		// Default: cooldown for the remainder of the current minute window
		remaining := r.minuteReset.Sub(now)
		if remaining < 5*time.Second {
			remaining = 10 * time.Second
		}
		r.cooldownUntil = now.Add(remaining)
	}
}

// RemainingMinute returns the remaining per-minute capacity.
func (r *RateLimiter) RemainingMinute() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refreshWindows(r.clock())
	return r.minuteTokens
}

// RemainingDay returns the remaining per-day capacity.
func (r *RateLimiter) RemainingDay() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refreshWindows(r.clock())
	return r.dayCredits
}

// Stats returns rate limiter statistics.
func (r *RateLimiter) Stats() (total int64, rateLimited int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.totalRequests, r.rateLimitedCount
}

func (r *RateLimiter) refreshWindows(now time.Time) {
	if now.After(r.secondReset) || now.Equal(r.secondReset) {
		r.secondTokens = r.config.RequestsPerSecond
		r.secondReset = now.Add(time.Second)
	}
	if now.After(r.minuteReset) || now.Equal(r.minuteReset) {
		r.minuteTokens = maxInt(r.config.RequestsPerMinute, r.config.CreditsPerMinute)
		r.minuteReset = now.Add(time.Minute)
	}
	if now.After(r.dayReset) || now.Equal(r.dayReset) {
		r.dayCredits = maxInt64(r.config.RequestsPerDay, r.config.CreditsPerDay)
		r.dayReset = startOfNextDay(now)
	}
}

func startOfNextDay(t time.Time) time.Time {
	year, month, day := t.UTC().Date()
	return time.Date(year, month, day+1, 0, 0, 0, 0, time.UTC)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// ---------- Backoff ----------

// Backoff computes exponential backoff with jitter.
type Backoff struct {
	BaseDelay  time.Duration
	MaxDelay   time.Duration
	Multiplier float64
	attempt    int
}

// NewBackoff creates a backoff calculator.
func NewBackoff(baseDelay, maxDelay time.Duration, multiplier float64) *Backoff {
	if multiplier <= 1 {
		multiplier = 2.0
	}
	return &Backoff{
		BaseDelay:  baseDelay,
		MaxDelay:   maxDelay,
		Multiplier: multiplier,
	}
}

// Next returns the next backoff duration and increments the attempt counter.
func (b *Backoff) Next() time.Duration {
	delay := float64(b.BaseDelay) * math.Pow(b.Multiplier, float64(b.attempt))
	if delay > float64(b.MaxDelay) {
		delay = float64(b.MaxDelay)
	}
	// Add jitter: ±25%
	jitter := delay * 0.25 * (rand.Float64()*2 - 1)
	delay += jitter
	b.attempt++
	return time.Duration(delay)
}

// Reset resets the attempt counter.
func (b *Backoff) Reset() {
	b.attempt = 0
}

// Attempt returns the current attempt number.
func (b *Backoff) Attempt() int {
	return b.attempt
}

// ---------- Rate limiter registry ----------

// RateLimiterRegistry manages rate limiters for all providers.
type RateLimiterRegistry struct {
	mu       sync.RWMutex
	limiters map[string]*RateLimiter
}

// NewRateLimiterRegistry creates a registry from provider configurations.
func NewRateLimiterRegistry(configs map[string]ProviderConfig, clock func() time.Time) *RateLimiterRegistry {
	reg := &RateLimiterRegistry{
		limiters: make(map[string]*RateLimiter, len(configs)),
	}
	for name, cfg := range configs {
		if cfg.Enabled {
			reg.limiters[name] = NewRateLimiter(cfg.RateLimit, clock)
		}
	}
	return reg
}

// Get returns the rate limiter for a provider.
func (r *RateLimiterRegistry) Get(provider string) *RateLimiter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.limiters[provider]
}

// AllowProvider checks if the named provider can accept a request.
func (r *RateLimiterRegistry) AllowProvider(provider string) bool {
	limiter := r.Get(provider)
	if limiter == nil {
		return true // No limiter = no limit (unknown provider)
	}
	return limiter.Allow()
}
