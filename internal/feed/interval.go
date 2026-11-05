package feed

import (
	"strings"
	"time"
)

// IntervalDuration converts a feed interval string to a duration.
//
// Vendors disagree on spelling: Binance uses "15m"/"1d", Twelve Data uses
// "15min"/"1day", and others mix both. A single table is used everywhere so
// a valid vendor interval can never silently resolve to zero — which
// previously caused every candle on a Binance "15m" stream to be dropped
// without any error, because the engine only knew "15min".
//
// It returns 0 only for a genuinely unknown interval.
func IntervalDuration(interval string) time.Duration {
	switch strings.TrimSpace(interval) {
	case "1s":
		return time.Second
	case "1m", "1min":
		return time.Minute
	case "3m", "3min":
		return 3 * time.Minute
	case "5m", "5min":
		return 5 * time.Minute
	case "15m", "15min":
		return 15 * time.Minute
	case "30m", "30min":
		return 30 * time.Minute
	case "45m", "45min":
		return 45 * time.Minute
	case "1h", "60m", "1hour":
		return time.Hour
	case "2h":
		return 2 * time.Hour
	case "4h":
		return 4 * time.Hour
	case "6h":
		return 6 * time.Hour
	case "8h":
		return 8 * time.Hour
	case "12h":
		return 12 * time.Hour
	case "1d", "1day":
		return 24 * time.Hour
	case "3d":
		return 72 * time.Hour
	case "1w", "1week":
		return 7 * 24 * time.Hour
	}
	return 0
}

// resolveBarTimes derives a candle's open and close instants from a vendor
// timestamp that denotes the bar's OPEN. The close is computed as open plus
// the interval rather than taken from the vendor, because some vendors
// report a close one millisecond early (Binance reports 23:59:59.999 for a
// bar that owns up to 24:00:00) and domain.Candle requires close to equal
// open plus timeframe exactly.
func resolveBarTimes(openTime time.Time, interval string) (time.Time, time.Time) {
	open := openTime.UTC()
	duration := IntervalDuration(interval)
	if duration <= 0 {
		return open, time.Time{}
	}
	return open, open.Add(duration)
}
