package manager

import (
	"testing"
	"time"
)

func TestRateLimiterAllowsWithinLimit(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	limiter := NewRateLimiter(RateLimitConfig{
		RequestsPerMinute: 5,
		CreditsPerDay:     100,
	}, func() time.Time { return now })

	for i := 0; i < 5; i++ {
		if !limiter.Allow() {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}
	// 6th should be blocked
	if limiter.Allow() {
		t.Fatal("6th request should be blocked (minute limit)")
	}
}

func TestRateLimiterPerSecondLimit(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	limiter := NewRateLimiter(RateLimitConfig{
		RequestsPerSecond: 2,
		RequestsPerMinute: 100,
	}, func() time.Time { return now })

	if !limiter.Allow() {
		t.Fatal("1st should be allowed")
	}
	if !limiter.Allow() {
		t.Fatal("2nd should be allowed")
	}
	if limiter.Allow() {
		t.Fatal("3rd should be blocked (per-second)")
	}

	// Advance 1 second
	now = now.Add(time.Second)
	if !limiter.Allow() {
		t.Fatal("should be allowed after second reset")
	}
}

func TestRateLimiterDayLimitExhaustion(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	limiter := NewRateLimiter(RateLimitConfig{
		RequestsPerMinute: 100,
		CreditsPerDay:     3,
	}, func() time.Time { return now })

	for i := 0; i < 3; i++ {
		if !limiter.Allow() {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}
	if limiter.Allow() {
		t.Fatal("should be blocked (day limit)")
	}

	// Advance to next day
	now = time.Date(2026, 8, 21, 0, 0, 1, 0, time.UTC)
	if !limiter.Allow() {
		t.Fatal("should be allowed after day reset")
	}
}

func TestRateLimiterMinuteWindowReset(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	limiter := NewRateLimiter(RateLimitConfig{
		RequestsPerMinute: 3,
		CreditsPerDay:     1000,
	}, func() time.Time { return now })

	// Exhaust minute
	for i := 0; i < 3; i++ {
		limiter.Allow()
	}
	if limiter.Allow() {
		t.Fatal("should be blocked")
	}

	// Advance past minute window
	now = now.Add(61 * time.Second)
	if !limiter.Allow() {
		t.Fatal("should be allowed after minute reset")
	}
}

func TestRateLimiterCooldownOnRateLimit(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	limiter := NewRateLimiter(RateLimitConfig{
		RequestsPerMinute: 100,
		CreditsPerDay:     1000,
	}, func() time.Time { return now })

	// Record a 429 with Retry-After
	limiter.RecordRateLimit(30 * time.Second)

	// Should be blocked for 30 seconds
	if limiter.Allow() {
		t.Fatal("should be blocked during cooldown")
	}

	// Wait time should be ~30s
	wait := limiter.WaitTime()
	if wait < 29*time.Second || wait > 31*time.Second {
		t.Fatalf("unexpected wait: %v", wait)
	}

	// Advance past cooldown
	now = now.Add(31 * time.Second)
	if !limiter.Allow() {
		t.Fatal("should be allowed after cooldown")
	}
}

func TestRateLimiterCooldownDefaultsWhenNoRetryAfter(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 30, 0, time.UTC)
	limiter := NewRateLimiter(RateLimitConfig{
		RequestsPerMinute: 100,
		CreditsPerDay:     1000,
	}, func() time.Time { return now })

	limiter.RecordRateLimit(0)
	if limiter.Allow() {
		t.Fatal("should be blocked after rate limit without Retry-After")
	}
}

func TestRateLimiterWaitTimeZeroWhenAllowed(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	limiter := NewRateLimiter(RateLimitConfig{
		RequestsPerMinute: 10,
		CreditsPerDay:     1000,
	}, func() time.Time { return now })

	wait := limiter.WaitTime()
	if wait != 0 {
		t.Fatalf("expected 0, got %v", wait)
	}
}

func TestRateLimiterStats(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	limiter := NewRateLimiter(RateLimitConfig{
		RequestsPerMinute: 2,
		CreditsPerDay:     1000,
	}, func() time.Time { return now })

	limiter.Allow()
	limiter.Allow()
	limiter.Allow() // blocked

	total, rateLimited := limiter.Stats()
	if total != 2 {
		t.Fatalf("expected 2 total, got %d", total)
	}
	if rateLimited != 1 {
		t.Fatalf("expected 1 rate limited, got %d", rateLimited)
	}
}

func TestRateLimiterRemainingCounters(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	limiter := NewRateLimiter(RateLimitConfig{
		RequestsPerMinute: 8,
		CreditsPerDay:     800,
	}, func() time.Time { return now })

	if limiter.RemainingMinute() != 8 {
		t.Fatalf("expected 8 remaining minute, got %d", limiter.RemainingMinute())
	}
	if limiter.RemainingDay() != 800 {
		t.Fatalf("expected 800 remaining day, got %d", limiter.RemainingDay())
	}

	limiter.Allow()
	if limiter.RemainingMinute() != 7 {
		t.Fatalf("expected 7 after one call, got %d", limiter.RemainingMinute())
	}
	if limiter.RemainingDay() != 799 {
		t.Fatalf("expected 799 after one call, got %d", limiter.RemainingDay())
	}
}

// ---------- Backoff tests ----------

func TestBackoffExponential(t *testing.T) {
	b := NewBackoff(time.Second, 30*time.Second, 2.0)

	d1 := b.Next()
	d2 := b.Next()
	d3 := b.Next()

	// d1 ~ 1s, d2 ~ 2s, d3 ~ 4s (with jitter)
	if d1 < 750*time.Millisecond || d1 > 1250*time.Millisecond {
		t.Fatalf("d1 out of range: %v", d1)
	}
	if d2 < 1500*time.Millisecond || d2 > 2500*time.Millisecond {
		t.Fatalf("d2 out of range: %v", d2)
	}
	if d3 < 3*time.Second || d3 > 5*time.Second {
		t.Fatalf("d3 out of range: %v", d3)
	}
}

func TestBackoffCapsAtMaxDelay(t *testing.T) {
	b := NewBackoff(time.Second, 5*time.Second, 2.0)

	// After many attempts, should cap at MaxDelay
	for i := 0; i < 20; i++ {
		d := b.Next()
		if d > 5*time.Second+2*time.Second { // max + jitter
			t.Fatalf("attempt %d exceeded max: %v", i, d)
		}
	}
}

func TestBackoffReset(t *testing.T) {
	b := NewBackoff(time.Second, 30*time.Second, 2.0)
	b.Next()
	b.Next()
	b.Next()
	if b.Attempt() != 3 {
		t.Fatal("attempt counter wrong")
	}
	b.Reset()
	if b.Attempt() != 0 {
		t.Fatal("should be 0 after reset")
	}
	d := b.Next()
	if d > 1500*time.Millisecond {
		t.Fatalf("after reset should be ~1s, got %v", d)
	}
}

// ---------- Registry tests ----------

func TestRateLimiterRegistryAllowProvider(t *testing.T) {
	configs := map[string]ProviderConfig{
		"alpha": {Name: "alpha", Enabled: true, RateLimit: RateLimitConfig{RequestsPerMinute: 2, CreditsPerDay: 100}},
		"beta":  {Name: "beta", Enabled: true, RateLimit: RateLimitConfig{RequestsPerMinute: 100, CreditsPerDay: 1000}},
	}
	reg := NewRateLimiterRegistry(configs, time.Now)

	// Alpha: allow 2
	if !reg.AllowProvider("alpha") {
		t.Fatal("alpha 1st should be allowed")
	}
	if !reg.AllowProvider("alpha") {
		t.Fatal("alpha 2nd should be allowed")
	}
	if reg.AllowProvider("alpha") {
		t.Fatal("alpha 3rd should be blocked")
	}

	// Beta still has capacity
	if !reg.AllowProvider("beta") {
		t.Fatal("beta should be allowed")
	}

	// Unknown provider (no limiter) always allowed
	if !reg.AllowProvider("unknown") {
		t.Fatal("unknown should be allowed")
	}
}

func TestRateLimiterRegistryDisabledProvider(t *testing.T) {
	configs := map[string]ProviderConfig{
		"disabled": {Name: "disabled", Enabled: false, RateLimit: RateLimitConfig{RequestsPerMinute: 1}},
	}
	reg := NewRateLimiterRegistry(configs, time.Now)

	// Disabled provider has no limiter, so always allowed
	if !reg.AllowProvider("disabled") {
		t.Fatal("disabled provider should have no limiter")
	}
}
