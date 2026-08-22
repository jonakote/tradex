package replay

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"tradex/internal/domain"
	"tradex/internal/journal"
	"tradex/internal/strategy/icc"
)

func TestEURUSDScenarioProducesCostAdjustedWinner(t *testing.T) {
	scenario := loadFixture(t, "eurusd_icc_win.json")
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	result, err := Run(context.Background(), scenario, auditPath)
	if err != nil {
		t.Fatal(err)
	}

	assertCompletedTrade(t, result, 11000, 108103, 108527, domain.FillTakeProfit, 4620)
	if result.Report.Trades != 1 || result.Report.Wins != 1 || result.Report.Losses != 0 ||
		result.Report.NetProfitMinor != 4620 || result.Report.TotalCommissionMinor != 44 ||
		result.Report.MaxDrawdownMinor != 0 {
		t.Fatalf("unexpected EUR/USD report: %+v", result.Report)
	}
	assertJournal(t, auditPath)

	second, err := Run(context.Background(), scenario, filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result, second) {
		t.Fatal("identical replay input did not produce an identical result")
	}
}

func TestXAUUSDScenarioModelsGapThroughStop(t *testing.T) {
	scenario := loadFixture(t, "xauusd_icc_loss.json")
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	result, err := Run(context.Background(), scenario, auditPath)
	if err != nil {
		t.Fatal(err)
	}

	assertCompletedTrade(t, result, 7, 240007, 239648, domain.FillStopLoss, -2583)
	if result.Report.Trades != 1 || result.Report.Wins != 0 || result.Report.Losses != 1 ||
		result.Report.NetProfitMinor != -2583 || result.Report.TotalCommissionMinor != 70 ||
		result.Report.MaxDrawdownMinor != 2583 || result.Report.MaxConsecutiveLosses != 1 {
		t.Fatalf("unexpected XAU/USD report: %+v", result.Report)
	}
	assertJournal(t, auditPath)
}

func TestDecodeRejectsTrailingJSON(t *testing.T) {
	_, err := Decode(strings.NewReader(`{} {}`))
	if err == nil || !strings.Contains(err.Error(), "trailing JSON") {
		t.Fatalf("expected trailing JSON error, got %v", err)
	}
}

func TestVerifiedReplayRejectsDatasetChangedAfterManifestFreeze(t *testing.T) {
	scenario := loadFixture(t, "eurusd_icc_win.json")
	frozen, err := ManifestFor(scenario)
	if err != nil {
		t.Fatal(err)
	}
	scenario.Events[len(scenario.Events)-1].Quote.Bid--
	actual, err := ManifestFor(scenario)
	if err != nil {
		t.Fatal(err)
	}
	if actual.Dataset.SHA256 == frozen.Dataset.SHA256 {
		t.Fatal("dataset mutation did not change its fingerprint")
	}
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	_, err = RunVerified(context.Background(), scenario, frozen, auditPath)
	if err == nil || !strings.Contains(err.Error(), "frozen experiment manifest") {
		t.Fatalf("expected frozen-manifest rejection, got %v", err)
	}
	if _, statErr := os.Stat(auditPath); !os.IsNotExist(statErr) {
		t.Fatalf("rejected replay created an audit file: %v", statErr)
	}
}

func TestScenarioValidationRejectsMismatchedExecutionSymbolAndTime(t *testing.T) {
	base := loadFixture(t, "eurusd_icc_win.json")
	tests := []struct {
		name   string
		mutate func(*Scenario)
	}{
		{name: "execution account", mutate: func(s *Scenario) { s.Execution.AccountID = "other" }},
		{name: "candle symbol", mutate: func(s *Scenario) { s.Events[1].Candle.Symbol = "GBPUSD" }},
		{name: "quote symbol", mutate: func(s *Scenario) { s.Events[3].Quote.Symbol = "GBPUSD" }},
		{name: "event order", mutate: func(s *Scenario) { s.Events[2].At = s.Events[1].At.Add(-time.Second) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scenario := base
			scenario.Events = cloneScenarioEvents(base.Events)
			test.mutate(&scenario)
			if err := scenario.Validate(); err == nil {
				t.Fatal("invalid scenario passed validation")
			}
		})
	}
}

func cloneScenarioEvents(source []Event) []Event {
	result := make([]Event, len(source))
	for index, event := range source {
		result[index] = event
		if event.Candle != nil {
			candle := *event.Candle
			result[index].Candle = &candle
		}
		if event.Quote != nil {
			quote := *event.Quote
			result[index].Quote = &quote
		}
	}
	return result
}

func loadFixture(t *testing.T, name string) Scenario {
	t.Helper()
	scenario, err := Load(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return scenario
}

func assertCompletedTrade(
	t *testing.T,
	result Result,
	quantity int64,
	entryPrice, exitPrice domain.Price,
	exitRole domain.FillRole,
	closedNet int64,
) {
	t.Helper()
	if len(result.Transitions) != 3 || result.Transitions[2].To != icc.Completed {
		t.Fatalf("ICC did not complete: %+v", result.Transitions)
	}
	if len(result.Decisions) != 1 || !result.Decisions[0].Approved {
		t.Fatalf("risk did not approve exactly once: %+v", result.Decisions)
	}
	if len(result.Orders) != 1 || result.Orders[0].Status != domain.OrderFilled ||
		result.Orders[0].Quantity != quantity {
		t.Fatalf("unexpected order: %+v", result.Orders)
	}
	if len(result.Fills) != 2 {
		t.Fatalf("expected entry and exit fills, got %+v", result.Fills)
	}
	entry, exit := result.Fills[0], result.Fills[1]
	if entry.Role != domain.FillEntry || entry.Price != entryPrice || entry.Quantity != quantity {
		t.Fatalf("unexpected entry fill: %+v", entry)
	}
	if exit.Role != exitRole || exit.Price != exitPrice || exit.Quantity != quantity ||
		exit.ClosedTradeNetMinor != closedNet {
		t.Fatalf("unexpected exit fill: %+v", exit)
	}
}

func assertJournal(t *testing.T, path string) {
	t.Helper()
	events, err := journal.ReadAll(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 11 || events[0].Kind != "EXPERIMENT_MANIFEST" || events[len(events)-1].Kind != "REPORT" {
		t.Fatalf("unexpected verified audit trail: %+v", events)
	}
}
