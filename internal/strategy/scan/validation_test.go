package scan

import (
	"testing"
	"time"
	"tradex/internal/instrument"
)

func TestStreamRejectsUnverifiedPrecision(t *testing.T) {
	spec, _ := instrument.Lookup("EURUSD")
	bad := spec
	bad.PriceScale = 100
	for _, s := range []instrument.Spec{{}, bad} {
		if _, err := NewStream("EURUSD", s, time.Hour, IBS{}, DefaultBTParams()); err == nil {
			t.Fatal("accepted unverified precision")
		}
	}
}
func TestBacktestRejectsNilDetector(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil detector panicked: %v", r)
		}
	}()
	spec, _ := instrument.Lookup("EURUSD")
	if _, _, err := Backtest("EURUSD", spec, time.Hour, streamBars(), nil, DefaultBTParams()); err == nil {
		t.Fatal("nil detector accepted")
	}
}
