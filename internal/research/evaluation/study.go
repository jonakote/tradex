package evaluation

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"

	"tradex/internal/experiment"
	"tradex/internal/research/replay"
)

const (
	SchemaVersion  = "tradex.walk-forward/v1"
	SelectionScore = "NET_PROFIT_MINUS_MAX_DRAWDOWN"
)

var candidateIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type Candidate struct {
	ID       string          `json:"id"`
	Scenario replay.Scenario `json:"scenario"`
}

type Config struct {
	InitialTrainCycles int    `json:"initial_train_cycles"`
	TestCycles         int    `json:"test_cycles"`
	StepCycles         int    `json:"step_cycles"`
	HoldoutCycles      int    `json:"holdout_cycles"`
	MinTrainingTrades  int    `json:"min_training_trades"`
	SelectionMetric    string `json:"selection_metric"`
}

type Study struct {
	Name       string      `json:"name"`
	Config     Config      `json:"config"`
	Candidates []Candidate `json:"candidates"`
}

func Load(path string) (Study, error) {
	file, err := os.Open(path)
	if err != nil {
		return Study{}, err
	}
	defer file.Close()
	return Decode(file)
}

func Decode(reader io.Reader) (Study, error) {
	var study Study
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&study); err != nil {
		return Study{}, fmt.Errorf("decode walk-forward study: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Study{}, errors.New("walk-forward study contains trailing JSON")
	}
	if err := study.Validate(); err != nil {
		return Study{}, err
	}
	return study, nil
}

func (s Study) Validate() error {
	if s.Name == "" || len(s.Candidates) < 2 {
		return errors.New("walk-forward study name and at least two candidates are required")
	}
	config := s.Config
	if config.InitialTrainCycles <= 0 || config.TestCycles <= 0 || config.StepCycles < config.TestCycles ||
		config.HoldoutCycles <= 0 || config.MinTrainingTrades <= 0 || config.SelectionMetric != SelectionScore {
		return errors.New("invalid walk-forward split or selection configuration")
	}
	ids := make(map[string]bool, len(s.Candidates))
	var dataHash string
	var cycleCount int
	first := s.Candidates[0].Scenario
	for index, candidate := range s.Candidates {
		if !candidateIDPattern.MatchString(candidate.ID) || ids[candidate.ID] {
			return fmt.Errorf("invalid or duplicate candidate ID %q", candidate.ID)
		}
		ids[candidate.ID] = true
		if err := candidate.Scenario.Validate(); err != nil {
			return fmt.Errorf("validate candidate %s: %w", candidate.ID, err)
		}
		if index > 0 && (candidate.Scenario.InitialEquityMinor != first.InitialEquityMinor ||
			candidate.Scenario.Instrument != first.Instrument || candidate.Scenario.PriceValue != first.PriceValue ||
			candidate.Scenario.Paper != first.Paper || candidate.Scenario.Risk != first.Risk) {
			return fmt.Errorf("candidate %s changes account, instrument, risk, or cost assumptions", candidate.ID)
		}
		hash, err := datasetHash(candidate.Scenario)
		if err != nil {
			return err
		}
		cycles, err := splitCycles(candidate.Scenario.Events)
		if err != nil {
			return fmt.Errorf("candidate %s: %w", candidate.ID, err)
		}
		if index == 0 {
			dataHash, cycleCount = hash, len(cycles)
		} else if hash != dataHash || len(cycles) != cycleCount {
			return fmt.Errorf("candidate %s does not use the identical event dataset", candidate.ID)
		}
	}
	developmentCycles := cycleCount - config.HoldoutCycles
	if developmentCycles < config.InitialTrainCycles+config.TestCycles {
		return errors.New("insufficient pre-holdout cycles for one walk-forward fold")
	}
	return nil
}

func ManifestFor(study Study) (experiment.Manifest, error) {
	if err := study.Validate(); err != nil {
		return experiment.Manifest{}, err
	}
	first := study.Candidates[0].Scenario
	type strategyDefinition struct {
		ID             string
		Detector       any
		DetectorParams any
		Execution      any
	}
	strategies := make([]strategyDefinition, 0, len(study.Candidates))
	for _, candidate := range study.Candidates {
		strategies = append(strategies, strategyDefinition{
			ID: candidate.ID, Detector: candidate.Scenario.Detector, DetectorParams: candidate.Scenario.DetectorParams,
			Execution: candidate.Scenario.Execution,
		})
	}
	dataset := struct {
		Events          []replay.Event
		RawDataset      any
		ResampleReports any
	}{first.Events, first.RawDataset, first.ResampleReports}
	return experiment.Build(experiment.Input{
		ExperimentID: study.Name,
		DatasetID:    study.Name + "/walk-forward-events", Dataset: dataset,
		EngineID: "tradex-walk-forward", EngineVersion: "v3",
		Engine: struct {
			Schema string
			Config Config
		}{SchemaVersion, study.Config},
		StrategyID: "candidate-set", StrategyVersion: "v1", Strategy: strategies,
		RiskVersion: "v1", Risk: first.Risk,
		CostModelID: "paper", CostVersion: "v2", CostModel: struct {
			Paper      any
			PriceValue any
		}{first.Paper, first.PriceValue},
		InstrumentID: first.Instrument.Symbol, InstrumentVersion: "v1", Instrument: first.Instrument,
		Scenario: study,
	})
}

func datasetHash(scenario replay.Scenario) (string, error) {
	return experiment.Digest(struct {
		Events          []replay.Event
		RawDataset      any
		ResampleReports any
	}{scenario.Events, scenario.RawDataset, scenario.ResampleReports})
}

// splitCycles partitions the event stream into per-bar units: one
// EventCandle (closed bar) plus whatever quote/market-status events
// immediately precede it. A scan.Detector's lifecycle has no ICC-style
// context/confirmation cycle boundary to split on, so the walk-forward
// fold unit is the bar itself -- the atomic unit scan.Stream advances on.
func splitCycles(events []replay.Event) ([][]replay.Event, error) {
	if len(events) == 0 {
		return nil, errors.New("event dataset is empty")
	}
	for index := 1; index < len(events); index++ {
		if events[index].At.Before(events[index-1].At) {
			return nil, fmt.Errorf("event dataset is out of order at index %d", index)
		}
	}
	cycles := make([][]replay.Event, 0)
	start := 0
	for index, event := range events {
		if event.Kind == replay.EventCandle {
			cycles = append(cycles, events[start:index+1])
			start = index + 1
		}
	}
	if start != len(events) {
		return nil, errors.New("event dataset has trailing events after its last candle; every walk-forward cycle must close on a candle boundary")
	}
	if len(cycles) == 0 {
		return nil, errors.New("event dataset has no candle (bar) cycles")
	}
	return cycles, nil
}
