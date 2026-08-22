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
	if len(result.Folds) != 2 || result.CycleCount != 5 || result.DevelopmentCycleCount != 4 ||
		result.HoldoutCycleCount != 1 {
		t.Fatalf("unexpected split geometry: %+v", result)
	}
	for index, fold := range result.Folds {
		if fold.SelectedCandidateID != "two-r" || fold.Test.CandidateID != "two-r" ||
			fold.Test.Report.Trades != 1 || fold.Test.Report.Wins != 1 {
			t.Fatalf("fold %d did not select and test the development winner: %+v", index, fold)
		}
		if len(fold.Training) != 2 || fold.Training[0].CandidateID != "one-r" || fold.Training[0].Eligible ||
			fold.Training[1].CandidateID != "two-r" || !fold.Training[1].Eligible {
			t.Fatalf("fold %d candidate eligibility is wrong: %+v", index, fold.Training)
		}
	}
	if result.FinalSelectedCandidateID != "two-r" || result.Holdout.CandidateID != "two-r" ||
		result.Holdout.Segment.StartCycle != 4 || result.Holdout.Segment.EndCycle != 5 ||
		result.Holdout.Report.Trades != 1 || result.Holdout.Report.Wins != 1 {
		t.Fatalf("holdout was not isolated behind final selection: %+v", result.Holdout)
	}
	if err := result.Verify(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(directory, "audits-one"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 9 {
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
		events[last].Quote.Bid--
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

func TestCycleSplitCarriesLeadingMarketStatusIntoFirstCycle(t *testing.T) {
	study := testStudy(t)
	events := cloneEvents(study.Candidates[0].Scenario.Events[:len(study.Candidates[0].Scenario.Events)/5])
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
	base, err := replay.Load(filepath.Join("..", "replay", "testdata", "eurusd_icc_win.json"))
	if err != nil {
		t.Fatal(err)
	}
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
	// Build each candidate as a shared scenario whose two legs carry identical five-cycle data.
	makeLeg := func(source replay.Scenario) replay.Scenario {
		result := source
		result.Events = nil
		for cycle := 0; cycle < 5; cycle++ {
			result.Events = append(result.Events, shiftEvents(source.Events, time.Duration(cycle)*24*time.Hour, uint64(cycle)*10)...)
		}
		return result
	}
	eurusd, gbpusd := makeLeg(base), makeLeg(gbp)
	first := replay.SharedScenario{Name: "shared-first", InitialEquityMinor: base.InitialEquityMinor, Risk: base.Risk, Legs: []replay.Scenario{eurusd, gbpusd}}
	second := first
	second.Name = "shared-second"
	second.Legs = []replay.Scenario{eurusd, gbpusd}
	second.Legs[0].Execution.RewardRiskBPS = 10000
	study := SharedStudy{Name: "shared-five-cycle-study", Config: Config{InitialTrainCycles: 2, TestCycles: 1, StepCycles: 1, HoldoutCycles: 1, MinTrainingTrades: 1, SelectionMetric: SelectionScore}, Candidates: []SharedCandidate{{ID: "two-r", Scenario: first}, {ID: "one-r", Scenario: second}}}
	manifest, err := ManifestForShared(study)
	if err != nil {
		t.Fatal(err)
	}
	result, err := RunSharedVerified(context.Background(), study, manifest, filepath.Join(t.TempDir(), "audits"))
	if err != nil {
		t.Fatal(err)
	}
	if result.CycleCount != 5 || result.DevelopmentCycleCount != 4 || result.Holdout.CandidateID != result.FinalSelectedCandidateID || len(result.Folds) != 2 {
		t.Fatalf("unexpected shared result: %+v", result)
	}
	if err := result.Verify(); err != nil {
		t.Fatal(err)
	}
}

func testStudy(t *testing.T) Study {
	t.Helper()
	base, err := replay.Load(filepath.Join("..", "replay", "testdata", "eurusd_icc_win.json"))
	if err != nil {
		t.Fatal(err)
	}
	events := make([]replay.Event, 0, len(base.Events)*5)
	for cycle := 0; cycle < 5; cycle++ {
		events = append(events, shiftEvents(base.Events, time.Duration(cycle)*24*time.Hour, uint64(cycle)*10)...)
	}
	oneR := base
	oneR.Name = "one-r-candidate"
	oneR.Events = cloneEvents(events)
	oneR.Execution.RewardRiskBPS = 10000
	twoR := base
	twoR.Name = "two-r-candidate"
	twoR.Events = cloneEvents(events)
	return Study{
		Name: "eurusd-five-cycle-study",
		Config: Config{
			InitialTrainCycles: 2, TestCycles: 1, StepCycles: 1, HoldoutCycles: 1,
			MinTrainingTrades: 1, SelectionMetric: SelectionScore,
		},
		Candidates: []Candidate{{ID: "two-r", Scenario: twoR}, {ID: "one-r", Scenario: oneR}},
	}
}

func shiftEvents(source []replay.Event, shift time.Duration, sequenceOffset uint64) []replay.Event {
	result := cloneEvents(source)
	for index := range result {
		result[index].At = result[index].At.Add(shift)
		if result[index].Candle != nil {
			result[index].Candle.OpenTime = result[index].Candle.OpenTime.Add(shift)
			result[index].Candle.CloseTime = result[index].Candle.CloseTime.Add(shift)
		}
		if result[index].Quote != nil {
			result[index].Quote.At = result[index].Quote.At.Add(shift)
			result[index].Quote.Sequence += sequenceOffset
		}
	}
	return result
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
