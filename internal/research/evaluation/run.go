package evaluation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"tradex/internal/analytics"
	"tradex/internal/experiment"
	"tradex/internal/research/replay"
)

type Segment struct {
	StartCycle int       `json:"start_cycle"`
	EndCycle   int       `json:"end_cycle_exclusive"`
	StartAt    time.Time `json:"start_at"`
	EndAt      time.Time `json:"end_at"`
}

type Evaluation struct {
	CandidateID       string           `json:"candidate_id"`
	Segment           Segment          `json:"segment"`
	Eligible          bool             `json:"eligible"`
	SelectionScore    int64            `json:"selection_score_minor"`
	Report            analytics.Report `json:"report"`
	ManifestSHA256    string           `json:"manifest_sha256"`
	AuditSHA256       string           `json:"audit_sha256"`
	CompletedTrades   int              `json:"completed_trades"`
	RejectedDecisions int              `json:"rejected_decisions"`
}

type Fold struct {
	Index               int          `json:"index"`
	Training            []Evaluation `json:"training"`
	SelectedCandidateID string       `json:"selected_candidate_id"`
	Test                Evaluation   `json:"test"`
}

type Result struct {
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

func RunVerified(ctx context.Context, study Study, expected experiment.Manifest, auditDirectory string) (Result, error) {
	if err := expected.Verify(); err != nil {
		return Result{}, fmt.Errorf("verify walk-forward manifest: %w", err)
	}
	actual, err := ManifestFor(study)
	if err != nil {
		return Result{}, err
	}
	if actual.ManifestSHA256 != expected.ManifestSHA256 {
		return Result{}, errors.New("walk-forward study does not match the frozen experiment manifest")
	}
	return run(ctx, study, actual, auditDirectory)
}

func run(ctx context.Context, study Study, manifest experiment.Manifest, auditDirectory string) (Result, error) {
	if auditDirectory == "" {
		return Result{}, errors.New("walk-forward audit directory is required")
	}
	if err := os.Mkdir(auditDirectory, 0o700); err != nil {
		return Result{}, fmt.Errorf("create walk-forward audit directory: %w", err)
	}
	cycles, err := splitCycles(study.Candidates[0].Scenario.Events)
	if err != nil {
		return Result{}, err
	}
	datasetSHA, err := datasetHash(study.Candidates[0].Scenario)
	if err != nil {
		return Result{}, err
	}
	definitions := make(map[string]string, len(study.Candidates))
	for _, candidate := range study.Candidates {
		definitions[candidate.ID], err = experiment.Digest(struct {
			Detector       any
			DetectorParams any
			Execution      any
		}{candidate.Scenario.Detector, candidate.Scenario.DetectorParams, candidate.Scenario.Execution})
		if err != nil {
			return Result{}, err
		}
	}
	developmentEnd := len(cycles) - study.Config.HoldoutCycles
	result := Result{
		SchemaVersion: SchemaVersion, StudyName: study.Name, StudyManifestSHA256: manifest.ManifestSHA256,
		DatasetSHA256: datasetSHA, Config: study.Config, CycleCount: len(cycles),
		DevelopmentCycleCount: developmentEnd, HoldoutCycleCount: study.Config.HoldoutCycles,
		CandidateDefinitions: definitions,
	}
	for testStart, foldIndex := study.Config.InitialTrainCycles, 0; testStart+study.Config.TestCycles <= developmentEnd; testStart, foldIndex = testStart+study.Config.StepCycles, foldIndex+1 {
		training, err := evaluateAll(ctx, study, cycles, 0, testStart,
			fmt.Sprintf("fold-%03d-train", foldIndex), auditDirectory)
		if err != nil {
			return Result{}, err
		}
		selected, err := selectCandidate(training)
		if err != nil {
			return Result{}, fmt.Errorf("fold %d: %w", foldIndex, err)
		}
		test, err := evaluateOne(ctx, study, selected, cycles, testStart,
			testStart+study.Config.TestCycles, fmt.Sprintf("fold-%03d-test", foldIndex), auditDirectory)
		if err != nil {
			return Result{}, err
		}
		result.Folds = append(result.Folds, Fold{
			Index: foldIndex, Training: training, SelectedCandidateID: selected, Test: test,
		})
	}
	result.DevelopmentEvaluations, err = evaluateAll(ctx, study, cycles, 0, developmentEnd, "final-development", auditDirectory)
	if err != nil {
		return Result{}, err
	}
	result.FinalSelectedCandidateID, err = selectCandidate(result.DevelopmentEvaluations)
	if err != nil {
		return Result{}, fmt.Errorf("final development selection: %w", err)
	}
	// Holdout execution intentionally occurs only after final selection has been
	// determined from the complete pre-holdout development segment.
	result.Holdout, err = evaluateOne(ctx, study, result.FinalSelectedCandidateID, cycles,
		developmentEnd, len(cycles), "untouched-holdout", auditDirectory)
	if err != nil {
		return Result{}, err
	}
	result.ResultSHA256, err = result.digest()
	if err != nil {
		return Result{}, err
	}
	return result, result.Verify()
}

func evaluateAll(ctx context.Context, study Study, cycles [][]replay.Event, start, end int,
	label, auditDirectory string) ([]Evaluation, error) {
	result := make([]Evaluation, 0, len(study.Candidates))
	candidates := append([]Candidate(nil), study.Candidates...)
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	for _, candidate := range candidates {
		evaluation, err := evaluateCandidate(ctx, study, candidate, cycles, start, end, label, auditDirectory)
		if err != nil {
			return nil, err
		}
		result = append(result, evaluation)
	}
	return result, nil
}

func evaluateOne(ctx context.Context, study Study, candidateID string, cycles [][]replay.Event,
	start, end int, label, auditDirectory string) (Evaluation, error) {
	for _, candidate := range study.Candidates {
		if candidate.ID == candidateID {
			return evaluateCandidate(ctx, study, candidate, cycles, start, end, label, auditDirectory)
		}
	}
	return Evaluation{}, fmt.Errorf("candidate %s not found", candidateID)
}

func evaluateCandidate(ctx context.Context, study Study, candidate Candidate, cycles [][]replay.Event,
	start, end int, label, auditDirectory string) (Evaluation, error) {
	if start < 0 || end > len(cycles) || start >= end {
		return Evaluation{}, errors.New("invalid evaluation cycle segment")
	}
	scenario := candidate.Scenario
	scenario.Name = fmt.Sprintf("%s/%s/%s", study.Name, candidate.ID, label)
	scenario.Events = joinCycles(cycles[start:end])
	auditPath := filepath.Join(auditDirectory, fmt.Sprintf("%s--%s.jsonl", label, candidate.ID))
	replayed, err := replay.Run(ctx, scenario, auditPath)
	if err != nil {
		return Evaluation{}, fmt.Errorf("evaluate %s on %s: %w", candidate.ID, label, err)
	}
	auditHash, err := fileSHA256(auditPath)
	if err != nil {
		return Evaluation{}, err
	}
	rejected := 0
	for _, decision := range replayed.Decisions {
		if !decision.Approved {
			rejected++
		}
	}
	return Evaluation{
		CandidateID: candidate.ID,
		Segment: Segment{StartCycle: start, EndCycle: end, StartAt: scenario.Events[0].At,
			EndAt: scenario.Events[len(scenario.Events)-1].At},
		Eligible:       replayed.Report.Trades >= study.Config.MinTrainingTrades,
		SelectionScore: replayed.Report.NetProfitMinor - replayed.Report.MaxDrawdownMinor,
		Report:         replayed.Report, ManifestSHA256: replayed.Manifest.ManifestSHA256,
		AuditSHA256: auditHash, CompletedTrades: replayed.Report.Trades, RejectedDecisions: rejected,
	}, nil
}

func selectCandidate(evaluations []Evaluation) (string, error) {
	eligible := make([]Evaluation, 0, len(evaluations))
	for _, evaluation := range evaluations {
		if evaluation.Eligible {
			eligible = append(eligible, evaluation)
		}
	}
	if len(eligible) == 0 {
		return "", errors.New("no candidate meets the minimum completed-trade requirement")
	}
	sort.Slice(eligible, func(i, j int) bool {
		if eligible[i].SelectionScore == eligible[j].SelectionScore {
			return eligible[i].CandidateID < eligible[j].CandidateID
		}
		return eligible[i].SelectionScore > eligible[j].SelectionScore
	})
	return eligible[0].CandidateID, nil
}

func joinCycles(cycles [][]replay.Event) []replay.Event {
	count := 0
	for _, cycle := range cycles {
		count += len(cycle)
	}
	result := make([]replay.Event, 0, count)
	for _, cycle := range cycles {
		result = append(result, cycle...)
	}
	return result
}

func fileSHA256(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:]), nil
}

func (r Result) Verify() error {
	if r.SchemaVersion != SchemaVersion || r.StudyName == "" || r.Config.SelectionMetric != SelectionScore ||
		!validHash(r.StudyManifestSHA256) || !validHash(r.DatasetSHA256) || !validHash(r.ResultSHA256) ||
		r.CycleCount != r.DevelopmentCycleCount+r.HoldoutCycleCount || len(r.Folds) == 0 ||
		r.FinalSelectedCandidateID == "" || r.Holdout.CandidateID != r.FinalSelectedCandidateID ||
		len(r.CandidateDefinitions) < 2 || r.DevelopmentCycleCount < r.Config.InitialTrainCycles+r.Config.TestCycles ||
		r.HoldoutCycleCount != r.Config.HoldoutCycles || r.Config.StepCycles < r.Config.TestCycles ||
		r.Config.MinTrainingTrades <= 0 {
		return errors.New("invalid walk-forward result identity or split")
	}
	for candidateID, hash := range r.CandidateDefinitions {
		if !candidateIDPattern.MatchString(candidateID) || !validHash(hash) {
			return errors.New("invalid walk-forward candidate definition")
		}
	}
	expectedFoldCount := 0
	for testStart := r.Config.InitialTrainCycles; testStart+r.Config.TestCycles <= r.DevelopmentCycleCount; testStart += r.Config.StepCycles {
		expectedFoldCount++
	}
	if len(r.Folds) != expectedFoldCount {
		return errors.New("walk-forward fold count does not match its configuration")
	}
	for index, fold := range r.Folds {
		trainEnd := r.Config.InitialTrainCycles + index*r.Config.StepCycles
		if fold.Index != index || fold.SelectedCandidateID == "" ||
			!sameCandidateSet(fold.Training, r.CandidateDefinitions) {
			return errors.New("invalid walk-forward training fold")
		}
		for _, evaluation := range fold.Training {
			if err := r.verifyEvaluation(evaluation, 0, trainEnd); err != nil {
				return err
			}
		}
		selected, err := selectCandidate(fold.Training)
		if err != nil || selected != fold.SelectedCandidateID {
			return errors.New("walk-forward fold selection cannot be reproduced")
		}
		if fold.Test.CandidateID != selected {
			return errors.New("walk-forward test uses a candidate not selected by training")
		}
		if err := r.verifyEvaluation(fold.Test, trainEnd, trainEnd+r.Config.TestCycles); err != nil {
			return err
		}
	}
	if !sameCandidateSet(r.DevelopmentEvaluations, r.CandidateDefinitions) {
		return errors.New("final development evaluation has incomplete candidates")
	}
	for _, evaluation := range r.DevelopmentEvaluations {
		if err := r.verifyEvaluation(evaluation, 0, r.DevelopmentCycleCount); err != nil {
			return err
		}
	}
	selected, err := selectCandidate(r.DevelopmentEvaluations)
	if err != nil || selected != r.FinalSelectedCandidateID {
		return errors.New("final development selection cannot be reproduced")
	}
	if err := r.verifyEvaluation(r.Holdout, r.DevelopmentCycleCount, r.CycleCount); err != nil {
		return err
	}
	want, err := r.digest()
	if err != nil {
		return err
	}
	if want != r.ResultSHA256 {
		return errors.New("walk-forward result hash mismatch")
	}
	return nil
}

func (r Result) verifyEvaluation(evaluation Evaluation, start, end int) error {
	_, known := r.CandidateDefinitions[evaluation.CandidateID]
	if !known || evaluation.Segment.StartCycle != start || evaluation.Segment.EndCycle != end ||
		evaluation.Segment.StartAt.IsZero() || evaluation.Segment.EndAt.IsZero() ||
		evaluation.Segment.EndAt.Before(evaluation.Segment.StartAt) ||
		!validHash(evaluation.ManifestSHA256) || !validHash(evaluation.AuditSHA256) ||
		evaluation.CompletedTrades != evaluation.Report.Trades || evaluation.RejectedDecisions < 0 ||
		evaluation.SelectionScore != evaluation.Report.NetProfitMinor-evaluation.Report.MaxDrawdownMinor ||
		evaluation.Eligible != (evaluation.CompletedTrades >= r.Config.MinTrainingTrades) {
		return errors.New("invalid walk-forward candidate evaluation")
	}
	return nil
}

func sameCandidateSet(evaluations []Evaluation, definitions map[string]string) bool {
	if len(evaluations) != len(definitions) {
		return false
	}
	seen := make(map[string]bool, len(evaluations))
	for _, evaluation := range evaluations {
		if _, exists := definitions[evaluation.CandidateID]; !exists || seen[evaluation.CandidateID] {
			return false
		}
		seen[evaluation.CandidateID] = true
	}
	return true
}

func (r Result) digest() (string, error) {
	copy := r
	copy.ResultSHA256 = ""
	return experiment.Digest(copy)
}

func WriteResultNew(path string, result Result) (writeErr error) {
	if path == "" {
		return errors.New("walk-forward result path is required")
	}
	if err := result.Verify(); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create walk-forward result: %w", err)
	}
	defer func() {
		if closeErr := file.Close(); writeErr == nil && closeErr != nil {
			writeErr = closeErr
		}
		if writeErr != nil {
			_ = os.Remove(path)
		}
	}()
	written, err := file.Write(encoded)
	if err != nil {
		return err
	}
	if written != len(encoded) {
		return errors.New("write walk-forward result: short write")
	}
	return file.Sync()
}

func validHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
