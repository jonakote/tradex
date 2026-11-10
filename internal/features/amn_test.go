package features

import (
	"testing"
	"time"

	"tradex/internal/domain"
)

func amnBar(i int, o, h, l, c int64) domain.Candle {
	ot := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Hour)
	return domain.Candle{
		Source: "t", Symbol: "EURUSD", Timeframe: time.Hour, OpenTime: ot, CloseTime: ot.Add(time.Hour),
		Open: domain.Price(o), High: domain.Price(h), Low: domain.Price(l), Close: domain.Price(c), TickCount: 1,
	}
}

func amnTestConfig() AMNConfig {
	c := DefaultAMNConfig()
	c.TrendBars = 10
	c.SweepLookback = 3
	c.StructureBars = 6
	c.TargetBars = 12
	return c
}

// Uptrend, the final bar sweeps a prior structural low then closes back above
// it (sweep + reclaim): a long limit at the reclaimed level, stop below the
// sweep (now ATR-scaled, amn-0.2), target the prior high.
func TestDetectAMNBullish(t *testing.T) {
	var w []domain.Candle
	// 22 rising bars, modest 50-tick range so the ATR-scaled stop buffer
	// stays small relative to the sweep/target distances below.
	for i := 0; i < 22; i++ {
		base := int64(100000 + i*20)
		w = append(w, amnBar(i, base, base+40, base-10, base+30))
	}
	// Final bar: dips to 100000 (well below the recent structural low
	// ~100310) then closes at 100500, back above it — the sweep and
	// reclaim. Its high (101500) stands in as the far liquidity target.
	w = append(w, amnBar(22, 100300, 101500, 100000, 100500))

	p, ok := DetectAMN(w, 2, amnTestConfig())
	if !ok {
		t.Fatal("expected a bullish AMN setup")
	}
	if p.Kind != AMNBullish {
		t.Fatalf("kind = %s", p.Kind)
	}
	if !(p.Stop < p.Entry && p.Entry < p.Target) {
		t.Errorf("geometry inconsistent: stop=%d entry=%d target=%d", p.Stop, p.Entry, p.Target)
	}
	if p.SweepTicks <= 0 {
		t.Errorf("sweep ticks must be positive, got %d", p.SweepTicks)
	}
	if p.SpecVersion != AMNSpecVersion {
		t.Errorf("spec = %q", p.SpecVersion)
	}
	// Stop must sit beyond a fixed 2-tick buffer now: ATR over the 50-tick-
	// range structure bars scales the buffer to ~75 ticks here.
	buffer := int64(p.SweepExtreme) - int64(p.Stop)
	if buffer <= 2 {
		t.Errorf("stop buffer should be ATR-scaled (>2 ticks), got %d", buffer)
	}
}

// No sweep (price never takes out a prior low) => no setup.
func TestDetectAMNNoSweep(t *testing.T) {
	var w []domain.Candle
	for i := 0; i < 30; i++ {
		base := int64(100000 + i*50) // clean uptrend, no lows taken
		w = append(w, amnBar(i, base, base+400, base+200, base+350))
	}
	if _, ok := DetectAMN(w, 2, amnTestConfig()); ok {
		t.Error("a trend with no liquidity sweep must return false")
	}
}

func TestAMNConfigValidate(t *testing.T) {
	if err := DefaultAMNConfig().Validate(); err != nil {
		t.Fatalf("default invalid: %v", err)
	}
	bad := DefaultAMNConfig()
	bad.MinRewardRiskBps = 0
	if err := bad.Validate(); err == nil {
		t.Error("zero reward floor must fail")
	}
	negATR := DefaultAMNConfig()
	negATR.StopATRMultipleBps = -1
	if err := negATR.Validate(); err == nil {
		t.Error("negative ATR multiple must fail")
	}
}

// A near-zero-ATR regime (flat, tiny-range bars) must not collapse the stop
// buffer to nothing: MinStopBufferTicks is the floor.
func TestStopBufferFloorsOnLowATR(t *testing.T) {
	var flat []domain.Candle
	for i := 0; i < 6; i++ {
		base := int64(100000 + i)
		flat = append(flat, amnBar(i, base, base+1, base, base))
	}
	cfg := DefaultAMNConfig()
	cfg.MinStopBufferTicks = 5
	cfg.StopATRMultipleBps = 15000
	got := stopBufferTicks(flat, cfg)
	if got != 5 {
		t.Errorf("expected floor of 5 on near-zero ATR, got %d", got)
	}
}
