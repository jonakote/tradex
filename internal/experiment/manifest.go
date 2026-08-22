package experiment

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

const SchemaVersion = "tradex.experiment/v1"

type Component struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

type Manifest struct {
	SchemaVersion  string    `json:"schema_version"`
	ExperimentID   string    `json:"experiment_id"`
	Dataset        Component `json:"dataset"`
	Engine         Component `json:"engine"`
	Strategy       Component `json:"strategy"`
	Risk           Component `json:"risk"`
	CostModel      Component `json:"cost_model"`
	Instrument     Component `json:"instrument"`
	ScenarioSHA256 string    `json:"scenario_sha256"`
	ManifestSHA256 string    `json:"manifest_sha256"`
}

type Input struct {
	ExperimentID      string
	DatasetID         string
	Dataset           any
	EngineID          string
	EngineVersion     string
	Engine            any
	StrategyID        string
	StrategyVersion   string
	Strategy          any
	RiskVersion       string
	Risk              any
	CostModelID       string
	CostVersion       string
	CostModel         any
	InstrumentID      string
	InstrumentVersion string
	Instrument        any
	Scenario          any
}

func Load(path string) (Manifest, error) {
	file, err := os.Open(path)
	if err != nil {
		return Manifest{}, err
	}
	defer file.Close()
	return Decode(file)
}

func Decode(reader io.Reader) (Manifest, error) {
	var manifest Manifest
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode experiment manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Manifest{}, errors.New("experiment manifest contains trailing JSON")
	}
	if err := manifest.Verify(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func WriteNew(path string, manifest Manifest) (writeErr error) {
	if path == "" {
		return errors.New("manifest path is required")
	}
	if err := manifest.Verify(); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create experiment manifest: %w", err)
	}
	defer func() {
		if err := file.Close(); writeErr == nil && err != nil {
			writeErr = err
		}
	}()
	written, err := file.Write(encoded)
	if err != nil {
		return fmt.Errorf("write experiment manifest: %w", err)
	}
	if written != len(encoded) {
		return errors.New("write experiment manifest: short write")
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync experiment manifest: %w", err)
	}
	return nil
}

func Build(input Input) (Manifest, error) {
	if input.ExperimentID == "" || input.DatasetID == "" || input.EngineID == "" ||
		input.EngineVersion == "" || input.StrategyID == "" ||
		input.StrategyVersion == "" || input.RiskVersion == "" || input.CostModelID == "" ||
		input.CostVersion == "" || input.InstrumentID == "" || input.InstrumentVersion == "" {
		return Manifest{}, errors.New("experiment and component identities are required")
	}
	datasetHash, err := Digest(input.Dataset)
	if err != nil {
		return Manifest{}, fmt.Errorf("digest dataset: %w", err)
	}
	engineHash, err := Digest(input.Engine)
	if err != nil {
		return Manifest{}, fmt.Errorf("digest engine: %w", err)
	}
	strategyHash, err := Digest(input.Strategy)
	if err != nil {
		return Manifest{}, fmt.Errorf("digest strategy: %w", err)
	}
	riskHash, err := Digest(input.Risk)
	if err != nil {
		return Manifest{}, fmt.Errorf("digest risk: %w", err)
	}
	costHash, err := Digest(input.CostModel)
	if err != nil {
		return Manifest{}, fmt.Errorf("digest cost model: %w", err)
	}
	instrumentHash, err := Digest(input.Instrument)
	if err != nil {
		return Manifest{}, fmt.Errorf("digest instrument: %w", err)
	}
	scenarioHash, err := Digest(input.Scenario)
	if err != nil {
		return Manifest{}, fmt.Errorf("digest scenario: %w", err)
	}
	manifest := Manifest{
		SchemaVersion: SchemaVersion, ExperimentID: input.ExperimentID,
		Dataset:        Component{ID: input.DatasetID, Version: "v1", SHA256: datasetHash},
		Engine:         Component{ID: input.EngineID, Version: input.EngineVersion, SHA256: engineHash},
		Strategy:       Component{ID: input.StrategyID, Version: input.StrategyVersion, SHA256: strategyHash},
		Risk:           Component{ID: "risk", Version: input.RiskVersion, SHA256: riskHash},
		CostModel:      Component{ID: input.CostModelID, Version: input.CostVersion, SHA256: costHash},
		Instrument:     Component{ID: input.InstrumentID, Version: input.InstrumentVersion, SHA256: instrumentHash},
		ScenarioSHA256: scenarioHash,
	}
	manifest.ManifestSHA256, err = manifest.digest()
	if err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func Digest(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func (m Manifest) Verify() error {
	if m.SchemaVersion != SchemaVersion || m.ExperimentID == "" {
		return errors.New("invalid experiment manifest identity")
	}
	components := []Component{m.Dataset, m.Engine, m.Strategy, m.Risk, m.CostModel, m.Instrument}
	for _, component := range components {
		if component.ID == "" || component.Version == "" || !validDigest(component.SHA256) {
			return errors.New("invalid experiment manifest component")
		}
	}
	if !validDigest(m.ScenarioSHA256) || !validDigest(m.ManifestSHA256) {
		return errors.New("invalid experiment manifest digest")
	}
	want, err := m.digest()
	if err != nil {
		return err
	}
	if m.ManifestSHA256 != want {
		return errors.New("experiment manifest hash mismatch")
	}
	return nil
}

func (m Manifest) digest() (string, error) {
	copy := m
	copy.ManifestSHA256 = ""
	return Digest(copy)
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}
