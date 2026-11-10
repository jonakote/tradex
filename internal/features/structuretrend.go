package features

import (
	"errors"
	"time"

	"tradex/internal/domain"
)

// StructureTrendSpecVersion is stamped on emitted StructureTrend setups.
const StructureTrendSpecVersion = "structuretrend-0.2.0"

// StructureTrend mechanizes the live monitor's original "Structure Break +
// Trend" idea (internal/signal/engine.go's evaluateSignal), as a pure,
// stateless detector so it can be measured by cmd/stratlab before it is
// trusted. The window is recomputed fresh every call -- no cross-call state
// -- which matches the Continuation detector's pattern.
//
// The reasoning:
//
//  1. A structure break alone is noise; it must align with the prevailing
//     swing-high/swing-low trend (features.ClassifyTrend). A bullish break
//     in a bearish trend is a counter-trend wiggle, not a continuation.
//  2. A break goes stale. The live engine required the break to be known
//     within the last 2 bars; this detector keeps that rule but makes the
//     window configurable (MaxBreakAgeBars) instead of hardcoding it.
//  3. Entry is the last candle's close (a market fill, matching the live
//     engine). Stop sits beyond the 10-bar recent extreme (the same
//     findRecentLow/findRecentHigh lookback the live engine used), with a
//     tick buffer -- the live engine had no buffer at all, which this adds.
//  4. v0.2: the live engine's target was a stop-multiple (entry +/-
//     risk*RR), kept in v0.1 "for parity" with that legacy logic. Measured
//     diagnosis (docs/STRATEGY_SUITE.md) found this was the actual failure
//     mechanism, not a tight stop: mean favorable excursion before a stop
//     was only ~0.5R across EURUSD/XAUUSD/USDJPY while the stop-multiple
//     target sat a fixed 2.0R away — an unreachable, fabricated target, the
//     exact anti-pattern this project's own geometry rules reject elsewhere
//     ("a target must be a price that actually traded, never a multiple of
//     the stop"). The target is now the extreme of a trailing TargetBars
//     window, matching AMN/ICT. Entry, stop and trend/break logic unchanged.
//
// Ambiguous or missing data returns false. Never guess.
type StructureTrendKind string

const (
	StructureTrendBullish StructureTrendKind = "BULLISH_STRUCTURE_TREND"
	StructureTrendBearish StructureTrendKind = "BEARISH_STRUCTURE_TREND"
)

// StructureTrendConfig controls the detector.
type StructureTrendConfig struct {
	// SwingLeft/SwingRight size the pivot window recomputed fresh over the
	// whole input window (features.NewSwingDetector(left, right)).
	SwingLeft  int `json:"swing_left"`
	SwingRight int `json:"swing_right"`
	// MinRewardRiskBps rejects geometry that does not pay for its own stop
	// (20000 bps = 2.0R, the live engine's default MinRewardRisk).
	MinRewardRiskBps int64 `json:"min_reward_risk_bps"`
	// MaxBreakAgeBars rejects a structure break more than this many bars
	// old relative to the window's last candle. The live engine hardcoded
	// 2 bars; this makes that a configurable, stated prior.
	MaxBreakAgeBars int `json:"max_break_age_bars"`
	// StopBufferTicks is added beyond the 10-bar recent extreme. The live
	// engine had no buffer; this is a real addition, not a tuned result.
	StopBufferTicks int64 `json:"stop_buffer_ticks"`
	// MinStopSpreadMultiple floors risk at this multiple of the measured
	// spread, matching Continuation. The live engine never had this floor.
	MinStopSpreadMultiple int64 `json:"min_stop_spread_multiple"`
	// TargetBars is the trailing window whose extreme becomes the target
	// (v0.2: replaces the v0.1 stop-multiple target with a traded level).
	TargetBars int `json:"target_bars"`
}

// DefaultStructureTrendConfig mirrors the live engine's own defaults
// (EngineConfig.SwingLeft/SwingRight/MinRewardRisk and the hardcoded 2-bar
// freshness rule), plus the new stop buffer and spread floor carried over
// from Continuation. These are stated priors, set before any measurement.
func DefaultStructureTrendConfig() StructureTrendConfig {
	return StructureTrendConfig{
		SwingLeft:             3,
		SwingRight:            2,
		MinRewardRiskBps:      20000,
		MaxBreakAgeBars:       2,
		StopBufferTicks:       2,
		MinStopSpreadMultiple: 4,
		TargetBars:            40,
	}
}

func (c StructureTrendConfig) Validate() error {
	if c.SwingLeft <= 0 || c.SwingRight <= 0 {
		return errors.New("structuretrend swing left/right windows must be positive")
	}
	if c.MinRewardRiskBps <= 0 {
		return errors.New("structuretrend reward/risk floor must be positive")
	}
	if c.MaxBreakAgeBars < 0 {
		return errors.New("structuretrend max break age cannot be negative")
	}
	if c.StopBufferTicks < 0 {
		return errors.New("structuretrend stop buffer cannot be negative")
	}
	if c.MinStopSpreadMultiple < 0 {
		return errors.New("structuretrend stop spread multiple cannot be negative")
	}
	if c.TargetBars < 2 {
		return errors.New("structuretrend target window must be at least 2 bars")
	}
	return nil
}

// recentExtremeLookback is the "10-bar recent low/high" window ported from
// internal/signal/engine.go's findRecentLow/findRecentHigh.
const recentExtremeLookback = 10

// RequiredBars is how much trailing history the detector needs to recompute
// swings, structure and trend fresh and still have enough bars left over
// for the recent-extreme stop and a meaningful freshness check. This is a
// stated prior (not fitted to data): enough pivot windows to plausibly
// confirm several swing highs and lows for ClassifyTrend.
func (c StructureTrendConfig) RequiredBars() int {
	pivot := c.SwingLeft + c.SwingRight + 1
	required := pivot * 10
	if required < 60 {
		required = 60
	}
	if c.TargetBars > required {
		required = c.TargetBars
	}
	return required
}

// StructureTrend is a detected setup. It is evidence, not an executable
// trade: fill and exit adjudication belong to the research harness.
type StructureTrend struct {
	Kind       StructureTrendKind `json:"kind"`
	Symbol     string             `json:"symbol"`
	Entry      domain.Price       `json:"entry"`
	Stop       domain.Price       `json:"stop"`
	Target     domain.Price       `json:"target"`
	DetectedAt time.Time          `json:"detected_at"`

	// Diagnostics, so a signal can be audited without re-running detection.
	Trend          Trend          `json:"trend"`
	BreakDirection BreakDirection `json:"break_direction"`
	BrokenSwing    domain.Price   `json:"broken_swing"`
	BreakAgeBars   int            `json:"break_age_bars"`
	RiskTicks      int64          `json:"risk_ticks"`
	RewardTicks    int64          `json:"reward_ticks"`
	SpreadTicks    int64          `json:"spread_ticks"`
	RewardRiskBps  int64          `json:"reward_risk_bps"`
	SpecVersion    string         `json:"spec_version"`
}

func (s StructureTrend) Direction() BreakDirection {
	if s.Kind == StructureTrendBullish {
		return BreakBullish
	}
	return BreakBearish
}

// DetectStructureTrend evaluates the trailing window of CLOSED candles.
//
// window must be ordered oldest to newest and contain at least
// config.RequiredBars() bars. Swings, trend and structure breaks are
// recomputed fresh from only this window via a brand-new SwingDetector and
// StructureDetector -- no cross-call state. spreadTicks is the measured
// spread at the confirming bar; pass 0 only when the source cannot report
// one, which disables the cost floor and must be recorded as a weaker
// result.
func DetectStructureTrend(window []domain.Candle, spreadTicks int64,
	config StructureTrendConfig) (StructureTrend, bool) {

	if err := config.Validate(); err != nil {
		return StructureTrend{}, false
	}
	if len(window) < config.RequiredBars() {
		return StructureTrend{}, false
	}
	if spreadTicks < 0 {
		return StructureTrend{}, false
	}

	swingDet, err := NewSwingDetector(config.SwingLeft, config.SwingRight)
	if err != nil {
		return StructureTrend{}, false
	}
	structDet := &StructureDetector{}

	var allSwings []Swing
	var lastBullish, lastBearish *StructureBreak
	lastBullishIdx, lastBearishIdx := -1, -1

	for i, candle := range window {
		newSwings, err := swingDet.Push(candle)
		if err != nil {
			return StructureTrend{}, false
		}
		allSwings = append(allSwings, newSwings...)

		breaks := structDet.OnCandle(candle, newSwings)
		for j := range breaks {
			brk := breaks[j]
			if brk.Direction == BreakBullish {
				lastBullish = &brk
				lastBullishIdx = i
			} else {
				lastBearish = &brk
				lastBearishIdx = i
			}
		}
	}

	trend := ClassifyTrend(allSwings)

	var brk *StructureBreak
	var breakIdx int
	switch trend {
	case TrendBullish:
		if lastBullish == nil {
			return StructureTrend{}, false
		}
		brk, breakIdx = lastBullish, lastBullishIdx
	case TrendBearish:
		if lastBearish == nil {
			return StructureTrend{}, false
		}
		brk, breakIdx = lastBearish, lastBearishIdx
	default:
		return StructureTrend{}, false
	}

	breakAge := (len(window) - 1) - breakIdx
	if breakAge < 0 || breakAge > config.MaxBreakAgeBars {
		return StructureTrend{}, false
	}

	size := len(window)
	last := window[size-1]
	lookback := recentExtremeLookback
	if size < lookback {
		lookback = size
	}
	recent := window[size-lookback:]

	setup := StructureTrend{
		Symbol:         last.Symbol,
		DetectedAt:     last.CloseTime,
		Trend:          trend,
		BreakDirection: brk.Direction,
		BrokenSwing:    brk.BrokenSwing.Price,
		BreakAgeBars:   breakAge,
		SpreadTicks:    spreadTicks,
		SpecVersion:    StructureTrendSpecVersion,
	}

	entry := last.Close
	setup.Entry = entry

	targetWindowLen := config.TargetBars
	if targetWindowLen > size {
		targetWindowLen = size
	}
	targetWindow := window[size-targetWindowLen:]

	if trend == TrendBullish {
		setup.Kind = StructureTrendBullish
		stop := lowestLow(recent) - domain.Price(config.StopBufferTicks)
		if stop >= entry {
			return StructureTrend{}, false
		}
		risk := int64(entry - stop)
		target := highestHigh(targetWindow)
		if risk <= 0 {
			return StructureTrend{}, false
		}
		setup.Stop = stop
		setup.Target = target
		if !(setup.Stop < setup.Entry && setup.Entry < setup.Target) {
			return StructureTrend{}, false
		}
		setup.RiskTicks = risk
		setup.RewardTicks = int64(target - entry)
	} else {
		setup.Kind = StructureTrendBearish
		stop := highestHigh(recent) + domain.Price(config.StopBufferTicks)
		if stop <= entry {
			return StructureTrend{}, false
		}
		risk := int64(stop - entry)
		target := lowestLow(targetWindow)
		if risk <= 0 {
			return StructureTrend{}, false
		}
		setup.Stop = stop
		setup.Target = target
		if !(setup.Stop > setup.Entry && setup.Entry > setup.Target) {
			return StructureTrend{}, false
		}
		setup.RiskTicks = risk
		setup.RewardTicks = int64(entry - target)
	}

	if setup.RiskTicks <= 0 || setup.RewardTicks <= 0 {
		return StructureTrend{}, false
	}
	// A target or stop at or below zero is not a price that can ever
	// trade; reject rather than emit a geometrically "valid" but
	// physically impossible setup (seen on BTCUSDT's 2020 crash, where a
	// stop-multiple projection pushed the bearish target negative).
	if setup.Stop <= 0 || setup.Target <= 0 {
		return StructureTrend{}, false
	}
	// Cost floor: the stop must be meaningfully wider than the spread. This
	// is the real cost control the live engine never had.
	if spreadTicks > 0 && setup.RiskTicks < spreadTicks*config.MinStopSpreadMultiple {
		return StructureTrend{}, false
	}
	setup.RewardRiskBps = setup.RewardTicks * 10000 / setup.RiskTicks
	if setup.RewardRiskBps < config.MinRewardRiskBps {
		return StructureTrend{}, false
	}
	return setup, true
}
