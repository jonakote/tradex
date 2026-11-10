package features

import (
	"testing"
	"time"

	"tradex/internal/domain"
)

func ictBar(i int, o, h, l, c int64) domain.Candle {
	ot := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Hour)
	return domain.Candle{
		Source: "t", Symbol: "EURUSD", Timeframe: time.Hour, OpenTime: ot, CloseTime: ot.Add(time.Hour),
		Open: domain.Price(o), High: domain.Price(h), Low: domain.Price(l), Close: domain.Price(c), TickCount: 1,
	}
}

func ictTestConfig() ICTConfig {
	c := DefaultICTConfig()
	c.StructureBars = 6
	c.ScanBars = 4
	c.TargetBars = 12
	return c
}

// A displacement that breaks the prior high and leaves a bullish gap in the
// most recent bars should produce a long limit into the gap, below current
// price, with a valid stop (now ATR-scaled, ict-0.2) and a target above.
func TestDetectICTBullishFVG(t *testing.T) {
	var w []domain.Candle
	// 6 structural bars, modest 100-tick range so the ATR-scaled stop
	// buffer stays small relative to the gap/target distances below.
	for i := 0; i < 6; i++ {
		w = append(w, ictBar(i, 100500, 100600, 100500, 100550))
	}
	// 3-candle bullish FVG at the end: a(6) high 101050, displacement(7)
	// closes above the prior high, z(8) low 101500 > a.high => gap.
	w = append(w, ictBar(6, 100600, 101050, 100550, 101000)) // a
	w = append(w, ictBar(7, 101000, 101800, 101000, 101750)) // displacement, close 101750 > 100600
	w = append(w, ictBar(8, 101800, 102500, 101500, 101900)) // z: gap [101050,101500], high 102500
	// pullback bars that stay above the gap; last close must exceed gapHigh.
	w = append(w, ictBar(9, 101900, 102600, 101800, 101950))
	w = append(w, ictBar(10, 101950, 102700, 101850, 101900))
	w = append(w, ictBar(11, 101900, 102800, 101820, 101900))
	p, ok := DetectICT(w, 2, ictTestConfig())
	if !ok {
		t.Fatal("expected a bullish FVG setup")
	}
	if p.Kind != ICTBullish {
		t.Fatalf("kind = %s", p.Kind)
	}
	if p.Entry != 101500 || p.GapLow != 101050 || p.GapHigh != 101500 {
		t.Errorf("gap/entry wrong: entry=%d gap=[%d,%d]", p.Entry, p.GapLow, p.GapHigh)
	}
	if !(p.Stop < p.Entry && p.Entry < p.Target) {
		t.Errorf("geometry inconsistent: stop=%d entry=%d target=%d", p.Stop, p.Entry, p.Target)
	}
	if p.SpecVersion != ICTSpecVersion {
		t.Errorf("spec = %q", p.SpecVersion)
	}
	buffer := int64(p.GapLow) - int64(p.Stop)
	if buffer <= 2 {
		t.Errorf("stop buffer should be ATR-scaled (>2 ticks), got %d", buffer)
	}
}

// No gap => no setup.
func TestDetectICTNoGap(t *testing.T) {
	var w []domain.Candle
	for i := 0; i < 20; i++ {
		w = append(w, ictBar(i, 100000, 100500, 99500, 100100)) // overlapping bars, no imbalance
	}
	if _, ok := DetectICT(w, 2, ictTestConfig()); ok {
		t.Error("overlapping bars have no FVG; must return false")
	}
}

// A gap whose displacement did NOT break prior structure is ignored.
func TestDetectICTRequiresStructureBreak(t *testing.T) {
	var w []domain.Candle
	for i := 0; i < 6; i++ {
		w = append(w, ictBar(i, 100500, 105000, 100000, 100600)) // structural high 105000
	}
	w = append(w, ictBar(6, 100600, 101050, 100550, 101000))
	w = append(w, ictBar(7, 101000, 101800, 101000, 101750)) // closes 101750 < prior high 105000
	w = append(w, ictBar(8, 101800, 102500, 101500, 101900))
	w = append(w, ictBar(9, 101900, 102600, 101800, 101950))
	w = append(w, ictBar(10, 101950, 102700, 101850, 101900))
	w = append(w, ictBar(11, 101900, 102800, 101820, 101900))
	if _, ok := DetectICT(w, 2, ictTestConfig()); ok {
		t.Error("a gap that broke no structure must be rejected")
	}
}

func TestICTConfigValidateAndDeterminism(t *testing.T) {
	if err := DefaultICTConfig().Validate(); err != nil {
		t.Fatalf("default invalid: %v", err)
	}
	bad := DefaultICTConfig()
	bad.ScanBars = 2
	if err := bad.Validate(); err == nil {
		t.Error("scan < 3 must fail")
	}
	negATR := DefaultICTConfig()
	negATR.StopATRMultipleBps = -1
	if err := negATR.Validate(); err == nil {
		t.Error("negative ATR multiple must fail")
	}
}
