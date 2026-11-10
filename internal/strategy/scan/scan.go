// Package scan is the multi-strategy layer: one Detector interface every
// strategy implements, so the live monitor and the measurement harness run
// an arbitrary set of strategies through the same code and every signal is
// tagged with the strategy that produced it.
//
// A strategy is a pure detector over a trailing window of closed candles. It
// returns a Setup — a direction, an entry, and either a stop/target geometry
// or a hold horizon — or nothing. It never places an order.
package scan

import (
	"time"

	"tradex/internal/domain"
	"tradex/internal/instrument"
)

// Setup is a strategy-agnostic signal. Prices are integer ticks.
type Setup struct {
	Side        string // "buy" or "sell"
	EntryType   string // "LIMIT" or "MARKET"
	Entry       domain.Price
	Stop        domain.Price // 0 when HoldBars drives the exit
	Target      domain.Price // 0 when HoldBars drives the exit
	HoldBars    int          // >0 => time exit; 0 => stop/target exit
	DetectedAt  time.Time
	RiskTicks   int64
	Reward      int64 // reward in ticks (stop/target) or 0
	Reason      string
	Diagnostics map[string]any

	// HoldExitRule optionally exits a HoldBars position EARLY, once a
	// later bar's own signal crosses HoldExitThresholdBps, instead of
	// always waiting the full HoldBars (fixed hold understates both win
	// rate and expectancy for mean-reversion — Connors' "exit on
	// strength" rule). Empty disables early exit; HoldBars remains the
	// cap either way. Recognized rules (checked generically here, kept as
	// plain strings/ints so Setup stays JSON-serializable for the audit
	// trail): "ibs_strength_above" (exit when the current bar's IBS >=
	// threshold), "ibs_strength_below" (exit when IBS <= threshold).
	HoldExitRule         string `json:"hold_exit_rule,omitempty"`
	HoldExitThresholdBps int64  `json:"hold_exit_threshold_bps,omitempty"`
}

// StopTarget reports whether the setup exits on a stop/target geometry.
func (s Setup) StopTarget() bool { return s.HoldBars == 0 }

// Detector is one strategy. Name/Spec label the signal; Validated reports
// whether it has a measured out-of-sample edge (false => UNVALIDATED label).
type Detector interface {
	Name() string
	Spec() string
	Validated() bool
	RequiredBars() int
	// Detect examines window (oldest to newest closed bars) for the latest
	// bar and returns a setup if one is present. spreadTicks is the measured
	// spread for the cost floor; 0 disables it.
	Detect(spec instrument.Spec, window []domain.Candle, spreadTicks int64) (Setup, bool)
}
