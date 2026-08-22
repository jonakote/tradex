package risk

import (
	"testing"
	"time"

	"tradex/internal/config"
	"tradex/internal/domain"
)

func validRequest() Request {
	now := time.Date(2026, time.August, 18, 10, 0, 0, 0, time.UTC)
	instrument := domain.Instrument{
		Symbol: "EURUSD", BaseCurrency: "EUR", QuoteCurrency: "USD",
		PriceScale: 100000, TickSize: 1, QuantityStep: 1000,
		MinQuantity: 1000, MaxQuantity: 10000000,
	}
	return Request{
		Now: now,
		Intent: domain.OrderIntent{
			ID: "i1", StrategyID: "icc", StrategyVersion: "v1", AccountID: "demo",
			Symbol: "EURUSD", Side: domain.SideBuy, Type: domain.OrderTypeMarket,
			Quantity: 1000, StopLoss: 108000, TakeProfit: 110000,
			SignalTime: now.Add(-time.Second), ExpiresAt: now.Add(time.Minute),
		},
		Market: domain.MarketSnapshot{
			Instrument: instrument, Bid: 108400, Ask: 108410, ObservedAt: now.Add(-time.Second),
		},
		EquityMinor: 1000000, EstimatedLossMinor: 2000, EstimatedRewardMinor: 5000,
		AggregateOpenRiskMinor: 0, AdapterConnected: true,
		BucketOpenRiskMinor: map[string]int64{}, ExposureBuckets: []string{"EUR", "USD"},
	}
}

func TestEngineApprovesRequestWithinEveryLimit(t *testing.T) {
	engine, err := New(config.DefaultRisk())
	if err != nil {
		t.Fatal(err)
	}
	decision := engine.Evaluate(validRequest())
	if !decision.Approved {
		t.Fatalf("request rejected: %s %s", decision.Code, decision.Reason)
	}
	if decision.RiskBudget != 2500 || decision.EstimatedLossMinor != 2000 || decision.EstimatedRewardMinor != 5000 {
		t.Fatalf("unexpected approved economics: %+v", decision)
	}
}

func TestEngineRejectsSafetyFailures(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Request)
		want   RejectionCode
	}{
		{"disconnected", func(r *Request) { r.AdapterConnected = false }, RejectAdapterDisconnected},
		{"mismatch", func(r *Request) { r.ReconciliationMismatch = true }, RejectReconciliationMismatch},
		{"stale", func(r *Request) { r.Market.ObservedAt = r.Now.Add(-time.Minute) }, RejectStaleMarket},
		{"trade risk", func(r *Request) { r.EstimatedLossMinor = 2501 }, RejectTradeRisk},
		{"aggregate", func(r *Request) { r.AggregateOpenRiskMinor = 6000 }, RejectAggregateRisk},
		{"open positions", func(r *Request) { r.OpenPositions = 3 }, RejectOpenPositions},
		{"same symbol", func(r *Request) { r.OpenPositions = 1; r.SymbolOpenPositions = 1 }, RejectSymbolPositions},
		{"exposure bucket", func(r *Request) { r.BucketOpenRiskMinor["USD"] = 3001 }, RejectExposureBucket},
		{"daily loss", func(r *Request) { r.DailyPnLMinor = -10000 }, RejectDailyLoss},
		{"weekly loss", func(r *Request) { r.WeeklyPnLMinor = -25000 }, RejectWeeklyLoss},
		{"drawdown", func(r *Request) { r.DrawdownBPS = 500 }, RejectDrawdown},
		{"loss streak", func(r *Request) { r.ConsecutiveLosses = 4 }, RejectConsecutiveLosses},
		{"reward risk", func(r *Request) { r.EstimatedRewardMinor = 3000; r.EstimatedLossMinor = 2000 }, RejectRewardRisk},
		{"spread", func(r *Request) { r.Market.Ask = 108700 }, RejectSpread},
	}
	engine, err := New(config.DefaultRisk())
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := validRequest()
			test.mutate(&req)
			decision := engine.Evaluate(req)
			if decision.Approved || decision.Code != test.want {
				t.Fatalf("got approved=%v code=%s, want false/%s", decision.Approved, decision.Code, test.want)
			}
		})
	}
}

func TestSizeByRiskFloorsToQuantityStep(t *testing.T) {
	quantity, err := SizeByRisk(SizeRequest{
		EquityMinor: 1000000, RiskBPS: 25, LossPerQuantityStepMinor: 200,
		QuantityStep: 100, MinQuantity: 100, MaxQuantity: 10000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if quantity != 1200 {
		t.Fatalf("quantity: got %d want 1200", quantity)
	}
}

func TestEngineRejectsInvalidOrderGeometryAndQuantity(t *testing.T) {
	engine, err := New(config.DefaultRisk())
	if err != nil {
		t.Fatal(err)
	}

	req := validRequest()
	req.Intent.Quantity = 1500
	if got := engine.Evaluate(req); got.Code != RejectInvalidIntent {
		t.Fatalf("quantity rejection: got %s want %s", got.Code, RejectInvalidIntent)
	}

	req = validRequest()
	req.Intent.StopLoss = req.Market.Ask
	if got := engine.Evaluate(req); got.Code != RejectInvalidIntent {
		t.Fatalf("geometry rejection: got %s want %s", got.Code, RejectInvalidIntent)
	}
}
