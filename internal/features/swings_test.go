package features

import (
	"testing"
	"time"

	"tradex/internal/domain"
)

func featureCandle(index int, high, low, close domain.Price) domain.Candle {
	openTime := time.Date(2026, time.August, 18, 0, index, 0, 0, time.UTC)
	open := close
	return domain.Candle{
		Source: "test", Symbol: "EURUSD", Timeframe: time.Minute,
		OpenTime: openTime, CloseTime: openTime.Add(time.Minute),
		Open: open, High: high, Low: low, Close: close, TickCount: 1,
	}
}

func TestSwingIsKnownOnlyAfterRightWindow(t *testing.T) {
	detector, err := NewSwingDetector(2, 2)
	if err != nil {
		t.Fatal(err)
	}
	candles := []domain.Candle{
		featureCandle(0, 101, 99, 100), featureCandle(1, 103, 100, 102),
		featureCandle(2, 110, 101, 105), featureCandle(3, 104, 100, 102),
		featureCandle(4, 102, 98, 100),
	}
	for i := 0; i < 4; i++ {
		swings, err := detector.Push(candles[i])
		if err != nil || len(swings) != 0 {
			t.Fatalf("bar %d produced early swing=%+v err=%v", i, swings, err)
		}
	}
	swings, err := detector.Push(candles[4])
	if err != nil {
		t.Fatal(err)
	}
	if len(swings) != 1 || swings[0].Kind != SwingHigh || swings[0].Price != 110 {
		t.Fatalf("unexpected swings: %+v", swings)
	}
	if !swings[0].ConfirmedAt.Equal(candles[4].CloseTime) {
		t.Fatalf("confirmed at %s, want %s", swings[0].ConfirmedAt, candles[4].CloseTime)
	}
}

func TestClassifyTrend(t *testing.T) {
	swings := []Swing{
		{Kind: SwingHigh, Price: 100}, {Kind: SwingLow, Price: 90},
		{Kind: SwingHigh, Price: 110}, {Kind: SwingLow, Price: 95},
	}
	if got := ClassifyTrend(swings); got != TrendBullish {
		t.Fatalf("got %s want %s", got, TrendBullish)
	}
}

func TestStructureBreakEmitsOncePerSwing(t *testing.T) {
	detector := StructureDetector{}
	swing := Swing{Kind: SwingHigh, Price: 105, PivotTime: time.Date(2026, 8, 18, 0, 1, 0, 0, time.UTC)}
	candle := featureCandle(5, 110, 104, 108)
	if got := detector.OnCandle(candle, []Swing{swing}); len(got) != 1 || got[0].Direction != BreakBullish {
		t.Fatalf("unexpected first break: %+v", got)
	}
	if got := detector.OnCandle(featureCandle(6, 111, 105, 109), nil); len(got) != 0 {
		t.Fatalf("duplicate break emitted: %+v", got)
	}
}
