package icc

import (
	"testing"
	"time"

	"tradex/internal/domain"
	"tradex/internal/features"
)

func iccCandle(openTime time.Time, timeframe time.Duration, high, low, close domain.Price) domain.Candle {
	return domain.Candle{
		Source: "test", Symbol: "EURUSD", Timeframe: timeframe,
		OpenTime: openTime, CloseTime: openTime.Add(timeframe),
		Open: close, High: high, Low: low, Close: close, TickCount: 1,
	}
}

func bullishIndication(at time.Time) Indication {
	candle := iccCandle(at, time.Hour, 1110, 1040, 1100)
	swing := features.Swing{
		Kind: features.SwingHigh, Symbol: "EURUSD", Timeframe: time.Hour,
		Price: 1080, PivotTime: at.Add(-2 * time.Hour), ConfirmedAt: at,
	}
	return Indication{
		Break: features.StructureBreak{
			Direction: features.BreakBullish, BrokenSwing: swing,
			Candle: candle, KnownAt: candle.CloseTime,
		},
		InvalidationPrice: 1000, AllowedSession: true,
	}
}

func TestBullishICCCompletesWithAuditableTransitions(t *testing.T) {
	machine, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := machine.SetContext(features.TrendBullish, features.TrendBullish); err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, time.August, 18, 8, 0, 0, 0, time.UTC)
	if err := machine.Begin(bullishIndication(t0)); err != nil {
		t.Fatal(err)
	}
	correction := iccCandle(t0.Add(time.Hour), time.Hour, 1100, 1070, 1090)
	if err := machine.OnSetupCandle(correction); err != nil {
		t.Fatal(err)
	}
	confirmationCandle := iccCandle(t0.Add(2*time.Hour), 15*time.Minute, 1110, 1085, 1105)
	confirmation := features.StructureBreak{
		Direction: features.BreakBullish, Candle: confirmationCandle,
		KnownAt: confirmationCandle.CloseTime,
	}
	signal, err := machine.OnConfirmation(confirmation, true)
	if err != nil {
		t.Fatal(err)
	}
	if signal.StopReference != 1070 || signal.EntryReference != 1105 || machine.State() != Completed {
		t.Fatalf("unexpected signal/state: %+v %s", signal, machine.State())
	}
	transitions := machine.Transitions()
	if len(transitions) != 3 || transitions[0].To != AwaitingCorrection ||
		transitions[1].To != AwaitingContinuation || transitions[2].To != Completed {
		t.Fatalf("unexpected transitions: %+v", transitions)
	}
}

func TestICCRejectsMisalignedContext(t *testing.T) {
	machine, _ := New(DefaultConfig())
	if err := machine.SetContext(features.TrendBullish, features.TrendBearish); err != nil {
		t.Fatal(err)
	}
	err := machine.Begin(bullishIndication(time.Date(2026, 8, 18, 8, 0, 0, 0, time.UTC)))
	if err == nil || machine.State() != SeekingIndication || len(machine.Transitions()) != 0 {
		t.Fatalf("misaligned context accepted: err=%v state=%s", err, machine.State())
	}
}

func TestICCInvalidatesOnStructuralClose(t *testing.T) {
	machine, _ := New(DefaultConfig())
	_ = machine.SetContext(features.TrendBullish, features.TrendBullish)
	t0 := time.Date(2026, 8, 18, 8, 0, 0, 0, time.UTC)
	if err := machine.Begin(bullishIndication(t0)); err != nil {
		t.Fatal(err)
	}
	invalidation := iccCandle(t0.Add(time.Hour), time.Hour, 1050, 980, 990)
	if err := machine.OnSetupCandle(invalidation); err != nil {
		t.Fatal(err)
	}
	if machine.State() != Invalidated {
		t.Fatalf("got %s want %s", machine.State(), Invalidated)
	}
}

func TestICCExpiresAfterConfiguredBars(t *testing.T) {
	config := DefaultConfig()
	config.MaxCorrectionBars = 2
	machine, _ := New(config)
	_ = machine.SetContext(features.TrendBullish, features.TrendBullish)
	t0 := time.Date(2026, 8, 18, 8, 0, 0, 0, time.UTC)
	if err := machine.Begin(bullishIndication(t0)); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		candle := iccCandle(t0.Add(time.Duration(i)*time.Hour), time.Hour, 1120, 1090, 1110)
		if err := machine.OnSetupCandle(candle); err != nil {
			t.Fatal(err)
		}
	}
	if machine.State() != Invalidated {
		t.Fatalf("got %s want %s", machine.State(), Invalidated)
	}
}

func TestBearishICCIsSymmetric(t *testing.T) {
	machine, _ := New(DefaultConfig())
	_ = machine.SetContext(features.TrendBearish, features.TrendBearish)
	t0 := time.Date(2026, 8, 18, 8, 0, 0, 0, time.UTC)
	indicationCandle := iccCandle(t0, time.Hour, 1160, 1080, 1090)
	indication := Indication{
		Break: features.StructureBreak{
			Direction: features.BreakBearish,
			BrokenSwing: features.Swing{
				Kind: features.SwingLow, Symbol: "EURUSD", Timeframe: time.Hour,
				Price: 1120, PivotTime: t0.Add(-2 * time.Hour), ConfirmedAt: t0,
			},
			Candle: indicationCandle, KnownAt: indicationCandle.CloseTime,
		},
		InvalidationPrice: 1200, AllowedSession: true,
	}
	if err := machine.Begin(indication); err != nil {
		t.Fatal(err)
	}
	correction := iccCandle(t0.Add(time.Hour), time.Hour, 1140, 1090, 1110)
	if err := machine.OnSetupCandle(correction); err != nil {
		t.Fatal(err)
	}
	confirmationCandle := iccCandle(t0.Add(2*time.Hour), 15*time.Minute, 1120, 1070, 1080)
	signal, err := machine.OnConfirmation(features.StructureBreak{
		Direction: features.BreakBearish, Candle: confirmationCandle,
		KnownAt: confirmationCandle.CloseTime,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if signal.StopReference != 1140 || signal.EntryReference != 1080 || signal.Direction != features.BreakBearish {
		t.Fatalf("unexpected bearish signal: %+v", signal)
	}
}
