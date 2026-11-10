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
)

func TestEURUSDContinuationScenarioProducesCostAdjustedWinner(t *testing.T) {
	scenario := loadFixture(t, "eurusd_continuation_win.json")
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	result, err := Run(context.Background(), scenario, auditPath)
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Decisions) != 1 || !result.Decisions[0].Approved {
		t.Fatalf("risk did not approve exactly once: %+v", result.Decisions)
	}
	if len(result.Orders) != 1 || result.Orders[0].Status != domain.OrderFilled {
		t.Fatalf("unexpected order: %+v", result.Orders)
	}
	if len(result.Fills) != 2 {
		t.Fatalf("expected entry and exit fills, got %+v", result.Fills)
	}
	entry, exit := result.Fills[0], result.Fills[1]
	if entry.Role != domain.FillEntry || entry.Price != 108050 {
		t.Fatalf("unexpected entry fill: %+v", entry)
	}
	if exit.Role != domain.FillTakeProfit || exit.Price != 108300 {
		t.Fatalf("unexpected exit fill: %+v", exit)
	}
	if result.Report.Trades != 1 || result.Report.Wins != 1 || result.Report.Losses != 0 {
		t.Fatalf("unexpected report: %+v", result.Report)
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

func TestContinuationScenarioStopLossLoss(t *testing.T) {
	scenario := loadFixture(t, "eurusd_continuation_win.json")
	scenario.Name = "eurusd-continuation-loss"
	events := cloneScenarioEvents(scenario.Events)
	// Replace the target-reaching bar with one that instead takes out the
	// stop (107989), producing the mirror-image loss.
	loss := *events[9].Candle
	loss.High, loss.Low, loss.Close = 108070, 107980, 107990
	events[9].Candle = &loss
	scenario.Events = events

	result, err := Run(context.Background(), scenario, filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Fills) != 2 || result.Fills[1].Role != domain.FillStopLoss {
		t.Fatalf("unexpected loss-side fills: %+v", result.Fills)
	}
	if result.Report.Trades != 1 || result.Report.Wins != 0 || result.Report.Losses != 1 {
		t.Fatalf("unexpected loss report: %+v", result.Report)
	}
}

func TestDecodeRejectsTrailingJSON(t *testing.T) {
	_, err := Decode(strings.NewReader(`{} {}`))
	if err == nil || !strings.Contains(err.Error(), "trailing JSON") {
		t.Fatalf("expected trailing JSON error, got %v", err)
	}
}

func TestDecodeRejectsICCConfiguration(t *testing.T) {
	_, err := Decode(strings.NewReader(`{"name":"x","icc":{}}`))
	if err == nil || !strings.Contains(err.Error(), "ICC configuration/field is no longer supported") {
		t.Fatalf("expected explicit ICC migration error, got %v", err)
	}
}

func TestDecodeRejectsLegacyICCEventKinds(t *testing.T) {
	scenario := loadFixture(t, "eurusd_continuation_win.json")
	events := cloneScenarioEvents(scenario.Events)
	events[0].Kind = "CONTEXT"
	scenario.Events = events
	err := scenario.Validate()
	if err == nil || !strings.Contains(err.Error(), "ICC configuration/field is no longer supported") {
		t.Fatalf("expected explicit ICC event-kind migration error, got %v", err)
	}
}

func TestVerifiedReplayRejectsDatasetChangedAfterManifestFreeze(t *testing.T) {
	scenario := loadFixture(t, "eurusd_continuation_win.json")
	frozen, err := ManifestFor(scenario)
	if err != nil {
		t.Fatal(err)
	}
	scenario.Events[len(scenario.Events)-1].Candle.Close--
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
	base := loadFixture(t, "eurusd_continuation_win.json")
	tests := []struct {
		name   string
		mutate func(*Scenario)
	}{
		{name: "execution account", mutate: func(s *Scenario) { s.Execution.AccountID = "other" }},
		{name: "candle symbol", mutate: func(s *Scenario) { s.Events[1].Candle.Symbol = "GBPUSD" }},
		{name: "quote symbol", mutate: func(s *Scenario) { s.Events[5].Quote.Symbol = "GBPUSD" }},
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

func assertJournal(t *testing.T, path string) {
	t.Helper()
	events, err := journal.ReadAll(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 || events[0].Kind != "EXPERIMENT_MANIFEST" || events[len(events)-1].Kind != "REPORT" {
		t.Fatalf("unexpected verified audit trail: %+v", events)
	}
}
