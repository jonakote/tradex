package scan

import (
	"errors"
	"testing"
	"time"
	"tradex/internal/domain"
	"tradex/internal/instrument"
)

func TestStreamSettlesExitBeforeNextAdmission(t *testing.T) {
	bars := streamBars()
	for i := range bars {
		bars[i].Close = 91
	}
	spec, _ := instrument.Lookup("EURUSD")
	s, err := NewStream("EURUSD", spec, time.Hour, IBS{HoldBars: 1, LongBelowBps: 2000}, DefaultBTParams())
	if err != nil {
		t.Fatal(err)
	}
	active := false
	admissions := 0
	exits := 0
	admit := func(Setup) (bool, error) {
		if active {
			return false, errors.New("previous risk was not released")
		}
		active = true
		admissions++
		return true, nil
	}
	observe := func(e LifecycleEvent) error {
		if e.Kind == LifecycleExit {
			active = false
			exits++
		}
		return nil
	}
	for _, b := range bars {
		if _, err := s.AdvanceWithObserver(b, admit, observe); err != nil {
			t.Fatal(err)
		}
	}
	if admissions != 4 || exits != 3 {
		t.Fatalf("admissions %d exits %d", admissions, exits)
	}
}

func TestStreamObserverFailureStopsBeforeAdmission(t *testing.T) {
	bars := streamBars()
	spec, _ := instrument.Lookup("EURUSD")
	s, _ := NewStream("EURUSD", spec, time.Hour, IBS{HoldBars: 1, LongBelowBps: 6000}, DefaultBTParams())
	sentinel := errors.New("audit unavailable")
	s.Advance(bars[0], nil)
	admitted := false
	_, err := s.AdvanceWithObserver(bars[1], func(Setup) (bool, error) { admitted = true; return true, nil }, func(LifecycleEvent) error { return sentinel })
	if !errors.Is(err, sentinel) || admitted {
		t.Fatalf("err %v admitted %v", err, admitted)
	}
	if _, err := s.Advance(bars[2], nil); err == nil {
		t.Fatal("failed stream resumed without audit")
	}
}

// A hostile detector must not rewrite a previously accepted candle through
// its window. The instrument feed owns the original immutable dataset.
type mutatingDetector struct{ scriptedDetector }

func (d mutatingDetector) Detect(_ instrument.Spec, b []domain.Candle, _ int64) (Setup, bool) {
	b[0].Close = 999
	return Setup{}, false
}
func TestStreamDetectorCannotMutateInput(t *testing.T) {
	bars := streamBars()
	spec, _ := instrument.Lookup("EURUSD")
	s, _ := NewStream("EURUSD", spec, time.Hour, mutatingDetector{}, DefaultBTParams())
	for _, b := range bars {
		if _, err := s.Advance(b, nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, b := range bars {
		if b.Close != 100 {
			t.Fatal("detector mutated source dataset")
		}
	}
}
