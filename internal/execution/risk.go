// risk.go: everything that stands between a detected signal and an actual
// order. Every check here is a hard gate, not advisory — if any one of
// them fails, the signal is skipped and logged as skipped, never
// downgraded-and-sent.
package execution

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"time"
)

// RiskConfig is the set of account-level guardrails. Defaults are
// deliberately conservative for a $1,000-class demo account; revisit
// before ever pointing this at a live account.
type RiskConfig struct {
	// MaxOpenPositions caps how many positions can be open at once,
	// across all instruments, regardless of how many symbols signal.
	MaxOpenPositions int
	// MaxTotalExposurePct caps the sum of (committed margin / equity)
	// across all open positions. Independent of the per-trade risk cap:
	// a run of simultaneous signals could each pass the per-trade check
	// and still over-commit the account in aggregate without this.
	MaxTotalExposurePct float64
	// SoftDailyLossCapPct: once today's REALIZED loss reaches this
	// fraction of starting-of-day equity, stop opening new positions for
	// the rest of the UTC day. Existing positions still manage normally.
	SoftDailyLossCapPct float64
	// HardDailyLossCapPct: once today's realized loss reaches this
	// fraction, force-close every open position immediately, in addition
	// to blocking new entries. This is the "don't wipe the account" floor.
	HardDailyLossCapPct float64
	// StatePath persists today's realized P&L so a restart doesn't reset
	// the daily counters to zero (the actual bug this would otherwise
	// reproduce: restarting resets in-memory state).
	StatePath string
	// KillSwitchPath: if this file exists, block ALL new entries
	// unconditionally. A human-operable manual stop, independent of any
	// of the above logic working correctly.
	KillSwitchPath string
}

// DefaultRiskConfig is deliberately conservative: even with 4 live
// instruments all signalling on the same day, 4 * 2% per-trade cap would
// be 8% max simultaneous risk — the hard cap sits just above that so a
// worst-case simultaneous-loss day halts everything, not a slow bleed.
func DefaultRiskConfig() RiskConfig {
	return RiskConfig{
		MaxOpenPositions:    4,
		MaxTotalExposurePct: 25, // committed margin, not risk — leveraged instruments need headroom
		SoftDailyLossCapPct: 5,
		HardDailyLossCapPct: 9,
		StatePath:           "data/risk_state.json",
		KillSwitchPath:      "data/KILL_SWITCH",
	}
}

type dailyState struct {
	Date        string  `json:"date"` // UTC yyyy-mm-dd
	RealizedPnL float64 `json:"realized_pnl"` // in account currency, negative = loss
}

// RiskManager tracks realized P&L for the current UTC day across restarts.
type RiskManager struct {
	cfg   RiskConfig
	state dailyState
}

func NewRiskManager(cfg RiskConfig) (*RiskManager, error) {
	rm := &RiskManager{cfg: cfg}
	rm.load()
	return rm, nil
}

func (rm *RiskManager) today() string { return time.Now().UTC().Format("2006-01-02") }

func (rm *RiskManager) load() {
	data, err := os.ReadFile(rm.cfg.StatePath)
	if err != nil {
		rm.state = dailyState{Date: rm.today()}
		return
	}
	var s dailyState
	if json.Unmarshal(data, &s) != nil || s.Date != rm.today() {
		rm.state = dailyState{Date: rm.today()} // new day or corrupt file: reset, never carry yesterday's number
		return
	}
	rm.state = s
}

func (rm *RiskManager) save() error {
	rm.state.Date = rm.today()
	data, err := json.MarshalIndent(rm.state, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(rm.cfg.StatePath); dir != "." {
		os.MkdirAll(dir, 0o755)
	}
	return os.WriteFile(rm.cfg.StatePath, data, 0o644)
}

// KillSwitchActive reports whether the manual stop file is present.
func (rm *RiskManager) KillSwitchActive() bool {
	_, err := os.Stat(rm.cfg.KillSwitchPath)
	return err == nil
}

// RecordClose updates today's realized P&L after a position closes.
func (rm *RiskManager) RecordClose(pnl float64) error {
	rm.load() // pick up today's figure fresh in case of a day rollover mid-run
	rm.state.RealizedPnL += pnl
	return rm.save()
}

// DailyPnL returns today's realized P&L so far.
func (rm *RiskManager) DailyPnL() float64 {
	rm.load()
	return rm.state.RealizedPnL
}

// ApproveEntry runs every pre-trade gate. equity is current account
// balance; openPositions is the current count; committedMarginPct is the
// current sum of (margin / equity) across open positions, BEFORE adding
// this trade's own margin.
func (rm *RiskManager) ApproveEntry(equity float64, openPositions int, committedMarginPct, thisTradeMarginPct float64) (ok bool, reason string) {
	if rm.KillSwitchActive() {
		return false, "kill switch active (" + rm.cfg.KillSwitchPath + " exists)"
	}
	rm.load()
	if equity > 0 && rm.state.RealizedPnL <= -equity*rm.cfg.HardDailyLossCapPct/100 {
		return false, "hard daily loss cap breached — should already be flattened"
	}
	if equity > 0 && rm.state.RealizedPnL <= -equity*rm.cfg.SoftDailyLossCapPct/100 {
		return false, "soft daily loss cap breached — no new entries until next UTC day"
	}
	if openPositions >= rm.cfg.MaxOpenPositions {
		return false, "max open positions reached"
	}
	if committedMarginPct+thisTradeMarginPct > rm.cfg.MaxTotalExposurePct {
		return false, "max total exposure would be exceeded"
	}
	return true, ""
}

// ShouldFlattenEverything reports whether the hard daily loss cap has been
// breached and every open position must be force-closed now.
func (rm *RiskManager) ShouldFlattenEverything(equity float64) bool {
	rm.load()
	return equity > 0 && rm.state.RealizedPnL <= -equity*rm.cfg.HardDailyLossCapPct/100
}

// SizeToStop converts a risk-percent-of-equity and a chosen protective
// stop distance (in price units) into a contract size, rounded down to
// minDealSize so the actual risk is never above what was asked.
//
// stopDistancePrice must be > 0 — this function will not size a trade
// with no protective stop; callers decide that distance (see
// ProtectiveStopBps below for the policy used by cmd/tradex execute).
func SizeToStop(equity, riskPct, stopDistancePrice, minDealSize float64) float64 {
	if stopDistancePrice <= 0 || equity <= 0 || riskPct <= 0 {
		return 0
	}
	riskAmount := equity * riskPct / 100
	size := riskAmount / stopDistancePrice
	if minDealSize > 0 {
		size = math.Floor(size/minDealSize) * minDealSize
	}
	return size
}

// ProtectiveStopBps returns the distance, in bps of entry price, for the
// HARD broker-side stop placed on every live order. This is NOT the
// backtested exit rule (IBS exits on a time/strength condition with no
// price stop at all, by design — see docs/MEANREVERSION_SPEC.md) — it is
// a safety floor added specifically because real money needs one even
// though the measured strategy doesn't use one. Set generously (3x the
// instrument's measured average loss) so it essentially never fires under
// normal conditions and only protects against a genuine tail event (gap,
// feed outage, flash move) the backtest never modeled either.
func ProtectiveStopBps(avgLossBps float64) float64 {
	if avgLossBps <= 0 {
		return 0
	}
	return avgLossBps * 3
}
