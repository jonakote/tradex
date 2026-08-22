package replay

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"tradex/internal/risk"
)

func TestReplayResetsICCAndCarriesPortfolioAcrossSequentialTrades(t *testing.T) {
	scenario := loadFixture(t, "eurusd_icc_win.json")
	second := shiftedEvents(scenario.Events, 24*time.Hour, 2)
	scenario.Name = "eurusd-two-wins"
	scenario.Events = append(scenario.Events, second...)
	result, err := Run(context.Background(), scenario, filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Transitions) != 6 || len(result.Decisions) != 2 || len(result.Orders) != 2 || len(result.Fills) != 4 {
		t.Fatalf("two ICC cycles did not execute independently: %+v", result)
	}
	if result.Report.Trades != 2 || result.Report.Wins != 2 || result.Report.NetProfitMinor != 9240 {
		t.Fatalf("unexpected sequential report: %+v", result.Report)
	}
	if result.Portfolio.EquityMinor != 1009240 || result.Portfolio.AggregateOpenRiskMinor != 0 || result.Portfolio.OpenPositions != 0 {
		t.Fatalf("unexpected final portfolio: %+v", result.Portfolio)
	}
}

func TestReplayRejectsOverlappingSameSymbolPosition(t *testing.T) {
	scenario := loadFixture(t, "eurusd_icc_win.json")
	firstWithoutExit := append([]Event(nil), scenario.Events[:5]...)
	second := shiftedEvents(scenario.Events[:5], 3*time.Hour, 1)
	closingQuote := *scenario.Events[5].Quote
	closingQuote.At = second[len(second)-1].At.Add(time.Minute)
	closingQuote.Sequence = 3
	scenario.Name = "eurusd-overlap-veto"
	scenario.Events = append(firstWithoutExit, second...)
	scenario.Events = append(scenario.Events, Event{At: closingQuote.At, Kind: EventQuote, Quote: &closingQuote})

	result, err := Run(context.Background(), scenario, filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Decisions) != 2 || !result.Decisions[0].Approved || result.Decisions[1].Approved ||
		result.Decisions[1].Code != risk.RejectSymbolPositions {
		t.Fatalf("same-symbol overlap was not vetoed: %+v", result.Decisions)
	}
	if len(result.Orders) != 1 || result.Report.Trades != 1 || result.Portfolio.OpenPositions != 0 {
		t.Fatalf("unexpected overlap replay result: %+v", result)
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
