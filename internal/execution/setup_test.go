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
	"tradex/internal/risk"
	"tradex/internal/strategy/scan"
)

func setupFixture(t *testing.T) (SetupPlan, risk.Engine, SetupRequest) {
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
	plan := SetupPlan{
		AccountID: "paper-1", StrategyID: "continuation", StrategyVersion: "v1",
		IntentTTL: time.Minute, RiskBPS: 25, SlippageTicks: 1,
		RoundTripCommissionPerStepMinor:  4,
		PriceValue:                       economics.PriceValue{AccountCurrency: "USD", Numerator: 1, Denominator: 1},
		StoplessMaxAdverseExcursionTicks: 50,
	}
	setup := scan.Setup{
		Side: "buy", EntryType: "LIMIT", Entry: 108400, Stop: 108350, Target: 108600,
		DetectedAt: now.Add(-time.Second), RiskTicks: 50, Reward: 200,
	}
	request := SetupRequest{
		IntentID: "setup-1", ApprovalID: "risk-setup-1", Now: now, Setup: setup, Symbol: "EURUSD",
		Market: domain.MarketSnapshot{
			Instrument: instrument, Bid: 108400, Ask: 108402, ObservedAt: now.Add(-time.Second),
		},
		Context: RiskContext{EquityMinor: 1000000, AdapterConnected: true},
	}
	return plan, engine, request
}

func TestPrepareSetupProducesRiskApprovedSizedOrder(t *testing.T) {
	plan, engine, request := setupFixture(t)
	approved, decision, err := PrepareSetup(plan, engine, request)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Approved {
		t.Fatalf("risk rejected order: %s %s", decision.Code, decision.Reason)
	}
	if approved.Intent.Quantity != 45000 {
		t.Fatalf("quantity: got %d want 45000", approved.Intent.Quantity)
	}
	if approved.Intent.StopLoss != 108350 || approved.Intent.TakeProfit != 108600 {
		t.Fatalf("unexpected protective levels: %+v", approved.Intent)
	}
	if approved.ApprovalID != request.ApprovalID {
		t.Fatalf("approval ID: got %s", approved.ApprovalID)
	}
}

func TestPrepareSetupReturnsRiskVetoWithoutApprovedOrder(t *testing.T) {
	plan, engine, request := setupFixture(t)
	request.Context.AdapterConnected = false
	approved, decision, err := PrepareSetup(plan, engine, request)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Code != risk.RejectAdapterDisconnected || approved.ApprovalID != "" {
		t.Fatalf("unexpected veto result: approved=%+v decision=%+v", approved, decision)
	}
}

func TestPrepareSetupRejectsInvalidGeometryBeforeRisk(t *testing.T) {
	plan, engine, request := setupFixture(t)
	request.Setup.Stop = request.Setup.Entry + 10 // stop on the wrong side of a buy
	if _, _, err := PrepareSetup(plan, engine, request); err == nil {
		t.Fatal("invalid setup geometry accepted")
	}
}

// TestPrepareSetupInstallsConservativeBracketForStoplessSetup proves the
// fail-closed policy for a genuinely stopless (hold-bars) detector such as
// IBS: PrepareSetup never reads Setup.RiskTicks as a loss bound for a
// time-exit setup; it installs the explicit, symmetric
// StoplessMaxAdverseExcursionTicks bracket instead.
func TestPrepareSetupInstallsConservativeBracketForStoplessSetup(t *testing.T) {
	plan, _, request := setupFixture(t)
	// The symmetric protective bracket is exactly 1:1 reward:risk by
	// construction (see StoplessMaxAdverseExcursionTicks), so this test
	// uses a lenient risk engine whose reward:risk floor that 1:1 bracket
	// can actually clear -- the point under test is which geometry gets
	// installed, not whether the default 2R floor approves it.
	lenientLimits := config.DefaultRisk()
	lenientLimits.MinRewardRiskBPS = 1
	engine, err := risk.New(lenientLimits)
	if err != nil {
		t.Fatal(err)
	}
	request.Setup = scan.Setup{
		Side: "buy", EntryType: "MARKET", Entry: 108400, HoldBars: 1,
		DetectedAt: request.Setup.DetectedAt, RiskTicks: 9999, // a measurement convention, not a stop
	}
	approved, decision, err := PrepareSetup(plan, engine, request)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Approved {
		t.Fatalf("risk rejected the bracketed stopless order: %s %s", decision.Code, decision.Reason)
	}
	expectedFill := request.Market.Ask + domain.Price(plan.SlippageTicks)
	excursion := domain.Price(plan.StoplessMaxAdverseExcursionTicks)
	if approved.Intent.StopLoss != expectedFill-excursion || approved.Intent.TakeProfit != expectedFill+excursion {
		t.Fatalf("stopless bracket did not use the policy excursion, not RiskTicks: %+v", approved.Intent)
	}
}

func TestSetupSignalToPaperFillPipeline(t *testing.T) {
	plan, engine, request := setupFixture(t)
	request.Setup.EntryType = "MARKET" // exercise the broker fill path, not limit-crossing (paper package's own)
	approved, decision, err := PrepareSetup(plan, engine, request)
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
		t.Fatalf("approved order was not filled: order=%+v fills=%+v", order, paperBroker.Fills())
	}
}
