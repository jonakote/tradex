package marketdata

import (
	"errors"
	"testing"
	"time"

	"tradex/internal/domain"
)

func quote(at time.Time, sequence uint64, bid, ask domain.Price) domain.Quote {
	return domain.Quote{Source: "pepperstone", Symbol: "EURUSD", Bid: bid, Ask: ask, At: at, Sequence: sequence}
}

func TestBarBuilderBuildsOHLCAndReportsGap(t *testing.T) {
	builder, err := NewBarBuilder("pepperstone", "EURUSD", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, time.August, 18, 10, 0, 10, 0, time.UTC)
	inputs := []domain.Quote{
		quote(t0, 1, 100, 102),
		quote(t0.Add(10*time.Second), 2, 104, 106),
		quote(t0.Add(20*time.Second), 3, 98, 100),
	}
	for _, input := range inputs {
		if update, err := builder.Push(input); err != nil || update.Completed != nil {
			t.Fatalf("unexpected update=%+v err=%v", update, err)
		}
	}

	update, err := builder.Push(quote(t0.Add(3*time.Minute), 4, 110, 112))
	if err != nil {
		t.Fatal(err)
	}
	if update.Completed == nil || update.GapPeriods != 2 {
		t.Fatalf("unexpected completion: %+v", update)
	}
	bar := update.Completed
	if bar.Open != 101 || bar.High != 105 || bar.Low != 99 || bar.Close != 99 || bar.TickCount != 3 {
		t.Fatalf("unexpected bar: %+v", bar)
	}
}

func TestBarBuilderRejectsDuplicateSequence(t *testing.T) {
	builder, _ := NewBarBuilder("pepperstone", "EURUSD", time.Minute)
	t0 := time.Date(2026, time.August, 18, 10, 0, 0, 0, time.UTC)
	if _, err := builder.Push(quote(t0, 1, 100, 102)); err != nil {
		t.Fatal(err)
	}
	if _, err := builder.Push(quote(t0.Add(time.Second), 1, 100, 102)); !errors.Is(err, ErrQuoteOutOfOrder) {
		t.Fatalf("got %v, want ErrQuoteOutOfOrder", err)
	}
}

func TestBarBuilderFinishIsOneWay(t *testing.T) {
	builder, _ := NewBarBuilder("pepperstone", "EURUSD", time.Minute)
	t0 := time.Date(2026, time.August, 18, 10, 0, 0, 0, time.UTC)
	if _, err := builder.Push(quote(t0, 1, 100, 102)); err != nil {
		t.Fatal(err)
	}
	if _, err := builder.Finish(); err != nil {
		t.Fatal(err)
	}
	if _, err := builder.Push(quote(t0.Add(time.Second), 2, 100, 102)); !errors.Is(err, ErrBuilderFinished) {
		t.Fatalf("got %v, want ErrBuilderFinished", err)
	}
}
