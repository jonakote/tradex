package execution

import (
	"context"
	"testing"
	"time"

	"tradex/internal/broker/paper"
	"tradex/internal/clock"
	"tradex/internal/config"
	"tradex/internal/domain"
	"tradex/internal/economics"
	"tradex/internal/features"
	"tradex/internal/risk"
	"tradex/internal/strategy/icc"
)

func executionFixture(t *testing.T) (ICCPlan, risk.Engine, ICCRequest) {
	t.Helper()
	limits := config.DefaultRisk()
	engine, err := risk.New(limits)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	instrument := domain.Instrument{
		Symbol: "EURUSD", BaseCurrency: "EUR", QuoteCurrency: "USD",
		PriceScale: 100000, TickSize: 1, QuantityStep: 1000,
		MinQuantity: 1000, MaxQuantity: 1000000,
	}
	plan := ICCPlan{
		AccountID: "paper-1", StrategyID: "icc", StrategyVersion: "v1",
		IntentTTL: time.Minute, RiskBPS: 25, RewardRiskBPS: 20000,
		StopBufferTicks: 2, SlippageTicks: 1,
		RoundTripCommissionPerStepMinor: 4,
		PriceValue:                      economics.PriceValue{AccountCurrency: "USD", Numerator: 1, Denominator: 1},
	}
	request := ICCRequest{
		IntentID: "icc-1", ApprovalID: "risk-icc-1", Now: now,
		Signal: icc.Signal{
			Direction: features.BreakBullish, Symbol: "EURUSD", KnownAt: now.Add(-time.Second),
			EntryReference: 108400, StopReference: 108350, IndicationLevel: 108380,
		},
		Market: domain.MarketSnapshot{
			Instrument: instrument, Bid: 108400, Ask: 108402, ObservedAt: now.Add(-time.Second),
		},
		Context: RiskContext{EquityMinor: 1000000, AdapterConnected: true},
	}
	return plan, engine, request
}

func TestPrepareICCProducesRiskApprovedSizedOrder(t *testing.T) {
	plan, engine, request := executionFixture(t)
	approved, decision, err := PrepareICC(plan, engine, request)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Approved {
		t.Fatalf("risk rejected order: %s %s", decision.Code, decision.Reason)
	}
	if approved.Intent.Quantity != 41000 {
		t.Fatalf("quantity: got %d want 41000", approved.Intent.Quantity)
	}
	if approved.Intent.StopLoss != 108348 || approved.Intent.TakeProfit != 108527 {
		t.Fatalf("unexpected protective levels: %+v", approved.Intent)
	}
	if approved.ApprovalID != request.ApprovalID {
		t.Fatalf("approval ID: got %s", approved.ApprovalID)
	}
}

func TestPrepareICCReturnsRiskVetoWithoutApprovedOrder(t *testing.T) {
	plan, engine, request := executionFixture(t)
	request.Context.AdapterConnected = false
	approved, decision, err := PrepareICC(plan, engine, request)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Code != risk.RejectAdapterDisconnected || approved.ApprovalID != "" {
		t.Fatalf("unexpected veto result: approved=%+v decision=%+v", approved, decision)
	}
}

func TestPrepareICCRejectsInvalidStopBeforeRisk(t *testing.T) {
	plan, engine, request := executionFixture(t)
	request.Signal.StopReference = request.Market.Ask + 10
	if _, _, err := PrepareICC(plan, engine, request); err == nil {
		t.Fatal("invalid signal stop accepted")
	}
}

func TestICCSignalToPaperFillPipeline(t *testing.T) {
	plan, engine, request := executionFixture(t)
	approved, decision, err := PrepareICC(plan, engine, request)
	if err != nil || !decision.Approved {
		t.Fatalf("prepare: decision=%+v err=%v", decision, err)
	}
	paperBroker, err := paper.New(paper.Config{
		AccountID: plan.AccountID, MaxQuoteAge: 5 * time.Second,
		Instruments: map[string]paper.InstrumentConfig{
			"EURUSD": {
				Instrument: request.Market.Instrument, SlippageTicks: plan.SlippageTicks,
				HalfSpreadTicksForCandle: 1, CommissionPerQuantityStepMinor: 2,
				PriceValue: plan.PriceValue,
			},
		},
	}, clock.Fixed{Time: request.Now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := paperBroker.OnQuote(domain.Quote{
		Source: "paper-feed", Symbol: "EURUSD", Bid: request.Market.Bid,
		Ask: request.Market.Ask, At: request.Market.ObservedAt, Sequence: 1,
	}); err != nil {
		t.Fatal(err)
	}
	order, err := paperBroker.Submit(context.Background(), approved)
	if err != nil {
		t.Fatal(err)
	}
	if order.Status != domain.OrderFilled || len(paperBroker.Fills()) != 1 {
		t.Fatalf("approved ICC order was not filled: order=%+v fills=%+v", order, paperBroker.Fills())
	}
}
