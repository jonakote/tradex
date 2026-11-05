package features

import (
	"testing"
	"time"

	"tradex/internal/domain"
)

const crtTimeframe = 4 * time.Hour

func crtCandle(openTime time.Time, open, high, low, close domain.Price) domain.Candle {
	return domain.Candle{
		Source: "test", Symbol: "EURUSD", Timeframe: crtTimeframe,
		OpenTime: openTime, CloseTime: openTime.Add(crtTimeframe),
		Open: open, High: high, Low: low, Close: close, TickCount: 100,
	}
}

// rangeCandleBase is 200 ticks tall: 100000 .. 100200.
func rangeCandleBase(openTime time.Time) domain.Candle {
	return crtCandle(openTime, 100050, 100200, 100000, 100150)
}

func TestDetectCandleRangeAcceptsBullishPurgeAndCloseBackInside(t *testing.T) {
	t0 := time.Date(2026, time.August, 18, 0, 0, 0, 0, time.UTC)
	c1 := rangeCandleBase(t0)
	// Purges the low by 40 ticks, closes 55 ticks back inside the range.
	c2 := crtCandle(c1.CloseTime, 100020, 100060, 99960, 100055)

	pattern, ok := DetectCandleRange(c1, c2, DefaultCandleRangeConfig())
	if !ok {
		t.Fatal("expected a bullish candle range")
	}
	if pattern.Kind != CandleRangeBullish {
		t.Fatalf("kind: got %s", pattern.Kind)
	}
	if pattern.Direction() != BreakBullish {
		t.Fatalf("direction: got %s", pattern.Direction())
	}
	if pattern.SweptLevel != 100000 || pattern.PurgeExtreme != 99960 {
		t.Fatalf("swept geometry: got %d / %d", pattern.SweptLevel, pattern.PurgeExtreme)
	}
	// Entry is the purge candle's equilibrium: 99960 + (100060-99960)/2.
	if pattern.EntryReference != 100010 {
		t.Fatalf("entry: got %d want 100010", pattern.EntryReference)
	}
	if pattern.StopReference != 99958 {
		t.Fatalf("stop: got %d want 99958", pattern.StopReference)
	}
	if pattern.TargetReference != 100200 {
		t.Fatalf("target: got %d want 100200", pattern.TargetReference)
	}
	if pattern.RiskTicks != 52 || pattern.RewardTicks != 190 {
		t.Fatalf("risk/reward ticks: got %d / %d", pattern.RiskTicks, pattern.RewardTicks)
	}
	if pattern.RewardRiskBps != 190*10000/52 {
		t.Fatalf("reward/risk: got %d", pattern.RewardRiskBps)
	}
	if !pattern.DetectedAt.Equal(c2.CloseTime) {
		t.Fatalf("detection must be knowable at the purge close, got %s", pattern.DetectedAt)
	}
	if pattern.SpecVersion != CandleRangeSpecVersion {
		t.Fatalf("spec version: got %q", pattern.SpecVersion)
	}
}

func TestDetectCandleRangeAcceptsBearishPurgeMirrorImage(t *testing.T) {
	t0 := time.Date(2026, time.August, 18, 0, 0, 0, 0, time.UTC)
	c1 := rangeCandleBase(t0)
	c2 := crtCandle(c1.CloseTime, 100180, 100240, 100140, 100145)

	pattern, ok := DetectCandleRange(c1, c2, DefaultCandleRangeConfig())
	if !ok {
		t.Fatal("expected a bearish candle range")
	}
	if pattern.Kind != CandleRangeBearish || pattern.Direction() != BreakBearish {
		t.Fatalf("kind/direction: got %s / %s", pattern.Kind, pattern.Direction())
	}
	if pattern.EntryReference != 100190 || pattern.StopReference != 100242 || pattern.TargetReference != 100000 {
		t.Fatalf("levels: entry %d stop %d target %d",
			pattern.EntryReference, pattern.StopReference, pattern.TargetReference)
	}
	if pattern.RiskTicks != 52 || pattern.RewardTicks != 190 {
		t.Fatalf("risk/reward ticks: got %d / %d", pattern.RiskTicks, pattern.RewardTicks)
	}
}

func TestDetectCandleRangeRejectsAmbiguousAndUnqualifiedPairs(t *testing.T) {
	t0 := time.Date(2026, time.August, 18, 0, 0, 0, 0, time.UTC)
	c1 := rangeCandleBase(t0)
	next := c1.CloseTime
	config := DefaultCandleRangeConfig()

	cases := []struct {
		name  string
		first domain.Candle
		purge domain.Candle
	}{
		{
			// Both extremes swept: direction is unknowable, so refuse to guess.
			name:  "both sides purged",
			first: c1,
			purge: crtCandle(next, 100100, 100240, 99960, 100100),
		},
		{
			name:  "no purge at all",
			first: c1,
			purge: crtCandle(next, 100050, 100150, 100020, 100100),
		},
		{
			// Wick through the low but closes below the reclaim threshold.
			name:  "closes outside the range",
			first: c1,
			purge: crtCandle(next, 100020, 100040, 99960, 99990),
		},
		{
			// 150-tick penetration of a 200-tick range is displacement.
			name:  "purge too deep",
			first: c1,
			purge: crtCandle(next, 100020, 100060, 99850, 100055),
		},
		{
			name:  "range candle too small",
			first: crtCandle(t0, 100100, 100110, 100100, 100105),
			purge: crtCandle(t0.Add(crtTimeframe), 100105, 100108, 100095, 100104),
		},
		{
			// Valid purge, but equilibrium entry leaves reward/risk under 1.5R.
			name:  "reward to risk below floor",
			first: c1,
			purge: crtCandle(next, 100150, 100180, 99950, 100120),
		},
		{
			// A gap in the stream changes which liquidity was swept.
			name:  "purge candle is not adjacent",
			first: c1,
			purge: crtCandle(next.Add(crtTimeframe), 100020, 100060, 99960, 100055),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := DetectCandleRange(tc.first, tc.purge, config); ok {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestDetectCandleRangeRejectsStreamAndConfigViolations(t *testing.T) {
	t0 := time.Date(2026, time.August, 18, 0, 0, 0, 0, time.UTC)
	c1 := rangeCandleBase(t0)
	c2 := crtCandle(c1.CloseTime, 100020, 100060, 99960, 100055)

	mixed := c2
	mixed.Symbol = "GBPUSD"
	if _, ok := DetectCandleRange(c1, mixed, DefaultCandleRangeConfig()); ok {
		t.Fatal("expected rejection across symbols")
	}

	invalid := c2
	invalid.TickCount = 0
	if _, ok := DetectCandleRange(c1, invalid, DefaultCandleRangeConfig()); ok {
		t.Fatal("expected rejection of an invalid candle")
	}

	badConfig := DefaultCandleRangeConfig()
	badConfig.EntryBps = 10000
	if _, ok := DetectCandleRange(c1, c2, badConfig); ok {
		t.Fatal("expected rejection under an invalid config")
	}
}

func TestCandleRangeConfigValidation(t *testing.T) {
	if err := DefaultCandleRangeConfig().Validate(); err != nil {
		t.Fatalf("default config must be valid: %v", err)
	}
	cases := map[string]func(*CandleRangeConfig){
		"range":       func(c *CandleRangeConfig) { c.MinRangeTicks = 0 },
		"purge":       func(c *CandleRangeConfig) { c.MinPurgeTicks = 0 },
		"max purge":   func(c *CandleRangeConfig) { c.MaxPurgeBps = 10001 },
		"reclaim":     func(c *CandleRangeConfig) { c.MinReclaimBps = 10000 },
		"entry":       func(c *CandleRangeConfig) { c.EntryBps = 0 },
		"stop buffer": func(c *CandleRangeConfig) { c.StopBufferTicks = -1 },
		"reward risk": func(c *CandleRangeConfig) { c.MinRewardRiskBps = 0 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			config := DefaultCandleRangeConfig()
			mutate(&config)
			if err := config.Validate(); err == nil {
				t.Fatal("expected a validation error")
			}
		})
	}
}

// Detection must be a pure function of its inputs: identical pairs must give
// byte-identical patterns on every call.
func TestDetectCandleRangeIsDeterministic(t *testing.T) {
	t0 := time.Date(2026, time.August, 18, 0, 0, 0, 0, time.UTC)
	c1 := rangeCandleBase(t0)
	c2 := crtCandle(c1.CloseTime, 100020, 100060, 99960, 100055)
	first, ok := DetectCandleRange(c1, c2, DefaultCandleRangeConfig())
	if !ok {
		t.Fatal("expected detection")
	}
	for i := 0; i < 50; i++ {
		again, ok := DetectCandleRange(c1, c2, DefaultCandleRangeConfig())
		if !ok || again != first {
			t.Fatalf("iteration %d diverged", i)
		}
	}
}
