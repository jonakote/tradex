package scan

import (
	"reflect"
	"testing"
	"time"

	"tradex/internal/domain"
	"tradex/internal/instrument"
)

type scriptedDetector struct{ setup Setup }

func (d scriptedDetector) Name() string      { return "scripted" }
func (d scriptedDetector) Spec() string      { return "test-v1" }
func (d scriptedDetector) Validated() bool   { return false }
func (d scriptedDetector) RequiredBars() int { return 2 }
func (d scriptedDetector) Detect(_ instrument.Spec, bars []domain.Candle, _ int64) (Setup, bool) {
	s := d.setup
	if !bars[len(bars)-1].CloseTime.Equal(s.DetectedAt) {
		return Setup{}, false
	}
	return s, true
}
func streamBars() []domain.Candle {
	at := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	bars := make([]domain.Candle, 5)
	for i := range bars {
		bars[i] = domain.Candle{Source: "test", TickCount: 1, Symbol: "EURUSD", Timeframe: time.Hour, OpenTime: at.Add(time.Duration(i) * time.Hour), CloseTime: at.Add(time.Duration(i+1) * time.Hour), Open: 100, High: 110, Low: 90, Close: 100}
	}
	return bars
}
func TestStreamTimeExitAndBacktestParity(t *testing.T) {
	bars := streamBars()
	bars[3].Close = 105
	setup := Setup{Side: "buy", EntryType: "MARKET", Entry: 100, HoldBars: 2, DetectedAt: bars[1].CloseTime}
	det := scriptedDetector{setup}
	spec, ok := instrument.Lookup("EURUSD")
	if !ok {
		t.Fatal("missing instrument")
	}
	p := DefaultBTParams()
	p.Costs = Costs{SpreadTicks: 1, SlippageTicks: 1}
	stream, err := NewStream("EURUSD", spec, time.Hour, det, p)
	if err != nil {
		t.Fatal(err)
	}
	var events []LifecycleEvent
	for _, b := range bars {
		e, err := stream.Advance(b, nil)
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, e...)
	}
	if len(events) != 3 || events[0].Kind != LifecycleDetected || events[1].Kind != LifecycleEntry || events[2].Kind != LifecycleExit || events[2].ExitReason != "TIME_EXIT" || events[2].Price != 105 || !events[2].At.Equal(bars[3].CloseTime) {
		t.Fatalf("events: %+v", events)
	}
	result, fills, err := Backtest("EURUSD", spec, time.Hour, bars, det, p)
	if err != nil {
		t.Fatal(err)
	}
	if result.Report.Trades != 1 || len(fills) != 2 || fills[1].Price != events[2].Price || !fills[1].FilledAt.Equal(events[2].At) {
		t.Fatalf("backtest diverges: %+v %+v", result, fills)
	}
	second, _ := NewStream("EURUSD", spec, time.Hour, det, p)
	var again []LifecycleEvent
	for _, b := range bars {
		e, _ := second.Advance(b, nil)
		again = append(again, e...)
	}
	if !reflect.DeepEqual(events, again) {
		t.Fatal("non deterministic events")
	}
}

// A HoldExitRule exits a position as soon as a later bar's own signal
// crosses the threshold, instead of always waiting the full HoldBars cap —
// the mean-reversion "exit on strength" rule (IBS v0.2).
func TestStreamHoldExitRuleExitsBeforeHoldBarsCap(t *testing.T) {
	bars := streamBars()
	bars[2].Close = 108 // IBS = (108-90)/(110-90) = 90% >= the 70% threshold below
	setup := Setup{
		Side: "buy", EntryType: "MARKET", Entry: 100, HoldBars: 3, DetectedAt: bars[1].CloseTime,
		HoldExitRule: "ibs_strength_above", HoldExitThresholdBps: 7000,
	}
	det := scriptedDetector{setup}
	spec, ok := instrument.Lookup("EURUSD")
	if !ok {
		t.Fatal("missing instrument")
	}
	p := DefaultBTParams()
	p.Costs = Costs{SpreadTicks: 1, SlippageTicks: 1}
	stream, err := NewStream("EURUSD", spec, time.Hour, det, p)
	if err != nil {
		t.Fatal(err)
	}
	var events []LifecycleEvent
	for _, b := range bars {
		e, err := stream.Advance(b, nil)
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, e...)
	}
	// Entry fills on the detection bar (MARKET); the strength exit must
	// fire on bars[2] (the very next bar), one bar BEFORE HoldBars=3 would
	// have forced a TIME_EXIT at bars[4].
	if len(events) != 3 || events[2].Kind != LifecycleExit || events[2].ExitReason != "STRENGTH_EXIT" || events[2].Price != 108 || !events[2].At.Equal(bars[2].CloseTime) {
		t.Fatalf("events: %+v", events)
	}
}

func TestStreamRejectsMalformedAndFutureCandles(t *testing.T) {
	bars := streamBars()
	spec, _ := instrument.Lookup("EURUSD")
	for _, mutate := range []func(*domain.Candle){func(b *domain.Candle) { b.Symbol = "GBPUSD" }, func(b *domain.Candle) { b.CloseTime = b.OpenTime }, func(b *domain.Candle) { b.Timeframe = time.Minute }} {
		s, _ := NewStream("EURUSD", spec, time.Hour, scriptedDetector{}, DefaultBTParams())
		b := bars[0]
		mutate(&b)
		if _, err := s.Advance(b, nil); err == nil {
			t.Fatal("accepted malformed candle")
		}
	}
	s, _ := NewStream("EURUSD", spec, time.Hour, scriptedDetector{}, DefaultBTParams())
	if _, err := s.Advance(bars[1], nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Advance(bars[0], nil); err == nil {
		t.Fatal("accepted past candle")
	}
}
