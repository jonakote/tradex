package features

import (
	"errors"
	"time"

	"tradex/internal/domain"
)

// AMNSpecVersion is stamped on emitted AMN setups.
const AMNSpecVersion = "amn-0.2.0"

// AMN mechanizes the "Adeel Trades" AMN model: in a clear trend, wait for a
// liquidity sweep of a prior extreme that is then reclaimed, and enter on a
// retest of the reclaimed level toward the opposing liquidity, targeting at
// least 1.5R.
//
// Adeel's five steps are: clear trend, break of structure, a smaller break,
// a liquidity sweep, then entry at a demand/supply point of interest. The
// trend filter plus sweep-and-reclaim captures the testable core; the
// discretionary "point of interest" is modelled as the reclaimed swept level.
// Like CRT it is a sweep-reclaim idea, distinguished here by the mandatory
// trend filter and the prior-liquidity target. Emits as UNVALIDATED.
//
// v0.2 mechanism fix (measured against amn-0.1.0 on EURUSD/XAUUSD 1h 2y,
// see docs/STRATEGY_SUITE.md): 61-67% of v0.1's stop-losses filled and
// stopped on the SAME bar because the stop sat a fixed 2 ticks beyond the
// sweep extreme — inside ordinary post-reclaim noise, not a real invalidation
// distance. The stop buffer is now scaled to the measured ATR of the
// structure window (StopATRMultipleBps x ATR), floored at the old fixed
// MinStopBufferTicks so a near-zero-ATR regime cannot collapse the stop to
// nothing. This does not touch the target, entry or trend/sweep logic.
type AMNKind string

const (
	AMNBullish AMNKind = "BULLISH_AMN"
	AMNBearish AMNKind = "BEARISH_AMN"
)

// AMNConfig controls the detector.
type AMNConfig struct {
	// TrendBars is the SMA window defining the trend the setup must align to.
	TrendBars int `json:"trend_bars"`
	// SweepLookback is how many recent bars are searched for the sweep.
	SweepLookback int `json:"sweep_lookback"`
	// StructureBars is the window before a candidate sweep whose extreme it
	// must take out to count as a liquidity sweep. Also the ATR window for
	// the stop buffer.
	StructureBars int `json:"structure_bars"`
	// MinSweepTicks is the minimum penetration beyond the swept extreme.
	MinSweepTicks int64 `json:"min_sweep_ticks"`
	// MinStopBufferTicks floors the ATR-scaled stop buffer so a near-zero-ATR
	// regime cannot collapse the stop onto the sweep extreme.
	MinStopBufferTicks int64 `json:"min_stop_buffer_ticks"`
	// StopATRMultipleBps scales the structure window's ATR into the stop
	// buffer beyond the sweep extreme (15000 = 1.5x ATR).
	StopATRMultipleBps int64 `json:"stop_atr_multiple_bps"`
	// MinStopSpreadMultiple floors risk at this multiple of the spread.
	MinStopSpreadMultiple int64 `json:"min_stop_spread_multiple"`
	// TargetBars is the window whose extreme is the prior-liquidity target.
	TargetBars int `json:"target_bars"`
	// MinRewardRiskBps rejects geometry below this (15000 = 1.5R, Adeel's floor).
	MinRewardRiskBps int64 `json:"min_reward_risk_bps"`
}

func DefaultAMNConfig() AMNConfig {
	return AMNConfig{
		TrendBars:             50,
		SweepLookback:         5,
		StructureBars:         20,
		MinSweepTicks:         1,
		MinStopBufferTicks:    2,
		StopATRMultipleBps:    15000,
		MinStopSpreadMultiple: 4,
		TargetBars:            40,
		MinRewardRiskBps:      15000,
	}
}

func (c AMNConfig) Validate() error {
	if c.TrendBars < 2 {
		return errors.New("amn trend window must be at least 2 bars")
	}
	if c.SweepLookback < 1 {
		return errors.New("amn sweep lookback must be positive")
	}
	if c.StructureBars < 3 {
		return errors.New("amn structure window must be at least 3 bars")
	}
	if c.MinSweepTicks < 1 {
		return errors.New("amn minimum sweep must be at least one tick")
	}
	if c.MinStopBufferTicks < 0 {
		return errors.New("amn stop buffer floor cannot be negative")
	}
	if c.StopATRMultipleBps < 0 {
		return errors.New("amn stop ATR multiple cannot be negative")
	}
	if c.MinStopSpreadMultiple < 0 {
		return errors.New("amn stop spread multiple cannot be negative")
	}
	if c.TargetBars < 2 {
		return errors.New("amn target window must be at least 2 bars")
	}
	if c.MinRewardRiskBps <= 0 {
		return errors.New("amn reward/risk floor must be positive")
	}
	return nil
}

func (c AMNConfig) RequiredBars() int {
	required := c.TrendBars
	if n := c.SweepLookback + c.StructureBars; n > required {
		required = n
	}
	if c.TargetBars > required {
		required = c.TargetBars
	}
	return required + 1
}

// AMNPattern is a detected setup: a limit at the reclaimed swept level.
type AMNPattern struct {
	Kind         AMNKind      `json:"kind"`
	Symbol       string       `json:"symbol"`
	SweptLevel   domain.Price `json:"swept_level"`
	SweepExtreme domain.Price `json:"sweep_extreme"`
	Entry        domain.Price `json:"entry"`
	Stop         domain.Price `json:"stop"`
	Target       domain.Price `json:"target"`
	DetectedAt   time.Time    `json:"detected_at"`
	SweepTicks   int64        `json:"sweep_ticks"`
	RiskTicks    int64        `json:"risk_ticks"`
	RewardTicks  int64        `json:"reward_ticks"`
	SpecVersion  string       `json:"spec_version"`
}

func (p AMNPattern) Direction() BreakDirection {
	if p.Kind == AMNBullish {
		return BreakBullish
	}
	return BreakBearish
}

// DetectAMN scans the trailing window for a trend-aligned sweep-and-reclaim.
func DetectAMN(window []domain.Candle, spreadTicks int64, config AMNConfig) (AMNPattern, bool) {
	if err := config.Validate(); err != nil {
		return AMNPattern{}, false
	}
	if len(window) < config.RequiredBars() || spreadTicks < 0 {
		return AMNPattern{}, false
	}
	size := len(window)
	last := window[size-1]
	sma := smaCloseCandles(window[size-config.TrendBars:])
	target := window[size-config.TargetBars:]
	up := int64(last.Close) > sma

	// Search the recent sweep window newest-first for a bar that took out the
	// extreme of the structure window immediately before it.
	for s := size - 1; s >= size-config.SweepLookback && s-config.StructureBars >= 0; s-- {
		structure := window[s-config.StructureBars : s]
		sweepBar := window[s]
		stopBuffer := stopBufferTicks(structure, config)

		if up {
			priorLow := lowestLow(structure)
			if int64(priorLow)-int64(sweepBar.Low) < config.MinSweepTicks {
				continue // did not sweep below the prior low
			}
			if int64(last.Close) <= int64(priorLow) {
				continue // not reclaimed above the swept level
			}
			entry := priorLow // limit: retest of the reclaimed level
			if int64(entry) >= int64(last.Close) {
				continue
			}
			p := AMNPattern{
				Kind: AMNBullish, Symbol: last.Symbol, SweptLevel: priorLow, SweepExtreme: sweepBar.Low,
				Entry: entry, Stop: sweepBar.Low - domain.Price(stopBuffer),
				Target: highestHigh(target), DetectedAt: last.CloseTime,
				SweepTicks: int64(priorLow) - int64(sweepBar.Low), SpecVersion: AMNSpecVersion,
			}
			if finishAMN(&p, spreadTicks, config) {
				return p, true
			}
		} else {
			priorHigh := highestHigh(structure)
			if int64(sweepBar.High)-int64(priorHigh) < config.MinSweepTicks {
				continue
			}
			if int64(last.Close) >= int64(priorHigh) {
				continue
			}
			entry := priorHigh
			if int64(entry) <= int64(last.Close) {
				continue
			}
			p := AMNPattern{
				Kind: AMNBearish, Symbol: last.Symbol, SweptLevel: priorHigh, SweepExtreme: sweepBar.High,
				Entry: entry, Stop: sweepBar.High + domain.Price(stopBuffer),
				Target: lowestLow(target), DetectedAt: last.CloseTime,
				SweepTicks: int64(sweepBar.High) - int64(priorHigh), SpecVersion: AMNSpecVersion,
			}
			if finishAMN(&p, spreadTicks, config) {
				return p, true
			}
		}
	}
	return AMNPattern{}, false
}

// stopBufferTicks scales the structure window's average true range by
// StopATRMultipleBps, floored at MinStopBufferTicks. A fixed small buffer
// (the v0.1 behavior) sits inside ordinary post-reclaim noise and caused the
// majority of v0.1's losses to fill and stop on the same bar; see the amn-0.2
// note above DetectAMN's package comment.
func stopBufferTicks(structure []domain.Candle, config AMNConfig) int64 {
	return stopBufferTicksGeneric(structure, config.StopATRMultipleBps, config.MinStopBufferTicks)
}

// averageTrueRange computes the mean true range (integer ticks) over window,
// using the prior bar's close where available (gap-aware) and falling back
// to the bar's own high-low range for the first bar in the window.
func averageTrueRange(window []domain.Candle) int64 {
	if len(window) == 0 {
		return 0
	}
	var total int64
	for i, b := range window {
		hl := int64(b.High) - int64(b.Low)
		tr := hl
		if i > 0 {
			prevClose := int64(window[i-1].Close)
			hc := int64(b.High) - prevClose
			if hc < 0 {
				hc = -hc
			}
			lc := int64(b.Low) - prevClose
			if lc < 0 {
				lc = -lc
			}
			if hc > tr {
				tr = hc
			}
			if lc > tr {
				tr = lc
			}
		}
		total += tr
	}
	return total / int64(len(window))
}

func finishAMN(p *AMNPattern, spreadTicks int64, config AMNConfig) bool {
	if p.Kind == AMNBullish {
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

func smaCloseCandles(bars []domain.Candle) int64 {
	if len(bars) == 0 {
		return 0
	}
	var total int64
	for _, b := range bars {
		total += int64(b.Close)
	}
	return total / int64(len(bars))
}
