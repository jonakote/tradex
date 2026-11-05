package dukascopy

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"tradex/internal/domain"
	"tradex/internal/instrument"
)

// Tick-file support. Dukascopy publishes one file per calendar hour per
// instrument, which is the only keyless route to recent FX and metal prices:
// the aggregated candle files are published per month and so lag by up to a
// month.
//
// Record layout (big-endian, 20 bytes):
//
//	uint32  milliseconds from the start of the file's hour
//	uint32  ask, in instrument points
//	uint32  bid, in instrument points
//	float32 ask volume
//	float32 bid volume
//
// Prices are integer points at the instrument's quoted precision, so they
// convert to domain.Price ticks without any floating-point step.
const tickRecordSize = 20

// Tick is one published quote.
type Tick struct {
	At        time.Time
	Bid       domain.Price
	Ask       domain.Price
	BidVolume float64
	AskVolume float64
}

// Mid is the midpoint, truncated so it remains an exact integer tick.
func (t Tick) Mid() domain.Price { return (t.Bid + t.Ask) / 2 }

func (t Tick) price(side Side) domain.Price {
	switch side {
	case Ask:
		return t.Ask
	case Mid:
		return t.Mid()
	default:
		return t.Bid
	}
}

// Ticks returns every published tick whose timestamp lies in [from, to).
//
// The current, incomplete hour is never requested: its file does not exist
// until the hour ends. This is the feed's hard latency floor — roughly one
// hour plus publication delay.
func (c *Client) Ticks(symbol string, from, to time.Time) ([]Tick, error) {
	instr, err := Lookup(symbol)
	if err != nil {
		return nil, err
	}
	from, to = from.UTC(), to.UTC()
	if !to.After(from) {
		return nil, errors.New("dukascopy: the time range must be non-empty")
	}

	hours := completedHours(from, to, c.clock())
	if len(hours) == 0 {
		return nil, nil
	}

	decoded := make([][]Tick, len(hours))
	failures := make([]error, len(hours))
	queue := make(chan int)
	workers := c.Workers
	if workers <= 0 {
		workers = 1
	}
	if workers > len(hours) {
		workers = len(hours)
	}
	var waiting sync.WaitGroup
	for w := 0; w < workers; w++ {
		waiting.Add(1)
		go func() {
			defer waiting.Done()
			for index := range queue {
				hour := hours[index]
				payload, err := c.fetchTickHour(instr.Symbol, hour)
				if err != nil {
					failures[index] = err
					continue
				}
				ticks, err := decodeTicks(payload, hour)
				if err != nil {
					failures[index] = fmt.Errorf("%s %s: %w",
						instr.Symbol, hour.Format("2006-01-02 15h"), err)
					continue
				}
				decoded[index] = ticks
			}
		}()
	}
	for index := range hours {
		queue <- index
	}
	close(queue)
	waiting.Wait()
	for _, err := range failures {
		if err != nil {
			return nil, err
		}
	}

	var result []Tick
	for _, ticks := range decoded {
		for _, tick := range ticks {
			if tick.At.Before(from) || !tick.At.Before(to) {
				continue
			}
			result = append(result, tick)
		}
	}
	// Hours are fetched concurrently but merged in order, then sorted, so
	// the output never depends on completion order.
	sort.SliceStable(result, func(a, b int) bool { return result[a].At.Before(result[b].At) })
	return result, nil
}

// completedHours lists the hour files covering [from, to) that can exist.
// The in-progress hour is excluded.
func completedHours(from, to, now time.Time) []time.Time {
	cutoff := now.UTC().Truncate(time.Hour)
	cursor := from.UTC().Truncate(time.Hour)
	var hours []time.Time
	for cursor.Before(to) {
		if !cursor.Before(cutoff) {
			break
		}
		hours = append(hours, cursor)
		cursor = cursor.Add(time.Hour)
	}
	return hours
}

func (c *Client) clock() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) fetchTickHour(symbol string, hour time.Time) ([]byte, error) {
	// Months are zero-indexed in Dukascopy paths.
	path := fmt.Sprintf("%04d/%02d/%02d", hour.Year(), int(hour.Month())-1, hour.Day())
	name := fmt.Sprintf("%02dh_ticks.bi5", hour.Hour())
	return c.fetchFile(symbol, path, name)
}

func decodeTicks(payload []byte, hour time.Time) ([]Tick, error) {
	if len(payload) == 0 {
		// A closed market publishes an empty file; that is not an error.
		return nil, nil
	}
	if len(payload)%tickRecordSize != 0 {
		return nil, fmt.Errorf("payload of %d bytes is not a whole number of %d-byte records",
			len(payload), tickRecordSize)
	}
	epoch := hour.UTC().Truncate(time.Hour)
	ticks := make([]Tick, 0, len(payload)/tickRecordSize)
	for offset := 0; offset < len(payload); offset += tickRecordSize {
		record := payload[offset : offset+tickRecordSize]
		millis := binary.BigEndian.Uint32(record[0:4])
		ask := int64(binary.BigEndian.Uint32(record[4:8]))
		bid := int64(binary.BigEndian.Uint32(record[8:12]))
		if ask <= 0 || bid <= 0 {
			continue
		}
		ticks = append(ticks, Tick{
			At:        epoch.Add(time.Duration(millis) * time.Millisecond),
			Bid:       domain.Price(bid),
			Ask:       domain.Price(ask),
			AskVolume: float64(math.Float32frombits(binary.BigEndian.Uint32(record[12:16]))),
			BidVolume: float64(math.Float32frombits(binary.BigEndian.Uint32(record[16:20]))),
		})
	}
	return ticks, nil
}

// TickCandles aggregates published ticks into completed candles of the given
// timeframe.
//
// A bar is emitted only when every hour it spans has been published, so a
// partially covered bar can never be mistaken for a complete one. Open is the
// first tick, close the last, high and low the extremes — all on the selected
// side, in integer ticks.
func (c *Client) TickCandles(symbol string, timeframe time.Duration, side Side,
	from, to time.Time) ([]domain.Candle, error) {

	instr, err := Lookup(symbol)
	if err != nil {
		return nil, err
	}
	if !side.Valid() {
		return nil, fmt.Errorf("dukascopy: invalid side %q", side)
	}
	if timeframe <= 0 {
		return nil, errors.New("dukascopy: timeframe must be positive")
	}
	if timeframe > time.Hour && timeframe%time.Hour != 0 {
		return nil, errors.New("dukascopy: timeframes above one hour must be whole hours")
	}
	if timeframe < time.Hour && time.Hour%timeframe != 0 {
		return nil, errors.New("dukascopy: sub-hour timeframes must divide an hour")
	}

	// Align the window to bar boundaries so a partial leading bar is not
	// built from a fragment of its ticks.
	start := from.UTC().Truncate(timeframe)
	end := to.UTC().Truncate(timeframe)
	if !end.After(start) {
		return nil, nil
	}

	ticks, err := c.Ticks(instr.Symbol, start, end)
	if err != nil {
		return nil, err
	}
	if len(ticks) == 0 {
		return nil, nil
	}

	// Which hours actually produced data, so coverage can be checked.
	covered := map[time.Time]bool{}
	for _, tick := range ticks {
		covered[tick.At.Truncate(time.Hour)] = true
	}

	type accumulator struct {
		open, high, low, close domain.Price
		count                  uint64
	}
	bars := map[time.Time]*accumulator{}
	for _, tick := range ticks {
		price := tick.price(side)
		slot := tick.At.Truncate(timeframe)
		bar := bars[slot]
		if bar == nil {
			bars[slot] = &accumulator{open: price, high: price, low: price, close: price, count: 1}
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
	sort.Slice(slots, func(a, b int) bool { return slots[a].Before(slots[b]) })

	candles := make([]domain.Candle, 0, len(slots))
	for _, slot := range slots {
		if !fullyCovered(slot, timeframe, covered) {
			continue
		}
		bar := bars[slot]
		candle := domain.Candle{
			Source: Source, Symbol: instr.Symbol, Timeframe: timeframe,
			OpenTime: slot, CloseTime: slot.Add(timeframe),
			Open: bar.open, High: bar.high, Low: bar.low, Close: bar.close,
			TickCount: bar.count,
		}
		if err := candle.Validate(); err != nil {
			return nil, fmt.Errorf("dukascopy: aggregated candle %s is invalid: %w", slot, err)
		}
		candles = append(candles, candle)
	}
	return candles, nil
}

// fullyCovered reports whether every hour the bar spans produced ticks. A bar
// built from a partially published window would understate its own range.
func fullyCovered(slot time.Time, timeframe time.Duration, covered map[time.Time]bool) bool {
	first := slot.Truncate(time.Hour)
	last := slot.Add(timeframe - time.Nanosecond).Truncate(time.Hour)
	for hour := first; !hour.After(last); hour = hour.Add(time.Hour) {
		if !covered[hour] {
			return false
		}
	}
	return true
}

// Scale exposes the verified price scale so callers can convert the integer
// ticks returned here into decimal prices.
func Scale(symbol string) (int64, error) {
	spec, ok := instrument.Lookup(symbol)
	if !ok {
		return 0, fmt.Errorf("dukascopy: no verified precision for %q", symbol)
	}
	return spec.PriceScale, nil
}
