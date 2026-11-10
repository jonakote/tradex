package features

import (
	"errors"
	"time"

	"tradex/internal/domain"
)

// TriangleBreakoutSpecVersion is stamped on emitted TriangleBreakout setups.
const TriangleBreakoutSpecVersion = "trianglebreakout-0.1.0"

// TriangleBreakout wraps the existing features.DetectTriangle geometry (see
// triangle.go) with the entry/stop/target construction the live monitor
// used (internal/signal/engine.go's evaluateTriangleSignal), as a pure,
// stateless detector so it can be measured by cmd/stratlab before it is
// trusted.
//
// Triangle geometry itself is NOT reimplemented here; this file only adds:
//
//  1. Fresh swing recomputation over the whole window (no cross-call
//     state), feeding DetectTriangle.
//  2. A "genuinely new breakout" check: the live engine signalled a
//     triangle once per pattern (triangleSignaled). Recomputed statelessly,
//     that becomes "the breakout condition must NOT already have been true
//     on the second-to-last candle" -- otherwise every bar after a breakout
//     would re-signal.
//  3. Entry/stop/target exactly as evaluateTriangleSignal: entry at the
//     confirming close, stop at the worse of the 10-bar recent extreme and
//     the OPPOSITE triangle boundary, target a stop-multiple.
//  4. A spread-floor-on-stop-width check (features.Continuation has this;
//     the live engine never did), which is the one real addition.
//
// Ambiguous or missing data returns false. Never guess.
type TriangleBreakoutConfig struct {
	// SwingLeft/SwingRight size the pivot window recomputed fresh over the
	// whole input window (features.NewSwingDetector(left, right)).
	SwingLeft  int `json:"swing_left"`
	SwingRight int `json:"swing_right"`
	// Triangle controls the underlying triangle geometry (triangle.go).
	Triangle TriangleConfig `json:"triangle"`
	// BreakoutBufferTicks is an additional close-through buffer in price
	// ticks, matching the live engine's TriangleBreakoutBufferTicks.
	BreakoutBufferTicks int64 `json:"breakout_buffer_ticks"`
	// MinRewardRiskBps rejects geometry that does not pay for its own stop
	// (20000 bps = 2.0R, the live engine's default MinRewardRisk).
	MinRewardRiskBps int64 `json:"min_reward_risk_bps"`
	// MinStopSpreadMultiple floors risk at this multiple of the measured
	// spread, matching Continuation. The live engine never had this floor.
	MinStopSpreadMultiple int64 `json:"min_stop_spread_multiple"`
}

// DefaultTriangleBreakoutConfig mirrors the live engine's own defaults
// (EngineConfig.SwingLeft/SwingRight/MinRewardRisk/Triangle/
// TriangleBreakoutBufferTicks), plus the new spread floor carried over from
// Continuation. These are stated priors, set before any measurement.
func DefaultTriangleBreakoutConfig() TriangleBreakoutConfig {
	return TriangleBreakoutConfig{
		SwingLeft:             3,
		SwingRight:            2,
		Triangle:              DefaultTriangleConfig(),
		BreakoutBufferTicks:   0,
		MinRewardRiskBps:      20000,
		MinStopSpreadMultiple: 4,
	}
}

func (c TriangleBreakoutConfig) Validate() error {
	if c.SwingLeft <= 0 || c.SwingRight <= 0 {
		return errors.New("trianglebreakout swing left/right windows must be positive")
	}
	if err := c.Triangle.Validate(); err != nil {
		return err
	}
	if c.BreakoutBufferTicks < 0 {
		return errors.New("trianglebreakout buffer cannot be negative")
	}
	if c.MinRewardRiskBps <= 0 {
		return errors.New("trianglebreakout reward/risk floor must be positive")
	}
	if c.MinStopSpreadMultiple < 0 {
		return errors.New("trianglebreakout stop spread multiple cannot be negative")
	}
	return nil
}

// RequiredBars is how much trailing history the detector needs to recompute
// swings and a triangle fresh and still have two trailing bars to test for
// a genuinely new breakout. A stated prior, not fitted to data: enough pivot
// windows to plausibly confirm MinTouches highs and lows on each boundary.
func (c TriangleBreakoutConfig) RequiredBars() int {
	pivot := c.SwingLeft + c.SwingRight + 1
	required := pivot * 12
	if required < 80 {
		required = 80
	}
	return required
}

// TriangleBreakout is a detected setup. It is evidence, not an executable
// trade: fill and exit adjudication belong to the research harness.
type TriangleBreakout struct {
	Kind       TriangleKind `json:"kind"`
	Symbol     string       `json:"symbol"`
	Entry      domain.Price `json:"entry"`
	Stop       domain.Price `json:"stop"`
	Target     domain.Price `json:"target"`
	DetectedAt time.Time    `json:"detected_at"`

	// Diagnostics, so a signal can be audited without re-running detection.
	BreakoutDirection BreakDirection `json:"breakout_direction"`
	RiskTicks         int64          `json:"risk_ticks"`
	RewardTicks       int64          `json:"reward_ticks"`
	SpreadTicks       int64          `json:"spread_ticks"`
	RewardRiskBps     int64          `json:"reward_risk_bps"`
	SpecVersion       string         `json:"spec_version"`
}

// DetectTriangleBreakout evaluates the trailing window of CLOSED candles.
//
// window must be ordered oldest to newest and contain at least
// config.RequiredBars() bars. Swings and the triangle pattern are
// recomputed fresh from only this window via a brand-new SwingDetector --
// no cross-call state. spreadTicks is the measured spread at the
// confirming bar; pass 0 only when the source cannot report one, which
// disables the cost floor and must be recorded as a weaker result.
func DetectTriangleBreakout(window []domain.Candle, spreadTicks int64,
	config TriangleBreakoutConfig) (TriangleBreakout, bool) {

	if err := config.Validate(); err != nil {
		return TriangleBreakout{}, false
	}
	if len(window) < config.RequiredBars() {
		return TriangleBreakout{}, false
	}
	if spreadTicks < 0 {
		return TriangleBreakout{}, false
	}

	swingDet, err := NewSwingDetector(config.SwingLeft, config.SwingRight)
	if err != nil {
		return TriangleBreakout{}, false
	}
	var swings []Swing
	for _, candle := range window {
		newSwings, err := swingDet.Push(candle)
		if err != nil {
			return TriangleBreakout{}, false
		}
		swings = append(swings, newSwings...)
	}

	pattern, ok := DetectTriangle(swings, config.Triangle)
	if !ok {
		return TriangleBreakout{}, false
	}

	size := len(window)
	last := window[size-1]
	lastIndex := size - 1
	buffer := domain.Price(config.BreakoutBufferTicks)

	direction, broke := pattern.Breakout(last.Close, lastIndex, buffer)
	if !broke {
		return TriangleBreakout{}, false
	}

	// Reject a stale breakout: the condition must not already have been
	// true on the second-to-last candle, otherwise every bar after a real
	// breakout would re-signal. This recomputes statelessly what the live
	// engine's triangleSignaled flag enforced with cross-call state.
	if lastIndex-1 > pattern.LastPivotIndex {
		prevCandle := window[size-2]
		if _, prevBroke := pattern.Breakout(prevCandle.Close, lastIndex-1, buffer); prevBroke {
			return TriangleBreakout{}, false
		}
	}

	entry := last.Close
	lookback := recentExtremeLookback
	if size < lookback {
		lookback = size
	}
	recent := window[size-lookback:]

	setup := TriangleBreakout{
		Symbol:            last.Symbol,
		Kind:              pattern.Kind,
		DetectedAt:        last.CloseTime,
		BreakoutDirection: direction,
		SpreadTicks:       spreadTicks,
		SpecVersion:       TriangleBreakoutSpecVersion,
		Entry:             entry,
	}

	switch direction {
	case BreakBullish:
		boundary := pattern.LowerBoundaryAt(lastIndex)
		stop := lowestLow(recent)
		if boundary < stop {
			stop = boundary
		}
		if stop >= entry {
			return TriangleBreakout{}, false
		}
		risk := int64(entry - stop)
		reward := mulBps(risk, config.MinRewardRiskBps)
		if risk <= 0 || reward <= 0 {
			return TriangleBreakout{}, false
		}
		setup.Stop = stop
		setup.Target = entry + domain.Price(reward)
		if !(setup.Stop < setup.Entry && setup.Entry < setup.Target) {
			return TriangleBreakout{}, false
		}
		setup.RiskTicks = risk
		setup.RewardTicks = reward
	case BreakBearish:
		boundary := pattern.UpperBoundaryAt(lastIndex)
		stop := highestHigh(recent)
		if boundary > stop {
			stop = boundary
		}
		if stop <= entry {
			return TriangleBreakout{}, false
		}
		risk := int64(stop - entry)
		reward := mulBps(risk, config.MinRewardRiskBps)
		if risk <= 0 || reward <= 0 {
			return TriangleBreakout{}, false
		}
		setup.Stop = stop
		setup.Target = entry - domain.Price(reward)
		if !(setup.Stop > setup.Entry && setup.Entry > setup.Target) {
			return TriangleBreakout{}, false
		}
		setup.RiskTicks = risk
		setup.RewardTicks = reward
	default:
		return TriangleBreakout{}, false
	}

	if setup.RiskTicks <= 0 || setup.RewardTicks <= 0 {
		return TriangleBreakout{}, false
	}
	// A target or stop at or below zero is not a price that can ever
	// trade; reject rather than emit a geometrically "valid" but
	// physically impossible setup.
	if setup.Stop <= 0 || setup.Target <= 0 {
		return TriangleBreakout{}, false
	}
	// Cost floor: the stop must be meaningfully wider than the spread. This
	// is the real cost control the live engine never had.
	if spreadTicks > 0 && setup.RiskTicks < spreadTicks*config.MinStopSpreadMultiple {
		return TriangleBreakout{}, false
	}
	setup.RewardRiskBps = setup.RewardTicks * 10000 / setup.RiskTicks
	if setup.RewardRiskBps < config.MinRewardRiskBps {
		return TriangleBreakout{}, false
	}
	return setup, true
}
