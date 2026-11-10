package scan

import (
	"testing"
	"time"

	"tradex/internal/domain"
	"tradex/internal/instrument"
)

func cndl(i int, o, h, l, c int64) domain.Candle {
	ot := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Hour)
	return domain.Candle{
		Source: "t", Symbol: "EURUSD", Timeframe: time.Hour, OpenTime: ot, CloseTime: ot.Add(time.Hour),
		Open: domain.Price(o), High: domain.Price(h), Low: domain.Price(l), Close: domain.Price(c), TickCount: 1,
	}
}

// fixedDetector arms one long limit on a chosen bar, so the backtest's fill,
// target and R accounting can be checked against a known outcome.
type fixedDetector struct {
	armBar              int
	entry, stop, target domain.Price
}

func (fixedDetector) Name() string      { return "Fixed" }
func (fixedDetector) Spec() string      { return "fixed-test" }
func (fixedDetector) Validated() bool   { return false }
func (fixedDetector) RequiredBars() int { return 1 }
func (d fixedDetector) Detect(_ instrument.Spec, window []domain.Candle, _ int64) (Setup, bool) {
	last := window[len(window)-1]
	if last.OpenTime.Hour() != d.armBar {
		return Setup{}, false
	}
	return Setup{Side: "buy", EntryType: "LIMIT", Entry: d.entry, Stop: d.stop, Target: d.target,
		DetectedAt: last.CloseTime, RiskTicks: int64(d.entry - d.stop), Reward: int64(d.target - d.entry),
		Reason: "test"}, true
}

// A long that fills then reaches its target must book a positive-R win net of
// costs, with exactly one trade.
func TestBacktestBooksWinningLimit(t *testing.T) {
	var c []domain.Candle
	c = append(c, cndl(0, 1000, 1010, 990, 1005))  // arm bar
	c = append(c, cndl(1, 1005, 1006, 995, 1000))  // pullback fills limit at 998
	c = append(c, cndl(2, 1000, 1030, 999, 1025))  // reaches target 1020
	c = append(c, cndl(3, 1025, 1040, 1020, 1035)) // flat tail

	det := fixedDetector{armBar: 0, entry: 998, stop: 990, target: 1020}
	p := DefaultBTParams()
	p.Costs = Costs{SpreadTicks: 2, SlippageTicks: 0}
	p.SpreadTicks = 0
	p.ExpiryBars = 4
	p.MaxHoldBars = 10

	spec, _ := instrument.Lookup("EURUSD")
	res, fills, err := Backtest("EURUSD", spec, time.Hour, c, det, p)
	if err != nil {
		t.Fatalf("backtest: %v", err)
	}
	if res.Armed != 1 || res.Filled != 1 {
		t.Fatalf("armed=%d filled=%d, want 1/1", res.Armed, res.Filled)
	}
	if res.Report.Trades != 1 || res.Wins != 1 {
		t.Fatalf("trades=%d wins=%d, want 1/1", res.Report.Trades, res.Wins)
	}
	// Risk 8 ticks, reward 22 ticks => +2.75R gross, minus 2-tick spread
	// (0.25R) => +2.50R net.
	if got := res.ExpectancyRBps; got != 25000 {
		t.Errorf("expectancy = %d bps, want 25000 (+2.50R)", got)
	}
	if len(fills) != 2 {
		t.Errorf("want entry+exit fills, got %d", len(fills))
	}
}

// When a single bar spans both stop and target, the backtest resolves to the
// stop (the honest, adverse assumption).
func TestBacktestAmbiguousBarResolvesToStop(t *testing.T) {
	var c []domain.Candle
	c = append(c, cndl(0, 1000, 1010, 990, 1005))
	c = append(c, cndl(1, 1005, 1006, 995, 1000)) // fills at 998
	c = append(c, cndl(2, 1000, 1030, 980, 1005)) // spans stop 990 AND target 1020
	c = append(c, cndl(3, 1005, 1010, 1000, 1005))

	det := fixedDetector{armBar: 0, entry: 998, stop: 990, target: 1020}
	p := DefaultBTParams()
	p.Costs = Costs{SpreadTicks: 0, SlippageTicks: 0}
	p.SpreadTicks = 0
	p.ExpiryBars = 4

	spec, _ := instrument.Lookup("EURUSD")
	res, _, err := Backtest("EURUSD", spec, time.Hour, c, det, p)
	if err != nil {
		t.Fatalf("backtest: %v", err)
	}
	if res.AmbiguousStops != 1 {
		t.Fatalf("ambiguous bars resolved to stop = %d, want 1", res.AmbiguousStops)
	}
	if res.Wins != 0 || res.ExpectancyRBps != -10000 {
		t.Errorf("expected a -1R loss, got wins=%d expectancy=%d", res.Wins, res.ExpectancyRBps)
	}
}
