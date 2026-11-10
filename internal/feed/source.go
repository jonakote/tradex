// Package feed provides market-data sources that yield completed candles.
//
// There is exactly one candle type in tradex: domain.Candle, whose prices are
// integer ticks at the instrument's verified precision. Sources convert
// vendor payloads into it once, at the boundary, and nothing downstream ever
// sees a floating-point price or a second candle representation.
//
// That is a deliberate constraint rather than a stylistic one. The codebase
// previously carried three competing candle types with conversions between
// them, and both a one-bar timestamp shift and a wrong-tick-size defect
// survived unnoticed inside those conversions.
package feed

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"tradex/internal/domain"
	"tradex/internal/instrument"
)

// Source yields completed candles from one market-data provider.
//
// A source must never return an in-progress bar: a signal derived from one
// repaints as the bar develops. It must also set each candle's OpenTime and
// CloseTime from the vendor's own bar boundaries rather than inferring them.
type Source interface {
	// Name identifies the source in logs and in candle provenance.
	Name() string
	// Interval is the bar interval this source produces.
	Interval() string
	// Symbols lists the configured instruments.
	Symbols() []string
	// Poll returns completed candles not yielded by an earlier call. An
	// empty result is normal: it means no new bar has closed.
	Poll(ctx context.Context) ([]domain.Candle, error)
}

// ErrNoSymbols is returned by a source configured without instruments.
var ErrNoSymbols = errors.New("feed: at least one symbol is required")

// resolve returns the verified precision for a symbol. Sources fail closed on
// an unverified instrument rather than assuming a tick size, because a
// plausible-looking wrong scale changes the meaning of every downstream
// tick-denominated parameter.
func resolve(symbol string) (instrument.Spec, error) {
	spec, ok := instrument.Lookup(symbol)
	if !ok {
		return instrument.Spec{}, &UnknownInstrumentError{Symbol: symbol}
	}
	return spec, nil
}

// UnknownInstrumentError reports a symbol with no verified precision.
type UnknownInstrumentError struct{ Symbol string }

func (e *UnknownInstrumentError) Error() string {
	return "feed: no verified precision for " + e.Symbol +
		"; add it to internal/instrument after verifying against the venue"
}

// cursor tracks the newest candle already yielded per key, so a polling
// source re-reading overlapping history emits each bar exactly once.
type cursor struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

func newCursor() *cursor { return &cursor{seen: map[string]time.Time{}} }

// advance sorts candles by open time, drops any already yielded under key,
// and records the newest. The returned slice is safe to hand downstream.
func (c *cursor) advance(key string, candles []domain.Candle) []domain.Candle {
	sort.SliceStable(candles, func(a, b int) bool {
		return candles[a].OpenTime.Before(candles[b].OpenTime)
	})

	c.mu.Lock()
	defer c.mu.Unlock()
	last := c.seen[key]
	fresh := make([]domain.Candle, 0, len(candles))
	newest := last
	for _, candle := range candles {
		if !candle.OpenTime.After(last) {
			continue
		}
		fresh = append(fresh, candle)
		if candle.OpenTime.After(newest) {
			newest = candle.OpenTime
		}
	}
	if newest.After(last) {
		c.seen[key] = newest
	}
	return fresh
}
