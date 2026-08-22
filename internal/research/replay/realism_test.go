package replay

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"tradex/internal/broker/paper"
	"tradex/internal/domain"
)

func TestReplayModelsLatencyPartialEntryAndRemainderCancellation(t *testing.T) {
	scenario := loadFixture(t, "eurusd_icc_win.json")
	scenario.Name = "eurusd-latency-partial"
	scenario.Paper.SubmissionLatency = 30 * time.Second
	events := cloneScenarioEvents(scenario.Events)
	available := int64(5000)
	entryQuote := *events[3].Quote
	entryQuote.At = events[4].At.Add(30 * time.Second)
	entryQuote.Sequence = 2
	partialEvent := Event{At: entryQuote.At, Kind: EventQuote, Quote: &entryQuote, AvailableQuantity: &available}
	noEntryLiquidity := int64(0)
	events[5].Quote.Sequence = 3
	events[5].AvailableQuantity = &noEntryLiquidity
	scenario.Events = append(append(append([]Event(nil), events[:5]...), partialEvent), events[5])

	result, err := Run(context.Background(), scenario, filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Orders) != 1 || result.Orders[0].Status != domain.OrderCancelled || result.Orders[0].Remaining != 6000 {
		t.Fatalf("partial order remainder was not cancelled: %+v", result.Orders)
	}
	if len(result.Fills) != 2 || result.Fills[0].Role != domain.FillEntry || result.Fills[0].Quantity != 5000 ||
		result.Fills[1].Role != domain.FillTakeProfit || result.Fills[1].Quantity != 5000 {
		t.Fatalf("unexpected partial-fill lifecycle: %+v", result.Fills)
	}
	if result.Report.Trades != 1 || result.Report.NetProfitMinor != 2100 || result.Portfolio.OpenPositions != 0 ||
		result.Portfolio.AggregateOpenRiskMinor != 0 {
		t.Fatalf("unexpected partial replay economics: report=%+v portfolio=%+v", result.Report, result.Portfolio)
	}
}

func TestReplayRejectsApprovedOrderWhenMarketClosesBeforeSubmission(t *testing.T) {
	scenario := loadFixture(t, "eurusd_icc_win.json")
	scenario.Name = "eurusd-market-closed"
	events := cloneScenarioEvents(scenario.Events)
	closed := false
	status := Event{At: events[3].At.Add(500 * time.Millisecond), Kind: EventMarketStatus, MarketOpen: &closed}
	scenario.Events = append(append(append([]Event(nil), events[:4]...), status), events[4:]...)

	result, err := Run(context.Background(), scenario, filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Decisions) != 1 || !result.Decisions[0].Approved || len(result.Orders) != 1 ||
		result.Orders[0].Status != domain.OrderRejected || result.Orders[0].RejectReason != "MARKET_CLOSED" {
		t.Fatalf("market closure was not modeled as an execution rejection: %+v", result)
	}
	if len(result.Fills) != 0 || result.Portfolio.OpenPositions != 0 || result.Portfolio.AggregateOpenRiskMinor != 0 {
		t.Fatalf("rejected execution retained fills or reserved risk: %+v", result)
	}
}

func TestReplayIncludesRolloverInClosedTradeNet(t *testing.T) {
	scenario := loadFixture(t, "eurusd_icc_win.json")
	scenario.Name = "eurusd-one-rollover"
	scenario.Paper.Rollover = paper.RolloverConfig{
		Enabled: true, MinuteUTC: 22 * 60,
		ChargeWeekdays: [7]bool{false, true, true, true, true, true, false},
		TripleWeekday:  time.Wednesday, TripleMultiplier: 3,
		LongPerQuantityStepMinor: -1, ShortPerQuantityStepMinor: 1,
	}
	events := cloneScenarioEvents(scenario.Events[:5])
	exit := *scenario.Events[5].Quote
	exit.At = exit.At.Add(24 * time.Hour)
	exit.Sequence = 2
	events = append(events, Event{At: exit.At, Kind: EventQuote, Quote: &exit})
	scenario.Events = events

	result, err := Run(context.Background(), scenario, filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Fills) != 2 || result.Fills[1].RolloverMinor != -11 ||
		result.Fills[1].ClosedTradeNetMinor != 4609 || result.Report.NetProfitMinor != 4609 {
		t.Fatalf("rollover was not carried into replay analytics: %+v", result)
	}
}
