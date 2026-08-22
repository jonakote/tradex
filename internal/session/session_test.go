package session

import (
	"testing"
	"time"
)

func TestLondonSessionTracksDaylightSaving(t *testing.T) {
	sessions, err := Defaults()
	if err != nil {
		t.Fatal(err)
	}

	winterStart, _ := sessions.London.Window(time.Date(2026, time.January, 12, 0, 0, 0, 0, time.UTC))
	if got, want := winterStart.Hour(), 8; got != want {
		t.Fatalf("winter London start hour: got %d want %d", got, want)
	}

	summerStart, _ := sessions.London.Window(time.Date(2026, time.July, 13, 0, 0, 0, 0, time.UTC))
	if got, want := summerStart.Hour(), 7; got != want {
		t.Fatalf("summer London start hour: got %d want %d", got, want)
	}
}

func TestLondonNewYorkOverlap(t *testing.T) {
	sessions, err := Defaults()
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, time.July, 13, 13, 0, 0, 0, time.UTC)
	if !sessions.LondonNewYorkOverlap(at) {
		t.Fatal("expected London/New York overlap")
	}
	if sessions.LondonNewYorkOverlap(time.Date(2026, time.July, 13, 10, 0, 0, 0, time.UTC)) {
		t.Fatal("unexpected overlap before New York session")
	}
}

func TestOvernightSession(t *testing.T) {
	d, err := New("overnight", "UTC", 22, 0, 6, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Contains(time.Date(2026, time.August, 18, 23, 0, 0, 0, time.UTC)) ||
		!d.Contains(time.Date(2026, time.August, 19, 5, 0, 0, 0, time.UTC)) {
		t.Fatal("overnight session should contain both sides of midnight")
	}
	start, end := d.Window(time.Date(2026, time.August, 19, 5, 0, 0, 0, time.UTC))
	if start.Day() != 18 || start.Hour() != 22 || end.Day() != 19 || end.Hour() != 6 {
		t.Fatalf("unexpected overnight window: %s - %s", start, end)
	}
}
