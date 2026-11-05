// Package dukascopy reads historical candles from Dukascopy's public data
// feed. It is a research data source: it fetches and decodes, and performs no
// account, order, or subscription operation.
//
// Dukascopy publishes prices as integer points at the instrument's quoted
// precision, which maps directly onto the tradex integer tick representation.
// No floating-point conversion occurs on any price, so decoding is exact and
// reproducible. Only volume is a float, and it is used solely as a tick-count
// proxy.
//
// Payload layout (big-endian, 24 bytes per record):
//
//	int32   timestamp offset from the file's epoch, in seconds
//	int32   open, in instrument points
//	int32   close, low, high, in instrument points
//	float32 volume
//
// Note the field order: open, close, low, high.
package dukascopy

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"tradex/internal/domain"
	"tradex/internal/encoding/lzma"
	"tradex/internal/instrument"
)

const (
	// Source identifies candles produced by this package in audit trails.
	Source = "dukascopy"

	recordSize = 24
	baseURL    = "https://datafeed.dukascopy.com/datafeed"

	// The feed rejects requests without a browser user agent.
	userAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

	// maxPayload bounds a single compressed file read.
	maxPayload = 64 << 20
)

// Instrument records the quoted precision needed to interpret raw points.
// PriceScale is 10^Digits and is the scale a tradex instrument must declare
// for prices decoded here.
type Instrument struct {
	Symbol     string
	Digits     int
	PriceScale int64
}

// published lists the instruments this feed is known to publish. Precision
// is NOT duplicated here: it comes from internal/instrument, the single
// verified source of truth, so the two cannot drift apart.
var published = map[string]struct{}{
	"EURUSD": {}, "GBPUSD": {}, "AUDUSD": {}, "NZDUSD": {},
	"USDCAD": {}, "USDCHF": {}, "EURGBP": {}, "EURCHF": {},
	"EURAUD": {}, "GBPAUD": {}, "AUDCAD": {}, "AUDNZD": {},
	"USDJPY": {}, "EURJPY": {}, "GBPJPY": {}, "AUDJPY": {},
	"CHFJPY": {}, "CADJPY": {}, "NZDJPY": {},
	"XAUUSD": {}, "XAGUSD": {},
}

// Lookup resolves an instrument's decoding precision from the verified
// registry. An instrument the feed does not publish, or whose precision is
// unverified, is refused rather than assumed: a wrong digit count misprices
// every candle by a factor of ten or more.
func Lookup(symbol string) (Instrument, error) {
	canonical := instrument.Normalize(symbol)
	if _, ok := published[canonical]; !ok {
		return Instrument{}, fmt.Errorf("dukascopy: %q is not a published instrument", symbol)
	}
	spec, ok := instrument.Lookup(canonical)
	if !ok || spec.Unverified {
		return Instrument{}, fmt.Errorf("dukascopy: %q has no verified precision in "+
			"internal/instrument; verify it against the venue before using it", symbol)
	}
	return Instrument{Symbol: spec.Symbol, Digits: spec.Digits, PriceScale: spec.PriceScale}, nil
}

// Symbols lists every published instrument, sorted for stable output.
func Symbols() []string {
	result := make([]string, 0, len(published))
	for symbol := range published {
		result = append(result, symbol)
	}
	sort.Strings(result)
	return result
}

// Timeframe selects which published aggregation to read. Each maps to a
// different file granularity, which determines the request count: a year of
// daily candles is one request, a year of hourly candles is twelve, and a
// year of minute candles is about 365.
type Timeframe string

const (
	Day    Timeframe = "d1"
	Hour   Timeframe = "h1"
	Minute Timeframe = "m1"
)

func (t Timeframe) Duration() time.Duration {
	switch t {
	case Day:
		return 24 * time.Hour
	case Hour:
		return time.Hour
	case Minute:
		return time.Minute
	}
	return 0
}

func (t Timeframe) Valid() bool { return t.Duration() > 0 }

// Side selects which published series to read. Mid is derived by averaging
// bid and ask with truncation, so it stays an exact integer tick.
type Side string

const (
	Bid Side = "bid"
	Ask Side = "ask"
	Mid Side = "mid"
)

func (s Side) Valid() bool { return s == Bid || s == Ask || s == Mid }

// Client fetches and decodes published candles. A cache directory is strongly
// recommended: the feed is slow (seconds per file) and rate-limits bursts.
type Client struct {
	HTTP     *http.Client
	CacheDir string
	// MinInterval paces consecutive network requests. The feed answers 429
	// when hit too quickly.
	MinInterval time.Duration
	// Attempts bounds retries per file. The feed sheds load with 503 on long
	// jobs, so this must be generous enough that one refused file does not
	// abort a multi-hundred-file fetch.
	Attempts int
	// RetryBase is the first backoff delay; each attempt doubles it.
	RetryBase time.Duration
	// Workers bounds concurrent in-flight requests. The feed answers a single
	// small file in tens of seconds, so fetching years of hourly data
	// sequentially is impractical; MinInterval still spaces request starts.
	Workers int

	// Now is injectable so the "which hours can exist" boundary is testable.
	Now func() time.Time

	pacer       sync.Mutex
	lastRequest time.Time
}

func NewClient(cacheDir string) *Client {
	return &Client{
		HTTP:        &http.Client{Timeout: 120 * time.Second},
		CacheDir:    cacheDir,
		MinInterval: 150 * time.Millisecond,
		Attempts:    7,
		RetryBase:   2 * time.Second,
		Workers:     6,
	}
}

// Candles returns every published candle whose open time lies in [from, to).
// Results are sorted, de-duplicated, and stripped of the feed's flat
// zero-volume filler bars, which mark closed-market periods rather than
// trading activity.
func (c *Client) Candles(symbol string, timeframe Timeframe, side Side,
	from, to time.Time) ([]domain.Candle, error) {

	instrument, err := Lookup(symbol)
	if err != nil {
		return nil, err
	}
	if !timeframe.Valid() {
		return nil, fmt.Errorf("dukascopy: invalid timeframe %q", timeframe)
	}
	if !side.Valid() {
		return nil, fmt.Errorf("dukascopy: invalid side %q", side)
	}
	from, to = from.UTC(), to.UTC()
	if !to.After(from) {
		return nil, errors.New("dukascopy: the time range must be non-empty")
	}

	sides := []Side{side}
	if side == Mid {
		sides = []Side{Bid, Ask}
	}

	// Build every (side, file) job up front, fetch them through a bounded
	// worker pool, then merge in job order. Concurrency never reaches the
	// result: the merge is keyed by open time, so output is identical
	// regardless of completion order.
	type job struct {
		sideIndex int
		slot      slot
	}
	var jobs []job
	for i := range sides {
		for _, one := range slots(timeframe, from, to) {
			jobs = append(jobs, job{sideIndex: i, slot: one})
		}
	}

	workers := c.Workers
	if workers <= 0 {
		workers = 1
	}
	if workers > len(jobs) {
		workers = len(jobs)
	}

	decoded := make([][]domain.Candle, len(jobs))
	failures := make([]error, len(jobs))
	queue := make(chan int)
	var waiting sync.WaitGroup
	for w := 0; w < workers; w++ {
		waiting.Add(1)
		go func() {
			defer waiting.Done()
			for index := range queue {
				current := jobs[index]
				payload, err := c.fetch(instrument.Symbol, timeframe,
					sides[current.sideIndex], current.slot)
				if err != nil {
					failures[index] = err
					continue
				}
				if len(payload) == 0 {
					continue
				}
				candles, err := decode(payload, instrument, timeframe, current.slot.epoch)
				if err != nil {
					failures[index] = fmt.Errorf("%s %s: %w",
						instrument.Symbol, current.slot.label, err)
					continue
				}
				decoded[index] = candles
			}
		}()
	}
	for index := range jobs {
		queue <- index
	}
	close(queue)
	waiting.Wait()
	for _, err := range failures {
		if err != nil {
			return nil, err
		}
	}

	// series[sideIndex][openTimeUnix] = candle
	series := make([]map[int64]domain.Candle, len(sides))
	for i := range series {
		series[i] = map[int64]domain.Candle{}
	}
	for index, candles := range decoded {
		for _, candle := range candles {
			if candle.OpenTime.Before(from) || !candle.OpenTime.Before(to) {
				continue
			}
			series[jobs[index].sideIndex][candle.OpenTime.Unix()] = candle
		}
	}

	var merged []domain.Candle
	if len(sides) == 1 {
		for _, candle := range series[0] {
			merged = append(merged, candle)
		}
	} else {
		for key, bid := range series[0] {
			ask, ok := series[1][key]
			if !ok {
				continue // one side missing: refuse to invent a mid
			}
			merged = append(merged, midCandle(bid, ask))
		}
	}
	sort.Slice(merged, func(a, b int) bool {
		return merged[a].OpenTime.Before(merged[b].OpenTime)
	})
	for i := range merged {
		if err := merged[i].Validate(); err != nil {
			return nil, fmt.Errorf("dukascopy: decoded candle %s is invalid: %w",
				merged[i].OpenTime, err)
		}
	}
	return merged, nil
}

func midCandle(bid, ask domain.Candle) domain.Candle {
	result := bid
	result.Open = (bid.Open + ask.Open) / 2
	result.High = (bid.High + ask.High) / 2
	result.Low = (bid.Low + ask.Low) / 2
	result.Close = (bid.Close + ask.Close) / 2
	result.TickCount = bid.TickCount + ask.TickCount
	// Averaging can invert geometry by a tick; clamp instead of emitting an
	// invalid candle.
	if result.High < result.Open {
		result.High = result.Open
	}
	if result.High < result.Close {
		result.High = result.Close
	}
	if result.Low > result.Open {
		result.Low = result.Open
	}
	if result.Low > result.Close {
		result.Low = result.Close
	}
	return result
}

// slot identifies one published file and the epoch its offsets count from.
type slot struct {
	path  string
	label string
	epoch time.Time
}

// slots enumerates the files covering [from, to) for a timeframe. Dukascopy
// numbers months from zero in its paths, which is the single most common
// source of off-by-one-month errors against this feed.
func slots(timeframe Timeframe, from, to time.Time) []slot {
	var result []slot
	switch timeframe {
	case Day:
		for year := from.Year(); year <= to.Year(); year++ {
			result = append(result, slot{
				path:  fmt.Sprintf("%04d", year),
				label: fmt.Sprintf("%04d", year),
				epoch: time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC),
			})
		}
	case Hour:
		cursor := time.Date(from.Year(), from.Month(), 1, 0, 0, 0, 0, time.UTC)
		for !cursor.After(to) {
			result = append(result, slot{
				path:  fmt.Sprintf("%04d/%02d", cursor.Year(), int(cursor.Month())-1),
				label: cursor.Format("2006-01"),
				epoch: cursor,
			})
			cursor = cursor.AddDate(0, 1, 0)
		}
	case Minute:
		cursor := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.UTC)
		for cursor.Before(to) {
			result = append(result, slot{
				path: fmt.Sprintf("%04d/%02d/%02d",
					cursor.Year(), int(cursor.Month())-1, cursor.Day()),
				label: cursor.Format("2006-01-02"),
				epoch: cursor,
			})
			cursor = cursor.AddDate(0, 0, 1)
		}
	}
	return result
}

func fileName(timeframe Timeframe, side Side) string {
	suffix := map[Timeframe]string{Day: "day_1", Hour: "hour_1", Minute: "min_1"}[timeframe]
	return fmt.Sprintf("%s_candles_%s.bi5", strings.ToUpper(string(side)), suffix)
}

// fetch returns the decompressed payload for one file, using the cache when
// available. A missing file is not an error: the feed omits periods it has no
// data for.
func (c *Client) fetch(symbol string, timeframe Timeframe, side Side, s slot) ([]byte, error) {
	return c.fetchFile(symbol, s.path, fileName(timeframe, side))
}

// fetchFile returns the decompressed payload for one published file, using
// the cache when available. A missing file is not an error: the feed omits
// periods it has no data for, including closed-market hours.
func (c *Client) fetchFile(symbol, path, name string) ([]byte, error) {
	relative := filepath.Join(symbol, path, name)
	url := fmt.Sprintf("%s/%s/%s/%s", baseURL, symbol, path, name)

	if c.CacheDir != "" {
		cached, err := os.ReadFile(filepath.Join(c.CacheDir, relative))
		if err == nil {
			if len(cached) == 0 {
				return nil, nil
			}
			return lzma.Decode(cached)
		}
	}

	compressed, err := c.get(url)
	if err != nil {
		return nil, err
	}
	if c.CacheDir != "" {
		target := filepath.Join(c.CacheDir, relative)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err == nil {
			_ = os.WriteFile(target, compressed, 0o644)
		}
	}
	if len(compressed) == 0 {
		return nil, nil
	}
	return lzma.Decode(compressed)
}

// backoff sleeps for an exponentially growing delay with deterministic
// jitter derived from the attempt, so concurrent workers retrying the same
// shed-load event do not resynchronise into another burst.
func (c *Client) backoff(attempt int) {
	base := c.RetryBase
	if base <= 0 {
		base = 2 * time.Second
	}
	if attempt > 5 {
		attempt = 5
	}
	delay := base << uint(attempt)
	jitter := time.Duration(attempt*379) * time.Millisecond
	time.Sleep(delay + jitter)
}

// await spaces request starts globally while still allowing Workers requests
// to be in flight at once.
func (c *Client) await() {
	c.pacer.Lock()
	defer c.pacer.Unlock()
	if wait := c.MinInterval - time.Since(c.lastRequest); wait > 0 {
		time.Sleep(wait)
	}
	c.lastRequest = time.Now()
}

func (c *Client) get(url string) ([]byte, error) {
	if c.HTTP == nil {
		c.HTTP = &http.Client{Timeout: 120 * time.Second}
	}
	attempts := c.Attempts
	if attempts <= 0 {
		attempts = 1
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		c.await()
		request, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("User-Agent", userAgent)
		request.Header.Set("Referer", "https://www.dukascopy.com/")

		response, err := c.HTTP.Do(request)
		if err != nil {
			lastErr = err
			c.backoff(attempt)
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, maxPayload))
		response.Body.Close()

		switch {
		case response.StatusCode == http.StatusNotFound:
			// No published data for this period.
			return nil, nil
		case response.StatusCode == http.StatusTooManyRequests ||
			response.StatusCode >= 500:
			// The feed throttles bursts with 429 and sheds load with 503.
			// Both are transient, and a long job must not die because one
			// file out of hundreds was refused once, so back off
			// exponentially rather than retrying on a flat short delay.
			lastErr = fmt.Errorf("dukascopy: %s returned %s", url, response.Status)
			c.backoff(attempt)
			continue
		case response.StatusCode != http.StatusOK:
			return nil, fmt.Errorf("dukascopy: %s returned %s", url, response.Status)
		}
		if readErr != nil {
			lastErr = readErr
			continue
		}
		return body, nil
	}
	return nil, fmt.Errorf("dukascopy: giving up on %s: %w", url, lastErr)
}

// decode converts a decompressed payload into candles. Prices stay integers
// throughout: the published points are already instrument ticks.
func decode(payload []byte, instrument Instrument, timeframe Timeframe,
	epoch time.Time) ([]domain.Candle, error) {

	if len(payload)%recordSize != 0 {
		return nil, fmt.Errorf("payload of %d bytes is not a whole number of %d-byte records",
			len(payload), recordSize)
	}
	duration := timeframe.Duration()
	candles := make([]domain.Candle, 0, len(payload)/recordSize)
	for offset := 0; offset < len(payload); offset += recordSize {
		record := payload[offset : offset+recordSize]
		seconds := int32(binary.BigEndian.Uint32(record[0:4]))
		open := int32(binary.BigEndian.Uint32(record[4:8]))
		closePrice := int32(binary.BigEndian.Uint32(record[8:12]))
		low := int32(binary.BigEndian.Uint32(record[12:16]))
		high := int32(binary.BigEndian.Uint32(record[16:20]))
		volume := math.Float32frombits(binary.BigEndian.Uint32(record[20:24]))

		// Filler bars mark a closed market: flat prices with no volume.
		// Treating them as candles would fabricate ranges and purges.
		if open <= 0 || high <= 0 || low <= 0 || closePrice <= 0 {
			continue
		}
		if volume <= 0 && open == closePrice && open == low && open == high {
			continue
		}
		if high < low || high < open || high < closePrice || low > open || low > closePrice {
			continue // reject incoherent geometry rather than clamping silently
		}

		ticks := uint64(volume)
		if ticks == 0 {
			ticks = 1
		}
		openTime := epoch.Add(time.Duration(seconds) * time.Second)
		candles = append(candles, domain.Candle{
			Source: Source, Symbol: instrument.Symbol, Timeframe: duration,
			OpenTime: openTime, CloseTime: openTime.Add(duration),
			Open:      domain.Price(open),
			High:      domain.Price(high),
			Low:       domain.Price(low),
			Close:     domain.Price(closePrice),
			TickCount: ticks,
		})
	}
	return candles, nil
}
