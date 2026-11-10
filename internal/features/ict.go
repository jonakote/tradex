package features

import (
	"errors"
	"time"

	"tradex/internal/domain"
)

// ICTSpecVersion is stamped on emitted ICT setups.
const ICTSpecVersion = "ict-0.2.0"

// ICT detects the Inner Circle Trader fair-value-gap continuation: a
// displacement that breaks a prior structural extreme AND leaves a 3-candle
// imbalance (the FVG), entered on the retrace into that gap.
//
// Honesty note stamped in the code because it will outlive this message:
// public mechanical tests of ICT's named patterns find no edge — order
// blocks and liquidity sweeps flat-to-negative, the Silver Bullet
// significantly negative, FVG indistinguishable from noise (tick-stream 7.5y
// NQ; statoasis 648 backtests; an SSRN G10 FX study). The one survivor,
// Optimal Trade Entry, reduces to "buy a pullback in a trend" with its
// Fibonacci precision shown irrelevant. This detector is built because the
// operator asked for ICT in the multi-strategy engine; it emits as
// UNVALIDATED and must clear the same measured gate as everything else.
//
// v0.2 mechanism fix (measured on EURUSD/XAUUSD/USDJPY 1h 2y, see
// docs/STRATEGY_SUITE.md): the same bug found and fixed in AMN v0.2 — a
// fixed 2-tick stop buffer beyond the gap's far edge sits inside ordinary
// post-fill noise. 61-69% of v0.1's stop-losses filled and stopped on the
// SAME bar, and even genuine multi-bar losers reached >=1.5R favorable
// roughly half the time before fully reversing. The stop buffer is now
// scaled to the measured ATR of the structure window, floored at the old
// fixed MinStopBufferTicks. Entry, gap detection, structure-break filter and
// target are unchanged.
type ICTKind string

const (
	ICTBullish ICTKind = "BULLISH_FVG"
	ICTBearish ICTKind = "BEARISH_FVG"
)

// ICTConfig controls the detector. Ratios are basis points.
type ICTConfig struct {
	// ScanBars is how many recent bars are searched for the newest FVG.
	ScanBars int `json:"scan_bars"`
	// StructureBars is the prior window whose extreme the displacement must
	// break, so a gap that broke no liquidity is ignored. Also the ATR
	// window for the stop buffer.
	StructureBars int `json:"structure_bars"`
	// MinGapTicks rejects an imbalance too small to be a real gap.
	MinGapTicks int64 `json:"min_gap_ticks"`
	// MinStopBufferTicks floors the ATR-scaled stop buffer so a near-zero-
	// ATR regime cannot collapse the stop onto the gap's far edge.
	MinStopBufferTicks int64 `json:"min_stop_buffer_ticks"`
	// StopATRMultipleBps scales the structure window's ATR into the stop
	// buffer beyond the gap's far edge (15000 = 1.5x ATR).
	StopATRMultipleBps int64 `json:"stop_atr_multiple_bps"`
	// MinStopSpreadMultiple floors risk at this multiple of the spread.
	MinStopSpreadMultiple int64 `json:"min_stop_spread_multiple"`
	// TargetBars is the window whose extreme becomes the objective target
	// (the liquidity the move is drawing toward).
	TargetBars int `json:"target_bars"`
	// MinRewardRiskBps rejects geometry below this (15000 = 1.5R).
	MinRewardRiskBps int64 `json:"min_reward_risk_bps"`
}

func DefaultICTConfig() ICTConfig {
	return ICTConfig{
		ScanBars:              6,
		StructureBars:         20,
		MinGapTicks:           1,
		MinStopBufferTicks:    2,
		StopATRMultipleBps:    15000,
		MinStopSpreadMultiple: 4,
		TargetBars:            40,
		MinRewardRiskBps:      15000,
	}
}

func (c ICTConfig) Validate() error {
	if c.ScanBars < 3 {
		return errors.New("ict scan window must be at least 3 bars")
	}
	if c.StructureBars < 3 {
		return errors.New("ict structure window must be at least 3 bars")
	}
	if c.MinGapTicks < 1 {
		return errors.New("ict minimum gap must be at least one tick")
	}
	if c.MinStopBufferTicks < 0 {
		return errors.New("ict stop buffer floor cannot be negative")
	}
	if c.StopATRMultipleBps < 0 {
		return errors.New("ict stop ATR multiple cannot be negative")
	}
	if c.MinStopSpreadMultiple < 0 {
		return errors.New("ict stop spread multiple cannot be negative")
	}
	if c.TargetBars < 2 {
		return errors.New("ict target window must be at least 2 bars")
	}
	if c.MinRewardRiskBps <= 0 {
		return errors.New("ict reward/risk floor must be positive")
	}
	return nil
}

func (c ICTConfig) RequiredBars() int {
	required := c.ScanBars + c.StructureBars + 2
	if c.TargetBars > required {
		required = c.TargetBars
	}
	return required
}

// ICTPattern is a detected setup. Entry is a limit at the proximal edge of
// the gap; the trade is evidence, not an order.
type ICTPattern struct {
	Kind        ICTKind      `json:"kind"`
	Symbol      string       `json:"symbol"`
	GapLow      domain.Price `json:"gap_low"`
	GapHigh     domain.Price `json:"gap_high"`
	Entry       domain.Price `json:"entry"`
	Stop        domain.Price `json:"stop"`
	Target      domain.Price `json:"target"`
	DetectedAt  time.Time    `json:"detected_at"`
	GapTicks    int64        `json:"gap_ticks"`
	RiskTicks   int64        `json:"risk_ticks"`
	RewardTicks int64        `json:"reward_ticks"`
	SpecVersion string       `json:"spec_version"`
}

func (p ICTPattern) Direction() BreakDirection {
	if p.Kind == ICTBullish {
		return BreakBullish
	}
	return BreakBearish
}

// DetectICT scans the trailing window (oldest to newest closed bars) for the
// newest FVG whose displacement broke a prior structural extreme, and returns
// a limit setup on the retrace into the gap.
func DetectICT(window []domain.Candle, spreadTicks int64, config ICTConfig) (ICTPattern, bool) {
	if err := config.Validate(); err != nil {
		return ICTPattern{}, false
	}
	if len(window) < config.RequiredBars() || spreadTicks < 0 {
		return ICTPattern{}, false
	}
	size := len(window)
	last := window[size-1]
	target := window[size-config.TargetBars:]

	// Search newest-first for a 3-candle gap inside the scan window. The
	// structural reference is local to each candidate: the StructureBars
	// immediately before the gap, so a gap is only valid if its own
	// displacement broke the structure that preceded it.
	scanStart := size - config.ScanBars
	if scanStart < 2 {
		scanStart = 2
	}
	for c := size - 1; c >= scanStart; c-- {
		structureStart := c - 2 - config.StructureBars
		if structureStart < 0 {
			continue
		}
		prior := window[structureStart : c-2]
		priorHigh := highestHigh(prior)
		priorLow := lowestLow(prior)
		a := window[c-2]
		mid := window[c-1]
		z := window[c]
		stopBuffer := stopBufferTicksGeneric(prior, config.StopATRMultipleBps, config.MinStopBufferTicks)

		// Bullish FVG: candle a's high below candle z's low (gap up), the
		// displacement (mid) having closed above the prior structural high.
		if int64(z.Low)-int64(a.High) >= config.MinGapTicks && int64(mid.Close) > int64(priorHigh) {
			gapLow, gapHigh := a.High, z.Low
			entry := gapHigh // proximal edge: first price touched on a pullback
			if int64(entry) >= int64(last.Close) {
				continue // the retrace has not left room below price
			}
			stop := gapLow - domain.Price(stopBuffer)
			tgt := highestHigh(target)
			p := ICTPattern{
				Kind: ICTBullish, Symbol: last.Symbol, GapLow: gapLow, GapHigh: gapHigh,
				Entry: entry, Stop: stop, Target: tgt, DetectedAt: last.CloseTime,
				GapTicks: int64(gapHigh) - int64(gapLow), SpecVersion: ICTSpecVersion,
			}
			if finishICT(&p, spreadTicks, config) {
				return p, true
			}
		}

		// Bearish FVG: candle a's low above candle z's high (gap down), the
		// displacement having closed below the prior structural low.
		if int64(a.Low)-int64(z.High) >= config.MinGapTicks && int64(mid.Close) < int64(priorLow) {
			gapHigh, gapLow := a.Low, z.High
			entry := gapLow
			if int64(entry) <= int64(last.Close) {
				continue
			}
			stop := gapHigh + domain.Price(stopBuffer)
			tgt := lowestLow(target)
			p := ICTPattern{
				Kind: ICTBearish, Symbol: last.Symbol, GapLow: gapLow, GapHigh: gapHigh,
				Entry: entry, Stop: stop, Target: tgt, DetectedAt: last.CloseTime,
				GapTicks: int64(gapHigh) - int64(gapLow), SpecVersion: ICTSpecVersion,
			}
			if finishICT(&p, spreadTicks, config) {
				return p, true
			}
		}
	}
	return ICTPattern{}, false
}

// stopBufferTicksGeneric scales window's ATR by multipleBps, floored at
// minTicks. Shared math with AMN's stopBufferTicks; kept as a separate
// function here since ICTConfig and AMNConfig are distinct types.
func stopBufferTicksGeneric(window []domain.Candle, multipleBps, minTicks int64) int64 {
	atr := averageTrueRange(window)
	scaled := atr * multipleBps / 10000
	if scaled < minTicks {
		return minTicks
	}
	return scaled
}

func finishICT(p *ICTPattern, spreadTicks int64, config ICTConfig) bool {
	if p.Kind == ICTBullish {
		if !(p.Stop < p.Entry && p.Entry < p.Target) {
			return false
		}
		p.RiskTicks = int64(p.Entry - p.Stop)
		p.RewardTicks = int64(p.Target - p.Entry)
	} else {
		if !(p.Stop > p.Entry && p.Entry > p.Target) {
			return false
		}
		p.RiskTicks = int64(p.Stop - p.Entry)
		p.RewardTicks = int64(p.Entry - p.Target)
	}
	if p.RiskTicks <= 0 || p.RewardTicks <= 0 {
		return false
	}
	if spreadTicks > 0 && p.RiskTicks < spreadTicks*config.MinStopSpreadMultiple {
		return false
	}
	if p.RewardTicks*10000/p.RiskTicks < config.MinRewardRiskBps {
		return false
	}
	return true
}
