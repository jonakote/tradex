// Package execution's sizing.go: position sizing for forward-tested
// signals, derived from each instrument's OWN measured backtest stats
// (cmd/ibsv2 output) — never a flat number applied blindly across
// instruments or strategies, and never invented for something unmeasured.
//
// Half-Kelly (f*/2, where f* = p - (1-p)/b) is the standard conservative
// fractional-Kelly used when the win-rate/payoff estimate comes from a
// finite historical sample rather than a known distribution — full Kelly
// is already too aggressive for a true edge; on ~150-300 trades of
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
// price, net of transaction cost AND real overnight financing/swap) for
// one instrument's IBS v0.2 signal, from `cmd/ibsv2` on the full available
// sample. Robust records whether BOTH backtest halves independently
// confirmed positive edge — see docs/MEANREVERSION_SPEC.md for the exact
// numbers and dates measured.
//
// Side records which side(s) of the IBS signal these stats (and the
// corresponding live config) cover. `ibsv2` only ever measured the LONG
// side until 2026-11-10 — the live scan.IBS detector fires both sides, so
// an instrument's short-side signals were running un-measured. Fixed:
// every entry below now matches exactly what its live watch config emits
// (see docs/examples_watch_forward_daily_ibs_v2.json for "both" symbols,
// docs/examples_watch_forward_daily_ibs_longonly_v2.json for "long" ones).
type IBSStats struct {
	WinRate    float64 // 0..1
	AvgWinBps  float64
	AvgLossBps float64
	Robust     bool
	Side       string // "long" or "both" -- must match the live config
}

// MeasuredIBSStats holds every instrument with a real `ibsv2` measurement
// on file, net of real per-side overnight swap rates pulled from Capital.com
// on 2026-11-10 (GET /api/v1/markets/{epic}). An instrument absent here has
// no suggested size — callers must not default to a guess.
//
// Source (6-year sample, split 2023-11-10, ibs_long_below=2000bps,
// ibs_short_above=8000bps, ibs_exit=5000bps, max_hold=10):
//   - US100 (both sides, swap long +2.221bp/day short +0.002bp/day): 316
//     trades, win 66.1%, both halves positive (+31.09/+12.63bp).
//   - PLATINUM (both sides, swap long +1.363bp/day short -0.541bp/day):
//     275 trades, win 63.3%, both halves positive (+9.35/+50.10bp).
//   - EURUSD (LONG ONLY — short side measured negative/not robust, swap
//     long +1.584bp/day): 149 trades, win 53.7%, both halves positive
//     (+4.03/+2.35bp) even after real swap cost.
//   - NATGAS (LONG ONLY — short side measured not robust, swap long
//     +1.096bp/day): 185 trades, win 55.1%, both halves positive
//     (+7.54/+7.13bp) even after real swap cost.
//
// XAGUSD is deliberately absent: already flagged not-robust on a uniform
// 6-year retest, and with real swap cost added (long +3.799bp/day) the
// full-sample edge flips negative (-2.50bp) with halves still disagreeing
// (-43.76/+23.67bp). Removed from both live configs and from this table —
// three independent checks now say no, not one.
var MeasuredIBSStats = map[string]IBSStats{
	"US100":    {WinRate: 0.661, AvgWinBps: 107.87, AvgLossBps: 117.23, Robust: true, Side: "both"},
	"PLATINUM": {WinRate: 0.633, AvgWinBps: 142.83, AvgLossBps: 136.42, Robust: true, Side: "both"},
	"EURUSD":   {WinRate: 0.537, AvgWinBps: 34.30, AvgLossBps: 34.28, Robust: true, Side: "long"},
	"NATGAS":   {WinRate: 0.551, AvgWinBps: 296.51, AvgLossBps: 322.73, Robust: true, Side: "long"},
}

const (
	// MaxRiskPct is a hard ceiling applied after half-Kelly and the
	// stability discount, regardless of what the formula outputs — raw
	// Kelly off a ~150-300 trade sample routinely suggests double-digit
	// percentages that no real risk desk would run.
	MaxRiskPct = 2.0
	// UnstableDiscount shrinks the suggested size for an instrument whose
	// backtest halves disagree (Robust == false): still tradeable while
	// collecting more live data, but at a fraction of the size a fully
	// confirmed instrument gets. Nothing currently in MeasuredIBSStats
	// uses this — kept because the next breadth-search instrument that
	// only partially confirms will need it.
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
