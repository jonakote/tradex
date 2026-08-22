package backtest

import (
	"errors"
	"testing"
	"time"
)

type testEvent struct {
	at time.Time
	id int
}

func (e testEvent) EventTime() time.Time { return e.at }

func TestReplayIsDeterministicAndRejectsOutOfOrderEvents(t *testing.T) {
	t0 := time.Date(2026, time.August, 18, 0, 0, 0, 0, time.UTC)
	events := []testEvent{{t0, 1}, {t0, 2}, {t0.Add(time.Second), 3}}
	var got []int
	if err := Replay(events, func(e testEvent) error {
		got = append(got, e.id)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for i, want := range []int{1, 2, 3} {
		if got[i] != want {
			t.Fatalf("event %d: got %d want %d", i, got[i], want)
		}
	}

	err := Replay([]testEvent{{t0.Add(time.Second), 1}, {t0, 2}}, func(testEvent) error { return nil })
	if !errors.Is(err, ErrEventsOutOfOrder) {
		t.Fatalf("got %v, want ErrEventsOutOfOrder", err)
	}
}
