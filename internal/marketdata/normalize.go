package marketdata

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"tradex/internal/domain"
)

const RawQuoteDatasetSchema = "tradex.raw-quotes/v1"

type RawQuote struct {
	Source    string       `json:"source"`
	Symbol    string       `json:"symbol"`
	Bid       domain.Price `json:"bid"`
	Ask       domain.Price `json:"ask"`
	Timestamp string       `json:"timestamp"`
	Sequence  uint64       `json:"sequence"`
}

type RawQuoteStream struct {
	Symbol string     `json:"symbol"`
	Quotes []RawQuote `json:"quotes"`
}

type RawQuoteDataset struct {
	SchemaVersion string           `json:"schema_version"`
	Provenance    QuoteProvenance  `json:"provenance"`
	Streams       []RawQuoteStream `json:"streams"`
}

type QuoteNormalization struct {
	TimestampBasis string            `json:"timestamp_basis"` // RFC3339, LOCAL_RFC3339, UNIX_MILLIS, or UNIX_NANOS
	SourceTimezone string            `json:"source_timezone"`
	SymbolAliases  map[string]string `json:"symbol_aliases"`
}

func LoadRawQuoteDataset(path string) (RawQuoteDataset, error) {
	file, err := os.Open(path)
	if err != nil {
		return RawQuoteDataset{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var dataset RawQuoteDataset
	if err := decoder.Decode(&dataset); err != nil {
		return RawQuoteDataset{}, fmt.Errorf("decode raw quote dataset: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return RawQuoteDataset{}, errors.New("raw quote dataset contains trailing JSON")
	}
	if err := dataset.Validate(); err != nil {
		return RawQuoteDataset{}, err
	}
	return dataset, nil
}

func (d RawQuoteDataset) Validate() error {
	if d.SchemaVersion != RawQuoteDatasetSchema || len(d.Streams) == 0 {
		return errors.New("invalid raw quote dataset schema or empty streams")
	}
	if d.Provenance.DatasetID == "" || d.Provenance.Vendor == "" || d.Provenance.Feed == "" || d.Provenance.Broker == "" || d.Provenance.RetrievedAt.IsZero() || !isUTC(d.Provenance.RetrievedAt) || d.Provenance.SourceTimezone == "" || !validSHA256(d.Provenance.RawArtifactSHA256) || d.Provenance.LicenseReference == "" {
		return errors.New("invalid raw quote provenance")
	}
	if _, err := time.LoadLocation(d.Provenance.SourceTimezone); err != nil {
		return fmt.Errorf("load raw quote source timezone: %w", err)
	}
	seen := make(map[string]bool, len(d.Streams))
	for _, stream := range d.Streams {
		if stream.Symbol == "" || len(stream.Quotes) == 0 || seen[stream.Symbol] {
			return errors.New("invalid or duplicate raw quote stream")
		}
		seen[stream.Symbol] = true
		for _, quote := range stream.Quotes {
			if quote.Source == "" || quote.Symbol != stream.Symbol || quote.Bid <= 0 || quote.Ask < quote.Bid || quote.Sequence == 0 || quote.Timestamp == "" {
				return errors.New("invalid raw quote")
			}
		}
	}
	return nil
}

func NormalizeQuotes(raw RawQuoteDataset, config QuoteNormalization) (QuoteDataset, error) {
	if err := raw.Validate(); err != nil {
		return QuoteDataset{}, err
	}
	if config.TimestampBasis != "RFC3339" && config.TimestampBasis != "LOCAL_RFC3339" && config.TimestampBasis != "UNIX_MILLIS" && config.TimestampBasis != "UNIX_NANOS" {
		return QuoteDataset{}, errors.New("unsupported quote timestamp basis")
	}
	if config.TimestampBasis == "LOCAL_RFC3339" {
		if config.SourceTimezone == "" {
			return QuoteDataset{}, errors.New("local quote timestamps require source timezone")
		}
		if _, err := time.LoadLocation(config.SourceTimezone); err != nil {
			return QuoteDataset{}, err
		}
	}
	streams := make(map[string][]domain.Quote)
	canonicalSources := make(map[string]string)
	for _, sourceStream := range raw.Streams {
		canonical := sourceStream.Symbol
		if alias, exists := config.SymbolAliases[canonical]; exists {
			canonical = alias
		}
		if canonical == "" {
			return QuoteDataset{}, errors.New("quote alias resolves to empty symbol")
		}
		if previous, exists := canonicalSources[canonical]; exists && previous != sourceStream.Symbol {
			return QuoteDataset{}, fmt.Errorf("multiple raw symbols normalize to %s", canonical)
		}
		canonicalSources[canonical] = sourceStream.Symbol
		for _, rawQuote := range sourceStream.Quotes {
			at, err := parseQuoteTimestamp(rawQuote.Timestamp, config)
			if err != nil {
				return QuoteDataset{}, fmt.Errorf("normalize %s quote: %w", sourceStream.Symbol, err)
			}
			streams[canonical] = append(streams[canonical], domain.Quote{Source: rawQuote.Source, Symbol: canonical, Bid: rawQuote.Bid, Ask: rawQuote.Ask, At: at, Sequence: rawQuote.Sequence})
		}
	}
	result := QuoteDataset{SchemaVersion: QuoteDatasetSchema, Provenance: raw.Provenance, Streams: make([]QuoteStream, 0, len(streams))}
	result.Provenance.TimestampBasis = "UTC_EVENT_TIME"
	for symbol, quotes := range streams {
		sort.SliceStable(quotes, func(i, j int) bool {
			if quotes[i].At.Equal(quotes[j].At) {
				return quotes[i].Sequence < quotes[j].Sequence
			}
			return quotes[i].At.Before(quotes[j].At)
		})
		result.Streams = append(result.Streams, QuoteStream{Symbol: symbol, Quotes: quotes})
	}
	sort.Slice(result.Streams, func(i, j int) bool { return result.Streams[i].Symbol < result.Streams[j].Symbol })
	if err := result.Validate(); err != nil {
		return QuoteDataset{}, fmt.Errorf("validate normalized quote dataset: %w", err)
	}
	return result, nil
}

func parseQuoteTimestamp(value string, config QuoteNormalization) (time.Time, error) {
	switch config.TimestampBasis {
	case "RFC3339":
		parsed, err := time.Parse(time.RFC3339Nano, value)
		return parsed.UTC(), err
	case "LOCAL_RFC3339":
		location, _ := time.LoadLocation(config.SourceTimezone)
		parsed, err := time.ParseInLocation("2006-01-02T15:04:05.999999999", value, location)
		return parsed.UTC(), err
	case "UNIX_MILLIS", "UNIX_NANOS":
		integer, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			return time.Time{}, err
		}
		if config.TimestampBasis == "UNIX_MILLIS" {
			return time.Unix(0, integer*int64(time.Millisecond)).UTC(), nil
		}
		return time.Unix(0, integer).UTC(), nil
	default:
		return time.Time{}, errors.New("unsupported quote timestamp basis")
	}
}
