package features

import (
	"testing"
	"time"

	"tradex/internal/domain"
)

// bar builds a closed 1h EURUSD candle from integer ticks at 5 decimals.
func bar(index int, open, high, low, close int64) domain.Candle {
	openTime := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(index) * time.Hour)
	return domain.Candle{
		Source: "test", Symbol: "EURUSD", Timeframe: time.Hour,
		OpenTime: openTime, CloseTime: openTime.Add(time.Hour),
		Open: domain.Price(open), High: domain.Price(high),
		Low: domain.Price(low), Close: domain.Price(close),
		TickCount: 100,
	}
}

func testConfig() ContinuationConfig {
	config := DefaultContinuationConfig()
	config.ImpulseBars = 6
	config.PriorBars = 6
	config.TargetBars = 12
	return config
}

// bearishWindow builds a range that then breaks down and pulls back.
//
//	bars 0-5   prior range, low 111900
//	bars 6-11  impulse: breaks 111900, drives to 111500, closes back at 111700
//
// The broken level is 111900; the retest limit rests there; the stop sits
// above the breaking leg's high; the target is the window's lowest low.
func bearishWindow() []domain.Candle {
	return []domain.Candle{
		bar(0, 112100, 112200, 111950, 112050),
		bar(1, 112050, 112150, 111900, 112000),
		bar(2, 112000, 112100, 111920, 112050),
		bar(3, 112050, 112120, 111960, 112000),
		bar(4, 112000, 112080, 111940, 112020),
		bar(5, 112020, 112090, 111930, 112000),
		// impulse: each bar closes down, taking out 111900
		bar(6, 112000, 112010, 111850, 111870),
		bar(7, 111870, 111880, 111750, 111770),
		bar(8, 111770, 111790, 111650, 111670),
		bar(9, 111670, 111700, 111500, 111520),
		bar(10, 111520, 111620, 111510, 111560),
		// final bar pulls back up but still closes down, below the level
		bar(11, 111560, 111800, 111550, 111700),
	}
}

func TestDetectContinuationBearish(t *testing.T) {
	setup, ok := DetectContinuation(bearishWindow(), 7, testConfig())
	if !ok {
		t.Fatal("expected a bearish continuation setup")
	}
	if setup.Kind != ContinuationBearish {
		t.Errorf("Kind = %s", setup.Kind)
	}
	// The prior window's lowest low is 111900; the impulse reached 111500.
	if setup.BrokenLevel != 111900 {
		t.Errorf("BrokenLevel = %d, want 111900", setup.BrokenLevel)
	}
	if setup.BreakTicks != 400 {
		t.Errorf("BreakTicks = %d, want 400", setup.BreakTicks)
	}
	// RetestOffsetBps defaults to 0, so the limit rests on the level.
	if setup.Entry != 111900 {
		t.Errorf("Entry = %d, want 111900", setup.Entry)
	}
	// The stop sits above the high of the breaking leg: the impulse's
	// highest high is bar 6's 112010, plus the 2-tick buffer.
	if setup.Stop != 112012 {
		t.Errorf("Stop = %d, want 112012", setup.Stop)
	}
	if setup.Target != 111500 {
		t.Errorf("Target = %d, want 111500", setup.Target)
	}
	if setup.RiskTicks != 112 || setup.RewardTicks != 400 {
		t.Errorf("risk/reward = %d/%d, want 112/400", setup.RiskTicks, setup.RewardTicks)
	}
	if setup.RewardRiskBps != 400*10000/112 {
		t.Errorf("RewardRiskBps = %d", setup.RewardRiskBps)
	}
	if setup.SpecVersion != ContinuationSpecVersion {
		t.Errorf("SpecVersion = %q", setup.SpecVersion)
	}
	// The setup is knowable only at the confirming bar's close.
	wantAt := bar(11, 0, 1, 0, 0).CloseTime
	if !setup.DetectedAt.Equal(wantAt) {
		t.Errorf("DetectedAt = %s, want %s", setup.DetectedAt, wantAt)
	}
}

func TestDetectContinuationBullishMirrorsBearish(t *testing.T) {
	// Mirror the bearish fixture around a price axis so the same geometry
	// must produce the mirrored setup. A detector that works in only one
	// direction is a detector with a sign bug.
	const axis = 224000 // 2 * 112000
	mirror := func(p domain.Price) domain.Price { return domain.Price(axis) - p }
	var window []domain.Candle
	for i, candle := range bearishWindow() {
		window = append(window, bar(i,
			int64(mirror(candle.Open)), int64(mirror(candle.Low)),
			int64(mirror(candle.High)), int64(mirror(candle.Close))))
	}

	setup, ok := DetectContinuation(window, 7, testConfig())
	if !ok {
		t.Fatal("expected a bullish continuation setup")
	}
	if setup.Kind != ContinuationBullish {
		t.Fatalf("Kind = %s", setup.Kind)
	}
	if setup.BrokenLevel != mirror(111900) {
		t.Errorf("BrokenLevel = %d, want %d", setup.BrokenLevel, mirror(111900))
	}
	if setup.Entry != mirror(111900) {
		t.Errorf("Entry = %d, want %d", setup.Entry, mirror(111900))
	}
	if setup.Target != mirror(111500) {
		t.Errorf("Target = %d, want %d", setup.Target, mirror(111500))
	}
	if setup.RiskTicks != 112 || setup.RewardTicks != 400 {
		t.Errorf("risk/reward = %d/%d, want 112/400", setup.RiskTicks, setup.RewardTicks)
	}
}

// Drift without a structural break is the failure mode this strategy exists
// to avoid: a one-way sequence that never takes out a prior extreme.
func TestDetectContinuationRejectsDriftWithoutBreak(t *testing.T) {
	window := bearishWindow()
	// Lower the prior window beneath the impulse so nothing is broken.
	for i := 0; i <= 5; i++ {
		window[i] = bar(i, 111000, 111100, 110000, 111050)
	}
	if _, ok := DetectContinuation(window, 7, testConfig()); ok {
		t.Error("a move that breaks no prior extreme must be rejected")
	}
}

// A choppy window that ends where it began has no displacement.
func TestDetectContinuationRejectsChop(t *testing.T) {
	window := bearishWindow()
	window[11] = bar(11, 111560, 112300, 111550, 112250)
	if _, ok := DetectContinuation(window, 7, testConfig()); ok {
		t.Error("a window with no net displacement must be rejected")
	}
}

// The cost floor is the filter CRT v0.1 lacked: a 2.6 pip stop against a
// 0.7 pip spread spends 27% of risk on cost before slippage.
func TestDetectContinuationRejectsStopInsideSpreadFloor(t *testing.T) {
	config := testConfig()
	window := bearishWindow()

	// Risk is 112 ticks. A spread of 7 clears 4x; a spread of 30 does not.
	if _, ok := DetectContinuation(window, 7, config); !ok {
		t.Fatal("7-tick spread should pass the cost floor")
	}
	if _, ok := DetectContinuation(window, 30, config); ok {
		t.Error("a 30-tick spread against a 112-tick stop must be rejected")
	}

	// A zero spread means the source could not report one; the floor is
	// then disabled rather than silently assumed to pass.
	if _, ok := DetectContinuation(window, 0, config); !ok {
		t.Error("an unreported spread must disable the floor, not reject")
	}
	if _, ok := DetectContinuation(window, -1, config); ok {
		t.Error("a negative spread is invalid input")
	}
}

func TestDetectContinuationRejectsPoorRewardRisk(t *testing.T) {
	config := testConfig()
	config.MinRewardRiskBps = 100000 // demand 10R
	if _, ok := DetectContinuation(bearishWindow(), 7, config); ok {
		t.Error("geometry below the reward/risk floor must be rejected")
	}
}

// A retest already behind price is not a trade: the limit would fill
// immediately at a worse price than the premise assumes.
func TestDetectContinuationRejectsEntryBehindPrice(t *testing.T) {
	window := bearishWindow()
	// Close the final bar back above the broken level.
	window[11] = bar(11, 111560, 112000, 111550, 111950)
	if _, ok := DetectContinuation(window, 7, testConfig()); ok {
		t.Error("an entry at or below the last close must be rejected for a short")
	}
}

func TestDetectContinuationRejectsUnreachableEntry(t *testing.T) {
	config := testConfig()
	config.MaxEntryDistanceBps = 100 // 1% of a mean bar
	if _, ok := DetectContinuation(bearishWindow(), 7, config); ok {
		t.Error("a retest too far from price to be reached must be rejected")
	}
}

func TestDetectContinuationRequiresEnoughHistory(t *testing.T) {
	config := testConfig()
	window := bearishWindow()
	if _, ok := DetectContinuation(window[:config.RequiredBars()-1], 7, config); ok {
		t.Error("a short window must return false, not a partial judgement")
	}
}

func TestContinuationConfigValidate(t *testing.T) {
	if err := DefaultContinuationConfig().Validate(); err != nil {
		t.Fatalf("default config invalid: %v", err)
	}
	cases := map[string]func(*ContinuationConfig){
		"impulse too short":   func(c *ContinuationConfig) { c.ImpulseBars = 2 },
		"prior too short":     func(c *ContinuationConfig) { c.PriorBars = 2 },
		"directional zero":    func(c *ContinuationConfig) { c.MinDirectionalBps = 0 },
		"directional over":    func(c *ContinuationConfig) { c.MinDirectionalBps = 10001 },
		"displacement zero":   func(c *ContinuationConfig) { c.MinDisplacementBps = 0 },
		"break zero":          func(c *ContinuationConfig) { c.MinBreakTicks = 0 },
		"negative offset":     func(c *ContinuationConfig) { c.RetestOffsetBps = -1 },
		"negative buffer":     func(c *ContinuationConfig) { c.StopBufferTicks = -1 },
		"negative multiple":   func(c *ContinuationConfig) { c.MinStopSpreadMultiple = -1 },
		"target too short":    func(c *ContinuationConfig) { c.TargetBars = 1 },
		"reward floor zero":   func(c *ContinuationConfig) { c.MinRewardRiskBps = 0 },
		"entry distance zero": func(c *ContinuationConfig) { c.MaxEntryDistanceBps = 0 },
	}
	for name, mutate := range cases {
		config := DefaultContinuationConfig()
		mutate(&config)
		if err := config.Validate(); err == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}
}

func TestContinuationRequiredBars(t *testing.T) {
	config := DefaultContinuationConfig()
	// Impulse plus prior is 36; the 48-bar target window dominates.
	if got := config.RequiredBars(); got != 48 {
		t.Errorf("RequiredBars = %d, want 48", got)
	}
	config.TargetBars = 4
	if got := config.RequiredBars(); got != 36 {
		t.Errorf("RequiredBars = %d, want 36", got)
	}
}

// Detection must not depend on anything outside the window: the same bars
// must give the same answer every time.
func TestDetectContinuationIsDeterministic(t *testing.T) {
	config := testConfig()
	first, ok := DetectContinuation(bearishWindow(), 7, config)
	if !ok {
		t.Fatal("expected a setup")
	}
	for i := 0; i < 50; i++ {
		again, ok := DetectContinuation(bearishWindow(), 7, config)
		if !ok || again != first {
			t.Fatalf("detection is not deterministic at iteration %d", i)
		}
	}
}
