package clock

import (
	"testing"
	"time"
)

func TestManualClockCannotMoveBackward(t *testing.T) {
	t0 := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	manual := NewManual(t0)
	if err := manual.Set(t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := manual.Set(t0); err == nil {
		t.Fatal("manual clock moved backward")
	}
}
