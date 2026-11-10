package features

import (
	"testing"
	"time"

	"tradex/internal/domain"
)

// stBar builds a closed 1h EURUSD candle from integer ticks.
func stBar(symbol string, index int, open, high, low, close int64) domain.Candle {
	openTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(index) * time.Hour)
	return domain.Candle{
		Source: "test", Symbol: symbol, Timeframe: time.Hour,
		OpenTime: openTime, CloseTime: openTime.Add(time.Hour),
		Open: domain.Price(open), High: domain.Price(high),
		Low: domain.Price(low), Close: domain.Price(close),
		TickCount: 100,
	}
}

func stTestConfig() StructureTrendConfig {
	config := DefaultStructureTrendConfig()
	config.SwingLeft = 2
	config.SwingRight = 2
	return config
}

// bullishZigzag builds a staircase uptrend: each leg rallies then pulls
// back to a higher low than the previous leg, giving confirmed higher
// swing highs and higher swing lows for features.ClassifyTrend.
func bullishZigzag(legs, legLen int) []domain.Candle {
	var out []domain.Candle
	base := int64(100000)
	idx := 0
	highWater := base
	lowWater := base - 500
	for leg := 0; leg < legs; leg++ {
		rallyTop := highWater + 300
		pullbackLow := lowWater + 300 // higher low each leg
		half := legLen / 2
		for i := 0; i < half; i++ {
			frac := int64(i + 1)
			h := highWater + frac*300/int64(half)
			out = append(out, stBar("EURUSD", idx, h-50, h, h-120, h-30))
			idx++
		}
		for i := 0; i < legLen-half; i++ {
			frac := int64(i + 1)
			l := rallyTop - frac*(rallyTop-pullbackLow)/int64(legLen-half)
			out = append(out, stBar("EURUSD", idx, l+50, l+120, l, l+30))
			idx++
		}
		highWater = rallyTop
		lowWater = pullbackLow
	}
	return out
}

// bullishSTWindow is the 10-leg bullish zigzag plus one strong breakout
// candle on the close, enough bars for stTestConfig's RequiredBars (60).
// The breakout bar's own high (prevClose+5000) stands in as unswept prior
// liquidity above price — v0.2's traded-extreme target, not a stop-multiple.
func bullishSTWindow() []domain.Candle {
	window := bullishZigzag(10, 6)
	last := window[len(window)-1]
	window = append(window, stBar("EURUSD", len(window),
		int64(last.Close), int64(last.Close)+5000, int64(last.Close)-10, int64(last.Close)+900))
	return window
}

func mirrorSTWindow(window []domain.Candle, axis domain.Price) []domain.Candle {
	var out []domain.Candle
	for i, c := range window {
		out = append(out, stBar("EURUSD", i,
			int64(axis-c.Open), int64(axis-c.Low), int64(axis-c.High), int64(axis-c.Close)))
	}
	return out
}

const stMirrorAxis = domain.Price(300000)

func TestDetectStructureTrendBullish(t *testing.T) {
	config := stTestConfig()
	window := bullishSTWindow()
	setup, ok := DetectStructureTrend(window, 2, config)
	if !ok {
		t.Fatal("expected a bullish structure+trend setup")
	}
	if setup.Kind != StructureTrendBullish {
		t.Errorf("Kind = %s", setup.Kind)
	}
	if setup.Trend != TrendBullish {
		t.Errorf("Trend = %s", setup.Trend)
	}
	if setup.BreakDirection != BreakBullish {
		t.Errorf("BreakDirection = %s", setup.BreakDirection)
	}
	if setup.Entry != 103430 {
		t.Errorf("Entry = %d, want 103430", setup.Entry)
	}
	if setup.Stop != 102198 {
		t.Errorf("Stop = %d, want 102198", setup.Stop)
	}
	if setup.Target != 107530 {
		t.Errorf("Target = %d, want 107530", setup.Target)
	}
	if setup.RiskTicks != 1232 || setup.RewardTicks != 4100 {
		t.Errorf("risk/reward = %d/%d, want 1232/4100", setup.RiskTicks, setup.RewardTicks)
	}
	if setup.RewardRiskBps != 33279 {
		t.Errorf("RewardRiskBps = %d, want 33279", setup.RewardRiskBps)
	}
	if setup.BreakAgeBars != 0 {
		t.Errorf("BreakAgeBars = %d, want 0", setup.BreakAgeBars)
	}
	if setup.SpecVersion != StructureTrendSpecVersion {
		t.Errorf("SpecVersion = %q", setup.SpecVersion)
	}
	wantAt := window[len(window)-1].CloseTime
	if !setup.DetectedAt.Equal(wantAt) {
		t.Errorf("DetectedAt = %s, want %s", setup.DetectedAt, wantAt)
	}
}

// Mirror test: reflecting the fixture around a price axis (swapping
// high/low) must produce the exact mirrored bearish setup with identical
// risk/reward. A one-sided bug would pass every single-direction test.
func TestDetectStructureTrendMirror(t *testing.T) {
	config := stTestConfig()
	window := bullishSTWindow()
	bullish, ok := DetectStructureTrend(window, 2, config)
	if !ok {
		t.Fatal("expected a bullish setup")
	}

	mirrored := mirrorSTWindow(window, stMirrorAxis)
	bearish, ok := DetectStructureTrend(mirrored, 2, config)
	if !ok {
		t.Fatal("expected the mirrored bearish setup")
	}
	if bearish.Kind != StructureTrendBearish {
		t.Errorf("Kind = %s", bearish.Kind)
	}
	if bearish.Entry != stMirrorAxis-bullish.Entry {
		t.Errorf("Entry = %d, want %d", bearish.Entry, stMirrorAxis-bullish.Entry)
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
// byte-identical struct. No map iteration, clock read, or float may creep
// into detection.
func TestDetectStructureTrendIsDeterministic(t *testing.T) {
	config := stTestConfig()
	window := bullishSTWindow()
	first, ok := DetectStructureTrend(window, 2, config)
	if !ok {
		t.Fatal("expected a setup")
	}
	for i := 0; i < 50; i++ {
		again, ok := DetectStructureTrend(window, 2, config)
		if !ok || again != first {
			t.Fatalf("detection is not deterministic at iteration %d", i)
		}
	}
}

// A structure break more than MaxBreakAgeBars old must be rejected: the
// setup is stale, matching the live engine's 2-bar freshness rule.
func TestDetectStructureTrendRejectsStaleBreak(t *testing.T) {
	config := stTestConfig()
	window := bullishSTWindow()
	lastClose := window[len(window)-1].Close
	for k := 0; k < 3; k++ {
		window = append(window, stBar("EURUSD", len(window),
			int64(lastClose), int64(lastClose)+5, int64(lastClose)-5, int64(lastClose)))
	}
	if _, ok := DetectStructureTrend(window, 2, config); ok {
		t.Error("a break older than MaxBreakAgeBars must be rejected")
	}
}

// A bullish break inside a bearish trend (or the mirror) is not a
// continuation and must be rejected.
func TestDetectStructureTrendRejectsMisalignment(t *testing.T) {
	config := stTestConfig()
	bear := mirrorSTWindow(bullishZigzag(10, 6), stMirrorAxis)
	last := bear[len(bear)-1]
	// Append a BULLISH-looking breakout onto a BEARISH trend.
	bear = append(bear, stBar("EURUSD", len(bear),
		int64(last.Close), int64(last.Close)+1000, int64(last.Close)-10, int64(last.Close)+900))
	if _, ok := DetectStructureTrend(bear, 2, config); ok {
		t.Error("a break misaligned with the prevailing trend must be rejected")
	}
}

// The reward/risk floor must reject geometry below it, now measured against
// a real traded target rather than a stop multiple.
func TestDetectStructureTrendRejectsRewardRiskFloor(t *testing.T) {
	config := stTestConfig()
	config.MinRewardRiskBps = 35000 // achieved is 33279bps; must reject
	window := bullishSTWindow()
	if _, ok := DetectStructureTrend(window, 2, config); ok {
		t.Error("geometry below the reward/risk floor must be rejected")
	}
}

// The cost floor: the stop must be meaningfully wider than the spread.
func TestDetectStructureTrendRejectsSpreadFloor(t *testing.T) {
	config := stTestConfig()
	window := bullishSTWindow()
	if _, ok := DetectStructureTrend(window, 2, config); !ok {
		t.Fatal("a 2-tick spread should clear the cost floor")
	}
	// Risk is 1232 ticks; a 400-tick spread at the default 4x multiple
	// needs 1600 ticks of risk, which this setup does not have.
	if _, ok := DetectStructureTrend(window, 400, config); ok {
		t.Error("a spread that fails the MinStopSpreadMultiple floor must be rejected")
	}
	// An unreported spread (0) disables the floor rather than failing.
	if _, ok := DetectStructureTrend(window, 0, config); !ok {
		t.Error("an unreported spread must disable the floor, not reject")
	}
	if _, ok := DetectStructureTrend(window, -1, config); ok {
		t.Error("a negative spread is invalid input")
	}
}

// Ambiguous geometry: when the trailing 10-bar extreme collapses onto the
// entry (a doji at the recomputed stop level with no buffer), the setup is
// not a trade and must be rejected rather than guessed.
func TestDetectStructureTrendRejectsAmbiguousGeometry(t *testing.T) {
	config := stTestConfig()
	config.StopBufferTicks = 0
	window := bullishSTWindow()
	base := int64(window[len(window)-11].Close)
	for k := 0; k < 10; k++ {
		i := len(window) - 10 + k
		v := base + int64(k)*5
		window[i] = stBar("EURUSD", i, v, v+8, v-1, v+3)
	}
	lastIdx := len(window) - 1
	v := int64(window[lastIdx].Close)
	window[lastIdx] = stBar("EURUSD", lastIdx, v, v+1, v, v)
	if _, ok := DetectStructureTrend(window, 2, config); ok {
		t.Error("a stop that collapses onto the entry must be rejected as ambiguous")
	}
}

func TestDetectStructureTrendRequiresEnoughHistory(t *testing.T) {
	config := stTestConfig()
	window := bullishSTWindow()
	if _, ok := DetectStructureTrend(window[:config.RequiredBars()-1], 2, config); ok {
		t.Error("a short window must return false, not a partial judgement")
	}
}

func TestStructureTrendConfigValidate(t *testing.T) {
	if err := DefaultStructureTrendConfig().Validate(); err != nil {
		t.Fatalf("default config invalid: %v", err)
	}
	cases := map[string]func(*StructureTrendConfig){
		"swing left zero":       func(c *StructureTrendConfig) { c.SwingLeft = 0 },
		"swing right zero":      func(c *StructureTrendConfig) { c.SwingRight = 0 },
		"reward floor zero":     func(c *StructureTrendConfig) { c.MinRewardRiskBps = 0 },
		"negative break age":    func(c *StructureTrendConfig) { c.MaxBreakAgeBars = -1 },
		"negative buffer":       func(c *StructureTrendConfig) { c.StopBufferTicks = -1 },
		"negative multiple":     func(c *StructureTrendConfig) { c.MinStopSpreadMultiple = -1 },
		"target bars too small": func(c *StructureTrendConfig) { c.TargetBars = 1 },
	}
	for name, mutate := range cases {
		config := DefaultStructureTrendConfig()
		mutate(&config)
		if err := config.Validate(); err == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}
}
