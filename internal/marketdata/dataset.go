package marketdata

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"tradex/internal/domain"
	"tradex/internal/experiment"
)

const CandleDatasetSchema = "tradex.candles/v1"

type PriceBasis string

const (
	PriceBid PriceBasis = "BID"
	PriceAsk PriceBasis = "ASK"
	PriceMid PriceBasis = "MID"
)

type Provenance struct {
	DatasetID         string     `json:"dataset_id"`
	Vendor            string     `json:"vendor"`
	Feed              string     `json:"feed"`
	Broker            string     `json:"broker"`
	RetrievedAt       time.Time  `json:"retrieved_at"`
	SourceTimezone    string     `json:"source_timezone"`
	TimestampBasis    string     `json:"timestamp_basis"`
	PriceBasis        PriceBasis `json:"price_basis"`
	RawArtifactSHA256 string     `json:"raw_artifact_sha256"`
	LicenseReference  string     `json:"license_reference"`
}

type CandleStream struct {
	Symbol    string          `json:"symbol"`
	Timeframe time.Duration   `json:"timeframe"`
	Candles   []domain.Candle `json:"candles"`
}

type Transformation struct {
	Kind            string        `json:"kind"`
	Version         string        `json:"version"`
	SourceTimeframe time.Duration `json:"source_timeframe"`
	TargetTimeframe time.Duration `json:"target_timeframe"`
}

type CandleDataset struct {
	SchemaVersion     string           `json:"schema_version"`
	Provenance        Provenance       `json:"provenance"`
	AllowGaps         bool             `json:"allow_gaps"`
	ParentFingerprint string           `json:"parent_fingerprint,omitempty"`
	Transformations   []Transformation `json:"transformations,omitempty"`
	Streams           []CandleStream   `json:"streams"`
}

func LoadCandleDataset(path string) (CandleDataset, error) {
	file, err := os.Open(path)
	if err != nil {
		return CandleDataset{}, err
	}
	defer file.Close()
	return DecodeCandleDataset(file)
}

func DecodeCandleDataset(reader io.Reader) (CandleDataset, error) {
	var dataset CandleDataset
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&dataset); err != nil {
		return CandleDataset{}, fmt.Errorf("decode candle dataset: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return CandleDataset{}, errors.New("candle dataset contains trailing JSON")
	}
	if err := dataset.Validate(); err != nil {
		return CandleDataset{}, err
	}
	return dataset, nil
}

func (d CandleDataset) Validate() error {
	if d.SchemaVersion != CandleDatasetSchema || len(d.Streams) == 0 {
		return errors.New("invalid candle dataset schema or empty streams")
	}
	if err := d.Provenance.Validate(); err != nil {
		return err
	}
	if len(d.Transformations) > 0 && !validSHA256(d.ParentFingerprint) {
		return errors.New("transformed candle dataset requires a valid parent fingerprint")
	}
	for index, transformation := range d.Transformations {
		if transformation.Kind != "OHLC_RESAMPLE" || transformation.Version == "" ||
			transformation.SourceTimeframe <= 0 || transformation.TargetTimeframe <= transformation.SourceTimeframe ||
			transformation.TargetTimeframe%transformation.SourceTimeframe != 0 {
			return fmt.Errorf("invalid candle dataset transformation %d", index)
		}
	}
	seen := make(map[string]struct{}, len(d.Streams))
	for index, stream := range d.Streams {
		if stream.Symbol == "" || stream.Timeframe <= 0 || len(stream.Candles) == 0 {
			return fmt.Errorf("candle stream %d has invalid identity or no candles", index)
		}
		key := fmt.Sprintf("%s/%d", stream.Symbol, stream.Timeframe)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate candle stream %s", key)
		}
		seen[key] = struct{}{}
		report, err := ValidateCandles(stream.Candles)
		if err != nil {
			return fmt.Errorf("validate candle stream %s: %w", key, err)
		}
		if len(report.Gaps) > 0 && !d.AllowGaps {
			return fmt.Errorf("candle stream %s contains %d undeclared gaps", key, len(report.Gaps))
		}
		for candleIndex, candle := range stream.Candles {
			if candle.Source != d.Provenance.Feed || candle.Symbol != stream.Symbol || candle.Timeframe != stream.Timeframe {
				return fmt.Errorf("candle stream %s candle %d contradicts stream provenance", key, candleIndex)
			}
			if !isUTC(candle.OpenTime) || !isUTC(candle.CloseTime) {
				return fmt.Errorf("candle stream %s candle %d is not UTC", key, candleIndex)
			}
			if candle.CloseTime.After(d.Provenance.RetrievedAt) {
				return fmt.Errorf("candle stream %s candle %d closes after retrieval", key, candleIndex)
			}
		}
	}
	return nil
}

func (d CandleDataset) Fingerprint() (string, error) {
	if err := d.Validate(); err != nil {
		return "", err
	}
	return experiment.Digest(d)
}

func (p Provenance) Validate() error {
	if p.DatasetID == "" || p.Vendor == "" || p.Feed == "" || p.Broker == "" ||
		p.RetrievedAt.IsZero() || p.SourceTimezone == "" || p.TimestampBasis != "UTC_OPEN_TIME" ||
		p.LicenseReference == "" || !validSHA256(p.RawArtifactSHA256) {
		return errors.New("invalid candle dataset provenance")
	}
	if p.PriceBasis != PriceBid && p.PriceBasis != PriceAsk && p.PriceBasis != PriceMid {
		return errors.New("invalid candle dataset price basis")
	}
	if _, err := time.LoadLocation(p.SourceTimezone); err != nil {
		return fmt.Errorf("load source timezone: %w", err)
	}
	return nil
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func isUTC(value time.Time) bool {
	_, offset := value.Zone()
	return offset == 0
}
