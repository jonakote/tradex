package replay

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestReplayRunsTwoSequentialDetectorCyclesIndependently appends a second,
// time-shifted copy of the fixture's full candle path after the first
// trade's exit, proving the detector can arm, fill and exit a fresh cycle
// without any ICC-style explicit state reset.
func TestReplayRunsTwoSequentialDetectorCyclesIndependently(t *testing.T) {
	scenario := loadFixture(t, "eurusd_continuation_win.json")
	second := shiftedEvents(scenario.Events, 24*time.Hour, 10)
	scenario.Name = "eurusd-continuation-two-wins"
	scenario.Events = append(scenario.Events, second...)
	result, err := Run(context.Background(), scenario, filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Decisions) != 2 || len(result.Orders) != 2 || len(result.Fills) != 4 {
		t.Fatalf("two detector cycles did not execute independently: %+v", result)
	}
	if result.Report.Trades != 2 || result.Report.Wins != 2 {
		t.Fatalf("unexpected sequential report: %+v", result.Report)
	}
	if result.Portfolio.AggregateOpenRiskMinor != 0 || result.Portfolio.OpenPositions != 0 {
		t.Fatalf("unexpected final portfolio: %+v", result.Portfolio)
	}
}

func shiftedEvents(source []Event, by time.Duration, quoteSequenceOffset uint64) []Event {
	result := make([]Event, len(source))
	for index, event := range source {
		copy := event
		copy.At = event.At.Add(by)
		if event.Candle != nil {
			candle := *event.Candle
			candle.OpenTime = candle.OpenTime.Add(by)
			candle.CloseTime = candle.CloseTime.Add(by)
			copy.Candle = &candle
		}
		if event.Quote != nil {
			quote := *event.Quote
			quote.At = quote.At.Add(by)
			quote.Sequence += quoteSequenceOffset
			copy.Quote = &quote
		}
		result[index] = copy
	}
	return result
}
