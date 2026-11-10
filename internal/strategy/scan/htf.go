package scan

import (
	"sort"
	"time"

	"tradex/internal/domain"
	"tradex/internal/features"
)

// HTFTrendSeries is the higher-timeframe bias known at each point in time,
// precomputed once from a separate (coarser) candle series so a
// lower-timeframe backtest or live poll can look up "what was the HTF trend
// as of this LTF bar's close" without any lookahead: TrendAt only ever
// returns a trend computed from HTF bars that had already closed.
type HTFTrendSeries struct {
	closeTime []time.Time
	trend     []features.Trend
}

// BuildHTFTrendSeries runs features.DetectHTFTrend over every trailing
// window of htf ending at each bar, oldest first, and records each bar's
// CloseTime alongside the resulting trend.
func BuildHTFTrendSeries(htf []domain.Candle, cfg features.HTFTrendConfig) HTFTrendSeries {
	required := cfg.RequiredBars()
	var series HTFTrendSeries
	for i := required; i <= len(htf); i++ {
		trend, ok := features.DetectHTFTrend(htf[i-required:i], cfg)
		if !ok {
			continue
		}
		series.closeTime = append(series.closeTime, htf[i-1].CloseTime)
		series.trend = append(series.trend, trend)
	}
	return series
}

// TrendAt returns the trend as of time t: the trend recorded at the latest
// HTF bar whose CloseTime is <= t. Returns (NEUTRAL, false) when no HTF bar
// had closed yet by t — callers must treat that as "unknown", not NEUTRAL.
func (s HTFTrendSeries) TrendAt(t time.Time) (features.Trend, bool) {
	idx := sort.Search(len(s.closeTime), func(i int) bool { return s.closeTime[i].After(t) })
	if idx == 0 {
		return features.TrendNeutral, false
	}
	return s.trend[idx-1], true
}

// HTFTrendAdmit builds a Stream/Backtest admission callback (see
// Stream.Advance's admit parameter) that only lets a setup through when its
// side agrees with the higher-timeframe trend known at the setup's
// DetectedAt. A setup against the HTF trend, or detected before any HTF
// trend was knowable, is rejected — never admitted on a coin flip.
func HTFTrendAdmit(series HTFTrendSeries) func(Setup) (bool, error) {
	return func(setup Setup) (bool, error) {
		trend, ok := series.TrendAt(setup.DetectedAt)
		if !ok {
			return false, nil
		}
		switch setup.Side {
		case "buy":
			return trend == features.TrendBullish, nil
		case "sell":
			return trend == features.TrendBearish, nil
		default:
			return false, nil
		}
	}
}
