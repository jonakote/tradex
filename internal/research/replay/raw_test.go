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
	"tradex/internal/marketdata"
)

// TestCompileRawBuildsExecutableCandleScenario proves the new, detector-
// agnostic raw compilation: a raw MID candle stream becomes EventQuote +
// EventCandle pairs with no hand-labelled structure, and the resulting
// scenario replays through the continuation detector exactly like a
// hand-authored one.
func TestCompileRawBuildsExecutableCandleScenario(t *testing.T) {
	raw := rawContinuationFixture(t)
	scenario, err := CompileRaw(raw)
	if err != nil {
		t.Fatal(err)
	}
	if scenario.RawDataset == nil || len(scenario.Events) != len(raw.Dataset.Streams[0].Candles)*2 {
		t.Fatalf("unexpected compiled scenario: raw=%v events=%d", scenario.RawDataset != nil, len(scenario.Events))
	}
	for index := 0; index < len(scenario.Events); index += 2 {
		if scenario.Events[index].Kind != EventQuote || scenario.Events[index+1].Kind != EventCandle {
			t.Fatalf("event %d/%d kinds=%s/%s, want QUOTE/EXECUTION_CANDLE", index, index+1, scenario.Events[index].Kind, scenario.Events[index+1].Kind)
		}
	}
	result, err := Run(context.Background(), scenario, filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	// CompileRaw's synthetic quote only samples each candle's CLOSE (plus a
	// disclosed half-spread), coarser than a dedicated execution quote feed
	// (that richer feed is CompileQuotes' job, layered on afterwards). So
	// this proves detection and risk-approved submission end to end, not
	// necessarily a fill on this coarse synthetic quote path.
	if len(result.Decisions) != 1 || !result.Decisions[0].Approved || len(result.Orders) != 1 {
		t.Fatalf("raw-compiled replay did not detect and submit the expected setup: %+v", result)
	}
}

func TestCompileRawRejectsNonMidPriceDataset(t *testing.T) {
	raw := rawContinuationFixture(t)
	raw.Dataset.Provenance.PriceBasis = marketdata.PriceBid
	if _, err := CompileRaw(raw); err == nil || !strings.Contains(err.Error(), "MID candles") {
		t.Fatalf("expected price-basis rejection, got %v", err)
	}
}

func TestRawScenarioRoundTripsThroughStrictJSON(t *testing.T) {
	raw := rawContinuationFixture(t)
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
	if err != nil || loaded.RawDataset == nil {
		t.Fatalf("loaded raw scenario raw=%v err=%v", loaded.RawDataset != nil, err)
	}
	if err := WriteScenarioNew(path, scenario); err == nil {
		t.Fatal("compiled scenario writer overwrote an existing file")
	}
}

// rawContinuationFixture builds a raw single-stream candle dataset from the
// same OHLC path as the continuation win fixture, so CompileRaw can be
// proven against the identical detector outcome without hand-labelling any
// ICC-era event.
func rawContinuationFixture(t *testing.T) RawScenario {
	t.Helper()
	labelled := loadFixture(t, "eurusd_continuation_win.json")
	candles := make([]domain.Candle, 0, len(labelled.Events))
	for _, event := range labelled.Events {
		if event.Kind == EventCandle {
			candles = append(candles, *event.Candle)
		}
	}
	dataset := marketdata.CandleDataset{
		SchemaVersion: marketdata.CandleDatasetSchema,
		Provenance: marketdata.Provenance{
			DatasetID: "raw-eurusd-continuation-v1", Vendor: "fixture", Feed: "fixture",
			Broker: "Pepperstone", RetrievedAt: candles[len(candles)-1].CloseTime, SourceTimezone: "UTC",
			TimestampBasis: "UTC_OPEN_TIME", PriceBasis: marketdata.PriceMid,
			RawArtifactSHA256: strings.Repeat("c", 64), LicenseReference: "fixture",
		},
		Streams: []marketdata.CandleStream{{Symbol: "EURUSD", Timeframe: time.Hour, Candles: candles}},
	}
	base := labelled
	base.Name = "raw-eurusd-continuation"
	base.Events = nil
	base.RawDataset = nil
	base.ResampleReports = nil
	return RawScenario{Base: base, Dataset: dataset, Compile: RawCompileConfig{SyntheticHalfSpreadTicks: 1}}
}
