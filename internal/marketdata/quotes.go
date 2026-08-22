package marketdata

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"tradex/internal/domain"
	"tradex/internal/experiment"
)

const QuoteDatasetSchema = "tradex.quotes/v1"

type QuoteProvenance struct {
	DatasetID         string    `json:"dataset_id"`
	Vendor            string    `json:"vendor"`
	Feed              string    `json:"feed"`
	Broker            string    `json:"broker"`
	RetrievedAt       time.Time `json:"retrieved_at"`
	SourceTimezone    string    `json:"source_timezone"`
	TimestampBasis    string    `json:"timestamp_basis"`
	RawArtifactSHA256 string    `json:"raw_artifact_sha256"`
	LicenseReference  string    `json:"license_reference"`
}

type QuoteStream struct {
	Symbol string         `json:"symbol"`
	Quotes []domain.Quote `json:"quotes"`
}

type QuoteDataset struct {
	SchemaVersion string          `json:"schema_version"`
	Provenance    QuoteProvenance `json:"provenance"`
	AllowGaps     bool            `json:"allow_gaps"`
	MaxGap        time.Duration   `json:"max_gap,omitempty"`
	Streams       []QuoteStream   `json:"streams"`
}

func LoadQuoteDataset(path string) (QuoteDataset, error) {
	file, err := os.Open(path)
	if err != nil {
		return QuoteDataset{}, err
	}
	defer file.Close()
	return DecodeQuoteDataset(file)
}

func DecodeQuoteDataset(reader io.Reader) (QuoteDataset, error) {
	var dataset QuoteDataset
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&dataset); err != nil {
		return QuoteDataset{}, fmt.Errorf("decode quote dataset: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return QuoteDataset{}, errors.New("quote dataset contains trailing JSON")
	}
	if err := dataset.Validate(); err != nil {
		return QuoteDataset{}, err
	}
	return dataset, nil
}

func (d QuoteDataset) Validate() error {
	if d.SchemaVersion != QuoteDatasetSchema || len(d.Streams) == 0 {
		return errors.New("invalid quote dataset schema or empty streams")
	}
	if err := d.Provenance.Validate(); err != nil {
		return err
	}
	if d.MaxGap < 0 {
		return errors.New("quote dataset max gap cannot be negative")
	}
	seen := make(map[string]struct{}, len(d.Streams))
	for index, stream := range d.Streams {
		if stream.Symbol == "" || len(stream.Quotes) == 0 {
			return fmt.Errorf("quote stream %d has invalid identity or no quotes", index)
		}
		if _, exists := seen[stream.Symbol]; exists {
			return fmt.Errorf("duplicate quote stream %s", stream.Symbol)
		}
		seen[stream.Symbol] = struct{}{}
		var previous domain.Quote
		for quoteIndex, quote := range stream.Quotes {
			if err := quote.Validate(); err != nil {
				return fmt.Errorf("validate quote stream %s quote %d: %w", stream.Symbol, quoteIndex, err)
			}
			if quote.Source != d.Provenance.Feed || quote.Symbol != stream.Symbol || !isUTC(quote.At) || quote.At.After(d.Provenance.RetrievedAt) {
				return fmt.Errorf("quote stream %s quote %d contradicts provenance", stream.Symbol, quoteIndex)
			}
			if quoteIndex > 0 {
				if quote.At.Before(previous.At) || quote.Sequence <= previous.Sequence {
					return fmt.Errorf("quote stream %s is out of order at index %d", stream.Symbol, quoteIndex)
				}
				if d.MaxGap > 0 && quote.At.Sub(previous.At) > d.MaxGap && !d.AllowGaps {
					return fmt.Errorf("quote stream %s has an undeclared gap at index %d", stream.Symbol, quoteIndex)
				}
			}
			previous = quote
		}
	}
	return nil
}

func (p QuoteProvenance) Validate() error {
	if p.DatasetID == "" || p.Vendor == "" || p.Feed == "" || p.Broker == "" || p.RetrievedAt.IsZero() || p.SourceTimezone == "" || p.TimestampBasis != "UTC_EVENT_TIME" || p.LicenseReference == "" || !validSHA256(p.RawArtifactSHA256) {
		return errors.New("invalid quote dataset provenance")
	}
	if !isUTC(p.RetrievedAt) {
		return errors.New("quote dataset retrieval time must be UTC")
	}
	if _, err := time.LoadLocation(p.SourceTimezone); err != nil {
		return fmt.Errorf("load quote source timezone: %w", err)
	}
	return nil
}

func (d QuoteDataset) Fingerprint() (string, error) {
	if err := d.Validate(); err != nil {
		return "", err
	}
	return experiment.Digest(d)
}
