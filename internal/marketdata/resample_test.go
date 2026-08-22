package marketdata

import (
	"strings"
	"testing"
	"time"

	"tradex/internal/domain"
)

func TestResampleDatasetBuildsExactOHLCAndLineage(t *testing.T) {
	dataset := fiveMinuteDataset(12)
	output, reports, err := ResampleDataset(dataset, []time.Duration{time.Hour, 15 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Streams) != 3 || len(output.Transformations) != 2 || len(reports) != 2 {
		t.Fatalf("unexpected resample output: streams=%d transformations=%d reports=%+v", len(output.Streams), len(output.Transformations), reports)
	}
	m15, h1 := output.Streams[1], output.Streams[2]
	if m15.Timeframe != 15*time.Minute || len(m15.Candles) != 4 || h1.Timeframe != time.Hour || len(h1.Candles) != 1 {
		t.Fatalf("unexpected derived streams: m15=%+v h1=%+v", m15, h1)
	}
	bar := h1.Candles[0]
	if bar.Open != 1000 || bar.High != 1013 || bar.Low != 999 || bar.Close != 1012 || bar.TickCount != 120 {
		t.Fatalf("unexpected H1 aggregate: %+v", bar)
	}
	parent, _ := dataset.Fingerprint()
	if output.ParentFingerprint != parent || output.Provenance.DatasetID != dataset.Provenance.DatasetID+"/resampled-v1" {
		t.Fatalf("transformation lineage not preserved: %+v", output)
	}
}

func TestResampleDropsGappedBucketWithoutInventingCandle(t *testing.T) {
	dataset := fiveMinuteDataset(6)
	dataset.AllowGaps = true
	dataset.Streams[0].Candles = append(dataset.Streams[0].Candles[:1], dataset.Streams[0].Candles[2:]...)
	output, reports, err := ResampleDataset(dataset, []time.Duration{15 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if reports[0].DroppedBuckets != 1 || reports[0].OutputCandles != 1 || len(output.Streams[1].Candles) != 1 {
		t.Fatalf("gap was not conservatively dropped: %+v", reports[0])
	}
	if !output.Streams[1].Candles[0].OpenTime.Equal(dataset.Streams[0].Candles[2].OpenTime) {
		t.Fatalf("resampler invented or shifted a bucket: %+v", output.Streams[1].Candles)
	}
}

func TestResampleReportsWhollyMissingBucket(t *testing.T) {
	dataset := fiveMinuteDataset(9)
	dataset.AllowGaps = true
	dataset.Streams[0].Candles = append(dataset.Streams[0].Candles[:3], dataset.Streams[0].Candles[6:]...)
	_, reports, err := ResampleDataset(dataset, []time.Duration{15 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if reports[0].DroppedBuckets != 1 || reports[0].OutputCandles != 2 {
		t.Fatalf("wholly missing bucket was not reported: %+v", reports[0])
	}
}

func fiveMinuteDataset(count int) CandleDataset {
	start := time.Date(2026, 8, 18, 8, 0, 0, 0, time.UTC)
	candles := make([]domain.Candle, 0, count)
	for index := 0; index < count; index++ {
		open := start.Add(time.Duration(index) * 5 * time.Minute)
		price := domain.Price(1000 + index)
		candles = append(candles, domain.Candle{
			Source: "base-feed", Symbol: "EURUSD", Timeframe: 5 * time.Minute,
			OpenTime: open, CloseTime: open.Add(5 * time.Minute), Open: price,
			High: price + 2, Low: price - 1, Close: price + 1, TickCount: 10,
		})
	}
	return CandleDataset{
		SchemaVersion: CandleDatasetSchema,
		Provenance: Provenance{
			DatasetID: "eurusd-m5", Vendor: "vendor", Feed: "base-feed", Broker: "Pepperstone",
			RetrievedAt: start.Add(24 * time.Hour), SourceTimezone: "UTC", TimestampBasis: "UTC_OPEN_TIME",
			PriceBasis: PriceMid, RawArtifactSHA256: strings.Repeat("d", 64), LicenseReference: "research-export",
		},
		Streams: []CandleStream{{Symbol: "EURUSD", Timeframe: 5 * time.Minute, Candles: candles}},
	}
}
