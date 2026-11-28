// validated.go: a single source of truth for whether a given
// strategy+symbol pair has REAL, measured backtest confirmation — derived
// from the same Measured*Stats tables that already gate real order
// placement in cmd/tradex/execute.go, instead of a hardcoded constant that
// a detector's Validated() method would otherwise have to carry.
//
// This deliberately answers a DIFFERENT, narrower question than "is this
// safe to trust completely": it reports backtest confirmation only (both
// halves positive, net of real cost, >=30 trades/half, >=2 instruments
// per strategy) -- it does NOT require live forward-test evidence. See
// docs/MEANREVERSION_SPEC.md and docs/STRATEGY_SUITE.md for the project's
// standing position that backtest confirmation and proven-in-live-practice
// are two different bars; this function only speaks to the first one.
package execution

import "strings"

// IsValidated reports whether `strategy` (case-insensitive: "ibs", "rsi2",
// "confluence") has a real, both-halves-confirmed backtest result for
// `symbol`. False for any strategy/symbol pair absent from the relevant
// Measured*Stats table, or present but flagged non-robust -- absence or
// instability must never be read as "trust this anyway."
func IsValidated(strategy, symbol string) bool {
	switch strings.ToUpper(strategy) {
	case "IBS":
		s, ok := MeasuredIBSStats[symbol]
		return ok && s.Robust
	case "RSI2":
		s, ok := MeasuredRSI2Stats[symbol]
		return ok && s.Robust
	case "CONFLUENCE":
		s, ok := MeasuredConfluenceStats[symbol]
		return ok && s.Robust
	case "BREAKOUT":
		s, ok := MeasuredBreakoutStats[symbol]
		return ok && s.Robust
	case "MONDAYGAP":
		s, ok := MeasuredMondayGapStats[symbol]
		return ok && s.Robust
	case "RSI2OVERNIGHT":
		s, ok := MeasuredRSI2OvernightStats[symbol]
		return ok && s.Robust
	}
	return false
}
