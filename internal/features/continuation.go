package features

import (
	"errors"
	"time"

	"tradex/internal/domain"
)

// ContinuationSpecVersion must be bumped whenever detection semantics change.
// Frozen manifests record it so a replay cannot silently change meaning.
const ContinuationSpecVersion = "continuation-0.1.0"

// Continuation detects a break-and-retest trend continuation.
//
// The reasoning, stated so it can be argued with rather than tuned:
//
//  1. Drift is not a trend. A sequence of same-direction closes can be
//     positioning rather than intent, and it reverses hardest on the next
//     catalyst. So displacement alone never qualifies — the move must also
//     TAKE OUT a prior structural extreme. That is an event, not a drift.
//  2. Chasing a broken level is the worst available entry. The trade is a
//     resting limit at the broken level itself, on the premise that a level
//     which failed as support is tested as resistance. If price never
//     returns, there is no trade; that is the correct outcome, not a miss.
//  3. A stop must be wider than the cost of being there. CRT v0.1 produced
//     setups with a 2.6 pip stop against a 0.7 pip spread, where cost was
//     27% of risk before slippage. Risk is therefore floored at a multiple
//     of the MEASURED spread, which Capital.com publishes per bar.
//  4. A target must be a place price has actually traded, not a multiple of
//     the stop. Reward/risk is then an output to filter on, never an input.
//
// Everything is integer ticks and basis points, so detection is
// bit-reproducible. Ambiguous geometry returns false rather than a guess.
type ContinuationKind string

const (
	// ContinuationBullish broke a prior high and is retested from above.
	ContinuationBullish ContinuationKind = "BULLISH_CONTINUATION"
	// ContinuationBearish broke a prior low and is retested from below.
	ContinuationBearish ContinuationKind = "BEARISH_CONTINUATION"
)

// ContinuationConfig controls the detector. Ratios are basis points
// (10000 bps = 100%).
type ContinuationConfig struct {
	// ImpulseBars is the window searched for the displacement leg.
	ImpulseBars int `json:"impulse_bars"`
	// PriorBars is the window immediately before the impulse whose extreme
	// must be broken. A break of a 2-bar high is noise; a break of this
	// window's extreme is structure.
	PriorBars int `json:"prior_bars"`
	// MinDirectionalBps is the share of impulse bars that must close in the
	// trend direction (6000 bps = 60% of bars).
	MinDirectionalBps int64 `json:"min_directional_bps"`
	// MinDisplacementBps is the impulse's net close-to-close displacement
	// as a share of its own mean bar range (15000 bps = 1.5 average bars).
	// This rejects a wide, choppy window that ends where it started.
	MinDisplacementBps int64 `json:"min_displacement_bps"`
	// MinBreakTicks is the minimum penetration beyond the prior extreme. A
	// touch is not a break.
	MinBreakTicks int64 `json:"min_break_ticks"`
	// RetestOffsetBps places the limit inside the broken level, as a share
	// of the impulse's mean bar range. 0 rests exactly on the level.
	RetestOffsetBps int64 `json:"retest_offset_bps"`
	// StopBufferTicks is added beyond the protective extreme.
	StopBufferTicks int64 `json:"stop_buffer_ticks"`
	// MinStopSpreadMultiple floors risk at this multiple of the measured
	// spread. Set to 0 only in tests; a live value below 3 is a cost trap.
	MinStopSpreadMultiple int64 `json:"min_stop_spread_multiple"`
	// TargetBars is the window whose extreme becomes the objective target.
	TargetBars int `json:"target_bars"`
	// MinRewardRiskBps rejects geometry that does not pay for its own stop
	// (20000 bps = 2.0R).
	MinRewardRiskBps int64 `json:"min_reward_risk_bps"`
	// MaxEntryDistanceBps rejects a retest level too far from the last
	// close to be reached, as a share of the impulse's mean bar range.
	MaxEntryDistanceBps int64 `json:"max_entry_distance_bps"`
	// EntryMode selects how the trade is entered:
	//   "retest"      a resting limit at the broken level (default)
	//   "break_close" a market entry at the confirming bar's close
	// The retest waits for a pullback and is subject to adverse selection:
	// the setups that fill are disproportionately the ones where the break
	// failed. break_close takes the momentum directly and fills every armed
	// setup, at the cost of a worse average price.
	EntryMode string `json:"entry_mode"`
	// StopLookbackBars sizes the break_close stop: it sits beyond the
	// extreme of this many trailing impulse bars, a momentum stop behind
	// the recent swing rather than behind the whole impulse.
	StopLookbackBars int `json:"stop_lookback_bars"`
	// TargetMoveBps sets the break_close target as a measured move: this
	// share of the impulse height projected beyond the entry (10000 = one
	// full impulse height). The target is a projection, not an
	// already-traded price, which is the one rule break_close relaxes
	// relative to the retest, and is why it is measured separately.
	TargetMoveBps int64 `json:"target_move_bps"`
}

// DefaultContinuationConfig is the starting point, not a tuned result. Every
// value is a stated prior; none has been fitted to a sample.
func DefaultContinuationConfig() ContinuationConfig {
	return ContinuationConfig{
		ImpulseBars:           12,
		PriorBars:             24,
		MinDirectionalBps:     6000,
		MinDisplacementBps:    15000,
		MinBreakTicks:         1,
		RetestOffsetBps:       0,
		StopBufferTicks:       2,
		MinStopSpreadMultiple: 4,
		TargetBars:            48,
		MinRewardRiskBps:      20000,
		MaxEntryDistanceBps:   20000,
		EntryMode:             "retest",
		StopLookbackBars:      4,
		TargetMoveBps:         10000,
	}
}

func (c ContinuationConfig) Validate() error {
	if c.ImpulseBars < 3 {
		return errors.New("continuation impulse window must be at least 3 bars")
	}
	if c.PriorBars < 3 {
		return errors.New("continuation prior window must be at least 3 bars")
	}
	if c.MinDirectionalBps <= 0 || c.MinDirectionalBps > 10000 {
		return errors.New("continuation directional share must be within (0, 10000] bps")
	}
	if c.MinDisplacementBps <= 0 {
		return errors.New("continuation displacement must be positive")
	}
	if c.MinBreakTicks <= 0 {
		return errors.New("continuation break must be at least one tick")
	}
	if c.RetestOffsetBps < 0 {
		return errors.New("continuation retest offset cannot be negative")
	}
	if c.StopBufferTicks < 0 {
		return errors.New("continuation stop buffer cannot be negative")
	}
	if c.MinStopSpreadMultiple < 0 {
		return errors.New("continuation stop spread multiple cannot be negative")
	}
	if c.TargetBars < 2 {
		return errors.New("continuation target window must be at least 2 bars")
	}
	if c.MinRewardRiskBps <= 0 {
		return errors.New("continuation reward/risk floor must be positive")
	}
	if c.MaxEntryDistanceBps <= 0 {
		return errors.New("continuation maximum entry distance must be positive")
	}
	switch c.EntryMode {
	case "", "retest":
	case "break_close":
		if c.StopLookbackBars < 1 || c.StopLookbackBars > c.ImpulseBars {
			return errors.New("break_close stop lookback must be within 1..impulse bars")
		}
		if c.TargetMoveBps <= 0 {
			return errors.New("break_close target move must be positive")
		}
	default:
		return errors.New("continuation entry mode must be retest or break_close")
	}
	return nil
}

func (c ContinuationConfig) breakClose() bool { return c.EntryMode == "break_close" }

// RequiredBars is how much trailing history the detector needs. A caller with
// fewer bars gets false, never a partial judgement.
func (c ContinuationConfig) RequiredBars() int {
	required := c.ImpulseBars + c.PriorBars
	if c.TargetBars > required {
		required = c.TargetBars
	}
	return required
}

// Continuation is a detected setup. It is evidence, not an executable trade:
// fill, stop and target adjudication belong to the research harness.
type Continuation struct {
	Kind   ContinuationKind `json:"kind"`
	Symbol string           `json:"symbol"`
	// BrokenLevel is the prior extreme the impulse took out.
	BrokenLevel domain.Price `json:"broken_level"`
	// Entry is the resting limit at the retest.
	Entry domain.Price `json:"entry"`
	// Stop sits beyond the impulse's protective extreme.
	Stop domain.Price `json:"stop"`
	// Target is the extreme of the target window: a price that traded.
	Target domain.Price `json:"target"`
	// DetectedAt is the confirming candle's CLOSE — the first instant the
	// setup is knowable. A backtest must reject fills at or before it.
	DetectedAt time.Time `json:"detected_at"`

	// Diagnostics, so a signal can be audited without re-running detection.
	BreakTicks       int64  `json:"break_ticks"`
	DisplacementTick int64  `json:"displacement_ticks"`
	DirectionalBars  int    `json:"directional_bars"`
	ImpulseBars      int    `json:"impulse_bars"`
	RiskTicks        int64  `json:"risk_ticks"`
	RewardTicks      int64  `json:"reward_ticks"`
	SpreadTicks      int64  `json:"spread_ticks"`
	RewardRiskBps    int64  `json:"reward_risk_bps"`
	SpecVersion      string `json:"spec_version"`
}

// Direction maps the setup onto the shared break direction vocabulary.
func (c Continuation) Direction() BreakDirection {
	if c.Kind == ContinuationBullish {
		return BreakBullish
	}
	return BreakBearish
}

// DetectContinuation evaluates the trailing window of CLOSED candles.
//
// window must be ordered oldest to newest and contain at least
// config.RequiredBars() bars. spreadTicks is the measured spread at the
// confirming bar; pass 0 only when the source cannot report one, which
// disables the cost floor and must be recorded as a weaker result.
func DetectContinuation(window []domain.Candle, spreadTicks int64,
	config ContinuationConfig) (Continuation, bool) {

	if err := config.Validate(); err != nil {
		return Continuation{}, false
	}
	if len(window) < config.RequiredBars() {
		return Continuation{}, false
	}
	if spreadTicks < 0 {
		return Continuation{}, false
	}

	size := len(window)
	last := window[size-1]
	impulse := window[size-config.ImpulseBars:]
	prior := window[size-config.ImpulseBars-config.PriorBars : size-config.ImpulseBars]
	target := window[size-config.TargetBars:]

	meanRange := meanCandleRange(impulse)
	if meanRange <= 0 {
		return Continuation{}, false
	}

	// Net displacement across the impulse, close to close.
	displacement := int64(last.Close) - int64(impulse[0].Open)
	minDisplacement := mulBps(meanRange, config.MinDisplacementBps)

	down, up := directionalBars(impulse)
	minDirectional := int(mulBps(int64(len(impulse)), config.MinDirectionalBps))

	// Resolve direction first and refuse anything that qualifies both ways
	// or neither. An instrument that looks bullish and bearish at once is
	// not a trend.
	bearish := -displacement >= minDisplacement && down >= minDirectional
	bullish := displacement >= minDisplacement && up >= minDirectional
	if bearish == bullish {
		return Continuation{}, false
	}

	setup := Continuation{
		Symbol:      last.Symbol,
		DetectedAt:  last.CloseTime,
		ImpulseBars: len(impulse),
		SpreadTicks: spreadTicks,
		SpecVersion: ContinuationSpecVersion,
	}

	offset := mulBps(meanRange, config.RetestOffsetBps)
	breakClose := config.breakClose()

	if bearish {
		setup.Kind = ContinuationBearish
		setup.DirectionalBars = down
		setup.DisplacementTick = -displacement

		priorLow := lowestLow(prior)
		impulseLow := lowestLow(impulse)
		impulseHigh := highestHigh(impulse)
		setup.BrokenLevel = priorLow
		setup.BreakTicks = int64(priorLow) - int64(impulseLow)
		if setup.BreakTicks < config.MinBreakTicks {
			return Continuation{}, false
		}
		if breakClose {
			// Enter the momentum at the confirming close; stop behind the
			// recent swing high; target a measured move below the entry.
			setup.Entry = last.Close
			lookback := impulse[len(impulse)-config.StopLookbackBars:]
			setup.Stop = highestHigh(lookback) + domain.Price(config.StopBufferTicks)
			move := mulBps(int64(impulseHigh)-int64(impulseLow), config.TargetMoveBps)
			setup.Target = last.Close - domain.Price(move)
		} else {
			// Sell the retest of broken support, slightly inside it. The
			// stop goes above the high of the leg that broke the level: if
			// price reclaims that high, the break failed and the premise is
			// gone.
			setup.Entry = priorLow - domain.Price(offset)
			setup.Stop = impulseHigh + domain.Price(config.StopBufferTicks)
			setup.Target = lowestLow(target)
		}
	} else {
		setup.Kind = ContinuationBullish
		setup.DirectionalBars = up
		setup.DisplacementTick = displacement

		priorHigh := highestHigh(prior)
		impulseHigh := highestHigh(impulse)
		impulseLow := lowestLow(impulse)
		setup.BrokenLevel = priorHigh
		setup.BreakTicks = int64(impulseHigh) - int64(priorHigh)
		if setup.BreakTicks < config.MinBreakTicks {
			return Continuation{}, false
		}
		if breakClose {
			setup.Entry = last.Close
			lookback := impulse[len(impulse)-config.StopLookbackBars:]
			setup.Stop = lowestLow(lookback) - domain.Price(config.StopBufferTicks)
			move := mulBps(int64(impulseHigh)-int64(impulseLow), config.TargetMoveBps)
			setup.Target = last.Close + domain.Price(move)
		} else {
			setup.Entry = priorHigh + domain.Price(offset)
			setup.Stop = lowestLow(impulse) - domain.Price(config.StopBufferTicks)
			setup.Target = highestHigh(target)
		}
	}

	// Geometry must be internally consistent: a short needs stop above
	// entry above target, and a long the mirror.
	if setup.Kind == ContinuationBearish {
		if !(setup.Stop > setup.Entry && setup.Entry > setup.Target) {
			return Continuation{}, false
		}
		// A resting limit must sit beyond the current price; a market
		// entry fills at it, so the proximity checks are retest-only.
		if !breakClose {
			if setup.Entry <= last.Close {
				return Continuation{}, false
			}
			if int64(setup.Entry-last.Close) > mulBps(meanRange, config.MaxEntryDistanceBps) {
				return Continuation{}, false
			}
		}
		setup.RiskTicks = int64(setup.Stop - setup.Entry)
		setup.RewardTicks = int64(setup.Entry - setup.Target)
	} else {
		if !(setup.Stop < setup.Entry && setup.Entry < setup.Target) {
			return Continuation{}, false
		}
		if !breakClose {
			if setup.Entry >= last.Close {
				return Continuation{}, false
			}
			if int64(last.Close-setup.Entry) > mulBps(meanRange, config.MaxEntryDistanceBps) {
				return Continuation{}, false
			}
		}
		setup.RiskTicks = int64(setup.Entry - setup.Stop)
		setup.RewardTicks = int64(setup.Target - setup.Entry)
	}

	if setup.RiskTicks <= 0 || setup.RewardTicks <= 0 {
		return Continuation{}, false
	}
	// Cost floor: the stop must be meaningfully wider than the spread.
	if spreadTicks > 0 && setup.RiskTicks < spreadTicks*config.MinStopSpreadMultiple {
		return Continuation{}, false
	}
	setup.RewardRiskBps = setup.RewardTicks * 10000 / setup.RiskTicks
	if setup.RewardRiskBps < config.MinRewardRiskBps {
		return Continuation{}, false
	}
	return setup, true
}

func meanCandleRange(window []domain.Candle) int64 {
	if len(window) == 0 {
		return 0
	}
	var total int64
	for _, candle := range window {
		total += int64(candle.High - candle.Low)
	}
	return total / int64(len(window))
}

// mulBps multiplies ticks by a basis-point ratio using integer truncation so
// the result is identical on every platform.
func mulBps(ticks, bps int64) int64 { return ticks * bps / 10000 }

// directionalBars counts closes below and above their own open. A doji
// counts for neither: it is not evidence of direction.
func directionalBars(window []domain.Candle) (down, up int) {
	for _, candle := range window {
		switch {
		case candle.Close < candle.Open:
			down++
		case candle.Close > candle.Open:
			up++
		}
	}
	return down, up
}

func lowestLow(window []domain.Candle) domain.Price {
	low := window[0].Low
	for _, candle := range window[1:] {
		if candle.Low < low {
			low = candle.Low
		}
	}
	return low
}

func highestHigh(window []domain.Candle) domain.Price {
	high := window[0].High
	for _, candle := range window[1:] {
		if candle.High > high {
			high = candle.High
		}
	}
	return high
}
