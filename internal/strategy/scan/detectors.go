package scan

import (
	"fmt"

	"tradex/internal/domain"
	"tradex/internal/features"
	"tradex/internal/instrument"
)

// This file adapts each pure detector to the Detector interface. The
// detectors live in internal/features; these wrappers translate their
// pattern structs into a strategy-agnostic Setup and carry the measured
// validation status.

// --- Continuation (break-and-retest) ---

type Continuation struct{ Config features.ContinuationConfig }

func (c Continuation) Name() string      { return "Continuation" }
func (c Continuation) Spec() string      { return features.ContinuationSpecVersion }
func (c Continuation) Validated() bool   { return false } // measured negative
func (c Continuation) RequiredBars() int { return c.Config.RequiredBars() }

func (c Continuation) Detect(_ instrument.Spec, window []domain.Candle, spread int64) (Setup, bool) {
	p, ok := features.DetectContinuation(window, spread, c.Config)
	if !ok {
		return Setup{}, false
	}
	side := "sell"
	if p.Kind == features.ContinuationBullish {
		side = "buy"
	}
	return Setup{
		Side: side, EntryType: "LIMIT", Entry: p.Entry, Stop: p.Stop, Target: p.Target,
		DetectedAt: p.DetectedAt, RiskTicks: p.RiskTicks, Reward: p.RewardTicks,
		Reason: fmt.Sprintf("broke the %d-bar extreme by %d ticks over %d bars (%d with trend); retest limit",
			p.ImpulseBars, p.BreakTicks, p.ImpulseBars, p.DirectionalBars),
		Diagnostics: map[string]any{"risk_ticks": p.RiskTicks, "reward_ticks": p.RewardTicks, "spread_ticks": spread},
	}, true
}

// --- ICT (fair value gap continuation) ---

type ICT struct{ Config features.ICTConfig }

func (c ICT) Name() string      { return "ICT" }
func (c ICT) Spec() string      { return features.ICTSpecVersion }
func (c ICT) Validated() bool   { return false }
func (c ICT) RequiredBars() int { return c.Config.RequiredBars() }

func (c ICT) Detect(_ instrument.Spec, window []domain.Candle, spread int64) (Setup, bool) {
	p, ok := features.DetectICT(window, spread, c.Config)
	if !ok {
		return Setup{}, false
	}
	side := "sell"
	if p.Kind == features.ICTBullish {
		side = "buy"
	}
	return Setup{
		Side: side, EntryType: "LIMIT", Entry: p.Entry, Stop: p.Stop, Target: p.Target,
		DetectedAt: p.DetectedAt, RiskTicks: p.RiskTicks, Reward: p.RewardTicks,
		Reason:      fmt.Sprintf("fair value gap of %d ticks after displacement broke structure; limit into the gap", p.GapTicks),
		Diagnostics: map[string]any{"gap_ticks": p.GapTicks, "risk_ticks": p.RiskTicks, "reward_ticks": p.RewardTicks, "spread_ticks": spread},
	}, true
}

// --- AMN (Adeel Trades sweep-and-reclaim) ---

type AMN struct{ Config features.AMNConfig }

func (c AMN) Name() string      { return "AMN" }
func (c AMN) Spec() string      { return features.AMNSpecVersion }
func (c AMN) Validated() bool   { return false }
func (c AMN) RequiredBars() int { return c.Config.RequiredBars() }

func (c AMN) Detect(_ instrument.Spec, window []domain.Candle, spread int64) (Setup, bool) {
	p, ok := features.DetectAMN(window, spread, c.Config)
	if !ok {
		return Setup{}, false
	}
	side := "sell"
	if p.Kind == features.AMNBullish {
		side = "buy"
	}
	return Setup{
		Side: side, EntryType: "LIMIT", Entry: p.Entry, Stop: p.Stop, Target: p.Target,
		DetectedAt: p.DetectedAt, RiskTicks: p.RiskTicks, Reward: p.RewardTicks,
		Reason:      fmt.Sprintf("trend-aligned sweep of %d ticks then reclaim; limit at the reclaimed level", p.SweepTicks),
		Diagnostics: map[string]any{"sweep_ticks": p.SweepTicks, "risk_ticks": p.RiskTicks, "reward_ticks": p.RewardTicks, "spread_ticks": spread},
	}, true
}

// --- StructureTrend (structure break + trend, migrated from the live
// monitor's original "Structure Break + Trend" ad hoc logic) ---

type StructureTrend struct{ Config features.StructureTrendConfig }

func (c StructureTrend) Name() string      { return "StructureTrend" }
func (c StructureTrend) Spec() string      { return features.StructureTrendSpecVersion }
func (c StructureTrend) Validated() bool   { return false } // unmeasured until stratlab says otherwise
func (c StructureTrend) RequiredBars() int { return c.Config.RequiredBars() }

func (c StructureTrend) Detect(_ instrument.Spec, window []domain.Candle, spread int64) (Setup, bool) {
	p, ok := features.DetectStructureTrend(window, spread, c.Config)
	if !ok {
		return Setup{}, false
	}
	side := "sell"
	if p.Kind == features.StructureTrendBullish {
		side = "buy"
	}
	return Setup{
		Side: side, EntryType: "MARKET", Entry: p.Entry, Stop: p.Stop, Target: p.Target,
		DetectedAt: p.DetectedAt, RiskTicks: p.RiskTicks, Reward: p.RewardTicks,
		Reason: fmt.Sprintf("%s break of %d aligned with %s trend, %d bars old; market entry",
			p.BreakDirection, p.BrokenSwing, p.Trend, p.BreakAgeBars),
		Diagnostics: map[string]any{
			"risk_ticks": p.RiskTicks, "reward_ticks": p.RewardTicks, "spread_ticks": spread,
			"trend": string(p.Trend), "break_direction": string(p.BreakDirection), "break_age_bars": p.BreakAgeBars,
		},
	}, true
}

// --- TriangleBreakout (consolidation triangle breakout, migrated from the
// live monitor's original "Triangle Breakout" ad hoc logic) ---

type TriangleBreakout struct {
	Config features.TriangleBreakoutConfig
}

func (c TriangleBreakout) Name() string      { return "TriangleBreakout" }
func (c TriangleBreakout) Spec() string      { return features.TriangleBreakoutSpecVersion }
func (c TriangleBreakout) Validated() bool   { return false } // unmeasured until stratlab says otherwise
func (c TriangleBreakout) RequiredBars() int { return c.Config.RequiredBars() }

func (c TriangleBreakout) Detect(_ instrument.Spec, window []domain.Candle, spread int64) (Setup, bool) {
	p, ok := features.DetectTriangleBreakout(window, spread, c.Config)
	if !ok {
		return Setup{}, false
	}
	side := "sell"
	if p.BreakoutDirection == features.BreakBullish {
		side = "buy"
	}
	return Setup{
		Side: side, EntryType: "MARKET", Entry: p.Entry, Stop: p.Stop, Target: p.Target,
		DetectedAt: p.DetectedAt, RiskTicks: p.RiskTicks, Reward: p.RewardTicks,
		Reason: fmt.Sprintf("confirmed %s close broke the projected %s boundary; market entry",
			p.Kind, p.BreakoutDirection),
		Diagnostics: map[string]any{
			"risk_ticks": p.RiskTicks, "reward_ticks": p.RewardTicks, "spread_ticks": spread,
			"triangle_kind": string(p.Kind), "breakout_direction": string(p.BreakoutDirection),
		},
	}, true
}

// --- IBS (internal bar strength mean reversion) ---
//
// v0.2: adds a trend filter (long-only above the TrendFilterBars SMA,
// matching the published Connors RSI-2 overlay this project's own
// MEANREVERSION_SPEC.md identified as required) and exit-on-strength (exit
// as soon as a later bar's own IBS crosses back toward the opposite
// extreme, instead of always waiting the full fixed hold). Measured
// positive, net of cost, in both time halves on XAGUSD (genuine holdout,
// never previously used for IBS) and EURUSD (previously measured "no
// effect" under the raw v0.1 signal); XAUUSD and ETHUSDT still disagree in
// sign between halves; BTCUSDT remains wrong-signed. See
// docs/MEANREVERSION_SPEC.md.
type IBS struct {
	HoldBars        int
	LongBelowBps    int64
	ShortAboveBps   int64
	TrendFilterBars int   // 0 disables the trend filter
	StrengthExitBps int64 // 0 disables exit-on-strength (always use fixed hold)
}

func (c IBS) Name() string    { return "IBS" }
func (c IBS) Spec() string    { return features.MeanReversionSpecVersion }
func (c IBS) Validated() bool { return false } // interface-level default only -- real per-symbol confirmation now lives in execution.IsValidated("IBS", symbol), which cmd/tradex/watch.go actually uses for signal tagging; this blunt false stays as the conservative fallback for any caller without a symbol in hand
func (c IBS) RequiredBars() int {
	if c.TrendFilterBars > 2 {
		return c.TrendFilterBars
	}
	return 2
}

func (c IBS) Detect(_ instrument.Spec, window []domain.Candle, _ int64) (Setup, bool) {
	if len(window) == 0 {
		return Setup{}, false
	}
	last := window[len(window)-1]
	ibs, ok := features.IBSBps(last)
	if !ok {
		return Setup{}, false
	}
	hold := c.HoldBars
	if hold < 1 {
		hold = 1
	}
	up, down := true, true // trend filter disabled (TrendFilterBars == 0) => both sides always pass
	if c.TrendFilterBars > 1 && len(window) >= c.TrendFilterBars {
		sma := smaCloseWindow(window[len(window)-c.TrendFilterBars:])
		up = int64(last.Close) > sma
		down = !up
	}
	switch {
	case c.LongBelowBps > 0 && ibs <= c.LongBelowBps && up:
		setup := Setup{Side: "buy", EntryType: "MARKET", Entry: last.Close, HoldBars: hold,
			DetectedAt:  last.CloseTime,
			Reason:      fmt.Sprintf("IBS %d bps (close near low), trend-filtered; mean-reversion long, exit in up to %d bars", ibs, hold),
			Diagnostics: map[string]any{"ibs_bps": ibs, "hold_bars": hold, "trend_filter_bars": c.TrendFilterBars}}
		if c.StrengthExitBps > 0 {
			setup.HoldExitRule = "ibs_strength_above"
			setup.HoldExitThresholdBps = c.StrengthExitBps
		}
		return setup, true
	case c.ShortAboveBps > 0 && ibs >= c.ShortAboveBps && down:
		setup := Setup{Side: "sell", EntryType: "MARKET", Entry: last.Close, HoldBars: hold,
			DetectedAt:  last.CloseTime,
			Reason:      fmt.Sprintf("IBS %d bps (close near high), trend-filtered; mean-reversion short, exit in up to %d bars", ibs, hold),
			Diagnostics: map[string]any{"ibs_bps": ibs, "hold_bars": hold, "trend_filter_bars": c.TrendFilterBars}}
		if c.StrengthExitBps > 0 {
			// Mirrored around IBS's own midpoint (5000): a long exits
			// when IBS rises ABOVE StrengthExitBps, so symmetrically a
			// short must exit when IBS falls BELOW (10000-StrengthExitBps),
			// not below StrengthExitBps itself. Every live "both sides"
			// config so far used StrengthExitBps=5000, where this
			// mirroring is a no-op (10000-5000=5000), which hid this bug:
			// using the UNMIRRORED value directly meant a short with any
			// StrengthExitBps far from 5000 (e.g. 9500, needed for the
			// intraday extreme-reversal variant) would trigger its
			// "exit below threshold" check almost immediately on the next
			// bar, since nearly every IBS reading is below 9500. Found
			// 2026-11-20 building the first config that needed a non-5000
			// threshold. See docs/STRATEGY_SUITE.md.
			setup.HoldExitRule = "ibs_strength_below"
			setup.HoldExitThresholdBps = 10000 - c.StrengthExitBps
		}
		return setup, true
	}
	return Setup{}, false
}

func smaCloseWindow(bars []domain.Candle) int64 {
	var total int64
	for _, b := range bars {
		total += int64(b.Close)
	}
	return total / int64(len(bars))
}

// RSI2 is Connors' 2-period RSI mean-reversion signal, structured identically
// to IBS (same trend filter, same exit-on-strength overlay, same hold cap) so
// the two are directly comparable and can be measured/deployed the same way.
// It is a DIFFERENT trigger from IBS (2-period RSI vs. close-position-in-
// range), not a restatement of it — measured signal-day overlap with IBS is
// only 21-39% on the instruments both confirm on, meaning most RSI2 trade
// days are genuinely new, not duplicates. See docs/MEANREVERSION_SPEC.md.
type RSI2 struct {
	HoldBars        int
	LongBelowBps    int64
	ShortAboveBps   int64
	TrendFilterBars int
	StrengthExitBps int64
}

func (c RSI2) Name() string    { return "RSI2" }
func (c RSI2) Spec() string    { return features.MeanReversionSpecVersion }
func (c RSI2) Validated() bool { return false } // interface-level default only -- real per-symbol confirmation now lives in execution.IsValidated("RSI2", symbol), which cmd/tradex/watch.go actually uses for signal tagging; this blunt false stays as the conservative fallback for any caller without a symbol in hand
func (c RSI2) RequiredBars() int {
	if c.TrendFilterBars > 3 {
		return c.TrendFilterBars
	}
	return 3
}

func (c RSI2) Detect(_ instrument.Spec, window []domain.Candle, _ int64) (Setup, bool) {
	if len(window) == 0 {
		return Setup{}, false
	}
	last := window[len(window)-1]
	rsi, ok := features.RSI2Bps(window)
	if !ok {
		return Setup{}, false
	}
	hold := c.HoldBars
	if hold < 1 {
		hold = 1
	}
	up, down := true, true
	if c.TrendFilterBars > 1 && len(window) >= c.TrendFilterBars {
		sma := smaCloseWindow(window[len(window)-c.TrendFilterBars:])
		up = int64(last.Close) > sma
		down = !up
	}
	switch {
	case c.LongBelowBps > 0 && rsi <= c.LongBelowBps && up:
		setup := Setup{Side: "buy", EntryType: "MARKET", Entry: last.Close, HoldBars: hold,
			DetectedAt:  last.CloseTime,
			Reason:      fmt.Sprintf("RSI(2) %d bps (oversold), trend-filtered; mean-reversion long, exit in up to %d bars", rsi, hold),
			Diagnostics: map[string]any{"rsi2_bps": rsi, "hold_bars": hold, "trend_filter_bars": c.TrendFilterBars}}
		if c.StrengthExitBps > 0 {
			setup.HoldExitRule = "rsi2_strength_above"
			setup.HoldExitThresholdBps = c.StrengthExitBps
		}
		return setup, true
	case c.ShortAboveBps > 0 && rsi >= c.ShortAboveBps && down:
		setup := Setup{Side: "sell", EntryType: "MARKET", Entry: last.Close, HoldBars: hold,
			DetectedAt:  last.CloseTime,
			Reason:      fmt.Sprintf("RSI(2) %d bps (overbought), trend-filtered; mean-reversion short, exit in up to %d bars", rsi, hold),
			Diagnostics: map[string]any{"rsi2_bps": rsi, "hold_bars": hold, "trend_filter_bars": c.TrendFilterBars}}
		if c.StrengthExitBps > 0 {
			// Mirrored around RSI2's own midpoint (5000), identical
			// reasoning to the IBS detector's fix above -- found
			// 2026-11-24 during a full security/bug audit, NOT yet
			// triggered live (every current RSI2 config disables
			// shorts: rsi2_short_above_bps: 0), but this is the exact
			// same latent bug that bit IBS the moment a config needed
			// a genuinely asymmetric threshold. Fixed proactively here
			// before anyone builds a both-sides RSI2 config and hits it.
			setup.HoldExitRule = "rsi2_strength_below"
			setup.HoldExitThresholdBps = 10000 - c.StrengthExitBps
		}
		return setup, true
	}
	return Setup{}, false
}

// Breakout is a trend-following Donchian-channel breakout: long when price
// closes above the highest high of the preceding EntryWindow bars, exit
// when it closes below the lowest low of the preceding ExitWindow bars
// (a trailing channel, recomputed every bar) or MaxHoldBars, whichever
// comes first. Deliberately the OPPOSITE structural bet from IBS/RSI2: low
// win rate, winners run far longer than losers. Long only -- short was
// never measured (see docs/STRATEGY_SUITE.md breadth round 7).
type Breakout struct {
	EntryWindow int
	ExitWindow  int
	MaxHoldBars int
}

func (c Breakout) Name() string { return "Breakout" }
func (c Breakout) Spec() string { return features.BreakoutSpecVersion }
func (c Breakout) Validated() bool {
	return false // interface-level default only -- real per-symbol confirmation lives in execution.IsValidated("Breakout", symbol)
}
func (c Breakout) RequiredBars() int {
	n := c.EntryWindow
	if c.ExitWindow > n {
		n = c.ExitWindow
	}
	return n + 1
}

func (c Breakout) Detect(_ instrument.Spec, window []domain.Candle, _ int64) (Setup, bool) {
	if !features.DonchianBreakoutLong(window, c.EntryWindow) {
		return Setup{}, false
	}
	last := window[len(window)-1]
	maxHold := c.MaxHoldBars
	if maxHold < 1 {
		maxHold = 90
	}
	return Setup{
		Side: "buy", EntryType: "MARKET", Entry: last.Close, HoldBars: maxHold,
		DetectedAt: last.CloseTime,
		Reason: fmt.Sprintf("close broke above the %d-bar high; trend-following long, trailing %d-bar channel exit (or %d-bar time cap)",
			c.EntryWindow, c.ExitWindow, maxHold),
		Diagnostics: map[string]any{"entry_window": c.EntryWindow, "exit_window": c.ExitWindow, "max_hold_bars": maxHold},
		// HoldExitThresholdBps repurposed here to carry the exit window
		// size (not a basis-point threshold) -- Breakout's exit is a
		// channel recomputed from fresh history each scan, not a
		// single-bar oscillator read, so execute.go's exit-management
		// loop checks HoldExitRule == "donchian_low_break" as a special
		// case rather than routing through currentOscillatorBps.
		HoldExitRule:         "donchian_low_break",
		HoldExitThresholdBps: int64(c.ExitWindow),
	}, true
}
