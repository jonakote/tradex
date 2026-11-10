package replay

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"tradex/internal/broker/paper"
	"tradex/internal/domain"
)

// TestReplayModelsPartialLiquidityFill caps the entry-triggering quote's
// available quantity below the sized order, then lets a second quote fill
// the remainder, proving partial fills flow through replay's real
// fills/report exactly like paper.Broker's own unit coverage
// (TestQuoteLiquidityProducesPartialFillsAndProtectiveExitCancelsRemainder)
// proves the broker mechanism itself.
func TestReplayModelsPartialLiquidityFill(t *testing.T) {
	scenario := loadFixture(t, "eurusd_continuation_win.json")
	scenario.Name = "eurusd-continuation-partial"
	events := cloneScenarioEvents(scenario.Events)
	entryQuote := events[7] // the 05:30 entry-triggering quote
	capped := int64(5000)
	entryQuote.AvailableQuantity = &capped
	remainder := *entryQuote.Quote
	remainder.At = remainder.At.Add(time.Minute)
	remainder.Sequence = 3
	remainderEvent := Event{At: remainder.At, Kind: EventQuote, Quote: &remainder}
	scenario.Events = append(append(append([]Event(nil), events[:7]...), entryQuote, remainderEvent), events[8:]...)

	result, err := Run(context.Background(), scenario, filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Fills) < 3 {
		t.Fatalf("expected at least two entry fills plus one exit, got %+v", result.Fills)
	}
	entries := 0
	var totalEntryQty int64
	for _, fill := range result.Fills {
		if fill.Role == domain.FillEntry {
			entries++
			totalEntryQty += fill.Quantity
		}
	}
	if entries < 2 {
		t.Fatalf("expected the order to split across two liquidity-capped fills, got %d", entries)
	}
	if result.Fills[0].Role == domain.FillEntry && result.Fills[0].Quantity != capped {
		t.Fatalf("first entry fill did not respect the disclosed liquidity cap: %+v", result.Fills[0])
	}
	if totalEntryQty != result.Orders[0].Quantity {
		t.Fatalf("split entry fills did not sum to the full sized order: total=%d order=%+v", totalEntryQty, result.Orders[0])
	}
	if result.Report.Trades != 1 {
		t.Fatalf("partial fills were not reconciled into one closed trade: %+v", result.Report)
	}
}

func TestReplayRejectsApprovedOrderWhenMarketClosesBeforeSubmission(t *testing.T) {
	scenario := loadFixture(t, "eurusd_continuation_win.json")
	scenario.Name = "eurusd-continuation-market-closed"
	events := cloneScenarioEvents(scenario.Events)
	closed := false
	// Close the market strictly before the DETECTED candle (index 6) is
	// processed, so the admission-time order submission is rejected.
	status := Event{At: events[5].At.Add(time.Second), Kind: EventMarketStatus, MarketOpen: &closed}
	scenario.Events = append(append(append([]Event(nil), events[:6]...), status), events[6:]...)

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
	scenario := loadFixture(t, "eurusd_continuation_win.json")
	scenario.Name = "eurusd-continuation-rollover"
	scenario.Paper.Rollover = paper.RolloverConfig{
		Enabled: true, MinuteUTC: 22 * 60,
		ChargeWeekdays: [7]bool{false, true, true, true, true, true, false},
		TripleWeekday:  time.Wednesday, TripleMultiplier: 3,
		LongPerQuantityStepMinor: -1, ShortPerQuantityStepMinor: 1,
	}
	events := cloneScenarioEvents(scenario.Events[:8]) // through the entry fill
	exit := *scenario.Events[9].Candle
	exit.OpenTime = exit.OpenTime.Add(24 * time.Hour)
	exit.CloseTime = exit.CloseTime.Add(24 * time.Hour)
	events = append(events, Event{At: exit.CloseTime, Kind: EventCandle, Candle: &exit})
	scenario.Events = events

	result, err := Run(context.Background(), scenario, filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Fills) != 2 {
		t.Fatalf("expected one entry and one exit fill, got %+v", result.Fills)
	}
	exitFill := result.Fills[1]
	if exitFill.RolloverMinor == 0 {
		t.Fatalf("rollover was not accrued into the closed trade: %+v", exitFill)
	}
	if exitFill.ClosedTradeNetMinor != result.Report.NetProfitMinor {
		t.Fatalf("rollover was not carried into replay analytics: fill=%+v report=%+v", exitFill, result.Report)
	}
}
