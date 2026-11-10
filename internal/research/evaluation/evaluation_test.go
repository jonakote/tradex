package evaluation

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"tradex/internal/research/replay"
	"tradex/internal/strategy/scan"
)

func TestWalkForwardSelectsOnDevelopmentThenEvaluatesHoldoutOnce(t *testing.T) {
	study := testStudy(t)
	manifest, err := ManifestFor(study)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	result, err := RunVerified(context.Background(), study, manifest, filepath.Join(directory, "audits-one"))
	if err != nil {
		t.Fatal(err)
	}
	if result.CycleCount != 11 || result.DevelopmentCycleCount != 9 || result.HoldoutCycleCount != 2 {
		t.Fatalf("unexpected split geometry: %+v", result)
	}
	for index, fold := range result.Folds {
		if fold.SelectedCandidateID != "detects" {
			t.Fatalf("fold %d did not select the detector that actually fires: %+v", index, fold)
		}
		if len(fold.Training) != 2 || fold.Training[0].CandidateID != "blind" || fold.Training[0].Eligible ||
			fold.Training[1].CandidateID != "detects" || !fold.Training[1].Eligible {
			t.Fatalf("fold %d candidate eligibility is wrong: %+v", index, fold.Training)
		}
	}
	if result.FinalSelectedCandidateID != "detects" || result.Holdout.CandidateID != "detects" {
		t.Fatalf("holdout was not isolated behind final selection: %+v", result.Holdout)
	}
	if err := result.Verify(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(directory, "audits-one"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatalf("expected one immutable audit per evaluation, got %d", len(entries))
	}

	second, err := RunVerified(context.Background(), study, manifest, filepath.Join(directory, "audits-two"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result, second) {
		t.Fatal("identical frozen study did not produce an identical walk-forward result")
	}
	resultPath := filepath.Join(directory, "walk-forward.json")
	if err := WriteResultNew(resultPath, result); err != nil {
		t.Fatal(err)
	}
	if err := WriteResultNew(resultPath, result); err == nil {
		t.Fatal("walk-forward result overwrote an existing artifact")
	}
	tampered := result
	tampered.Holdout.Report.NetProfitMinor++
	if err := tampered.Verify(); err == nil {
		t.Fatal("tampered walk-forward result passed verification")
	}
}

func TestWalkForwardRejectsStudyChangedAfterManifestFreeze(t *testing.T) {
	study := testStudy(t)
	manifest, err := ManifestFor(study)
	if err != nil {
		t.Fatal(err)
	}
	for index := range study.Candidates {
		events := cloneEvents(study.Candidates[index].Scenario.Events)
		last := len(events) - 1
		events[last].Candle.Close--
		study.Candidates[index].Scenario.Events = events
	}
	auditDirectory := filepath.Join(t.TempDir(), "rejected-audits")
	_, err = RunVerified(context.Background(), study, manifest, auditDirectory)
	if err == nil || !strings.Contains(err.Error(), "frozen experiment manifest") {
		t.Fatalf("expected frozen-study rejection, got %v", err)
	}
	if _, statErr := os.Stat(auditDirectory); !os.IsNotExist(statErr) {
		t.Fatalf("rejected study created an audit directory: %v", statErr)
	}
}

func TestStudyDecodeIsStrict(t *testing.T) {
	study := testStudy(t)
	payload, err := json.Marshal(study)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(strings.NewReader(string(payload) + ` {}`)); err == nil {
		t.Fatal("study decoder accepted trailing JSON")
	}
}

// TestCycleSplitCarriesLeadingMarketStatusIntoFirstCycle proves a cycle (one
// EventCandle plus anything preceding it) retains a leading market-status
// event -- the generic-detector analogue of the old ICC-context-boundary
// coverage, now split on the candle boundary scan.Stream itself advances on.
func TestCycleSplitCarriesLeadingMarketStatusIntoFirstCycle(t *testing.T) {
	study := testStudy(t)
	events := cloneEvents(study.Candidates[0].Scenario.Events[:1])
	closed := false
	status := replay.Event{At: events[0].At.Add(-time.Minute), Kind: replay.EventMarketStatus, MarketOpen: &closed}
	cycles, err := splitCycles(append([]replay.Event{status}, events...))
	if err != nil {
		t.Fatal(err)
	}
	if len(cycles) != 1 || cycles[0][0].Kind != replay.EventMarketStatus {
		t.Fatalf("leading market state was not retained: %+v", cycles)
	}
}

func TestSharedWalkForwardSelectsAcrossOneAccountAndFreezesHoldout(t *testing.T) {
	base := loadFixture(t)
	gbp := base
	gbp.Name = "gbpusd-leg"
	gbp.Instrument.Symbol = "GBPUSD"
	gbp.Instrument.BaseCurrency = "GBP"
	gbp.Events = cloneEvents(base.Events)
	for index := range gbp.Events {
		if gbp.Events[index].Candle != nil {
			candle := *gbp.Events[index].Candle
			candle.Symbol = "GBPUSD"
			gbp.Events[index].Candle = &candle
		}
		if gbp.Events[index].Quote != nil {
			quote := *gbp.Events[index].Quote
			quote.Symbol = "GBPUSD"
			gbp.Events[index].Quote = &quote
		}
	}
	first := replay.SharedScenario{Name: "shared-first", InitialEquityMinor: base.InitialEquityMinor, Risk: base.Risk, Legs: []replay.Scenario{base, gbp}}
	second := first
	second.Name = "shared-second"
	blindBase, blindGBP := base, gbp
	blindBase.Detector = blindDetectorConfig()
	blindGBP.Detector = blindDetectorConfig()
	second.Legs = []replay.Scenario{blindBase, blindGBP}
	study := SharedStudy{Name: "shared-detector-study", Config: Config{InitialTrainCycles: 8, TestCycles: 1, StepCycles: 1, HoldoutCycles: 2, MinTrainingTrades: 1, SelectionMetric: SelectionScore}, Candidates: []SharedCandidate{{ID: "detects", Scenario: first}, {ID: "blind", Scenario: second}}}
	manifest, err := ManifestForShared(study)
	if err != nil {
		t.Fatal(err)
	}
	result, err := RunSharedVerified(context.Background(), study, manifest, filepath.Join(t.TempDir(), "audits"))
	if err != nil {
		t.Fatal(err)
	}
	if result.CycleCount != 11 || result.DevelopmentCycleCount != 9 || result.Holdout.CandidateID != result.FinalSelectedCandidateID {
		t.Fatalf("unexpected shared result: %+v", result)
	}
	if err := result.Verify(); err != nil {
		t.Fatal(err)
	}
}

// blindDetectorConfig sets an unreachable reward:risk floor so the
// continuation detector never arms on the fixture's price path -- the
// generic-detector way to build an "ineligible candidate" fixture, since
// execution no longer owns any geometry (ICC's RewardRiskBPS) to detune.
func blindDetectorConfig() scan.Config {
	var config scan.Config
	raw := `{"name":"continuation","continuation":{
		"impulse_bars":3,"prior_bars":3,"min_directional_bps":1,"min_displacement_bps":1,
		"min_break_ticks":1,"retest_offset_bps":0,"stop_buffer_ticks":1,"min_stop_spread_multiple":0,
		"target_bars":3,"min_reward_risk_bps":1000000,"max_entry_distance_bps":100000,"entry_mode":"retest"}}`
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		panic(err)
	}
	return config
}

func testStudy(t *testing.T) Study {
	t.Helper()
	base := loadFixture(t)
	blind := base
	blind.Name = "blind-candidate"
	blind.Detector = blindDetectorConfig()
	detects := base
	detects.Name = "detects-candidate"
	return Study{
		Name: "eurusd-continuation-study",
		Config: Config{
			InitialTrainCycles: 8, TestCycles: 1, StepCycles: 1, HoldoutCycles: 2,
			MinTrainingTrades: 1, SelectionMetric: SelectionScore,
		},
		Candidates: []Candidate{{ID: "detects", Scenario: detects}, {ID: "blind", Scenario: blind}},
	}
}

func loadFixture(t *testing.T) replay.Scenario {
	t.Helper()
	scenario, err := replay.Load(filepath.Join("..", "replay", "testdata", "eurusd_continuation_win.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Pad with trailing flat bars so a walk-forward holdout/test window can
	// sit entirely after the fixture's one detect->entry->exit trade without
	// truncating it out of every training segment.
	last := *scenario.Events[len(scenario.Events)-1].Candle
	for i := 1; i <= 3; i++ {
		pad := last
		pad.OpenTime = last.OpenTime.Add(time.Duration(i) * time.Hour)
		pad.CloseTime = last.CloseTime.Add(time.Duration(i) * time.Hour)
		pad.Open, pad.High, pad.Low, pad.Close = 108300, 108305, 108295, 108300
		scenario.Events = append(scenario.Events, replay.Event{At: pad.CloseTime, Kind: replay.EventCandle, Candle: &pad})
	}
	return scenario
}

func cloneEvents(source []replay.Event) []replay.Event {
	result := make([]replay.Event, len(source))
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
