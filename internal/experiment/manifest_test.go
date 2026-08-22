package experiment

import (
	"path/filepath"
	"testing"
)

func TestManifestIsDeterministicAndDetectsModification(t *testing.T) {
	input := Input{
		ExperimentID: "experiment-1", DatasetID: "dataset-1", Dataset: []int{1, 2, 3},
		EngineID: "tradex-replay", EngineVersion: "v1", Engine: struct{ Deterministic bool }{Deterministic: true},
		StrategyID: "icc", StrategyVersion: "v1", Strategy: struct{ Bars int }{Bars: 8},
		RiskVersion: "v1", Risk: struct{ BPS int }{BPS: 25},
		CostModelID: "paper", CostVersion: "v1", CostModel: struct{ Slippage int }{Slippage: 1},
		InstrumentID: "EURUSD", InstrumentVersion: "v1", Instrument: struct{ Scale int }{Scale: 100000},
		Scenario: struct{ Name string }{Name: "experiment-1"},
	}
	first, err := Build(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Build(input)
	if err != nil || first != second {
		t.Fatalf("manifest is not deterministic: first=%+v second=%+v err=%v", first, second, err)
	}
	if err := first.Verify(); err != nil {
		t.Fatal(err)
	}
	tampered := first
	tampered.Dataset.SHA256 = first.Strategy.SHA256
	if err := tampered.Verify(); err == nil {
		t.Fatal("modified manifest was accepted")
	}
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := WriteNew(path, first); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil || loaded != first {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	if err := WriteNew(path, first); err == nil {
		t.Fatal("manifest writer overwrote an existing file")
	}
}
