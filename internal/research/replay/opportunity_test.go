package replay

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"tradex/internal/domain"
)

func TestReplayRecordsClosedOpportunitySeparatelyFromTrade(t *testing.T) {
	scenario := loadFixture(t, "eurusd_continuation_win.json")
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

// TestReplayRecordsExpiredOpportunityWithoutEntry truncates the fixture
// before the retest quote/candle ever reach the resting limit, so the
// detected setup must expire (scan.Stream's ExpiryBars) rather than fill --
// the generic-detector analogue of the old ICC "invalidated without
// confirmation" coverage.
func TestReplayRecordsExpiredOpportunityWithoutEntry(t *testing.T) {
	scenario := loadFixture(t, "eurusd_continuation_win.json")
	scenario.Name = "eurusd-continuation-expire"
	scenario.DetectorParams.ExpiryBars = 1
	// Keep every bar through DETECTED (index 6, the 05:00 candle), then
	// replace what follows with one flat bar that never dips to the 108050
	// retest level, so the armed limit ages out via ExpiryBars=1 instead of
	// ever touching -- scan.Stream's own expiry, not a hand-labelled one.
	nonTouching := *scenario.Events[6].Candle
	nonTouching.OpenTime = nonTouching.CloseTime
	nonTouching.CloseTime = nonTouching.CloseTime.Add(time.Hour)
	nonTouching.Open, nonTouching.High, nonTouching.Low, nonTouching.Close = 108290, 108295, 108250, 108270
	scenario.Events = append(append([]Event(nil), scenario.Events[:7]...),
		Event{At: nonTouching.CloseTime, Kind: EventCandle, Candle: &nonTouching})
	result, err := Run(context.Background(), scenario, filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Opportunities) != 1 || result.Opportunities[0].Status != OpportunityExpired {
		t.Fatalf("expected expired opportunity, got %+v", result.Opportunities)
	}
	if len(result.Fills) != 0 {
		t.Fatalf("expired setup produced fills: %+v", result.Fills)
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

func scenarioEndTime() (resultTime time.Time) { return time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC) }
