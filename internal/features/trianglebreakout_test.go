package features

import (
	"testing"
	"time"

	"tradex/internal/domain"
)

// tbBar builds a closed 1h EURUSD candle from integer ticks.
func tbBar(symbol string, index int, open, high, low, close int64) domain.Candle {
	openTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(index) * time.Hour)
	return domain.Candle{
		Source: "test", Symbol: symbol, Timeframe: time.Hour,
		OpenTime: openTime, CloseTime: openTime.Add(time.Hour),
		Open: domain.Price(open), High: domain.Price(high),
		Low: domain.Price(low), Close: domain.Price(close),
		TickCount: 100,
	}
}

func tbTestConfig() TriangleBreakoutConfig {
	config := DefaultTriangleBreakoutConfig()
	config.SwingLeft = 2
	config.SwingRight = 2
	return config
}

// ascendingTriangleWindow builds a contracting range: a flat resistance at
// `top`, touched repeatedly, and a rising support line, each a confirmed
// swing for features.DetectTriangle.
func ascendingTriangleWindow(touches int, legLen int, top int64, increment int64) []domain.Candle {
	var out []domain.Candle
	idx := 0
	lowStart := top - 2000
	for touch := 0; touch < touches; touch++ {
		support := lowStart + int64(touch)*increment // rising support
		half := legLen / 2
		for i := 0; i < half; i++ {
			frac := int64(i + 1)
			h := support + frac*(top-support)/int64(half)
			out = append(out, tbBar("EURUSD", idx, h-20, h, h-40, h-10))
			idx++
		}
		for i := 0; i < legLen-half; i++ {
			frac := int64(i + 1)
			nextSupport := lowStart + int64(touch+1)*increment
			l := top - frac*(top-nextSupport)/int64(legLen-half)
			out = append(out, tbBar("EURUSD", idx, l+20, l+40, l, l+10))
			idx++
		}
	}
	return out
}

// ascendingTriangleBreakoutWindow is the 6-touch ascending triangle plus a
// strong breakout candle above the flat top.
func ascendingTriangleBreakoutWindow() []domain.Candle {
	window := ascendingTriangleWindow(6, 14, 110000, 150)
	last := window[len(window)-1]
	window = append(window, tbBar("EURUSD", len(window),
		int64(last.Close), int64(last.Close)+1500, int64(last.Close)-10, int64(last.Close)+1400))
	return window
}

func mirrorTBWindow(window []domain.Candle, axis domain.Price) []domain.Candle {
	var out []domain.Candle
	for i, c := range window {
		out = append(out, tbBar("EURUSD", i,
			int64(axis-c.Open), int64(axis-c.Low), int64(axis-c.High), int64(axis-c.Close)))
	}
	return out
}

const tbMirrorAxis = domain.Price(300000)

func TestDetectTriangleBreakoutBullish(t *testing.T) {
	config := tbTestConfig()
	window := ascendingTriangleBreakoutWindow()
	setup, ok := DetectTriangleBreakout(window, 2, config)
	if !ok {
		t.Fatal("expected a bullish triangle breakout setup")
	}
	if setup.Kind != TriangleAscending {
		t.Errorf("Kind = %s", setup.Kind)
	}
	if setup.BreakoutDirection != BreakBullish {
		t.Errorf("BreakoutDirection = %s", setup.BreakoutDirection)
	}
	if setup.Entry != 110310 {
		t.Errorf("Entry = %d, want 110310", setup.Entry)
	}
	if setup.Stop != 108900 {
		t.Errorf("Stop = %d, want 108900", setup.Stop)
	}
	if setup.Target != 113130 {
		t.Errorf("Target = %d, want 113130", setup.Target)
	}
	if setup.RiskTicks != 1410 || setup.RewardTicks != 2820 {
		t.Errorf("risk/reward = %d/%d, want 1410/2820", setup.RiskTicks, setup.RewardTicks)
	}
	if setup.RewardRiskBps != 20000 {
		t.Errorf("RewardRiskBps = %d, want 20000", setup.RewardRiskBps)
	}
	if setup.SpecVersion != TriangleBreakoutSpecVersion {
		t.Errorf("SpecVersion = %q", setup.SpecVersion)
	}
	wantAt := window[len(window)-1].CloseTime
	if !setup.DetectedAt.Equal(wantAt) {
		t.Errorf("DetectedAt = %s, want %s", setup.DetectedAt, wantAt)
	}
}

// Mirror test: reflecting the fixture around a price axis (swapping
// high/low) must produce the exact mirrored descending-triangle setup
// with identical risk/reward.
func TestDetectTriangleBreakoutMirror(t *testing.T) {
	config := tbTestConfig()
	window := ascendingTriangleBreakoutWindow()
	bullish, ok := DetectTriangleBreakout(window, 2, config)
	if !ok {
		t.Fatal("expected a bullish setup")
	}

	mirrored := mirrorTBWindow(window, tbMirrorAxis)
	bearish, ok := DetectTriangleBreakout(mirrored, 2, config)
	if !ok {
		t.Fatal("expected the mirrored bearish setup")
	}
	if bearish.Kind != TriangleDescending {
		t.Errorf("Kind = %s", bearish.Kind)
	}
	if bearish.BreakoutDirection != BreakBearish {
		t.Errorf("BreakoutDirection = %s", bearish.BreakoutDirection)
	}
	if bearish.Entry != tbMirrorAxis-bullish.Entry {
		t.Errorf("Entry = %d, want %d", bearish.Entry, tbMirrorAxis-bullish.Entry)
	}
	if bearish.RiskTicks != bullish.RiskTicks || bearish.RewardTicks != bullish.RewardTicks {
		t.Errorf("mirrored risk/reward = %d/%d, want %d/%d",
			bearish.RiskTicks, bearish.RewardTicks, bullish.RiskTicks, bullish.RewardTicks)
	}
	if bearish.RewardRiskBps != bullish.RewardRiskBps {
		t.Errorf("mirrored RewardRiskBps = %d, want %d", bearish.RewardRiskBps, bullish.RewardRiskBps)
	}
}

// Determinism test: the same window run many times must give a
// byte-identical struct.
func TestDetectTriangleBreakoutIsDeterministic(t *testing.T) {
	config := tbTestConfig()
	window := ascendingTriangleBreakoutWindow()
	first, ok := DetectTriangleBreakout(window, 2, config)
	if !ok {
		t.Fatal("expected a setup")
	}
	for i := 0; i < 50; i++ {
		again, ok := DetectTriangleBreakout(window, 2, config)
		if !ok || again != first {
			t.Fatalf("detection is not deterministic at iteration %d", i)
		}
	}
}

// A breakout that was already true on the prior bar is not a NEW breakout
// and must be rejected, matching the live engine's once-only
// triangleSignaled semantics recomputed statelessly.
func TestDetectTriangleBreakoutRejectsNotNewBreakout(t *testing.T) {
	config := tbTestConfig()
	window := ascendingTriangleBreakoutWindow()
	last := window[len(window)-1]
	window = append(window, tbBar("EURUSD", len(window),
		int64(last.Close), int64(last.Close)+50, int64(last.Close)-50, int64(last.Close)+10))
	if _, ok := DetectTriangleBreakout(window, 2, config); ok {
		t.Error("a breakout already true on the prior bar must not re-signal")
	}
}

// No close beyond either projected boundary means no breakout at all.
func TestDetectTriangleBreakoutRejectsNoBreakout(t *testing.T) {
	config := tbTestConfig()
	window := ascendingTriangleBreakoutWindow()
	window = window[:len(window)-1]
	window = append(window, tbBar("EURUSD", len(window), 109500, 109600, 109400, 109500))
	if _, ok := DetectTriangleBreakout(window, 2, config); ok {
		t.Error("a close that stays inside both boundaries must be rejected")
	}
}

// Integer-truncation in the reward:risk floor must reject rather than
// silently pass.
func TestDetectTriangleBreakoutRejectsRewardRiskFloor(t *testing.T) {
	config := tbTestConfig()
	config.MinRewardRiskBps = 23333
	window := ascendingTriangleBreakoutWindow()
	if _, ok := DetectTriangleBreakout(window, 2, config); ok {
		t.Error("geometry truncating below the reward/risk floor must be rejected")
	}
}

// The cost floor: the stop must be meaningfully wider than the spread.
func TestDetectTriangleBreakoutRejectsSpreadFloor(t *testing.T) {
	config := tbTestConfig()
	window := ascendingTriangleBreakoutWindow()
	if _, ok := DetectTriangleBreakout(window, 2, config); !ok {
		t.Fatal("a 2-tick spread should clear the cost floor")
	}
	// Risk is 1410 ticks; a 400-tick spread at the default 4x multiple
	// needs 1600 ticks of risk, which this setup does not have.
	if _, ok := DetectTriangleBreakout(window, 400, config); ok {
		t.Error("a spread that fails the MinStopSpreadMultiple floor must be rejected")
	}
	if _, ok := DetectTriangleBreakout(window, 0, config); !ok {
		t.Error("an unreported spread must disable the floor, not reject")
	}
	if _, ok := DetectTriangleBreakout(window, -1, config); ok {
		t.Error("a negative spread is invalid input")
	}
}

func TestDetectTriangleBreakoutRequiresEnoughHistory(t *testing.T) {
	config := tbTestConfig()
	window := ascendingTriangleBreakoutWindow()
	if _, ok := DetectTriangleBreakout(window[:config.RequiredBars()-1], 2, config); ok {
		t.Error("a short window must return false, not a partial judgement")
	}
}

func TestTriangleBreakoutConfigValidate(t *testing.T) {
	if err := DefaultTriangleBreakoutConfig().Validate(); err != nil {
		t.Fatalf("default config invalid: %v", err)
	}
	cases := map[string]func(*TriangleBreakoutConfig){
		"swing left zero":   func(c *TriangleBreakoutConfig) { c.SwingLeft = 0 },
		"swing right zero":  func(c *TriangleBreakoutConfig) { c.SwingRight = 0 },
		"bad triangle":      func(c *TriangleBreakoutConfig) { c.Triangle.MinTouches = 1 },
		"negative buffer":   func(c *TriangleBreakoutConfig) { c.BreakoutBufferTicks = -1 },
		"reward floor zero": func(c *TriangleBreakoutConfig) { c.MinRewardRiskBps = 0 },
		"negative multiple": func(c *TriangleBreakoutConfig) { c.MinStopSpreadMultiple = -1 },
	}
	for name, mutate := range cases {
		config := DefaultTriangleBreakoutConfig()
		mutate(&config)
		if err := config.Validate(); err == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}
}
