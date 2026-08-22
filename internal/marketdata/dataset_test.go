package marketdata

import (
	"strings"
	"testing"
	"time"

	"tradex/internal/domain"
)

func TestCandleDatasetValidatesProvenanceAndFingerprintsContent(t *testing.T) {
	dataset := validCandleDataset()
	first, err := dataset.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	dataset.Streams[0].Candles[0].Close++
	second, err := dataset.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("candle mutation did not change dataset fingerprint")
	}
}

func TestCandleDatasetRejectsUndeclaredGapAndContradictoryFeed(t *testing.T) {
	dataset := validCandleDataset()
	gap := dataset.Streams[0].Candles[0]
	gap.OpenTime = gap.OpenTime.Add(2 * time.Hour)
	gap.CloseTime = gap.CloseTime.Add(2 * time.Hour)
	dataset.Streams[0].Candles = append(dataset.Streams[0].Candles, gap)
	if err := dataset.Validate(); err == nil || !strings.Contains(err.Error(), "undeclared gaps") {
		t.Fatalf("expected gap rejection, got %v", err)
	}
	dataset = validCandleDataset()
	dataset.Streams[0].Candles[0].Source = "another-feed"
	if err := dataset.Validate(); err == nil || !strings.Contains(err.Error(), "stream provenance") {
		t.Fatalf("expected feed rejection, got %v", err)
	}
}

func TestDecodeCandleDatasetIsStrict(t *testing.T) {
	_, err := DecodeCandleDataset(strings.NewReader(`{"unknown":true}`))
	if err == nil {
		t.Fatal("unknown candle dataset field was accepted")
	}
}

func validCandleDataset() CandleDataset {
	open := time.Date(2026, 8, 18, 8, 0, 0, 0, time.UTC)
	candle := domain.Candle{
		Source: "pepperstone-demo", Symbol: "EURUSD", Timeframe: time.Hour,
		OpenTime: open, CloseTime: open.Add(time.Hour), Open: 108000, High: 108200,
		Low: 107900, Close: 108100, TickCount: 100,
	}
	return CandleDataset{
		SchemaVersion: CandleDatasetSchema,
		Provenance: Provenance{
			DatasetID: "eurusd-2026-08-18", Vendor: "Pepperstone", Feed: "pepperstone-demo",
			Broker: "Pepperstone", RetrievedAt: open.Add(24 * time.Hour), SourceTimezone: "UTC",
			TimestampBasis: "UTC_OPEN_TIME", PriceBasis: PriceMid,
			RawArtifactSHA256: strings.Repeat("a", 64), LicenseReference: "demo-export-terms",
		},
		Streams: []CandleStream{{Symbol: "EURUSD", Timeframe: time.Hour, Candles: []domain.Candle{candle}}},
	}
}
