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
func (c IBS) Validated() bool { return false } // see docs/MEANREVERSION_SPEC.md: 2/5 instruments confirm so far, below the project's full promotion bar
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
			setup.HoldExitRule = "ibs_strength_below"
			setup.HoldExitThresholdBps = c.StrengthExitBps
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
