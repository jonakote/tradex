package dukascopy

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"tradex/internal/feed"
	"tradex/internal/instrument"
)

// LiveConfig configures the tick-derived candle poller.
//
// This is the only keyless route to recent FX and metal candles, and it is
// deliberately honest about its latency: Dukascopy publishes one tick file
// per completed clock hour, so a bar is visible roughly an hour after it
// closes. That is adequate for daily and 4h research monitoring and is NOT
// adequate for acting on a 15m or 1h limit, whose fill window closes before
// the data arrives.
type LiveConfig struct {
	Symbols []string `json:"symbols"`
	// Interval is a feed interval spelling, e.g. "1h", "4h", "15m".
	Interval string `json:"interval"`
	// Side selects bid, ask, or mid. Bid matches the published candle files.
	Side Side `json:"side"`
	// LookbackHours bounds how far back a poll scans. It must exceed the
	// interval plus the publication lag, or newly published bars are missed.
	LookbackHours int `json:"lookback_hours"`
	// CacheDir caches raw payloads; strongly recommended, as the feed is
	// slow and rate-limits bursts.
	CacheDir string `json:"cache_dir"`
}

func (c LiveConfig) Validate() error {
	if len(c.Symbols) == 0 {
		return errors.New("dukascopy live: at least one symbol is required")
	}
	timeframe := feed.IntervalDuration(c.Interval)
	if timeframe <= 0 {
		return fmt.Errorf("dukascopy live: unknown interval %q", c.Interval)
	}
	if timeframe > 24*time.Hour {
		return errors.New("dukascopy live: intervals above one day are not supported")
	}
	if c.Side != "" && !c.Side.Valid() {
		return fmt.Errorf("dukascopy live: invalid side %q", c.Side)
	}
	for _, symbol := range c.Symbols {
		if _, err := Lookup(symbol); err != nil {
			return err
		}
	}
	return nil
}

func (c LiveConfig) side() Side {
	if c.Side == "" {
		return Bid
	}
	return c.Side
}

func (c LiveConfig) lookback() time.Duration {
	hours := c.LookbackHours
	if hours <= 0 {
		// Two intervals plus a generous publication margin.
		hours = int(2*feed.IntervalDuration(c.Interval)/time.Hour) + 6
	}
	if hours < 3 {
		hours = 3
	}
	return time.Duration(hours) * time.Hour
}

// LiveFeed polls published tick files and emits completed candles, mirroring
// the shape of the other feed clients so the signals command can treat it
// identically.
type LiveFeed struct {
	client *Client
	config LiveConfig

	mu   sync.Mutex
	last map[string]time.Time
}

func NewLiveFeed(config LiveConfig) (*LiveFeed, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &LiveFeed{
		client: NewClient(config.CacheDir),
		config: config,
		last:   map[string]time.Time{},
	}, nil
}

// Interval reports the configured interval, for logging.
func (f *LiveFeed) Interval() string { return f.config.Interval }

// Side reports the resolved quote side, for logging.
func (f *LiveFeed) Side() Side { return f.config.side() }

// Symbols reports the configured symbols, for logging.
func (f *LiveFeed) Symbols() []string { return f.config.Symbols }

// FetchAllSymbols returns candles not seen in an earlier poll. Only bars
// whose every spanned hour has been published are returned, so an
// under-covered bar can never be mistaken for a complete one.
func (f *LiveFeed) FetchAllSymbols(ctx context.Context) ([]feed.Candle, error) {
	timeframe := feed.IntervalDuration(f.config.Interval)
	now := f.client.clock().UTC()
	from := now.Add(-f.config.lookback())

	var result []feed.Candle
	for _, symbol := range f.config.Symbols {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		spec, ok := instrument.Lookup(symbol)
		if !ok {
			return result, fmt.Errorf("dukascopy live: no verified precision for %q", symbol)
		}
		candles, err := f.client.TickCandles(symbol, timeframe, f.config.side(), from, now)
		if err != nil {
			return result, err
		}

		f.mu.Lock()
		seen := f.last[spec.Symbol]
		var latest time.Time
		for _, candle := range candles {
			if !candle.OpenTime.After(seen) {
				continue
			}
			result = append(result, feed.Candle{
				Symbol:   symbol,
				Interval: f.config.Interval,
				// Integer ticks converted once, at the boundary.
				Open:      spec.Price(int64(candle.Open)),
				High:      spec.Price(int64(candle.High)),
				Low:       spec.Price(int64(candle.Low)),
				Close:     spec.Price(int64(candle.Close)),
				Volume:    float64(candle.TickCount),
				Time:      candle.OpenTime,
				OpenTime:  candle.OpenTime,
				CloseTime: candle.CloseTime,
			})
			if candle.OpenTime.After(latest) {
				latest = candle.OpenTime
			}
		}
		if !latest.IsZero() {
			f.last[spec.Symbol] = latest
		}
		f.mu.Unlock()
	}
	sort.SliceStable(result, func(a, b int) bool { return result[a].Time.Before(result[b].Time) })
	return result, nil
}
