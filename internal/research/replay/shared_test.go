package replay

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"tradex/internal/journal"
	"tradex/internal/risk"
)

func TestSharedReplayEnforcesCrossSymbolExposureInEventTimeOrder(t *testing.T) {
	base := loadFixture(t, "eurusd_icc_win.json")
	equity := base.InitialEquityMinor
	limits := base.Risk

	eurusd := sharedLeg(base, "EURUSD", "EUR", 0)
	gbpusd := sharedLeg(base, "GBPUSD", "GBP", time.Hour)
	audusd := sharedLeg(base, "AUDUSD", "AUD", 2*time.Hour)
	scenario := SharedScenario{
		Name:               "shared-usd-exposure-cap",
		InitialEquityMinor: equity,
		Risk:               limits,
		Legs:               []Scenario{audusd, eurusd, gbpusd},
	}
	manifest, err := SharedManifestFor(scenario)
	if err != nil {
		t.Fatal(err)
	}
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	result, err := RunSharedVerified(context.Background(), scenario, manifest, auditPath)
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Decisions) != 3 {
		t.Fatalf("expected three decisions, got %+v", result.Decisions)
	}
	if result.Decisions[0].Symbol != "EURUSD" || !result.Decisions[0].Decision.Approved ||
		result.Decisions[1].Symbol != "GBPUSD" || !result.Decisions[1].Decision.Approved {
		t.Fatalf("event-time ordering did not approve the first two legs: %+v", result.Decisions)
	}
	third := result.Decisions[2]
	if third.Symbol != "AUDUSD" || third.Decision.Approved ||
		third.Decision.Code != risk.RejectExposureBucket || third.Decision.BreachedBucket != "USD" {
		t.Fatalf("third correlated trade was not rejected on USD exposure: %+v", third)
	}
	if len(result.Orders) != 2 || len(result.Fills) != 2 || result.Report.Trades != 0 {
		t.Fatalf("unexpected shared execution result: %+v", result)
	}
	if result.Portfolio.OpenPositions != 2 || result.Portfolio.AggregateOpenRiskMinor <= 0 ||
		result.Portfolio.BucketOpenRiskMinor["USD"] != result.Portfolio.AggregateOpenRiskMinor {
		t.Fatalf("unexpected shared portfolio: %+v", result.Portfolio)
	}
	for _, symbol := range []string{"EURUSD", "GBPUSD", "AUDUSD"} {
		if len(result.Transitions[symbol]) != 3 {
			t.Fatalf("%s transition history was not retained: %+v", symbol, result.Transitions[symbol])
		}
	}
	events, err := journal.ReadAll(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	if events[0].Kind != "EXPERIMENT_MANIFEST" || events[len(events)-1].Kind != "REPORT" {
		t.Fatalf("shared audit is incomplete: %+v", events)
	}
}

func TestSharedReplayBreaksEqualTimestampsBySymbol(t *testing.T) {
	base := loadFixture(t, "eurusd_icc_win.json")
	scenario := SharedScenario{
		Name:               "shared-lexical-tie-break",
		InitialEquityMinor: base.InitialEquityMinor,
		Risk:               base.Risk,
		Legs: []Scenario{
			sharedLeg(base, "GBPUSD", "GBP", 0),
			sharedLeg(base, "EURUSD", "EUR", 0),
		},
	}
	result, err := RunShared(context.Background(), scenario, filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Decisions) != 2 || result.Decisions[0].Symbol != "EURUSD" || result.Decisions[1].Symbol != "GBPUSD" {
		t.Fatalf("equal timestamps did not use lexical symbol ordering: %+v", result.Decisions)
	}
}

func TestSharedScenarioRejectsExecutionAccountMismatch(t *testing.T) {
	base := loadFixture(t, "eurusd_icc_win.json")
	eurusd := sharedLeg(base, "EURUSD", "EUR", 0)
	gbpusd := sharedLeg(base, "GBPUSD", "GBP", 0)
	gbpusd.Execution.AccountID = "another-account"
	scenario := SharedScenario{
		Name: "shared-account-mismatch", InitialEquityMinor: base.InitialEquityMinor, Risk: base.Risk,
		Legs: []Scenario{eurusd, gbpusd},
	}
	if err := scenario.Validate(); err == nil {
		t.Fatal("shared scenario accepted an execution plan for another account")
	}
}

func sharedLeg(base Scenario, symbol, baseCurrency string, shift time.Duration) Scenario {
	leg := base
	leg.Name = symbol + "-open"
	leg.Instrument.Symbol = symbol
	leg.Instrument.BaseCurrency = baseCurrency
	leg.Events = shiftedEvents(base.Events[:5], shift, 0)
	for index := range leg.Events {
		if leg.Events[index].Candle != nil {
			leg.Events[index].Candle.Symbol = symbol
		}
		if leg.Events[index].Quote != nil {
			leg.Events[index].Quote.Symbol = symbol
		}
	}
	return leg
}
