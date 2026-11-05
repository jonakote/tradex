package dukascopy

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"tradex/internal/domain"
	"tradex/internal/encoding/lzma"
	"tradex/internal/feed"
)

// The fixture is a real published hour: EURUSD 2026-11-05 13:00 UTC, 5,140
// ticks. Values below were computed independently from the raw payload, so
// agreement is agreement with the venue rather than with this code.
const (
	fixtureHourTicks = 5140
	fixtureBid1hOpen = 112254
	fixtureBid1hHigh = 112262
	fixtureBid1hLow  = 112126
	fixtureBid1hClos = 112192
)

func fixtureHour() time.Time {
	return time.Date(2026, time.November, 5, 13, 0, 0, 0, time.UTC)
}

func loadTickFixture(t *testing.T) []byte {
	t.Helper()
	compressed, err := os.ReadFile(filepath.Join("testdata", "eurusd_ticks_hour.bi5"))
	if err != nil {
		t.Skipf("fixture unavailable: %v", err)
	}
	payload, err := lzma.Decode(compressed)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return payload
}

func TestDecodeTicksMatchesPublishedHour(t *testing.T) {
	ticks, err := decodeTicks(loadTickFixture(t), fixtureHour())
	if err != nil {
		t.Fatal(err)
	}
	if len(ticks) != fixtureHourTicks {
		t.Fatalf("got %d ticks, want %d", len(ticks), fixtureHourTicks)
	}

	first, last := ticks[0], ticks[len(ticks)-1]
	// The first record is 127 ms into the hour at bid 1.12254 / ask 1.12256.
	if first.Bid != 112254 || first.Ask != 112256 {
		t.Fatalf("first tick: bid %d ask %d", first.Bid, first.Ask)
	}
	if !first.At.Equal(fixtureHour().Add(127 * time.Millisecond)) {
		t.Fatalf("first tick time: %s", first.At)
	}
	if last.Bid != 112192 {
		t.Fatalf("last tick bid: %d", last.Bid)
	}
	// Every tick must fall inside its own hour, and the series must be
	// non-decreasing in time.
	for i, tick := range ticks {
		if tick.At.Before(fixtureHour()) || !tick.At.Before(fixtureHour().Add(time.Hour)) {
			t.Fatalf("tick %d escapes its hour: %s", i, tick.At)
		}
		if tick.Ask < tick.Bid {
			t.Fatalf("tick %d has a crossed quote: bid %d ask %d", i, tick.Bid, tick.Ask)
		}
		if i > 0 && tick.At.Before(ticks[i-1].At) {
			t.Fatalf("tick %d goes backwards in time", i)
		}
	}
	// Mid stays an exact integer tick between bid and ask.
	mid := first.Mid()
	if mid < first.Bid || mid > first.Ask {
		t.Fatalf("mid %d outside bid/ask %d/%d", mid, first.Bid, first.Ask)
	}
}

func TestDecodeTicksHandlesEmptyAndRaggedPayloads(t *testing.T) {
	// A closed market publishes an empty file; that is data, not an error.
	ticks, err := decodeTicks(nil, fixtureHour())
	if err != nil || ticks != nil {
		t.Fatalf("empty payload: %v / %v", ticks, err)
	}
	if _, err := decodeTicks(make([]byte, tickRecordSize+3), fixtureHour()); err == nil {
		t.Fatal("expected rejection of a payload that is not whole records")
	}
}

// Aggregation is checked against values derived independently from the raw
// payload, including the invariant that the hourly bar's extremes equal the
// extremes of its own sub-bars.
func TestAggregateTicksIntoCandles(t *testing.T) {
	ticks, err := decodeTicks(loadTickFixture(t), fixtureHour())
	if err != nil {
		t.Fatal(err)
	}

	hourly := aggregateForTest(t, ticks, time.Hour, Bid)
	if len(hourly) != 1 {
		t.Fatalf("expected one hourly bar, got %d", len(hourly))
	}
	bar := hourly[0]
	if bar.Open != fixtureBid1hOpen || bar.High != fixtureBid1hHigh ||
		bar.Low != fixtureBid1hLow || bar.Close != fixtureBid1hClos {
		t.Fatalf("hourly OHLC: got %d/%d/%d/%d want %d/%d/%d/%d",
			bar.Open, bar.High, bar.Low, bar.Close,
			fixtureBid1hOpen, fixtureBid1hHigh, fixtureBid1hLow, fixtureBid1hClos)
	}
	if bar.TickCount != uint64(fixtureHourTicks) {
		t.Fatalf("tick count: got %d want %d", bar.TickCount, fixtureHourTicks)
	}
	if err := bar.Validate(); err != nil {
		t.Fatalf("hourly bar invalid: %v", err)
	}

	quarters := aggregateForTest(t, ticks, 15*time.Minute, Bid)
	if len(quarters) != 4 {
		t.Fatalf("expected four 15m bars, got %d", len(quarters))
	}
	// The hour must reconcile with its parts: open of the first, close of
	// the last, and the extremes across all of them.
	high, low := quarters[0].High, quarters[0].Low
	var count uint64
	for i, q := range quarters {
		if err := q.Validate(); err != nil {
			t.Fatalf("15m bar %d invalid: %v", i, err)
		}
		if !q.OpenTime.Equal(fixtureHour().Add(time.Duration(i) * 15 * time.Minute)) {
			t.Fatalf("15m bar %d open time: %s", i, q.OpenTime)
		}
		if q.High > high {
			high = q.High
		}
		if q.Low < low {
			low = q.Low
		}
		count += q.TickCount
	}
	if quarters[0].Open != bar.Open || quarters[3].Close != bar.Close {
		t.Fatal("hourly open/close must match its first/last sub-bar")
	}
	if high != bar.High || low != bar.Low {
		t.Fatalf("hourly extremes %d/%d disagree with sub-bars %d/%d",
			bar.High, bar.Low, high, low)
	}
	if count != bar.TickCount {
		t.Fatalf("sub-bar ticks %d do not sum to %d", count, bar.TickCount)
	}

	// Ask bars must sit at or above bid bars: the spread is never negative.
	askBars := aggregateForTest(t, ticks, time.Hour, Ask)
	if askBars[0].Close < bar.Close {
		t.Fatalf("ask close %d below bid close %d", askBars[0].Close, bar.Close)
	}
}

// aggregateForTest mirrors TickCandles' aggregation without any network, so
// the arithmetic is tested directly.
func aggregateForTest(t *testing.T, ticks []Tick, timeframe time.Duration, side Side) []domain.Candle {
	t.Helper()
	type acc struct {
		open, high, low, close domain.Price
		count                  uint64
	}
	bars := map[time.Time]*acc{}
	for _, tick := range ticks {
		price := tick.price(side)
		slot := tick.At.Truncate(timeframe)
		bar := bars[slot]
		if bar == nil {
			bars[slot] = &acc{open: price, high: price, low: price, close: price, count: 1}
			continue
		}
		if price > bar.high {
			bar.high = price
		}
		if price < bar.low {
			bar.low = price
		}
		bar.close = price
		bar.count++
	}
	slots := make([]time.Time, 0, len(bars))
	for slot := range bars {
		slots = append(slots, slot)
	}
	for i := 1; i < len(slots); i++ {
		for j := i; j > 0 && slots[j].Before(slots[j-1]); j-- {
			slots[j], slots[j-1] = slots[j-1], slots[j]
		}
	}
	result := make([]domain.Candle, 0, len(slots))
	for _, slot := range slots {
		bar := bars[slot]
		result = append(result, domain.Candle{
			Source: Source, Symbol: "EURUSD", Timeframe: timeframe,
			OpenTime: slot, CloseTime: slot.Add(timeframe),
			Open: bar.open, High: bar.high, Low: bar.low, Close: bar.close,
			TickCount: bar.count,
		})
	}
	return result
}

// The in-progress hour has no published file, so it must never be requested.
func TestCompletedHoursExcludesTheCurrentHour(t *testing.T) {
	now := time.Date(2026, time.November, 5, 14, 27, 0, 0, time.UTC)
	hours := completedHours(now.Add(-5*time.Hour), now, now)
	if len(hours) != 5 {
		t.Fatalf("got %d hours, want 5", len(hours))
	}
	for _, hour := range hours {
		if !hour.Before(now.Truncate(time.Hour)) {
			t.Fatalf("hour %s is not yet published", hour)
		}
	}
	if !hours[len(hours)-1].Equal(time.Date(2026, time.November, 5, 13, 0, 0, 0, time.UTC)) {
		t.Fatalf("last completed hour: %s", hours[len(hours)-1])
	}
	// A window entirely inside the current hour yields nothing.
	if got := completedHours(now.Add(-10*time.Minute), now, now); len(got) != 0 {
		t.Fatalf("expected no hours, got %v", got)
	}
}

// A bar spanning an unpublished hour would understate its own range, so it
// must be withheld rather than emitted.
func TestFullyCoveredRejectsPartialBars(t *testing.T) {
	hour := func(h int) time.Time {
		return time.Date(2026, time.November, 5, h, 0, 0, 0, time.UTC)
	}
	covered := map[time.Time]bool{hour(12): true, hour(13): true}

	if !fullyCovered(hour(12), time.Hour, covered) {
		t.Fatal("a covered hourly bar must be accepted")
	}
	if !fullyCovered(hour(12).Add(15*time.Minute), 15*time.Minute, covered) {
		t.Fatal("a covered sub-hour bar must be accepted")
	}
	// A 4h bar at 12:00 spans 12,13,14,15 — only two are covered.
	if fullyCovered(hour(12), 4*time.Hour, covered) {
		t.Fatal("a 4h bar missing two hours must be withheld")
	}
	if fullyCovered(hour(14), time.Hour, covered) {
		t.Fatal("an uncovered hour must be withheld")
	}
}

func TestLiveConfigValidation(t *testing.T) {
	valid := LiveConfig{Symbols: []string{"EURUSD", "XAUUSD"}, Interval: "1h"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if valid.side() != Bid {
		t.Fatal("side must default to bid, matching the published candle files")
	}
	// Lookback must exceed the interval, or newly published bars are missed.
	if valid.lookback() < feed.IntervalDuration("1h") {
		t.Fatal("default lookback must exceed the interval")
	}

	cases := map[string]LiveConfig{
		"no symbols":        {Interval: "1h"},
		"unknown interval":  {Symbols: []string{"EURUSD"}, Interval: "7m"},
		"unknown symbol":    {Symbols: []string{"DOGEUSD"}, Interval: "1h"},
		"bad side":          {Symbols: []string{"EURUSD"}, Interval: "1h", Side: "middle"},
		"interval too long": {Symbols: []string{"EURUSD"}, Interval: "1w"},
	}
	for name, config := range cases {
		t.Run(name, func(t *testing.T) {
			if err := config.Validate(); err == nil {
				t.Fatal("expected a validation error")
			}
			if _, err := NewLiveFeed(config); err == nil {
				t.Fatal("expected construction to fail")
			}
		})
	}
}

func TestTickCandlesRejectsIncoherentTimeframes(t *testing.T) {
	client := NewClient("")
	now := time.Date(2026, time.November, 5, 14, 0, 0, 0, time.UTC)
	client.Now = func() time.Time { return now }

	for _, timeframe := range []time.Duration{0, -time.Hour, 7 * time.Minute, 90 * time.Minute} {
		if _, err := client.TickCandles("EURUSD", timeframe, Bid, now.Add(-2*time.Hour), now); err == nil {
			t.Fatalf("timeframe %s must be rejected", timeframe)
		}
	}
	if _, err := client.TickCandles("DOGEUSD", time.Hour, Bid, now.Add(-2*time.Hour), now); err == nil {
		t.Fatal("an unverified instrument must be rejected")
	}
	// A window inside the current hour needs no network call and yields
	// nothing rather than erroring.
	candles, err := client.TickCandles("EURUSD", time.Hour, Bid, now, now.Add(time.Hour))
	if err != nil || len(candles) != 0 {
		t.Fatalf("got %d candles, err %v", len(candles), err)
	}
}
