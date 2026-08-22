package replay

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"tradex/internal/domain"
)

func TestReplayRecordsClosedOpportunitySeparatelyFromTrade(t *testing.T) {
	scenario, err := Load(filepath.Join("testdata", "eurusd_icc_win.json"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := Run(context.Background(), scenario, filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Opportunities) != 1 {
		t.Fatalf("expected one opportunity, got %+v", result.Opportunities)
	}
	opportunity := result.Opportunities[0]
	if opportunity.Status != OpportunityClosed || opportunity.IntentID == "" || opportunity.EntryQuantity <= 0 || opportunity.ClosedQuantity != opportunity.EntryQuantity {
		t.Fatalf("unexpected closed opportunity: %+v", opportunity)
	}
	if result.Report.Trades != 1 {
		t.Fatalf("opportunity accounting changed trade accounting: %+v", result.Report)
	}
}

func TestReplayRecordsInvalidatedOpportunityWithoutConfirmation(t *testing.T) {
	scenario, err := Load(filepath.Join("testdata", "eurusd_icc_win.json"))
	if err != nil {
		t.Fatal(err)
	}
	events := make([]Event, 0, len(scenario.Events))
	for _, event := range scenario.Events {
		if event.Kind == EventConfirmation {
			continue
		}
		events = append(events, event)
	}
	for index := range events {
		if events[index].Kind == EventSetupCandle {
			candle := *events[index].Candle
			candle.Low = 106900
			candle.Close = 107000
			events[index].Candle = &candle
		}
	}
	scenario.Events = events
	result, err := Run(context.Background(), scenario, filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Opportunities) != 1 || result.Opportunities[0].Status != OpportunityInvalidated {
		t.Fatalf("expected invalidated opportunity, got %+v", result.Opportunities)
	}
	if len(result.Decisions) != 0 || len(result.Orders) != 0 || len(result.Fills) != 0 {
		t.Fatalf("invalidated setup generated execution artifacts: decisions=%+v orders=%+v fills=%+v", result.Decisions, result.Orders, result.Fills)
	}
}

func TestFinalizeOpportunityClassifiesOpenAndNoFill(t *testing.T) {
	builders := []*opportunityBuilder{
		{opportunity: Opportunity{ID: "open", IntentID: "open-intent"}},
		{opportunity: Opportunity{ID: "none", IntentID: "none-intent"}},
	}
	orders := []domain.BrokerOrder{
		{IntentID: "open-intent", BrokerOrderID: "open-order", Status: domain.OrderFilled, Quantity: 1000},
		{IntentID: "none-intent", BrokerOrderID: "none-order", Status: domain.OrderCancelled, Quantity: 1000},
	}
	fills := []domain.Fill{{IntentID: "open-intent", BrokerOrderID: "open-order", Role: domain.FillEntry, Quantity: 1000}}
	result := finalizeOpportunities(builders, orders, fills, scenarioEndTime())
	if result[0].Status != OpportunityOpen || result[1].Status != OpportunityNoFill {
		t.Fatalf("unexpected classifications: %+v", result)
	}
}

func scenarioEndTime() (resultTime time.Time) { return time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC) }
