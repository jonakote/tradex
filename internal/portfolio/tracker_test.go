package portfolio

import (
	"testing"
	"time"

	"tradex/internal/domain"
)

func TestTrackerMaintainsRiskEquityDrawdownAndLossState(t *testing.T) {
	tracker, err := New(1000000)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	eurusd := domain.Instrument{Symbol: "EURUSD", BaseCurrency: "EUR", QuoteCurrency: "USD", PriceScale: 100000, TickSize: 1, QuantityStep: 1000, MinQuantity: 1000, MaxQuantity: 1000000}
	if err := tracker.Reserve("i1", eurusd, domain.SideBuy, 2500); err != nil {
		t.Fatal(err)
	}
	view, err := tracker.View(at, "EURUSD")
	if err != nil || view.AggregateOpenRiskMinor != 2500 || view.OpenPositions != 1 || view.SymbolOpenPositions != 1 {
		t.Fatalf("unexpected reserved view=%+v err=%v", view, err)
	}
	if view.BucketOpenRiskMinor["EUR"] != 2500 || view.BucketOpenRiskMinor["USD"] != 2500 ||
		view.DirectionalRiskMinor["EUR"] != 2500 || view.DirectionalRiskMinor["USD"] != -2500 {
		t.Fatalf("unexpected FX exposure buckets: %+v", view)
	}
	if err := tracker.ApplyClosingFill(domain.Fill{IntentID: "i1", Symbol: "EURUSD", Role: domain.FillStopLoss,
		ClosedTradeNetMinor: -2600, FilledAt: at.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	view, _ = tracker.View(at.Add(time.Hour), "EURUSD")
	if view.EquityMinor != 997400 || view.DailyPnLMinor != -2600 || view.WeeklyPnLMinor != -2600 ||
		view.DrawdownBPS != 26 || view.ConsecutiveLosses != 1 || view.OpenPositions != 0 {
		t.Fatalf("unexpected closed view: %+v", view)
	}
	if len(view.BucketOpenRiskMinor) != 0 || len(view.DirectionalRiskMinor) != 0 {
		t.Fatalf("closed exposure buckets were retained: %+v", view)
	}
}

func TestTrackerCombinesCurrencyAndMetalBucketsWithoutNettingGrossRisk(t *testing.T) {
	tracker, _ := New(1000000)
	eurusd := domain.Instrument{Symbol: "EURUSD", BaseCurrency: "EUR", QuoteCurrency: "USD", PriceScale: 100000, TickSize: 1, QuantityStep: 1000, MinQuantity: 1000, MaxQuantity: 1000000}
	gbpusd := eurusd
	gbpusd.Symbol, gbpusd.BaseCurrency = "GBPUSD", "GBP"
	if err := tracker.Reserve("eur", eurusd, domain.SideBuy, 2000); err != nil {
		t.Fatal(err)
	}
	if err := tracker.Reserve("gbp", gbpusd, domain.SideSell, 1500); err != nil {
		t.Fatal(err)
	}
	view, _ := tracker.View(time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC), "XAUUSD")
	if view.BucketOpenRiskMinor["USD"] != 3500 {
		t.Fatalf("gross USD risk should not net: %+v", view)
	}
	if view.DirectionalRiskMinor["USD"] != -500 {
		t.Fatalf("signed USD direction should show partial offset: %+v", view)
	}
}

func TestTrackerResetsPeriodPnLButRetainsLossStreak(t *testing.T) {
	tracker, _ := New(1000000)
	friday := time.Date(2026, 8, 21, 20, 0, 0, 0, time.UTC)
	xauusd := domain.Instrument{Symbol: "XAUUSD", BaseCurrency: "XAU", QuoteCurrency: "USD", PriceScale: 100, TickSize: 1, QuantityStep: 1, MinQuantity: 1, MaxQuantity: 1000}
	_ = tracker.Reserve("i1", xauusd, domain.SideBuy, 2500)
	_ = tracker.ApplyClosingFill(domain.Fill{IntentID: "i1", Symbol: "XAUUSD", Role: domain.FillStopLoss,
		ClosedTradeNetMinor: -2000, FilledAt: friday})
	monday := time.Date(2026, 8, 24, 8, 0, 0, 0, time.UTC)
	view, err := tracker.View(monday, "XAUUSD")
	if err != nil || view.DailyPnLMinor != 0 || view.WeeklyPnLMinor != 0 || view.ConsecutiveLosses != 1 {
		t.Fatalf("unexpected new-week view=%+v err=%v", view, err)
	}
}
