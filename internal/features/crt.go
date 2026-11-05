package features

import (
	"errors"
	"time"

	"tradex/internal/domain"
)

// CandleRangeSpecVersion must be bumped whenever detection semantics change.
// Frozen manifests record it so a replay cannot silently change meaning.
const CandleRangeSpecVersion = "crt-0.1.0"

// CandleRangeKind is the direction implied by a completed purge. A detected
// range is evidence only; it is not an executable trade by itself.
type CandleRangeKind string

const (
	// CandleRangeBullish purged the range low and closed back inside.
	CandleRangeBullish CandleRangeKind = "BULLISH_CANDLE_RANGE"
	// CandleRangeBearish purged the range high and closed back inside.
	CandleRangeBearish CandleRangeKind = "BEARISH_CANDLE_RANGE"
)

// CandleRangeConfig controls the deterministic Candle Range Theory detector.
// Prices are domain ticks; ratios are basis points of the range candle's
// height (10000 bps = 100%). All arithmetic is integer, so detection is
// bit-reproducible across platforms.
type CandleRangeConfig struct {
	// MinRangeTicks rejects a range candle too small to hold liquidity.
	MinRangeTicks int64 `json:"min_range_ticks"`
	// MinPurgeTicks is the minimum penetration beyond the range extreme. A
	// touch is not a purge.
	MinPurgeTicks int64 `json:"min_purge_ticks"`
	// MaxPurgeBps rejects a purge so deep it is a displacement, not a sweep.
	MaxPurgeBps int64 `json:"max_purge_bps"`
	// MinReclaimBps requires the purge candle to close back inside the range
	// by at least this fraction of range height, measured from the swept
	// extreme. This is the "close back inside" rule made numeric.
	MinReclaimBps int64 `json:"min_reclaim_bps"`
	// EntryBps locates the limit entry inside the purge candle's own range
	// (5000 bps = its equilibrium / 50%).
	EntryBps int64 `json:"entry_bps"`
	// StopBufferTicks is added beyond the purge wick.
	StopBufferTicks int64 `json:"stop_buffer_ticks"`
	// MinRewardRiskBps rejects geometry whose objective target does not pay
	// for its objective stop (15000 bps = 1.5R).
	MinRewardRiskBps int64 `json:"min_reward_risk_bps"`
}

func DefaultCandleRangeConfig() CandleRangeConfig {
	return CandleRangeConfig{
		MinRangeTicks:    20,
		MinPurgeTicks:    1,
		MaxPurgeBps:      5000,
		MinReclaimBps:    2500,
		EntryBps:         5000,
		StopBufferTicks:  2,
		MinRewardRiskBps: 15000,
	}
}

func (c CandleRangeConfig) Validate() error {
	if c.MinRangeTicks <= 0 {
		return errors.New("candle range minimum height must be positive")
	}
	if c.MinPurgeTicks <= 0 {
		return errors.New("candle range minimum purge must be positive")
	}
	if c.MaxPurgeBps <= 0 || c.MaxPurgeBps > 10000 {
		return errors.New("candle range maximum purge must lie in (0, 10000] bps")
	}
	if c.MinReclaimBps < 0 || c.MinReclaimBps >= 10000 {
		return errors.New("candle range minimum reclaim must lie in [0, 10000) bps")
	}
	if c.EntryBps <= 0 || c.EntryBps >= 10000 {
		return errors.New("candle range entry must lie in (0, 10000) bps")
	}
	if c.StopBufferTicks < 0 {
		return errors.New("candle range stop buffer cannot be negative")
	}
	if c.MinRewardRiskBps <= 0 {
		return errors.New("candle range minimum reward/risk must be positive")
	}
	return nil
}

// CandleRange is a confirmed two-candle purge pattern. Every price it exposes
// is derived from the two completed candles only, so it carries no look-ahead.
// DetectedAt is the purge candle's close: the first instant the pattern is
// knowable.
type CandleRange struct {
	Kind            CandleRangeKind `json:"kind"`
	Symbol          string          `json:"symbol"`
	Timeframe       time.Duration   `json:"timeframe"`
	RangeHigh       domain.Price    `json:"range_high"`
	RangeLow        domain.Price    `json:"range_low"`
	SweptLevel      domain.Price    `json:"swept_level"`
	PurgeExtreme    domain.Price    `json:"purge_extreme"`
	EntryReference  domain.Price    `json:"entry_reference"`
	StopReference   domain.Price    `json:"stop_reference"`
	TargetReference domain.Price    `json:"target_reference"`
	RangeOpenTime   time.Time       `json:"range_open_time"`
	PurgeOpenTime   time.Time       `json:"purge_open_time"`
	DetectedAt      time.Time       `json:"detected_at"`
	RangeTicks      int64           `json:"range_ticks"`
	PurgeTicks      int64           `json:"purge_ticks"`
	RiskTicks       int64           `json:"risk_ticks"`
	RewardTicks     int64           `json:"reward_ticks"`
	RewardRiskBps   int64           `json:"reward_risk_bps"`
	SpecVersion     string          `json:"spec_version"`
}

// Direction maps the range onto the shared structural direction vocabulary.
func (r CandleRange) Direction() BreakDirection {
	if r.Kind == CandleRangeBullish {
		return BreakBullish
	}
	return BreakBearish
}

// DetectCandleRange evaluates an adjacent candle pair under Candle Range
// Theory: the first candle defines the range, the second must purge exactly
// one of its extremes and close back inside it. A pair that purges both sides
// is ambiguous and is rejected rather than classified by guesswork.
func DetectCandleRange(rangeCandle, purgeCandle domain.Candle, config CandleRangeConfig) (CandleRange, bool) {
	if config.Validate() != nil {
		return CandleRange{}, false
	}
	if rangeCandle.Validate() != nil || purgeCandle.Validate() != nil {
		return CandleRange{}, false
	}
	if rangeCandle.Source != purgeCandle.Source || rangeCandle.Symbol != purgeCandle.Symbol ||
		rangeCandle.Timeframe != purgeCandle.Timeframe {
		return CandleRange{}, false
	}
	// The purge candle must be the immediate successor; a gap in the stream
	// would silently change which liquidity was swept.
	if !purgeCandle.OpenTime.Equal(rangeCandle.CloseTime) {
		return CandleRange{}, false
	}

	rangeTicks := int64(rangeCandle.High - rangeCandle.Low)
	if rangeTicks < config.MinRangeTicks {
		return CandleRange{}, false
	}

	purgedLow := int64(rangeCandle.Low-purgeCandle.Low) >= config.MinPurgeTicks
	purgedHigh := int64(purgeCandle.High-rangeCandle.High) >= config.MinPurgeTicks
	if purgedLow == purgedHigh { // neither side, or both sides
		return CandleRange{}, false
	}

	maxPurge := mulBps(rangeTicks, config.MaxPurgeBps)
	reclaim := mulBps(rangeTicks, config.MinReclaimBps)
	purgeTicks := int64(purgeCandle.High - purgeCandle.Low)
	if purgeTicks <= 0 {
		return CandleRange{}, false
	}

	pattern := CandleRange{
		Symbol:        rangeCandle.Symbol,
		Timeframe:     rangeCandle.Timeframe,
		RangeHigh:     rangeCandle.High,
		RangeLow:      rangeCandle.Low,
		RangeOpenTime: rangeCandle.OpenTime.UTC(),
		PurgeOpenTime: purgeCandle.OpenTime.UTC(),
		DetectedAt:    purgeCandle.CloseTime.UTC(),
		RangeTicks:    rangeTicks,
		SpecVersion:   CandleRangeSpecVersion,
	}

	if purgedLow {
		penetration := int64(rangeCandle.Low - purgeCandle.Low)
		if penetration > maxPurge {
			return CandleRange{}, false
		}
		// Close back inside the range, far enough above the swept low, and
		// not beyond the opposite extreme.
		if purgeCandle.Close < rangeCandle.Low+domain.Price(reclaim) || purgeCandle.Close > rangeCandle.High {
			return CandleRange{}, false
		}
		pattern.Kind = CandleRangeBullish
		pattern.SweptLevel = rangeCandle.Low
		pattern.PurgeExtreme = purgeCandle.Low
		pattern.PurgeTicks = penetration
		pattern.EntryReference = purgeCandle.Low + domain.Price(mulBps(purgeTicks, config.EntryBps))
		pattern.StopReference = purgeCandle.Low - domain.Price(config.StopBufferTicks)
		pattern.TargetReference = rangeCandle.High
		pattern.RiskTicks = int64(pattern.EntryReference - pattern.StopReference)
		pattern.RewardTicks = int64(pattern.TargetReference - pattern.EntryReference)
	} else {
		penetration := int64(purgeCandle.High - rangeCandle.High)
		if penetration > maxPurge {
			return CandleRange{}, false
		}
		if purgeCandle.Close > rangeCandle.High-domain.Price(reclaim) || purgeCandle.Close < rangeCandle.Low {
			return CandleRange{}, false
		}
		pattern.Kind = CandleRangeBearish
		pattern.SweptLevel = rangeCandle.High
		pattern.PurgeExtreme = purgeCandle.High
		pattern.PurgeTicks = penetration
		pattern.EntryReference = purgeCandle.High - domain.Price(mulBps(purgeTicks, config.EntryBps))
		pattern.StopReference = purgeCandle.High + domain.Price(config.StopBufferTicks)
		pattern.TargetReference = rangeCandle.Low
		pattern.RiskTicks = int64(pattern.StopReference - pattern.EntryReference)
		pattern.RewardTicks = int64(pattern.EntryReference - pattern.TargetReference)
	}

	if pattern.StopReference <= 0 || pattern.TargetReference <= 0 || pattern.EntryReference <= 0 {
		return CandleRange{}, false
	}
	if pattern.RiskTicks <= 0 || pattern.RewardTicks <= 0 {
		return CandleRange{}, false
	}
	pattern.RewardRiskBps = pattern.RewardTicks * 10000 / pattern.RiskTicks
	if pattern.RewardRiskBps < config.MinRewardRiskBps {
		return CandleRange{}, false
	}
	return pattern, true
}

// mulBps multiplies ticks by a basis-point ratio using integer truncation so
// the result is identical on every platform.
func mulBps(ticks, bps int64) int64 { return ticks * bps / 10000 }
