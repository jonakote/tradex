package replay

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"tradex/internal/domain"
	"tradex/internal/features"
	"tradex/internal/marketdata"
	"tradex/internal/research/derive"
	"tradex/internal/session"
)

type RawCompileConfig struct {
	Derivation               derive.Config `json:"derivation"`
	BaseTimeframe            time.Duration `json:"base_timeframe,omitempty"`
	ContextTimeframe         time.Duration `json:"context_timeframe"`
	SetupTimeframe           time.Duration `json:"setup_timeframe"`
	ConfirmationTimeframe    time.Duration `json:"confirmation_timeframe"`
	AllowedSessions          []string      `json:"allowed_sessions"`
	SyntheticHalfSpreadTicks int64         `json:"synthetic_half_spread_ticks"`
}

type RawScenario struct {
	Base    Scenario                 `json:"base"`
	Dataset marketdata.CandleDataset `json:"dataset"`
	Compile RawCompileConfig         `json:"compile"`
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

func (c RawCompileConfig) Validate() error {
	if err := c.Derivation.Validate(); err != nil {
		return err
	}
	if c.ContextTimeframe <= c.SetupTimeframe || c.SetupTimeframe <= c.ConfirmationTimeframe ||
		c.ConfirmationTimeframe <= 0 || len(c.AllowedSessions) == 0 || c.SyntheticHalfSpreadTicks < 0 {
		return errors.New("invalid raw ICC timeframe, session, or spread configuration")
	}
	if c.BaseTimeframe < 0 || c.BaseTimeframe >= c.ConfirmationTimeframe {
		return errors.New("raw base timeframe must be below the confirmation timeframe")
	}
	validSessions := map[string]bool{"sydney": true, "tokyo": true, "london": true, "new_york": true}
	for _, name := range c.AllowedSessions {
		if !validSessions[name] {
			return fmt.Errorf("unknown raw ICC session %q", name)
		}
	}
	return nil
}

// CompileRaw derives a single ICC research lifecycle from completed raw H4,
// H1, and confirmation candles. It does not accept caller-labelled breaks.
func CompileRaw(raw RawScenario) (Scenario, error) {
	if err := raw.Dataset.Validate(); err != nil {
		return Scenario{}, err
	}
	if err := raw.Compile.Validate(); err != nil {
		return Scenario{}, err
	}
	if raw.Dataset.Provenance.PriceBasis != marketdata.PriceMid {
		return Scenario{}, errors.New("raw ICC compilation requires MID candles for disclosed synthetic bid/ask")
	}
	if len(raw.Base.Events) != 0 || raw.Base.RawDataset != nil || len(raw.Base.ResampleReports) != 0 {
		return Scenario{}, errors.New("raw scenario base must not contain events, another dataset, or resample reports")
	}
	var resampleReports []marketdata.ResampleReport
	if raw.Compile.BaseTimeframe > 0 {
		if len(raw.Dataset.Streams) != 1 || raw.Dataset.Streams[0].Timeframe != raw.Compile.BaseTimeframe {
			return Scenario{}, errors.New("raw resampling requires exactly the configured base timeframe stream")
		}
		resampled, reports, err := marketdata.ResampleDataset(raw.Dataset, []time.Duration{
			raw.Compile.ConfirmationTimeframe, raw.Compile.SetupTimeframe, raw.Compile.ContextTimeframe,
		})
		if err != nil {
			return Scenario{}, fmt.Errorf("resample raw ICC dataset: %w", err)
		}
		raw.Dataset = resampled
		resampleReports = reports
	}
	base := raw.Base
	normalize(&base)
	if base.ICC.SetupTimeframe != raw.Compile.SetupTimeframe ||
		base.ICC.ConfirmationTimeframe != raw.Compile.ConfirmationTimeframe {
		return Scenario{}, errors.New("raw compiler and ICC strategy timeframes differ")
	}
	raw.Base = base
	for _, stream := range raw.Dataset.Streams {
		if stream.Symbol != raw.Base.Instrument.Symbol {
			return Scenario{}, errors.New("raw candle symbol does not match scenario instrument")
		}
	}
	report, err := derive.Structure(raw.Dataset, raw.Compile.Derivation)
	if err != nil {
		return Scenario{}, err
	}
	marketSessions, err := session.Defaults()
	if err != nil {
		return Scenario{}, err
	}
	allowed := make(map[string]bool, len(raw.Compile.AllowedSessions))
	for _, name := range raw.Compile.AllowedSessions {
		allowed[name] = true
	}
	isAllowed := func(at time.Time) bool {
		for _, active := range marketSessions.Active(at) {
			if allowed[active] {
				return true
			}
		}
		return false
	}

	var h4Trend, h1Trend features.Trend
	var latestH1High, latestH1Low *features.Swing
	var direction features.BreakDirection
	var indicationLevel, invalidation domain.Price
	indicationAt := time.Time{}
	corrected, invalidated, completed := false, false, false
	setupBars, indications := 0, 0
	hasSignal := false
	lastConfirmationAt := time.Time{}
	events := make([]Event, 0)
	quoteSequence := uint64(0)
	seenTimeframes := map[time.Duration]bool{}

	for _, derived := range report.Events {
		seenTimeframes[derived.Timeframe] = true
		switch derived.Timeframe {
		case raw.Compile.ContextTimeframe:
			h4Trend = derived.Trend
		case raw.Compile.SetupTimeframe:
			h1Trend = derived.Trend
			for index := range derived.ConfirmedSwings {
				swing := derived.ConfirmedSwings[index]
				if swing.Kind == features.SwingHigh {
					copy := swing
					latestH1High = &copy
				} else {
					copy := swing
					latestH1Low = &copy
				}
			}
			if completed || invalidated {
				direction = ""
				indicationLevel, invalidation = 0, 0
				indicationAt = time.Time{}
				corrected, invalidated, completed = false, false, false
				setupBars = 0
			}
			if direction == "" {
				if !isAllowed(derived.At) {
					continue
				}
				for _, structureBreak := range derived.StructureBreaks {
					candidateInvalidation, aligned := rawIndicationGeometry(structureBreak.Direction, h4Trend, h1Trend, latestH1High, latestH1Low)
					if structureBreak.Direction == features.BreakBullish && candidateInvalidation >= structureBreak.BrokenSwing.Price ||
						structureBreak.Direction == features.BreakBearish && candidateInvalidation <= structureBreak.BrokenSwing.Price {
						aligned = false
					}
					if !aligned {
						continue
					}
					direction = structureBreak.Direction
					indications++
					indicationLevel = structureBreak.BrokenSwing.Price
					invalidation = candidateInvalidation
					indicationAt = derived.At
					events = append(events,
						Event{At: derived.At, Kind: EventContext, H4Trend: h4Trend, H1Trend: h1Trend},
						Event{At: derived.At, Kind: EventIndication, Direction: direction, BrokenLevel: indicationLevel,
							InvalidationPrice: invalidation, AllowedSession: true, Candle: candlePointer(derived.Candle)},
					)
					break
				}
				continue
			}
			if !derived.At.After(indicationAt) {
				continue
			}
			events = append(events, Event{At: derived.At, Kind: EventSetupCandle, Candle: candlePointer(derived.Candle)})
			setupBars++
			if setupBars > raw.Base.ICC.MaxCorrectionBars {
				invalidated = true
				continue
			}
			if direction == features.BreakBullish {
				invalidated = derived.Candle.Close <= invalidation
				corrected = corrected || derived.Candle.Low <= indicationLevel
			} else {
				invalidated = derived.Candle.Close >= invalidation
				corrected = corrected || derived.Candle.High >= indicationLevel
			}
		case raw.Compile.ConfirmationTimeframe:
			if hasSignal && derived.At.After(lastConfirmationAt) {
				events = append(events, Event{At: derived.At, Kind: EventCandle, Candle: candlePointer(derived.Candle)})
			}
			if completed {
				continue
			}
			if direction == "" || !corrected || invalidated || !isAllowed(derived.At) {
				continue
			}
			for _, structureBreak := range derived.StructureBreaks {
				if structureBreak.Direction != direction || !reclaims(direction, derived.Candle.Close, indicationLevel) {
					continue
				}
				halfSpread := domain.Price(raw.Compile.SyntheticHalfSpreadTicks) * raw.Base.Instrument.TickSize
				if derived.Candle.Close <= halfSpread {
					return Scenario{}, errors.New("synthetic spread is not valid at confirmation price")
				}
				quoteSequence++
				quote := domain.Quote{Source: raw.Dataset.Provenance.Feed, Symbol: derived.Symbol,
					Bid: derived.Candle.Close - halfSpread, Ask: derived.Candle.Close + halfSpread,
					At: derived.At, Sequence: quoteSequence}
				events = append(events,
					Event{At: derived.At, Kind: EventQuote, Quote: &quote},
					Event{At: derived.At, Kind: EventConfirmation, Direction: direction,
						AllowedSession: true, Candle: candlePointer(derived.Candle)},
				)
				completed = true
				hasSignal = true
				lastConfirmationAt = derived.At
				break
			}
		}
	}
	for _, timeframe := range []time.Duration{raw.Compile.ContextTimeframe, raw.Compile.SetupTimeframe, raw.Compile.ConfirmationTimeframe} {
		if !seenTimeframes[timeframe] {
			return Scenario{}, fmt.Errorf("raw dataset is missing required timeframe %s", timeframe)
		}
	}
	if indications == 0 {
		return Scenario{}, errors.New("raw dataset produced no aligned ICC indication")
	}

	scenario := raw.Base
	scenario.Events = events
	datasetCopy := raw.Dataset
	scenario.RawDataset = &datasetCopy
	scenario.ResampleReports = resampleReports
	if err := scenario.Validate(); err != nil {
		return Scenario{}, err
	}
	return scenario, nil
}

func rawIndicationGeometry(
	direction features.BreakDirection,
	h4, h1 features.Trend,
	latestHigh, latestLow *features.Swing,
) (domain.Price, bool) {
	if direction == features.BreakBullish && h4 == features.TrendBullish && h1 == features.TrendBullish && latestLow != nil {
		return latestLow.Price, true
	}
	if direction == features.BreakBearish && h4 == features.TrendBearish && h1 == features.TrendBearish && latestHigh != nil {
		return latestHigh.Price, true
	}
	return 0, false
}

func reclaims(direction features.BreakDirection, close, level domain.Price) bool {
	return direction == features.BreakBullish && close > level || direction == features.BreakBearish && close < level
}

func candlePointer(candle domain.Candle) *domain.Candle {
	copy := candle
	return &copy
}
