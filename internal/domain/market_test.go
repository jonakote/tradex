package domain

import (
	"testing"
	"time"
)

func TestQuoteMidAvoidsAdditionOverflow(t *testing.T) {
	q := Quote{Source: "test", Symbol: "EURUSD", Bid: 108400, Ask: 108410, At: time.Now(), Sequence: 1}
	if err := q.Validate(); err != nil {
		t.Fatal(err)
	}
	if got, want := q.Mid(), Price(108405); got != want {
		t.Fatalf("mid: got %d want %d", got, want)
	}
}

func TestCandleRejectsImpossibleOHLC(t *testing.T) {
	open := time.Date(2026, time.August, 18, 10, 0, 0, 0, time.UTC)
	candle := Candle{
		Source: "test", Symbol: "EURUSD", Timeframe: time.Minute,
		OpenTime: open, CloseTime: open.Add(time.Minute),
		Open: 100, High: 99, Low: 98, Close: 99, TickCount: 1,
	}
	if err := candle.Validate(); err == nil {
		t.Fatal("impossible OHLC accepted")
	}
}
