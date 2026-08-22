package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"tradex/internal/experiment"
	"tradex/internal/research/replay"
)

type SharedCandidate struct {
	ID       string                `json:"id"`
	Scenario replay.SharedScenario `json:"scenario"`
}

type SharedStudy struct {
	Name       string            `json:"name"`
	Config     Config            `json:"config"`
	Candidates []SharedCandidate `json:"candidates"`
}

func LoadSharedStudy(path string) (SharedStudy, error) {
	file, err := os.Open(path)
	if err != nil {
		return SharedStudy{}, err
	}
	defer file.Close()
	return DecodeSharedStudy(file)
}

func DecodeSharedStudy(reader io.Reader) (SharedStudy, error) {
	var study SharedStudy
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&study); err != nil {
		return SharedStudy{}, fmt.Errorf("decode shared walk-forward study: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return SharedStudy{}, errors.New("shared walk-forward study contains trailing JSON")
	}
	if err := study.Validate(); err != nil {
		return SharedStudy{}, err
	}
	return study, nil
}

func (s SharedStudy) Validate() error {
	if s.Name == "" || len(s.Candidates) < 2 {
		return errors.New("shared study name and at least two candidates are required")
	}
	if err := validateConfig(s.Config); err != nil {
		return err
	}
	ids := make(map[string]bool, len(s.Candidates))
	var first replay.SharedScenario
	var firstCycles []replay.SharedScenario
	var dataHash string
	for index, candidate := range s.Candidates {
		if !candidateIDPattern.MatchString(candidate.ID) || ids[candidate.ID] {
			return fmt.Errorf("invalid or duplicate shared candidate ID %q", candidate.ID)
		}
		ids[candidate.ID] = true
		if err := candidate.Scenario.Validate(); err != nil {
			return fmt.Errorf("validate shared candidate %s: %w", candidate.ID, err)
		}
		cycles, err := splitSharedCycles(candidate.Scenario)
		if err != nil {
			return fmt.Errorf("shared candidate %s: %w", candidate.ID, err)
		}
		hash, err := sharedDatasetHash(candidate.Scenario)
		if err != nil {
			return err
		}
		if index == 0 {
			first, firstCycles, dataHash = candidate.Scenario, cycles, hash
			continue
		}
		if !sameSharedAssumptions(first, candidate.Scenario) {
			return fmt.Errorf("shared candidate %s changes account, risk, paper, or instrument assumptions", candidate.ID)
		}
		if hash != dataHash || len(cycles) != len(firstCycles) {
			return fmt.Errorf("shared candidate %s does not use the identical event dataset", candidate.ID)
		}
	}
	development := len(firstCycles) - s.Config.HoldoutCycles
	if development < s.Config.InitialTrainCycles+s.Config.TestCycles {
		return errors.New("insufficient pre-holdout shared cycles for one walk-forward fold")
	}
	return nil
}

func validateConfig(config Config) error {
	if config.InitialTrainCycles <= 0 || config.TestCycles <= 0 || config.StepCycles < config.TestCycles || config.HoldoutCycles <= 0 || config.MinTrainingTrades <= 0 || config.SelectionMetric != SelectionScore {
		return errors.New("invalid walk-forward split or selection configuration")
	}
	return nil
}

func ManifestForShared(study SharedStudy) (experiment.Manifest, error) {
	if err := study.Validate(); err != nil {
		return experiment.Manifest{}, err
	}
	first := study.Candidates[0].Scenario
	strategies := make([]any, 0, len(study.Candidates))
	for _, candidate := range study.Candidates {
		strategies = append(strategies, struct {
			ID   string
			Legs any
		}{candidate.ID, candidate.Scenario.Legs})
	}
	return experiment.Build(experiment.Input{
		ExperimentID: study.Name, DatasetID: study.Name + "/shared-walk-forward-events", Dataset: first,
		EngineID: "tradex-shared-walk-forward", EngineVersion: "v1",
		Engine: struct {
			Schema string
			Config Config
		}{SchemaVersion, study.Config},
		StrategyID: "shared-candidate-set", StrategyVersion: "v1", Strategy: strategies,
		RiskVersion: "v1", Risk: first.Risk,
		CostModelID: "multi-paper", CostVersion: "v2", CostModel: first.Legs,
		InstrumentID: "shared", InstrumentVersion: "v1", Instrument: first.Legs,
		Scenario: study,
	})
}

func sharedDatasetHash(s replay.SharedScenario) (string, error) {
	return experiment.Digest(struct {
		Legs []struct {
			Symbol string
			Events []replay.Event
			Raw    any
		}
	}{Legs: func() []struct {
		Symbol string
		Events []replay.Event
		Raw    any
	} {
		result := make([]struct {
			Symbol string
			Events []replay.Event
			Raw    any
		}, 0, len(s.Legs))
		for _, leg := range s.Legs {
			result = append(result, struct {
				Symbol string
				Events []replay.Event
				Raw    any
			}{leg.Instrument.Symbol, leg.Events, leg.RawDataset})
		}
		return result
	}()})
}

func sameSharedAssumptions(left, right replay.SharedScenario) bool {
	if left.InitialEquityMinor != right.InitialEquityMinor || left.Risk != right.Risk || len(left.Legs) != len(right.Legs) {
		return false
	}
	for index := range left.Legs {
		a, b := left.Legs[index], right.Legs[index]
		if a.Instrument != b.Instrument || a.Paper != b.Paper || a.PriceValue != b.PriceValue || (a.RawDataset == nil) != (b.RawDataset == nil) {
			return false
		}
	}
	return true
}

func splitSharedCycles(s replay.SharedScenario) ([]replay.SharedScenario, error) {
	perLeg := make([][][]replay.Event, len(s.Legs))
	for index, leg := range s.Legs {
		cycles, err := splitCycles(leg.Events)
		if err != nil {
			return nil, fmt.Errorf("leg %s: %w", leg.Instrument.Symbol, err)
		}
		perLeg[index] = cycles
		if index > 0 && len(cycles) != len(perLeg[0]) {
			return nil, errors.New("shared legs have different cycle counts")
		}
	}
	result := make([]replay.SharedScenario, len(perLeg[0]))
	for cycle := range result {
		copyScenario := s
		copyScenario.Name = fmt.Sprintf("%s/cycle-%03d", s.Name, cycle)
		copyScenario.Legs = make([]replay.Scenario, len(s.Legs))
		for legIndex, leg := range s.Legs {
			copyLeg := leg
			copyLeg.Events = append([]replay.Event(nil), perLeg[legIndex][cycle]...)
			copyScenario.Legs[legIndex] = copyLeg
		}
		result[cycle] = copyScenario
	}
	return result, nil
}

func joinSharedCycles(cycles []replay.SharedScenario, start, end int) replay.SharedScenario {
	result := cycles[start]
	result.Legs = append([]replay.Scenario(nil), cycles[start].Legs...)
	result.Name = cycles[start].Name
	for legIndex := range result.Legs {
		result.Legs[legIndex].Events = nil
		for cycle := start; cycle < end; cycle++ {
			result.Legs[legIndex].Events = append(result.Legs[legIndex].Events, cycles[cycle].Legs[legIndex].Events...)
		}
	}
	return result
}

func evaluateSharedCandidate(ctx context.Context, study SharedStudy, candidate SharedCandidate, cycles []replay.SharedScenario, start, end int, label, auditDirectory string) (Evaluation, error) {
	if start < 0 || end > len(cycles) || start >= end {
		return Evaluation{}, errors.New("invalid shared evaluation cycle segment")
	}
	scenario := joinSharedCycles(cycles, start, end)
	scenario.Name = fmt.Sprintf("%s/%s/%s", study.Name, candidate.ID, label)
	auditPath := filepath.Join(auditDirectory, fmt.Sprintf("%s--%s.jsonl", label, candidate.ID))
	replayed, err := replay.RunShared(ctx, scenario, auditPath)
	if err != nil {
		return Evaluation{}, fmt.Errorf("evaluate shared %s on %s: %w", candidate.ID, label, err)
	}
	auditHash, err := fileSHA256(auditPath)
	if err != nil {
		return Evaluation{}, err
	}
	rejected := 0
	for _, decision := range replayed.Decisions {
		if !decision.Decision.Approved {
			rejected++
		}
	}
	return Evaluation{CandidateID: candidate.ID, Segment: Segment{StartCycle: start, EndCycle: end, StartAt: scenario.Legs[0].Events[0].At, EndAt: scenario.Legs[0].Events[len(scenario.Legs[0].Events)-1].At}, Eligible: replayed.Report.Trades >= study.Config.MinTrainingTrades, SelectionScore: replayed.Report.NetProfitMinor - replayed.Report.MaxDrawdownMinor, Report: replayed.Report, ManifestSHA256: replayed.Manifest.ManifestSHA256, AuditSHA256: auditHash, CompletedTrades: replayed.Report.Trades, RejectedDecisions: rejected}, nil
}

type SharedResult struct {
	SchemaVersion            string            `json:"schema_version"`
	StudyName                string            `json:"study_name"`
	StudyManifestSHA256      string            `json:"study_manifest_sha256"`
	DatasetSHA256            string            `json:"dataset_sha256"`
	Config                   Config            `json:"config"`
	CycleCount               int               `json:"cycle_count"`
	DevelopmentCycleCount    int               `json:"development_cycle_count"`
	HoldoutCycleCount        int               `json:"holdout_cycle_count"`
	Folds                    []Fold            `json:"folds"`
	DevelopmentEvaluations   []Evaluation      `json:"development_evaluations"`
	FinalSelectedCandidateID string            `json:"final_selected_candidate_id"`
	Holdout                  Evaluation        `json:"holdout"`
	CandidateDefinitions     map[string]string `json:"candidate_definition_sha256"`
	ResultSHA256             string            `json:"result_sha256"`
}

func RunSharedVerified(ctx context.Context, study SharedStudy, expected experiment.Manifest, auditDirectory string) (SharedResult, error) {
	if err := expected.Verify(); err != nil {
		return SharedResult{}, fmt.Errorf("verify shared walk-forward manifest: %w", err)
	}
	actual, err := ManifestForShared(study)
	if err != nil {
		return SharedResult{}, err
	}
	if actual.ManifestSHA256 != expected.ManifestSHA256 {
		return SharedResult{}, errors.New("shared walk-forward study does not match the frozen experiment manifest")
	}
	if auditDirectory == "" {
		return SharedResult{}, errors.New("shared walk-forward audit directory is required")
	}
	if err := os.Mkdir(auditDirectory, 0o700); err != nil {
		return SharedResult{}, fmt.Errorf("create shared walk-forward audit directory: %w", err)
	}
	cycles, err := splitSharedCycles(study.Candidates[0].Scenario)
	if err != nil {
		return SharedResult{}, err
	}
	datasetSHA, err := sharedDatasetHash(study.Candidates[0].Scenario)
	if err != nil {
		return SharedResult{}, err
	}
	definitions := make(map[string]string, len(study.Candidates))
	for _, candidate := range study.Candidates {
		definitions[candidate.ID], err = experiment.Digest(candidate.Scenario.Legs)
		if err != nil {
			return SharedResult{}, err
		}
	}
	developmentEnd := len(cycles) - study.Config.HoldoutCycles
	result := SharedResult{SchemaVersion: SchemaVersion, StudyName: study.Name, StudyManifestSHA256: expected.ManifestSHA256, DatasetSHA256: datasetSHA, Config: study.Config, CycleCount: len(cycles), DevelopmentCycleCount: developmentEnd, HoldoutCycleCount: study.Config.HoldoutCycles, CandidateDefinitions: definitions}
	for testStart, foldIndex := study.Config.InitialTrainCycles, 0; testStart+study.Config.TestCycles <= developmentEnd; testStart, foldIndex = testStart+study.Config.StepCycles, foldIndex+1 {
		training, err := evaluateSharedAll(ctx, study, cycles, 0, testStart, fmt.Sprintf("fold-%03d-train", foldIndex), auditDirectory)
		if err != nil {
			return SharedResult{}, err
		}
		selected, err := selectCandidate(training)
		if err != nil {
			return SharedResult{}, err
		}
		test, err := evaluateSharedOne(ctx, study, selected, cycles, testStart, testStart+study.Config.TestCycles, fmt.Sprintf("fold-%03d-test", foldIndex), auditDirectory)
		if err != nil {
			return SharedResult{}, err
		}
		result.Folds = append(result.Folds, Fold{Index: foldIndex, Training: training, SelectedCandidateID: selected, Test: test})
	}
	result.DevelopmentEvaluations, err = evaluateSharedAll(ctx, study, cycles, 0, developmentEnd, "final-development", auditDirectory)
	if err != nil {
		return SharedResult{}, err
	}
	result.FinalSelectedCandidateID, err = selectCandidate(result.DevelopmentEvaluations)
	if err != nil {
		return SharedResult{}, err
	}
	result.Holdout, err = evaluateSharedOne(ctx, study, result.FinalSelectedCandidateID, cycles, developmentEnd, len(cycles), "untouched-holdout", auditDirectory)
	if err != nil {
		return SharedResult{}, err
	}
	result.ResultSHA256, err = result.digest()
	if err != nil {
		return SharedResult{}, err
	}
	return result, result.Verify()
}

func evaluateSharedAll(ctx context.Context, study SharedStudy, cycles []replay.SharedScenario, start, end int, label, directory string) ([]Evaluation, error) {
	candidates := append([]SharedCandidate(nil), study.Candidates...)
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	result := make([]Evaluation, 0, len(candidates))
	for _, candidate := range candidates {
		value, err := evaluateSharedCandidate(ctx, study, candidate, cycles, start, end, label, directory)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}

func evaluateSharedOne(ctx context.Context, study SharedStudy, id string, cycles []replay.SharedScenario, start, end int, label, directory string) (Evaluation, error) {
	for _, candidate := range study.Candidates {
		if candidate.ID == id {
			return evaluateSharedCandidate(ctx, study, candidate, cycles, start, end, label, directory)
		}
	}
	return Evaluation{}, fmt.Errorf("shared candidate %s not found", id)
}

func (r SharedResult) Verify() error {
	if r.SchemaVersion != SchemaVersion || r.StudyName == "" || r.Config.SelectionMetric != SelectionScore || !validHash(r.StudyManifestSHA256) || !validHash(r.DatasetSHA256) || !validHash(r.ResultSHA256) || r.CycleCount != r.DevelopmentCycleCount+r.HoldoutCycleCount || len(r.Folds) == 0 || r.FinalSelectedCandidateID == "" || r.Holdout.CandidateID != r.FinalSelectedCandidateID || len(r.CandidateDefinitions) < 2 {
		return errors.New("invalid shared walk-forward result identity or split")
	}
	for id, hash := range r.CandidateDefinitions {
		if !candidateIDPattern.MatchString(id) || !validHash(hash) {
			return errors.New("invalid shared walk-forward candidate definition")
		}
	}
	if !sameCandidateSet(r.DevelopmentEvaluations, r.CandidateDefinitions) {
		return errors.New("shared development evaluation has incomplete candidates")
	}
	expectedFoldCount := 0
	for testStart := r.Config.InitialTrainCycles; testStart+r.Config.TestCycles <= r.DevelopmentCycleCount; testStart += r.Config.StepCycles {
		expectedFoldCount++
	}
	if len(r.Folds) != expectedFoldCount {
		return errors.New("shared walk-forward fold count does not match its configuration")
	}
	for index, fold := range r.Folds {
		trainEnd := r.Config.InitialTrainCycles + index*r.Config.StepCycles
		if fold.Index != index || fold.SelectedCandidateID == "" || !sameCandidateSet(fold.Training, r.CandidateDefinitions) {
			return errors.New("invalid shared walk-forward training fold")
		}
		for _, evaluation := range fold.Training {
			if err := r.verifyEvaluation(evaluation, 0, trainEnd); err != nil {
				return err
			}
		}
		selected, err := selectCandidate(fold.Training)
		if err != nil || selected != fold.SelectedCandidateID || fold.Test.CandidateID != selected {
			return errors.New("shared walk-forward fold selection cannot be reproduced")
		}
		if err := r.verifyEvaluation(fold.Test, trainEnd, trainEnd+r.Config.TestCycles); err != nil {
			return err
		}
	}
	selected, err := selectCandidate(r.DevelopmentEvaluations)
	if err != nil || selected != r.FinalSelectedCandidateID {
		return errors.New("shared final selection cannot be reproduced")
	}
	if err := r.verifyEvaluation(r.Holdout, r.DevelopmentCycleCount, r.CycleCount); err != nil {
		return err
	}
	want, err := r.digest()
	if err != nil {
		return err
	}
	if want != r.ResultSHA256 {
		return errors.New("shared walk-forward result hash mismatch")
	}
	return nil
}

func (r SharedResult) verifyEvaluation(e Evaluation, start, end int) error {
	return (Result{Config: r.Config, CandidateDefinitions: r.CandidateDefinitions}).verifyEvaluation(e, start, end)
}
func (r SharedResult) digest() (string, error) {
	copy := r
	copy.ResultSHA256 = ""
	return experiment.Digest(copy)
}

func WriteSharedResultNew(path string, result SharedResult) error {
	if path == "" {
		return errors.New("shared walk-forward result path is required")
	}
	if err := result.Verify(); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if _, err := file.Write(encoded); err != nil {
		return err
	}
	return file.Sync()
}
