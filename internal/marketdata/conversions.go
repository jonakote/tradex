package marketdata

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"tradex/internal/economics"
	"tradex/internal/experiment"
)

const ConversionDatasetSchema = "tradex.conversions/v1"

type ConversionProvenance struct {
	DatasetID         string    `json:"dataset_id"`
	Vendor            string    `json:"vendor"`
	Feed              string    `json:"feed"`
	Broker            string    `json:"broker"`
	RetrievedAt       time.Time `json:"retrieved_at"`
	RawArtifactSHA256 string    `json:"raw_artifact_sha256"`
	LicenseReference  string    `json:"license_reference"`
}
type ConversionObservation struct {
	From        string    `json:"from"`
	To          string    `json:"to"`
	Numerator   int64     `json:"numerator"`
	Denominator int64     `json:"denominator"`
	Source      string    `json:"source"`
	ObservedAt  time.Time `json:"observed_at"`
}
type ConversionStream struct {
	From         string                  `json:"from"`
	To           string                  `json:"to"`
	Observations []ConversionObservation `json:"observations"`
}
type ConversionDataset struct {
	SchemaVersion   string               `json:"schema_version"`
	AccountCurrency string               `json:"account_currency"`
	Provenance      ConversionProvenance `json:"provenance"`
	Streams         []ConversionStream   `json:"streams"`
}

func LoadConversionDataset(path string) (ConversionDataset, error) {
	file, err := os.Open(path)
	if err != nil {
		return ConversionDataset{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var dataset ConversionDataset
	if err := decoder.Decode(&dataset); err != nil {
		return ConversionDataset{}, fmt.Errorf("decode conversion dataset: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return ConversionDataset{}, errors.New("conversion dataset contains trailing JSON")
	}
	if err := dataset.Validate(); err != nil {
		return ConversionDataset{}, err
	}
	return dataset, nil
}

func (d ConversionDataset) Validate() error {
	if d.SchemaVersion != ConversionDatasetSchema || d.AccountCurrency == "" || len(d.Streams) == 0 {
		return errors.New("invalid conversion dataset schema or empty streams")
	}
	if d.Provenance.DatasetID == "" || d.Provenance.Vendor == "" || d.Provenance.Feed == "" || d.Provenance.Broker == "" || d.Provenance.RetrievedAt.IsZero() || !isUTC(d.Provenance.RetrievedAt) || !validSHA256(d.Provenance.RawArtifactSHA256) || d.Provenance.LicenseReference == "" {
		return errors.New("invalid conversion dataset provenance")
	}
	for _, stream := range d.Streams {
		if stream.From == "" || stream.To == "" || stream.From == stream.To || len(stream.Observations) == 0 {
			return errors.New("invalid conversion stream")
		}
		var previous time.Time
		for _, observation := range stream.Observations {
			if observation.From != stream.From || observation.To != stream.To || observation.Numerator <= 0 || observation.Denominator <= 0 || observation.Source == "" || observation.ObservedAt.IsZero() || !isUTC(observation.ObservedAt) || (!previous.IsZero() && observation.ObservedAt.Before(previous)) {
				return errors.New("invalid or out-of-order conversion observation")
			}
			previous = observation.ObservedAt
		}
	}
	return nil
}

func (d ConversionDataset) Fingerprint() (string, error) {
	if err := d.Validate(); err != nil {
		return "", err
	}
	return experiment.Digest(d)
}

func (d ConversionDataset) RateAt(from, to string, at time.Time, maxAge time.Duration) (economics.Rate, error) {
	if err := d.Validate(); err != nil {
		return economics.Rate{}, err
	}
	if maxAge <= 0 || at.IsZero() {
		return economics.Rate{}, errors.New("conversion lookup requires time and maximum age")
	}
	for _, stream := range d.Streams {
		if stream.From != from || stream.To != to {
			continue
		}
		index := sort.Search(len(stream.Observations), func(i int) bool { return stream.Observations[i].ObservedAt.After(at) })
		if index == 0 {
			return economics.Rate{}, errors.New("no conversion observation at requested time")
		}
		observation := stream.Observations[index-1]
		if at.Sub(observation.ObservedAt) > maxAge {
			return economics.Rate{}, errors.New("conversion observation is stale")
		}
		rate := economics.Rate{From: observation.From, To: observation.To, Numerator: observation.Numerator, Denominator: observation.Denominator, Source: observation.Source, ObservedAt: observation.ObservedAt}
		return rate, rate.Validate()
	}
	return economics.Rate{}, errors.New("conversion stream not found")
}
