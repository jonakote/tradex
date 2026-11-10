package scan

import (
	"testing"
	"time"
	"tradex/internal/domain"
	"tradex/internal/instrument"
)

func TestStreamStopTargetExpiryAndAdmission(t *testing.T) {
	for _, tc := range []struct {
		name      string
		low, high domain.Price
		admit     bool
		entry     domain.Price
		kind      LifecycleKind
		reason    string
		ambiguous bool
	}{
		{"stop", 89, 105, true, 100, LifecycleExit, "STOP_LOSS", false},
		{"target", 95, 121, true, 100, LifecycleExit, "TAKE_PROFIT", false},
		{"both stop first", 89, 121, true, 100, LifecycleExit, "STOP_LOSS", true},
		{"expiry", 95, 105, true, 80, LifecycleExpired, "", false},
		{"risk rejection", 95, 105, false, 100, LifecycleRejected, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bars := streamBars()
			bars[2].Low = tc.low
			bars[2].High = tc.high
			setup := Setup{Side: "buy", EntryType: "LIMIT", Entry: tc.entry, Stop: tc.entry - 10, Target: tc.entry + 20, RiskTicks: 10, DetectedAt: bars[1].CloseTime}
			p := DefaultBTParams()
			p.ExpiryBars = 1
			spec, _ := instrument.Lookup("EURUSD")
			s, err := NewStream("EURUSD", spec, time.Hour, scriptedDetector{setup}, p)
			if err != nil {
				t.Fatal(err)
			}
			var events []LifecycleEvent
			called := 0
			for _, b := range bars {
				e, err := s.Advance(b, func(Setup) (bool, error) { called++; return tc.admit, nil })
				if err != nil {
					t.Fatal(err)
				}
				events = append(events, e...)
			}
			last := events[len(events)-1]
			if called != 1 || last.Kind != tc.kind || last.ExitReason != tc.reason || last.Ambiguous != tc.ambiguous {
				t.Fatalf("events %+v calls %d", events, called)
			}
		})
	}
}
func TestBacktestRejectsWrongSetupRiskAndFutureDetection(t *testing.T) {
	bars := streamBars()
	spec, _ := instrument.Lookup("EURUSD")
	setup := Setup{Side: "buy", EntryType: "MARKET", Entry: 100, Stop: 90, Target: 120, RiskTicks: 9, DetectedAt: bars[1].CloseTime}
	if _, _, err := Backtest("EURUSD", spec, time.Hour, bars, scriptedDetector{setup}, DefaultBTParams()); err == nil {
		t.Fatal("backtest accepted misrepresented risk")
	}
	setup.RiskTicks = 10
	if err := ValidateSetup(setup, bars[0].CloseTime); err == nil {
		t.Fatal("future setup accepted")
	}
}
