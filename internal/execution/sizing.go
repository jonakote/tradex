// Package execution's sizing.go: position sizing for forward-tested
// signals, derived from each instrument's OWN measured backtest stats
// (cmd/ibsv2 output) — never a flat number applied blindly across
// instruments or strategies, and never invented for something unmeasured.
//
// Half-Kelly (f*/2, where f* = p - (1-p)/b) is the standard conservative
// fractional-Kelly used when the win-rate/payoff estimate comes from a
// finite historical sample rather than a known distribution — full Kelly
// is already too aggressive for a true edge; on ~150-200 trades of
// estimation noise, using the full formula output would be reckless.
//
// Two additional safeguards on top of half-Kelly, because a Kelly formula
// applied literally to noisy backtest stats still routinely outputs
// double-digit percentages that no sane risk desk would run:
//   - a hard ceiling (MaxRiskPct) regardless of what the formula says.
//   - an UnstableDiscount applied to any instrument whose confirmation is
//     NOT robust across both backtest halves (see docs/MEANREVERSION_SPEC.md)
//     — a smaller bet on a shakier result, not the same bet.
package execution

// IBSStats are the measured win rate and average win/loss (in bps of
// price, net of cost) for one instrument's IBS v0.2 signal, from
// `cmd/ibsv2` on the full available sample. Robust records whether BOTH
// backtest halves independently confirmed positive edge — see
// docs/MEANREVERSION_SPEC.md for the exact numbers and dates measured.
type IBSStats struct {
	WinRate    float64 // 0..1
	AvgWinBps  float64
	AvgLossBps float64
	Robust     bool
}

// MeasuredIBSStats holds every instrument with a real `ibsv2` measurement
// on file. An instrument absent here has no suggested size — callers must
// not default to a guess.
//
// Source (6-year sample, split 2023-11-10, ibs_long_below=2000bps,
// ibs_exit_above=5000bps, max_hold=10, measured 2026-11-10):
//   - EURUSD: 149 trades, win 56.4%, both halves positive (+7.58/+5.60bp).
//   - US100:  205 trades, win 66.8%, both halves positive (+15.95/+13.37bp).
//   - XAGUSD: 152 trades, win 55.3%, full-sample edge positive but the
//     first half is STRONGLY negative (-35.72bp) on this exact window —
//     contradicts the earlier "confirmed" read from a different window/
//     split point (see MEANREVERSION_SPEC.md's "split-sensitivity" note).
//     Kept in the live forward test for more data, but sized down hard.
var MeasuredIBSStats = map[string]IBSStats{
	"EURUSD": {WinRate: 0.564, AvgWinBps: 35.13, AvgLossBps: 31.86, Robust: true},
	"US100":  {WinRate: 0.668, AvgWinBps: 79.28, AvgLossBps: 90.88, Robust: true},
	"XAGUSD": {WinRate: 0.553, AvgWinBps: 165.54, AvgLossBps: 170.12, Robust: false},
}

const (
	// MaxRiskPct is a hard ceiling applied after half-Kelly and the
	// stability discount, regardless of what the formula outputs — raw
	// Kelly off a ~150-trade sample routinely suggests double-digit
	// percentages that no real risk desk would run.
	MaxRiskPct = 2.0
	// UnstableDiscount shrinks the suggested size for an instrument whose
	// backtest halves disagree (Robust == false): still tradeable while
	// collecting more live data, but at a fraction of the size a fully
	// confirmed instrument gets.
	UnstableDiscount = 0.25
)

// HalfKellyRiskPct returns the suggested percent of equity to risk on one
// signal for symbol, and whether a measured size exists at all. 0, false
// means "no measured stats — do not invent a size."
func HalfKellyRiskPct(symbol string) (float64, bool) {
	stats, ok := MeasuredIBSStats[symbol]
	if !ok || stats.AvgLossBps <= 0 || stats.AvgWinBps <= 0 {
		return 0, false
	}
	b := stats.AvgWinBps / stats.AvgLossBps
	p := stats.WinRate
	full := p - (1-p)/b
	if full <= 0 {
		return 0, false // the formula itself says no edge; do not size a bet
	}
	pct := full / 2 * 100
	if !stats.Robust {
		pct *= UnstableDiscount
	}
	if pct > MaxRiskPct {
		pct = MaxRiskPct
	}
	return pct, true
}
