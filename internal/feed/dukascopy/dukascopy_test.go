package dukascopy

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"tradex/internal/encoding/lzma"
	"tradex/internal/instrument"
)

// Dukascopy numbers months from zero in its URL paths. Getting this wrong
// shifts every candle by a month while still returning plausible data, so it
// is pinned explicitly.
func TestSlotsUseZeroIndexedMonths(t *testing.T) {
	from := time.Date(2024, time.June, 10, 0, 0, 0, 0, time.UTC)
	to := time.Date(2024, time.July, 2, 0, 0, 0, 0, time.UTC)

	hourly := slots(Hour, from, to)
	if len(hourly) != 2 {
		t.Fatalf("expected two monthly files, got %d", len(hourly))
	}
	if hourly[0].path != "2024/05" {
		t.Fatalf("June must be month index 05, got %q", hourly[0].path)
	}
	if hourly[1].path != "2024/06" {
		t.Fatalf("July must be month index 06, got %q", hourly[1].path)
	}
	if !hourly[0].epoch.Equal(time.Date(2024, time.June, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("epoch must be the first of the month, got %s", hourly[0].epoch)
	}

	daily := slots(Day, from, to)
	if len(daily) != 1 || daily[0].path != "2024" {
		t.Fatalf("daily slots: %+v", daily)
	}

	minute := slots(Minute, from, from.AddDate(0, 0, 3))
	if len(minute) != 3 || minute[0].path != "2024/05/10" {
		t.Fatalf("minute slots: %+v", minute)
	}
}

func TestFileNameMatchesPublishedLayout(t *testing.T) {
	cases := map[string]string{
		fileName(Day, Bid):    "BID_candles_day_1.bi5",
		fileName(Hour, Ask):   "ASK_candles_hour_1.bi5",
		fileName(Minute, Bid): "BID_candles_min_1.bi5",
	}
	for got, want := range cases {
		if got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	}
}

// Digits are verified against real price levels, not guessed: a wrong value
// misprices every candle by a factor of 100.
func TestLookupDigitsAndScale(t *testing.T) {
	cases := []struct {
		symbol string
		digits int
		scale  int64
	}{
		{"EURUSD", 5, 100000},
		{"eurusd", 5, 100000},
		{"USDJPY", 3, 1000},
		{"XAUUSD", 3, 1000},
		{"XAGUSD", 3, 1000},
	}
	for _, tc := range cases {
		instrument, err := Lookup(tc.symbol)
		if err != nil {
			t.Fatalf("%s: %v", tc.symbol, err)
		}
		if instrument.Digits != tc.digits || instrument.PriceScale != tc.scale {
			t.Fatalf("%s: got %d digits scale %d", tc.symbol, instrument.Digits, instrument.PriceScale)
		}
	}
	if _, err := Lookup("DOGEUSD"); err == nil {
		t.Fatal("an unverified instrument must be rejected, not assumed")
	}
	if len(Symbols()) != len(published) {
		t.Fatal("Symbols must list every published instrument")
	}
	// Precision must come from the shared registry, never a local copy, or
	// the research feed and the live engine can silently disagree.
	for _, symbol := range Symbols() {
		decoded, err := Lookup(symbol)
		if err != nil {
			t.Fatalf("%s: %v", symbol, err)
		}
		spec, ok := instrument.Lookup(symbol)
		if !ok {
			t.Fatalf("%s is published but absent from the instrument registry", symbol)
		}
		if decoded.Digits != spec.Digits || decoded.PriceScale != spec.PriceScale {
			t.Fatalf("%s: feed says %d digits, registry says %d",
				symbol, decoded.Digits, spec.Digits)
		}
	}
}

// Decoding is exercised against a real published payload: EURUSD daily
// candles for 2024, decompressed by the in-repo LZMA decoder.
func TestDecodeRealPayload(t *testing.T) {
	compressed, err := os.ReadFile(filepath.Join("..", "..", "encoding", "lzma",
		"testdata", "eurusd_2024_day.bi5"))
	if err != nil {
		t.Skipf("fixture unavailable: %v", err)
	}
	payload, err := lzma.Decode(compressed)
	if err != nil {
		t.Fatal(err)
	}
	instrument, err := Lookup("EURUSD")
	if err != nil {
		t.Fatal(err)
	}
	epoch := time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)

	candles, err := decode(payload, instrument, Day, epoch)
	if err != nil {
		t.Fatal(err)
	}
	// 366 records exist; the 52 weekend filler bars must be dropped.
	if len(candles) != 314 {
		t.Fatalf("expected 314 trading days of 366 records, got %d", len(candles))
	}
	for _, candle := range candles {
		if err := candle.Validate(); err != nil {
			t.Fatalf("candle %s invalid: %v", candle.OpenTime, err)
		}
		if candle.Source != Source || candle.Symbol != "EURUSD" {
			t.Fatalf("provenance: %+v", candle)
		}
		if candle.Timeframe != 24*time.Hour {
			t.Fatalf("timeframe: %s", candle.Timeframe)
		}
		// EUR/USD traded between 0.95 and 1.25 in 2024; at 5 digits that is
		// 95000..125000 ticks. This catches a wrong digit count immediately.
		if candle.Close < 95000 || candle.Close > 125000 {
			t.Fatalf("implausible price %d at %s: check the digit count",
				candle.Close, candle.OpenTime)
		}
	}
	// Spot-check the first two trading days against the published values.
	// 2024-01-01 is a real but very thin session, not a filler bar.
	first := candles[0]
	if !first.OpenTime.Equal(time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("first trading day: got %s", first.OpenTime)
	}
	if first.Open != 110427 || first.Close != 110365 || first.Low != 110356 || first.High != 110447 {
		t.Fatalf("2024-01-01 OHLC: got %d/%d/%d/%d want 110427/110365/110356/110447",
			first.Open, first.Close, first.Low, first.High)
	}
	second := candles[1]
	if second.Open != 110366 || second.Close != 109416 || second.Low != 109375 || second.High != 110432 {
		t.Fatalf("2024-01-02 OHLC: got %d/%d/%d/%d want 110366/109416/109375/110432",
			second.Open, second.Close, second.Low, second.High)
	}
}

func TestDecodeRejectsRaggedPayload(t *testing.T) {
	instrument, err := Lookup("EURUSD")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decode(make([]byte, 25), instrument, Day, time.Now()); err == nil {
		t.Fatal("expected rejection of a payload that is not whole records")
	}
}

func TestMidCandleAveragesWithoutInvertingGeometry(t *testing.T) {
	instrument, _ := Lookup("EURUSD")
	epoch := time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)
	bid, err := decode(buildRecord(0, 100000, 100050, 99950, 100100, 500), instrument, Hour, epoch)
	if err != nil {
		t.Fatal(err)
	}
	ask, err := decode(buildRecord(0, 100002, 100052, 99952, 100102, 500), instrument, Hour, epoch)
	if err != nil {
		t.Fatal(err)
	}
	mid := midCandle(bid[0], ask[0])
	if err := mid.Validate(); err != nil {
		t.Fatalf("mid candle invalid: %v", err)
	}
	if mid.Open != 100001 || mid.Close != 100051 {
		t.Fatalf("mid open/close: got %d/%d", mid.Open, mid.Close)
	}
	if mid.TickCount != bid[0].TickCount+ask[0].TickCount {
		t.Fatalf("mid tick count: got %d", mid.TickCount)
	}
}

// buildRecord assembles one 24-byte published record: note the published
// field order is open, close, low, high.
func buildRecord(seconds int32, open, closePrice, low, high int32, volume float32) []byte {
	record := make([]byte, recordSize)
	putInt32(record[0:4], seconds)
	putInt32(record[4:8], open)
	putInt32(record[8:12], closePrice)
	putInt32(record[12:16], low)
	putInt32(record[16:20], high)
	putFloat32(record[20:24], volume)
	return record
}

func putInt32(target []byte, value int32) {
	target[0] = byte(value >> 24)
	target[1] = byte(value >> 16)
	target[2] = byte(value >> 8)
	target[3] = byte(value)
}

func putFloat32(target []byte, value float32) {
	binary.BigEndian.PutUint32(target, math.Float32bits(value))
}
