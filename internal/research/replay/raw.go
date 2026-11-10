package replay

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"

	"tradex/internal/domain"
	"tradex/internal/marketdata"
)

// RawCompileConfig fixes the one remaining raw-compilation policy choice:
// the synthetic half-spread used to turn a MID-basis candle close into a
// disclosed bid/ask for execution. The old ICC-era config additionally fixed
// context/setup/confirmation timeframes and allowed sessions to drive a
// hand-rolled structure-break derivation; that derivation is retired because
// a scan.Detector already does its own pattern detection directly over the
// raw candle window, so there is no separate multi-timeframe indication/
// confirmation state machine left to configure.
type RawCompileConfig struct {
	SyntheticHalfSpreadTicks int64 `json:"synthetic_half_spread_ticks"`
}

func (c RawCompileConfig) Validate() error {
	if c.SyntheticHalfSpreadTicks < 0 {
		return errors.New("raw synthetic spread cannot be negative")
	}
	return nil
}

type RawScenario struct {
	Base    Scenario                 `json:"base"`
	Dataset marketdata.CandleDataset `json:"dataset"`
	Compile RawCompileConfig         `json:"compile"`
}

// CompileRaw turns a single-stream raw MID candle dataset directly into
// EventCandle events (one scan.Stream advance per candle), each preceded by
// a synthetic EventQuote at the same timestamp so a detected setup always
// has a prior executable quote to size and risk-approve against. It
// replaces the old ICC-specific H4/H1/confirmation structure-break
// derivation entirely, per docs/MIGRATION_SCAN_REPLAY.md: a scan.Detector
// already derives its own setups from the raw window, so relabelling that
// derivation here would duplicate -- and could silently diverge from -- the
// detector's own logic.
func CompileRaw(raw RawScenario) (Scenario, error) {
	if err := raw.Dataset.Validate(); err != nil {
		return Scenario{}, err
	}
	if err := raw.Compile.Validate(); err != nil {
		return Scenario{}, err
	}
	if raw.Dataset.Provenance.PriceBasis != marketdata.PriceMid {
		return Scenario{}, errors.New("raw compilation requires MID candles for disclosed synthetic bid/ask")
	}
	if len(raw.Base.Events) != 0 || raw.Base.RawDataset != nil || len(raw.Base.ResampleReports) != 0 {
		return Scenario{}, errors.New("raw scenario base must not contain events, another dataset, or resample reports")
	}
	if len(raw.Dataset.Streams) != 1 {
		return Scenario{}, errors.New("raw compilation requires exactly one candle stream")
	}
	base := raw.Base
	normalize(&base)
	stream := raw.Dataset.Streams[0]
	if stream.Symbol != base.Instrument.Symbol {
		return Scenario{}, errors.New("raw candle symbol does not match scenario instrument")
	}
	if stream.Timeframe != base.DetectorParams.Timeframe {
		return Scenario{}, errors.New("raw candle timeframe does not match the detector timeframe")
	}
	raw.Base = base

	halfSpread := domain.Price(raw.Compile.SyntheticHalfSpreadTicks) * base.Instrument.TickSize
	events := make([]Event, 0, len(stream.Candles)*2)
	quoteSequence := uint64(0)
	for _, candle := range stream.Candles {
		if candle.Close <= halfSpread {
			return Scenario{}, errors.New("synthetic spread is not valid at this candle's close")
		}
		quoteSequence++
		quote := domain.Quote{
			Source: raw.Dataset.Provenance.Feed, Symbol: candle.Symbol,
			Bid: candle.Close - halfSpread, Ask: candle.Close + halfSpread,
			At: candle.CloseTime, Sequence: quoteSequence,
		}
		events = append(events, Event{At: candle.CloseTime, Kind: EventQuote, Quote: &quote})
		events = append(events, Event{At: candle.CloseTime, Kind: EventCandle, Candle: candlePointer(candle)})
	}
	if len(events) == 0 {
		return Scenario{}, errors.New("raw dataset produced no candle events")
	}

	scenario := raw.Base
	scenario.Events = events
	datasetCopy := raw.Dataset
	scenario.RawDataset = &datasetCopy
	if err := scenario.Validate(); err != nil {
		return Scenario{}, err
	}
	return scenario, nil
}

// CompileQuotes merges a validated broker-native bid/ask stream into an
// existing strategy scenario. Quotes at equal timestamps precede strategy
// events, making same-time execution deterministic and explicit.
func CompileQuotes(base Scenario, dataset marketdata.QuoteDataset) (Scenario, error) {
	if err := base.Validate(); err != nil {
		return Scenario{}, fmt.Errorf("validate base scenario: %w", err)
	}
	if err := dataset.Validate(); err != nil {
		return Scenario{}, err
	}
	if len(dataset.Streams) != 1 || dataset.Streams[0].Symbol != base.Instrument.Symbol {
		return Scenario{}, errors.New("quote dataset must contain exactly the base scenario instrument")
	}
	result := base
	result.Name = base.Name + "-quotes-" + dataset.Provenance.DatasetID
	result.QuoteDataset = &dataset
	type timedEvent struct {
		event Event
		order int
	}
	merged := make([]timedEvent, 0, len(base.Events)+len(dataset.Streams[0].Quotes))
	baseOrder := 0
	for _, event := range base.Events {
		if event.Kind == EventQuote {
			continue
		}
		merged = append(merged, timedEvent{event: event, order: baseOrder*2 + 1})
		baseOrder++
	}
	for index, quote := range dataset.Streams[0].Quotes {
		quoteCopy := quote
		merged = append(merged, timedEvent{event: Event{At: quote.At, Kind: EventQuote, Quote: &quoteCopy}, order: index * 2})
	}
	sort.SliceStable(merged, func(left, right int) bool {
		if merged[left].event.At.Equal(merged[right].event.At) {
			return merged[left].order < merged[right].order
		}
		return merged[left].event.At.Before(merged[right].event.At)
	})
	result.Events = make([]Event, len(merged))
	for index := range merged {
		result.Events[index] = merged[index].event
	}
	if err := result.Validate(); err != nil {
		return Scenario{}, fmt.Errorf("validate quote-compiled scenario: %w", err)
	}
	return result, nil
}

func LoadRaw(path string) (RawScenario, error) {
	file, err := os.Open(path)
	if err != nil {
		return RawScenario{}, err
	}
	defer file.Close()
	return DecodeRaw(file)
}

func DecodeRaw(reader io.Reader) (RawScenario, error) {
	var raw RawScenario
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil {
		return RawScenario{}, fmt.Errorf("decode raw replay scenario: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return RawScenario{}, errors.New("raw replay scenario contains trailing JSON")
	}
	return raw, nil
}

func WriteScenarioNew(path string, scenario Scenario) (writeErr error) {
	if path == "" {
		return errors.New("scenario path is required")
	}
	if err := scenario.Validate(); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(scenario, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create compiled scenario: %w", err)
	}
	defer func() {
		if err := file.Close(); writeErr == nil && err != nil {
			writeErr = err
		}
	}()
	written, err := file.Write(encoded)
	if err != nil {
		return fmt.Errorf("write compiled scenario: %w", err)
	}
	if written != len(encoded) {
		return errors.New("write compiled scenario: short write")
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync compiled scenario: %w", err)
	}
	return nil
}

func candlePointer(candle domain.Candle) *domain.Candle {
	copy := candle
	return &copy
}
