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

// MeasuredRSI2Stats: Connors' 2-period RSI mean-reversion, same v0.2
// overlay structure as IBS (200-day trend filter, exit-on-strength >=70,
// 10-day max hold), from `cmd/rsi2v2` on the full available 6-year sample,
// net of real cost AND real overnight swap (2026-11-11 rates), with proper
// drift-adjustment (forward return minus the unconditional baseline for
// the same hold length) -- the same standard IBS is held to.
//
// A first pass WITHOUT drift-adjustment (quick Python check) showed 6
// instruments confirming; re-measured properly in Go, only 3 survive:
//   - US100 (long only): 260 trades, win 71.9%, drift-adjusted edge both
//     halves positive (+24.42bp / +26.13bp).
//   - US500 (long only, a genuinely NEW instrument -- not in any live IBS
//     config): 272 trades, win 71.7%, both halves positive (+4.99bp thin
//     but positive / +28.49bp).
//   - PLATINUM (long only): 156 trades, win 71.2%, both halves positive
//     (+26.84bp / +97.23bp).
//
// EURUSD, NATGAS and BRENT do NOT survive once drift and real swap are
// both applied -- EURUSD flips sign between halves (+5.00bp / -4.35bp),
// NATGAS and BRENT were only "confirmed" in the quick, non-drift-adjusted
// first pass and are excluded here. See docs/MEANREVERSION_SPEC.md.
//
// Signal-day overlap with IBS on the shared instruments is only 21-39%
// (measured before this correction) -- most RSI2 trade days are genuinely
// new, not a restatement of IBS's own signal.
var MeasuredRSI2Stats = map[string]IBSStats{
	"US100":    {WinRate: 0.719, AvgWinBps: 102.27, AvgLossBps: 116.36, Robust: true, Side: "long"},
	"US500":    {WinRate: 0.717, AvgWinBps: 81.39, AvgLossBps: 90.83, Robust: true, Side: "long"},
	"PLATINUM": {WinRate: 0.712, AvgWinBps: 194.46, AvgLossBps: 171.56, Robust: true, Side: "long"},
	// BTCUSD: measured on Capital.com's OWN price feed (NOT Binance's
	// BTCUSDT -- those are different venues/price series, see
	// internal/instrument/capital.go). Real daily swap cost included
	// (0.0616438%/day long rate -> 6.16bps/day). 257 trades, both halves
	// positive (+25.04bp/+53.38bp). ETHUSD was ALSO tested the same way
	// and REJECTED: it looks fine on Binance but sign-flips on
	// Capital.com's actual feed (-1.99bp/+86.85bp) -- not deployed.
	"BTCUSD": {WinRate: 0.638, AvgWinBps: 223.33, AvgLossBps: 285.34, Robust: true, Side: "long"},
}

// MeasuredConfluenceStats: a SEPARATE, stronger signal from IBS and RSI2
// agreeing on the same symbol, same day. Measured by splitting every
// IBS-or-RSI2 signal day into three buckets (IBS only, RSI2 only, both) on
// each instrument where at least one of IBS/RSI2 independently confirms;
// confluence beat BOTH individual signals on every instrument tested, both
// halves positive:
//   - US100: 96 trades, win 68.8%, both halves positive (+26.35/+34.63bp) --
//     a BOOST on top of IBS-alone and RSI2-alone, which are both ALSO
//     individually confirmed here (see MeasuredIBSStats/MeasuredRSI2Stats).
//   - PLATINUM: 50 trades (thinner, ~25/half -- right at the floor, not
//     comfortably above it), win 66.0%, both halves positive
//     (+35.97/+132.19bp) -- also a BOOST.
//   - XAUUSD (gold): 89 trades, win 66.3%, both halves positive
//     (+27.68/+17.07bp) -- IBS ALONE does not confirm here (sign flip), so
//     XAUUSD is CONFLUENCE-ONLY: trade only when both fire, never on
//     either alone.
//   - GBPUSD: 74 trades (~37/half, at the floor), win 70.3%, both halves
//     positive (+8.28/+15.05bp) -- neither IBS nor RSI2 confirms alone on
//     GBPUSD; confluence rescues an instrument neither part could confirm
//     by itself. CONFLUENCE-ONLY, same reasoning as XAUUSD.
//
// USDJPY and BTCUSDT were also tested: both show a sign flip on the
// confluence bucket (USDJPY +16.94/-9.17bp, BTCUSDT -1.62/+53.39bp) and are
// deliberately excluded.
var MeasuredConfluenceStats = map[string]IBSStats{
	"US100":    {WinRate: 0.688, AvgWinBps: 76.14, AvgLossBps: 69.93, Robust: true, Side: "long"},
	"PLATINUM": {WinRate: 0.660, AvgWinBps: 171.56, AvgLossBps: 85.74, Robust: true, Side: "long"},
	"XAUUSD":   {WinRate: 0.663, AvgWinBps: 84.88, AvgLossBps: 100.72, Robust: true, Side: "long"},
	"GBPUSD":   {WinRate: 0.703, AvgWinBps: 27.94, AvgLossBps: 26.81, Robust: true, Side: "long"},
}

// ConfluenceOnlySymbols: IBS and RSI2 are NOT individually confirmed here --
// only trade when both fire on the same symbol, same day.
var ConfluenceOnlySymbols = map[string]bool{"XAUUSD": true, "GBPUSD": true}

// ConfluenceBoostSymbols: IBS and RSI2 are ALREADY independently confirmed
// here -- a single-signal day still trades normally at its own strategy's
// size; confluence just gets a stronger, separately-measured size on top,
// not a gate.
var ConfluenceBoostSymbols = map[string]bool{"US100": true, "PLATINUM": true}

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
// IBS signal for symbol, and whether a measured size exists at all. 0,
// false means "no measured stats — do not invent a size."
func HalfKellyRiskPct(symbol string) (float64, bool) {
	return riskPct(MeasuredIBSStats, symbol)
}

// RSI2RiskPct is the RSI2 equivalent of HalfKellyRiskPct, same formula and
// same safeguards, against MeasuredRSI2Stats instead.
func RSI2RiskPct(symbol string) (float64, bool) {
	return riskPct(MeasuredRSI2Stats, symbol)
}

// ConfluenceRiskPct is the confluence equivalent of HalfKellyRiskPct, same
// formula and safeguards, against MeasuredConfluenceStats instead — used
// both for ConfluenceOnlySymbols (the only size they ever trade at) and
// ConfluenceBoostSymbols (a stronger size than their own single-signal
// HalfKellyRiskPct/RSI2RiskPct, applied only when both fire together).
func ConfluenceRiskPct(symbol string) (float64, bool) {
	return riskPct(MeasuredConfluenceStats, symbol)
}

func riskPct(table map[string]IBSStats, symbol string) (float64, bool) {
	stats, ok := table[symbol]
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
