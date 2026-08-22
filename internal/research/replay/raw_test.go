package replay

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tradex/internal/domain"
	"tradex/internal/economics"
	"tradex/internal/execution"
	"tradex/internal/features"
	"tradex/internal/marketdata"
	"tradex/internal/research/derive"
	"tradex/internal/strategy/icc"
)

func TestCompileRawDerivesAndExecutesICCWithoutLabelledEvents(t *testing.T) {
	raw := rawICCFixture()
	scenario, err := CompileRaw(raw)
	if err != nil {
		t.Fatal(err)
	}
	if scenario.RawDataset == nil || len(scenario.Events) != 6 {
		t.Fatalf("unexpected compiled scenario: raw=%v events=%+v", scenario.RawDataset != nil, scenario.Events)
	}
	wantKinds := []EventKind{EventContext, EventIndication, EventSetupCandle, EventQuote, EventConfirmation, EventCandle}
	for index, want := range wantKinds {
		if scenario.Events[index].Kind != want {
			t.Fatalf("event %d kind=%s want=%s", index, scenario.Events[index].Kind, want)
		}
	}
	if scenario.Events[1].Direction != features.BreakBullish || scenario.Events[1].BrokenLevel != 108115 {
		t.Fatalf("indication was not derived from raw structure: %+v", scenario.Events[1])
	}
	manifest, err := ManifestFor(scenario)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Dataset.ID != raw.Dataset.Provenance.DatasetID {
		t.Fatalf("manifest dataset=%q want=%q", manifest.Dataset.ID, raw.Dataset.Provenance.DatasetID)
	}
	result, err := RunVerified(context.Background(), scenario, manifest, filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Decisions) != 1 || !result.Decisions[0].Approved || len(result.Fills) != 2 || result.Report.Wins != 1 {
		t.Fatalf("raw compiled replay did not execute expected trade: %+v", result)
	}
}

func TestCompileRawRejectsNonMidPriceDataset(t *testing.T) {
	raw := rawICCFixture()
	raw.Dataset.Provenance.PriceBasis = marketdata.PriceBid
	if _, err := CompileRaw(raw); err == nil || !strings.Contains(err.Error(), "MID candles") {
		t.Fatalf("expected price-basis rejection, got %v", err)
	}
}

func TestCompileRawResamplesConfiguredBaseStreamBeforeDerivation(t *testing.T) {
	raw := rawICCFixture()
	start := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	candles := make([]domain.Candle, 0, 48)
	for index := 0; index < 48; index++ {
		open := start.Add(time.Duration(index) * 5 * time.Minute)
		candles = append(candles, domain.Candle{
			Source: "pepperstone-raw", Symbol: "EURUSD", Timeframe: 5 * time.Minute,
			OpenTime: open, CloseTime: open.Add(5 * time.Minute), Open: 108100,
			High: 108101, Low: 108099, Close: 108100, TickCount: 10,
		})
	}
	raw.Dataset.Streams = []marketdata.CandleStream{{Symbol: "EURUSD", Timeframe: 5 * time.Minute, Candles: candles}}
	raw.Compile.BaseTimeframe = 5 * time.Minute
	_, err := CompileRaw(raw)
	if err == nil || !strings.Contains(err.Error(), "no aligned ICC indication") {
		t.Fatalf("expected resampled flat data to reach derivation without a signal, got %v", err)
	}
}

func TestCompileRawDerivesMultipleIndependentICCCycles(t *testing.T) {
	raw := rawICCFixture()
	start := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	h1 := &raw.Dataset.Streams[1].Candles
	*h1 = append(*h1, rawTrendCandles(start.Add(32*time.Hour), time.Hour)...)
	*h1 = append(*h1, domain.Candle{
		Source: "pepperstone-raw", Symbol: "EURUSD", Timeframe: time.Hour,
		OpenTime: start.Add(39 * time.Hour), CloseTime: start.Add(40 * time.Hour),
		Open: 108116, High: 108117, Low: 108114, Close: 108116, TickCount: 100,
	})
	m15 := &raw.Dataset.Streams[2].Candles
	for index := 0; index < 24; index++ {
		open := start.Add(34*time.Hour + time.Duration(index)*15*time.Minute)
		*m15 = append(*m15, domain.Candle{
			Source: "pepperstone-raw", Symbol: "EURUSD", Timeframe: 15 * time.Minute,
			OpenTime: open, CloseTime: open.Add(15 * time.Minute), Open: 108100,
			High: 108101, Low: 108099, Close: 108100, TickCount: 100,
		})
	}
	*m15 = append(*m15, rawTrendCandles(start.Add(40*time.Hour), 15*time.Minute)...)
	*m15 = append(*m15, domain.Candle{
		Source: "pepperstone-raw", Symbol: "EURUSD", Timeframe: 15 * time.Minute,
		OpenTime: start.Add(41*time.Hour + 45*time.Minute), CloseTime: start.Add(42 * time.Hour),
		Open: 108116, High: 108200, Low: 108115, Close: 108190, TickCount: 100,
	})
	raw.Compile.AllowedSessions = []string{"london", "new_york"}
	raw.Dataset.Provenance.RetrievedAt = start.Add(60 * time.Hour)

	scenario, err := CompileRaw(raw)
	if err != nil {
		t.Fatal(err)
	}
	confirmations := 0
	for _, event := range scenario.Events {
		if event.Kind == EventConfirmation {
			confirmations++
		}
	}
	if confirmations != 2 {
		t.Fatalf("derived confirmations=%d want=2 events=%+v", confirmations, scenario.Events)
	}
	result, err := Run(context.Background(), scenario, filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Decisions) != 2 || len(result.Orders) != 2 || result.Report.Trades != 2 || result.Report.Wins != 2 {
		t.Fatalf("unexpected multi-cycle raw replay: %+v", result)
	}
}

func TestRawScenarioRoundTripsThroughStrictJSON(t *testing.T) {
	raw := rawICCFixture()
	encoded, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeRaw(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	scenario, err := CompileRaw(decoded)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "compiled.json")
	if err := WriteScenarioNew(path, scenario); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil || loaded.RawDataset == nil || len(loaded.Events) != 6 {
		t.Fatalf("loaded raw scenario events=%d raw=%v err=%v", len(loaded.Events), loaded.RawDataset != nil, err)
	}
	if err := WriteScenarioNew(path, scenario); err == nil {
		t.Fatal("compiled scenario writer overwrote an existing file")
	}
}

func rawICCFixture() RawScenario {
	start := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	h4 := rawTrendCandles(start, 4*time.Hour)
	h1 := rawTrendCandles(start.Add(24*time.Hour), time.Hour)
	h1 = append(h1, domain.Candle{
		Source: "pepperstone-raw", Symbol: "EURUSD", Timeframe: time.Hour,
		OpenTime: start.Add(31 * time.Hour), CloseTime: start.Add(32 * time.Hour),
		Open: 108116, High: 108117, Low: 108114, Close: 108116, TickCount: 100,
	})
	m15 := rawTrendCandles(start.Add(32*time.Hour), 15*time.Minute)
	m15 = append(m15, domain.Candle{
		Source: "pepperstone-raw", Symbol: "EURUSD", Timeframe: 15 * time.Minute,
		OpenTime: start.Add(33*time.Hour + 45*time.Minute), CloseTime: start.Add(34 * time.Hour),
		Open: 108116, High: 108200, Low: 108115, Close: 108190, TickCount: 100,
	})
	dataset := marketdata.CandleDataset{
		SchemaVersion: marketdata.CandleDatasetSchema,
		Provenance: marketdata.Provenance{
			DatasetID: "pepperstone-eurusd-raw-v1", Vendor: "Pepperstone", Feed: "pepperstone-raw",
			Broker: "Pepperstone", RetrievedAt: start.Add(48 * time.Hour), SourceTimezone: "UTC",
			TimestampBasis: "UTC_OPEN_TIME", PriceBasis: marketdata.PriceMid,
			RawArtifactSHA256: strings.Repeat("c", 64), LicenseReference: "demo-export-terms",
		},
		Streams: []marketdata.CandleStream{
			{Symbol: "EURUSD", Timeframe: 4 * time.Hour, Candles: h4},
			{Symbol: "EURUSD", Timeframe: time.Hour, Candles: h1},
			{Symbol: "EURUSD", Timeframe: 15 * time.Minute, Candles: m15},
		},
	}
	base := Scenario{
		Name: "raw-eurusd-icc", InitialEquityMinor: 1000000,
		Instrument: domain.Instrument{Symbol: "EURUSD", BaseCurrency: "EUR", QuoteCurrency: "USD",
			PriceScale: 100000, TickSize: 1, QuantityStep: 1, MinQuantity: 1, MaxQuantity: 1000000},
		PriceValue: economics.PriceValue{AccountCurrency: "USD", Numerator: 1, Denominator: 1},
		Paper: PaperSettings{AccountID: "raw-paper", MaxQuoteAge: 5 * time.Second,
			SlippageTicks: 1, HalfSpreadTicksForCandle: 1},
		ICC: icc.DefaultConfig(),
		Execution: execution.ICCPlan{StrategyID: "icc", StrategyVersion: "raw-v1", IntentTTL: time.Minute,
			RiskBPS: 25, RewardRiskBPS: 20000, StopBufferTicks: 1, SlippageTicks: 1},
	}
	return RawScenario{Base: base, Dataset: dataset, Compile: RawCompileConfig{
		Derivation: derive.Config{SwingLeft: 1, SwingRight: 1}, ContextTimeframe: 4 * time.Hour,
		SetupTimeframe: time.Hour, ConfirmationTimeframe: 15 * time.Minute,
		AllowedSessions: []string{"london"}, SyntheticHalfSpreadTicks: 1,
	}}
}

func rawTrendCandles(start time.Time, timeframe time.Duration) []domain.Candle {
	highs := []domain.Price{108100, 108110, 108102, 108115, 108105, 108108, 108118}
	lows := []domain.Price{108090, 108095, 108080, 108090, 108085, 108090, 108100}
	closes := []domain.Price{108095, 108100, 108090, 108100, 108095, 108100, 108116}
	candles := make([]domain.Candle, 0, len(highs))
	for index := range highs {
		open := start.Add(time.Duration(index) * timeframe)
		candles = append(candles, domain.Candle{
			Source: "pepperstone-raw", Symbol: "EURUSD", Timeframe: timeframe,
			OpenTime: open, CloseTime: open.Add(timeframe), Open: closes[index],
			High: highs[index], Low: lows[index], Close: closes[index], TickCount: 100,
		})
	}
	return candles
}
