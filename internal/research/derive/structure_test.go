package derive

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"tradex/internal/domain"
	"tradex/internal/features"
	"tradex/internal/marketdata"
)

func TestStructureDerivesConfirmedSwingAndBreakWithoutLookahead(t *testing.T) {
	dataset := derivedDataset([]domain.Price{101, 110, 103, 112, 111})
	prefix := dataset
	prefix.Streams = []marketdata.CandleStream{{
		Symbol: dataset.Streams[0].Symbol, Timeframe: dataset.Streams[0].Timeframe,
		Candles: append([]domain.Candle(nil), dataset.Streams[0].Candles[:4]...),
	}}
	first, err := Structure(prefix, Config{SwingLeft: 1, SwingRight: 1})
	if err != nil {
		t.Fatal(err)
	}
	full, err := Structure(dataset, Config{SwingLeft: 1, SwingRight: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.Events, full.Events[:len(first.Events)]) {
		t.Fatal("future candle changed an already-derived event")
	}
	if len(full.Events[2].ConfirmedSwings) != 1 || full.Events[2].ConfirmedSwings[0].Kind != features.SwingHigh ||
		!full.Events[2].ConfirmedSwings[0].ConfirmedAt.Equal(full.Events[2].At) {
		t.Fatalf("unexpected confirmed swing: %+v", full.Events[2])
	}
	if len(full.Events[3].StructureBreaks) != 1 || full.Events[3].StructureBreaks[0].Direction != features.BreakBullish {
		t.Fatalf("expected raw-candle bullish break: %+v", full.Events[3])
	}
}

func TestStructureOrdersEqualCloseHigherTimeframeFirst(t *testing.T) {
	dataset := derivedDataset([]domain.Price{101, 102, 103, 104})
	h4 := dataset.Streams[0]
	h4.Candles = append([]domain.Candle(nil), h4.Candles...)
	h4.Timeframe = 4 * time.Hour
	for index := range h4.Candles {
		h4.Candles[index].Timeframe = 4 * time.Hour
		h4.Candles[index].OpenTime = time.Date(2026, 8, 18, index*4, 0, 0, 0, time.UTC)
		h4.Candles[index].CloseTime = h4.Candles[index].OpenTime.Add(4 * time.Hour)
	}
	dataset.Streams = append(dataset.Streams, h4)
	report, err := Structure(dataset, Config{SwingLeft: 1, SwingRight: 1})
	if err != nil {
		t.Fatal(err)
	}
	for index := 1; index < len(report.Events); index++ {
		if report.Events[index].At.Equal(report.Events[index-1].At) && report.Events[index].Timeframe > report.Events[index-1].Timeframe {
			t.Fatal("equal-close events were not ordered higher timeframe first")
		}
	}
}

func derivedDataset(highs []domain.Price) marketdata.CandleDataset {
	open := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	candles := make([]domain.Candle, 0, len(highs))
	for index, high := range highs {
		barOpen := open.Add(time.Duration(index) * time.Hour)
		close := high - 1
		candles = append(candles, domain.Candle{
			Source: "raw-feed", Symbol: "EURUSD", Timeframe: time.Hour,
			OpenTime: barOpen, CloseTime: barOpen.Add(time.Hour), Open: close,
			High: high, Low: close - 2, Close: close, TickCount: 10,
		})
	}
	return marketdata.CandleDataset{
		SchemaVersion: marketdata.CandleDatasetSchema,
		Provenance: marketdata.Provenance{
			DatasetID: "raw-eurusd", Vendor: "vendor", Feed: "raw-feed", Broker: "Pepperstone",
			RetrievedAt: open.Add(48 * time.Hour), SourceTimezone: "UTC", TimestampBasis: "UTC_OPEN_TIME",
			PriceBasis: marketdata.PriceMid, RawArtifactSHA256: strings.Repeat("b", 64),
			LicenseReference: "research-export",
		},
		Streams: []marketdata.CandleStream{{Symbol: "EURUSD", Timeframe: time.Hour, Candles: candles}},
	}
}
