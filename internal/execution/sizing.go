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

import "fmt"

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
	// StopMultiplier overrides ProtectiveStopBps's default 3x-avg-loss
	// stop width when non-zero. Added 2026-11-20 for the intraday IBS
	// variant (docs/STRATEGY_SUITE.md): backtesting showed a tight
	// stop (near the default 3x ~= 30bp) clips the edge before a real
	// intraday reversal has time to play out -- confirmed robust only
	// from roughly 8x average loss upward. Every other entry leaves
	// this at zero (the old, unconditional 3x default).
	StopMultiplier float64
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
//
// StopMultiplier: all 31 entries across every map here that previously had
// no explicit value (defaulting to the old 3x-avg-loss width) were bulk-
// widened to 4x on 2026-11-26. The Nov 23 stop-width fix was only ever
// applied to MeasuredRSI2Stats; this map (and the confluence/breakout maps
// below) were silently missed, which is exactly why US30 (-$19.66) and
// JPM's IBS-side stop-outs kept happening after that fix shipped — they
// were trading on the still-unfixed 3x width the whole time. Caught via
// the user's "we have a problem" / live P&L review, not a backtest.
var MeasuredIBSStats = map[string]IBSStats{
	"US100":    {WinRate: 0.661, AvgWinBps: 107.87, AvgLossBps: 117.23, Robust: true, Side: "both", StopMultiplier: 4},
	"PLATINUM": {WinRate: 0.633, AvgWinBps: 142.83, AvgLossBps: 136.42, Robust: true, Side: "both", StopMultiplier: 4},
	// RETRACTED 2026-11-20 and reverted to the original daily entry.
	// The intraday 15-min IBS/EURUSD-GBPUSD variant that briefly lived
	// here was deployed on a methodology bug: the research backtest
	// allowed a new "trade" to start on every qualifying bar even
	// while a previous one was conceptually still open (counting
	// overlapping, non-sequential samples), which is NOT how the live
	// system actually trades (one open position per symbol, enforced
	// by trades.Trades[symbol]). Re-measured SEQUENTIALLY (the only
	// methodology that matches what the live system can actually
	// execute) at the exact same parameters: trade count dropped from
	// 1299/1508 to 662/735 and the result FLIPPED to not-robust
	// (EURUSD half1 -0.10bp, GBPUSD half1 -0.09bp). A full sequential
	// grid search (entry 100-500bps x exit 8500-9700bps x hold
	// 16-64 bars) found ZERO combinations that are both-halves-robust
	// on both symbols. Caught before any real demo trade was placed
	// on it (the live process was paused for an unrelated reason --
	// an unverified spread assumption -- just long enough for this
	// deeper bug to be found before it mattered). See
	// docs/STRATEGY_SUITE.md's retraction section. Generalizable
	// lesson: any backtest script for a strategy with a real one-
	// position-per-symbol constraint must enforce that constraint
	// sequentially, or its trade count and edge are both inflated by
	// counting opportunities the live system could never actually
	// take.
	"EURUSD": {WinRate: 0.537, AvgWinBps: 34.30, AvgLossBps: 34.28, Robust: true, Side: "long", StopMultiplier: 4},
	"NATGAS":   {WinRate: 0.551, AvgWinBps: 296.51, AvgLossBps: 322.73, Robust: true, Side: "long", StopMultiplier: 4},
	// Breadth round 5 (2026-11-12), same methodology, real swap included:
	// COPPER (long only, 110 trades, +13.53bp both halves) -- feeds BOTH
	// a standalone trade AND the COPPER confluence boost below; its own
	// RSI2 alone does NOT confirm, only in agreement with IBS.
	// US30 (long only, 119 trades, +8.58bp both halves) -- standalone
	// only; its confluence with RSI2 flips negative, deliberately NOT a
	// confluence symbol.
	// GBPJPY (long only, 65 trades, +4.70bp both halves) -- standalone
	// only, same reasoning: its own RSI2 also confirms standalone (see
	// MeasuredRSI2Stats) but the TWO COMBINED flip to -23.50bp -- GBPJPY
	// must never be a confluence symbol.
	"COPPER": {WinRate: 0.664, AvgWinBps: 87.11, AvgLossBps: 131.63, Robust: true, Side: "long", StopMultiplier: 4},
	"US30":   {WinRate: 0.605, AvgWinBps: 55.30, AvgLossBps: 62.99, Robust: true, Side: "long", StopMultiplier: 4},
	"GBPJPY": {WinRate: 0.600, AvgWinBps: 35.09, AvgLossBps: 40.88, Robust: true, Side: "long", StopMultiplier: 4},
	// US shares, breadth round 6 (2026-11-17), same methodology + real
	// swap (Capital.com's generic shares financing rate, identical
	// across all shares: 0.0222624%/day -> 2.226bps/day). AAPL/AMZN/META/
	// XOM/KO/TSLA/JPM confirm IBS alone, both halves positive.
	"AAPL": {WinRate: 0.562, AvgWinBps: 127.85, AvgLossBps: 144.14, Robust: true, Side: "long", StopMultiplier: 4},
	"TSLA": {WinRate: 0.617, AvgWinBps: 241.41, AvgLossBps: 356.66, Robust: true, Side: "long", StopMultiplier: 4},
	"AMZN": {WinRate: 0.625, AvgWinBps: 140.92, AvgLossBps: 181.08, Robust: true, Side: "long", StopMultiplier: 4},
	"META": {WinRate: 0.631, AvgWinBps: 209.54, AvgLossBps: 159.53, Robust: true, Side: "long", StopMultiplier: 4},
	// JPM confirms IBS alone AND RSI2 alone independently, but their
	// COMBINED confluence bucket flips negative (-12.31bp) -- same
	// GBPJPY pattern. JPM deliberately absent from every confluence set.
	"JPM": {WinRate: 0.705, AvgWinBps: 132.39, AvgLossBps: 133.14, Robust: true, Side: "long", StopMultiplier: 4},
	"XOM": {WinRate: 0.597, AvgWinBps: 161.95, AvgLossBps: 196.39, Robust: true, Side: "long", StopMultiplier: 4},
	"KO":  {WinRate: 0.641, AvgWinBps: 105.35, AvgLossBps: 133.15, Robust: true, Side: "long", StopMultiplier: 4},
	// Breadth round 8 (2026-11-23): more Capital.com-native crypto
	// pairs, found while checking which further crypto is untested --
	// high-volatility instruments (crypto, metals, growth stocks)
	// already carry this project's structurally largest measured
	// expectancy (+51bp/trade average vs. +7bp for FX majors), so this
	// is where the highest-value remaining breadth was. 7 tested
	// (XRPUSD, SOLUSD, ADAUSD, DOGEUSD, LTCUSD, DOTUSD, AVAXUSD), real
	// swap included (-0.0616438%/day = 6.16bps/day, identical generic
	// rate across all of them, confirmed live). Only SOLUSD and
	// DOGEUSD clear the real >=30-trades/half bar under IBS --
	// XRPUSD/ADAUSD/AVAXUSD's RSI2 numbers looked attractive
	// (+92-290bp/trade) but their samples (14-19 trades/half) are too
	// thin to trust and are explicitly NOT deployed; Donchian breakout
	// also looked attractive on several of these but none cleared
	// 30/half either (signals are too rare over the available crypto
	// history). LTCUSD/DOTUSD/remaining buckets rejected outright
	// (negative or sign-flipping). StopMultiplier 4 (not the default
	// 3x) applied here too, consistent with the same crypto-specific
	// stop-width reasoning already used for BTCUSD's RSI2/Breakout
	// entries -- crypto's larger moves need the same room to revert.
	"SOLUSD":  {WinRate: 0.585, AvgWinBps: 395.13, AvgLossBps: 435.19, Robust: true, Side: "long", StopMultiplier: 4},
	"DOGEUSD": {WinRate: 0.597, AvgWinBps: 395.42, AvgLossBps: 506.11, Robust: true, Side: "long", StopMultiplier: 4},
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
// StopMultiplier: 4 (not the global 3x default) on every entry below,
// added 2026-11-23 after a real live diagnostic: of 18 closed demo
// trades, 5 broker-side stop-outs accounted for -$52.68 while the
// strategies' OWN exit logic was net +$39.10 (7 wins +$49.43, 6 small
// losses -$10.33) -- the signal logic works, stop-outs were the drag.
// Checked historically (daily bars, 6y, stop-inclusive backtest) WHY:
// RSI2's entries catch sharper, more continuation-prone drops than
// IBS's before they revert, so RSI2's stop-hit rate is structurally
// higher (22-32% on JPM/MSFT) than IBS's (8-9% on US30/MSFT) at the
// same 3x multiplier -- not a bug, a real difference in what each
// detector's entry condition selects for. A stop-multiplier sweep
// (2x-999x/no-stop) on 4 of 5 symbols tested (JPM, MSFT, US100, AAPL;
// PLATINUM's sample here was too thin at n=26, below the 30-trade
// bar, to trust) showed expectancy improving monotonically as the
// stop widens -- a tighter stop clips the mean-reversion edge before
// it has room to revert, the same mechanism found in the rejected
// intraday IBS research. 4x is a real, moderate widening backed by
// that evidence, not a guess. Does NOT increase dollar risk per trade
// -- position sizing resizes to the stop distance to keep risk_pct
// constant, so a wider stop means a smaller position that can breathe
// longer, not a bigger loss when it does eventually fire.
var MeasuredRSI2Stats = map[string]IBSStats{
	"US100":    {WinRate: 0.719, AvgWinBps: 102.27, AvgLossBps: 116.36, Robust: true, Side: "long", StopMultiplier: 4},
	"US500":    {WinRate: 0.717, AvgWinBps: 81.39, AvgLossBps: 90.83, Robust: true, Side: "long", StopMultiplier: 4},
	"PLATINUM": {WinRate: 0.712, AvgWinBps: 194.46, AvgLossBps: 171.56, Robust: true, Side: "long", StopMultiplier: 4},
	// EURUSD/USDJPY: the first INTRADAY (15m) strategy this project has
	// actually deployed. Found 2026-11-28 improving on the earlier
	// "RSI2 intraday: real edge, explicitly NOT deployed" finding
	// (docs/STRATEGY_SUITE.md) -- that one failed on a single-point
	// entry-threshold optimum (no stable neighborhood) and used the
	// full 07-21 UTC session. This one is different on both counts:
	// restricting to the LATE session (16:00-21:00 UTC, NY afternoon
	// into close) and re-sweeping entry/exit/hold found entry<=2.5
	// sits in the MIDDLE of a genuine plateau (robust continuously from
	// entry 2.25 to 3.0, and insensitive to exit 65-90 and hold 16-96)
	// -- the parameter-insensitivity this project requires before
	// trusting a threshold, which the earlier attempt explicitly lacked.
	// EURJPY also looked robust at first pass but was REJECTED once its
	// REAL live spread (2.35bp, not the 1.3bp assumed) was substituted
	// in -- flips net negative. GBPUSD/AUDUSD never confirmed in this
	// session at any threshold tested. 10-year-FX-history-capped data,
	// ~2.9 years actually available (2024-01-01 to present, Capital.com's
	// real cap), real live spreads (EURUSD 0.64bp, USDJPY 0.75bp).
	// EURUSD n=236 (118/half, half1 +1.25bp/half2 +0.36bp); USDJPY
	// n=200 (100/half, half1 +1.57bp/half2 +0.56bp). Config: entry
	// RSI2<=2.5 (rsi2_long_below_bps=250), exit RSI2>=75
	// (rsi2_strength_exit_bps=7500), hold<=32 bars (8h), session hours
	// 16-21 UTC, 15m interval -- see
	// docs/examples_watch_forward_intraday_rsi2_v1.json.
	"EURUSD": {WinRate: 0.653, AvgWinBps: 5.05, AvgLossBps: 7.16, Robust: true, Side: "long", StopMultiplier: 4},
	"USDJPY": {WinRate: 0.675, AvgWinBps: 6.99, AvgLossBps: 11.24, Robust: true, Side: "long", StopMultiplier: 4},
	// XRPUSD: NOT backtest-confirmed by this project's own bar (only 35
	// trades / 17 per half, below the 30/half floor) -- Robust: false
	// is deliberate, not an oversight, so IsValidated() keeps tagging
	// every signal UNVALIDATED. Deployed to REAL demo execution anyway
	// (not just a tradex watch journal) because the user explicitly
	// asked for a forward test on the demo account, which is exactly
	// how a thin-but-promising sample is meant to grow into a real
	// confirmation or a real rejection -- not a shortcut around the
	// bar. 71.4% win rate, +104.2bp/trade average, both halves
	// nominally positive, real spread+swap included (entry RSI2<=10,
	// exit RSI2>=60, 200-day trend filter, 10-day hold). Revisit this
	// comment once real trade count reaches ~30: either flip Robust to
	// true with a real citation, or remove the entry if it degrades.
	"XRPUSD": {WinRate: 0.714, AvgWinBps: 457.34, AvgLossBps: 778.58, Robust: false, Side: "long", StopMultiplier: 4},
	// BTCUSD: measured on Capital.com's OWN price feed (NOT Binance's
	// BTCUSDT -- those are different venues/price series, see
	// internal/instrument/capital.go). Real daily swap cost included
	// (0.0616438%/day long rate -> 6.16bps/day). 257 trades, both halves
	// positive (+25.04bp/+53.38bp). ETHUSD was ALSO tested the same way
	// and REJECTED: it looks fine on Binance but sign-flips on
	// Capital.com's actual feed (-1.99bp/+86.85bp) -- not deployed.
	"BTCUSD": {WinRate: 0.638, AvgWinBps: 223.33, AvgLossBps: 285.34, Robust: true, Side: "long", StopMultiplier: 4},
	// Breadth round 5 (2026-11-12), real swap included:
	// J225 (171 trades, +29.86bp both halves) -- standalone; its
	// confluence with IBS is weaker than RSI2 alone here (odd, but
	// real), not worth a boost -- NOT a confluence symbol.
	// BRENT, WTI (long only) -- standalone RSI2 confirms AND agreement
	// with IBS confirms even stronger; both are confluence-boost symbols.
	// USDCAD, AUDJPY, EURJPY, EURGBP -- standalone only; each one's
	// confluence with IBS flips negative once real swap is included
	// (USDCAD's confluence flipped specifically from the swap addition),
	// so none of these four are confluence symbols.
	// GBPJPY -- standalone only, same reasoning as its IBS entry above.
	"J225":   {WinRate: 0.667, AvgWinBps: 105.10, AvgLossBps: 120.62, Robust: true, Side: "long", StopMultiplier: 4},
	"BRENT":  {WinRate: 0.642, AvgWinBps: 216.42, AvgLossBps: 223.61, Robust: true, Side: "long", StopMultiplier: 4},
	"WTI":    {WinRate: 0.573, AvgWinBps: 217.03, AvgLossBps: 196.16, Robust: true, Side: "long", StopMultiplier: 4},
	"USDCAD": {WinRate: 0.624, AvgWinBps: 29.45, AvgLossBps: 27.35, Robust: true, Side: "long", StopMultiplier: 4},
	"AUDJPY": {WinRate: 0.626, AvgWinBps: 47.49, AvgLossBps: 63.59, Robust: true, Side: "long", StopMultiplier: 4},
	"EURJPY": {WinRate: 0.605, AvgWinBps: 35.88, AvgLossBps: 38.34, Robust: true, Side: "long", StopMultiplier: 4},
	"EURGBP": {WinRate: 0.573, AvgWinBps: 27.58, AvgLossBps: 26.96, Robust: true, Side: "long", StopMultiplier: 4},
	"GBPJPY": {WinRate: 0.676, AvgWinBps: 33.21, AvgLossBps: 52.00, Robust: true, Side: "long", StopMultiplier: 4},
	// US shares, breadth round 6 (2026-11-17): RSI2 alone confirms,
	// both halves positive, with real swap included.
	"MSFT": {WinRate: 0.669, AvgWinBps: 124.96, AvgLossBps: 128.53, Robust: true, Side: "long", StopMultiplier: 4},
	"NVDA": {WinRate: 0.654, AvgWinBps: 259.26, AvgLossBps: 199.55, Robust: true, Side: "long", StopMultiplier: 4},
	"GOOGL": {WinRate: 0.617, AvgWinBps: 117.64, AvgLossBps: 112.85, Robust: true, Side: "long", StopMultiplier: 4},
	"JNJ":  {WinRate: 0.582, AvgWinBps: 118.70, AvgLossBps: 125.20, Robust: true, Side: "long", StopMultiplier: 4},
	"V":    {WinRate: 0.629, AvgWinBps: 93.77, AvgLossBps: 132.42, Robust: true, Side: "long", StopMultiplier: 4},
	// AAPL also confirms RSI2 alone (as well as IBS alone and
	// confluence -- all three bucket independently, see
	// MeasuredIBSStats/MeasuredConfluenceStats).
	"AAPL": {WinRate: 0.615, AvgWinBps: 123.99, AvgLossBps: 157.31, Robust: true, Side: "long", StopMultiplier: 4},
	// JPM confirms RSI2 alone too (see MeasuredIBSStats for the IBS
	// side) -- confluence deliberately excluded for JPM, same reasoning.
	"JPM": {WinRate: 0.632, AvgWinBps: 100.49, AvgLossBps: 142.02, Robust: true, Side: "long", StopMultiplier: 4},
	// AUDCAD, breadth round 7 (2026-11-20): found via a genuinely
	// different model first (z-score mean reversion, both sides, no
	// trend filter -- see docs/STRATEGY_SUITE.md), then confirmed to
	// ALSO exist under the already-deployed RSI2 engine, cheaper to
	// ship than a whole new strategy type for one symbol. IBS alone
	// does NOT confirm here (sign flip) -- RSI2 alone does, real swap
	// included (142 trades, both halves positive +3.3/+2.5bp).
	"AUDCAD": {WinRate: 0.577, AvgWinBps: 30.36, AvgLossBps: 34.63, Robust: true, Side: "long", StopMultiplier: 4},
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
	"US100":    {WinRate: 0.688, AvgWinBps: 76.14, AvgLossBps: 69.93, Robust: true, Side: "long", StopMultiplier: 4},
	"PLATINUM": {WinRate: 0.660, AvgWinBps: 171.56, AvgLossBps: 85.74, Robust: true, Side: "long", StopMultiplier: 4},
	"XAUUSD":   {WinRate: 0.663, AvgWinBps: 84.88, AvgLossBps: 100.72, Robust: true, Side: "long", StopMultiplier: 4},
	"GBPUSD":   {WinRate: 0.703, AvgWinBps: 27.94, AvgLossBps: 26.81, Robust: true, Side: "long", StopMultiplier: 4},
	// Breadth round 5 (2026-11-12), real swap included:
	// BRENT, WTI -- agreement beats RSI2 alone even further (boost).
	// COPPER -- agreement beats IBS alone (boost); COPPER's RSI2 alone
	// does NOT confirm, so COPPER is NOT in MeasuredRSI2Stats at all --
	// confluence is the only way RSI2's reading contributes here.
	// AUDUSD -- neither IBS nor RSI2 confirms alone; confluence rescues
	// it, same pattern as XAUUSD/GBPUSD above.
	"BRENT":  {WinRate: 0.673, AvgWinBps: 231.15, AvgLossBps: 190.14, Robust: true, Side: "long", StopMultiplier: 4},
	"WTI":    {WinRate: 0.638, AvgWinBps: 174.05, AvgLossBps: 182.49, Robust: true, Side: "long", StopMultiplier: 4},
	"COPPER": {WinRate: 0.756, AvgWinBps: 98.59, AvgLossBps: 243.68, Robust: true, Side: "long", StopMultiplier: 4},
	"AUDUSD": {WinRate: 0.645, AvgWinBps: 34.17, AvgLossBps: 41.67, Robust: true, Side: "long", StopMultiplier: 4},
	// US shares, breadth round 6 (2026-11-17), real swap included:
	// AAPL -- IBS, RSI2, AND confluence all three independently confirm
	// (the richest case measured yet). MSFT/NVDA/GOOGL -- RSI2 alone
	// confirms, IBS alone does not, but agreement confirms even
	// stronger (NVDA: RSI2 +100.44bp alone -> +149.39bp combined).
	// AMZN/META -- IBS alone confirms, RSI2 alone does not, agreement
	// confirms stronger still. WMT -- NEITHER signal confirms alone,
	// confluence rescues it (same AUDUSD/XAUUSD/GBPUSD pattern).
	"AAPL":  {WinRate: 0.607, AvgWinBps: 128.21, AvgLossBps: 158.05, Robust: true, Side: "long", StopMultiplier: 4},
	"MSFT":  {WinRate: 0.615, AvgWinBps: 119.40, AvgLossBps: 113.87, Robust: true, Side: "long", StopMultiplier: 4},
	"NVDA":  {WinRate: 0.716, AvgWinBps: 301.05, AvgLossBps: 232.21, Robust: true, Side: "long", StopMultiplier: 4},
	"GOOGL": {WinRate: 0.642, AvgWinBps: 135.72, AvgLossBps: 166.88, Robust: true, Side: "long", StopMultiplier: 4},
	"AMZN":  {WinRate: 0.636, AvgWinBps: 157.00, AvgLossBps: 189.05, Robust: true, Side: "long", StopMultiplier: 4},
	"META":  {WinRate: 0.708, AvgWinBps: 180.89, AvgLossBps: 189.43, Robust: true, Side: "long", StopMultiplier: 4},
	"WMT":   {WinRate: 0.609, AvgWinBps: 126.26, AvgLossBps: 105.58, Robust: true, Side: "long", StopMultiplier: 4},
	// AUDCAD, breadth round 7 (2026-11-20): RSI2 alone confirms (see
	// MeasuredRSI2Stats), confluence confirms stronger still -- 51
	// trades, thinner (~25-26/half, right at the floor like PLATINUM
	// confluence was), both halves positive (+24.4/+21.9bp), real swap
	// included.
	"AUDCAD": {WinRate: 0.686, AvgWinBps: 43.55, AvgLossBps: 21.52, Robust: true, Side: "long", StopMultiplier: 4},
}

// ConfluenceOnlySymbols: NEITHER IBS nor RSI2 confirms alone here -- only
// trade when both fire on the same symbol, same day (an individual
// fallback would have no valid stats entry to use anyway, but these are
// explicitly excluded from Measured{IBS,RSI2}Stats so the case is
// unreachable, not merely empty).
var ConfluenceOnlySymbols = map[string]bool{"XAUUSD": true, "GBPUSD": true, "AUDUSD": true, "WMT": true}

// ConfluenceBoostSymbols: AT LEAST ONE of IBS/RSI2 is already
// independently confirmed here (not necessarily both -- BRENT/WTI/COPPER
// and most of the shares below only have RSI2 or IBS alone, not both) --
// a single-signal day still trades normally at its own confirmed
// strategy's size; confluence just gets a stronger, separately-measured
// size on top when both agree, not a gate. A symbol whose OTHER signal
// isn't independently confirmed simply has no entry in that signal's
// Measured*Stats table, so firing on it alone safely finds nothing and
// skips -- no extra check needed here for that.
var ConfluenceBoostSymbols = map[string]bool{
	"US100": true, "PLATINUM": true, "BRENT": true, "WTI": true, "COPPER": true,
	"AAPL": true, "MSFT": true, "NVDA": true, "GOOGL": true, "AMZN": true, "META": true,
	"AUDCAD": true,
}

const (
	// MaxRiskPct is a hard ceiling applied after half-Kelly and the
	// stability discount, regardless of what the formula outputs — raw
	// Kelly off a ~150-300 trade sample routinely suggests double-digit
	// percentages that no real risk desk would run.
	//
	// Cut from 2.0 to 1.0 on 2026-11-20: live track record at that
	// point was 7 real trades (net +$12.15 realized, but 3 live
	// infrastructure bugs were found and fixed in the same few days --
	// float-precision position matching, a margin-pct unit bug, and a
	// broker date-range limit that corrupted recorded pnl twice). Not
	// evidence the strategies are wrong, but good reason to run smaller
	// while the live plumbing is this young and the sample this thin.
	// Revisit once there's a real sample (~30 trades) to judge against.
	MaxRiskPct = 1.0
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

// BreakoutRiskPct is the trend-following Breakout equivalent, same formula
// and safeguards, against MeasuredBreakoutStats instead. The same
// half-Kelly formula applies fine here even though the underlying bet is
// structurally opposite to IBS/RSI2/Confluence (low win rate, high payoff
// ratio, vs. the others' high win rate, modest payoff ratio) -- Kelly's
// formula only cares about win rate and payoff ratio, not which produced
// them.
func BreakoutRiskPct(symbol string) (float64, bool) {
	return riskPct(MeasuredBreakoutStats, symbol)
}

// MondayGapRiskPct is the Monday-gap equivalent of HalfKellyRiskPct, same
// formula and safeguards, against MeasuredMondayGapStats instead.
func MondayGapRiskPct(symbol string) (float64, bool) {
	return riskPct(MeasuredMondayGapStats, symbol)
}

// MeasuredMondayGapStats: found 2026-11-26 from the user's own from-scratch
// price-action request ("I need to start by analyzing from the basics") --
// NOT taken from a paper or forum, built bottom-up by testing raw candle
// constructs (consecutive-close streaks, swing-level reactions, volatility
// squeezes, pin bars, day-of-week seasonality) on 10-year FX daily data
// (3-year BTCUSD -- the real limit of Capital.com's crypto feed) and
// keeping only what survived.
//
// Finding: plain day-of-week seasonality showed nothing clean across all
// 5 instruments, but filtering to Mondays that open with a GAP DOWN from
// Friday's close did -- both-halves-robust, cost-inclusive, 8-9 of 11
// years positive per FX pair, on EVERY instrument tested (EURUSD, GBPUSD,
// USDJPY, AUDUSD, BTCUSD). Entry: Monday's live price vs the last closed
// (Friday's) daily bar, gap<0 required. Exit: same day only -- a sweep of
// holding 0-3 extra days showed exiting at Monday's own close is the ONLY
// timing where all 5 stay robust; holding even one extra day breaks
// EURUSD/GBPUSD/BTCUSD's robustness. Implemented as MaxHoldBars=1 (exits
// once Monday's own daily bar closes), not a new date-based exit rule --
// the existing BarsHeld>=MaxHoldBars mechanism already does exactly this
// on a daily timeframe.
//
// Real trade counts (gap-down Mondays only, cost-inclusive: 1bp FX /
// 10bp BTCUSD): EURUSD n=245, GBPUSD n=243, USDJPY n=266, AUDUSD n=262,
// BTCUSD n=83 -- all comfortably above the 30/half bar.
var MeasuredMondayGapStats = map[string]IBSStats{
	"EURUSD": {WinRate: 0.551, AvgWinBps: 27.51, AvgLossBps: 25.89, Robust: true, Side: "long", StopMultiplier: 3},
	"GBPUSD": {WinRate: 0.514, AvgWinBps: 33.21, AvgLossBps: 29.25, Robust: true, Side: "long", StopMultiplier: 3},
	"USDJPY": {WinRate: 0.579, AvgWinBps: 29.20, AvgLossBps: 31.17, Robust: true, Side: "long", StopMultiplier: 3},
	"AUDUSD": {WinRate: 0.557, AvgWinBps: 38.36, AvgLossBps: 30.54, Robust: true, Side: "long", StopMultiplier: 3},
	"BTCUSD": {WinRate: 0.530, AvgWinBps: 238.59, AvgLossBps: 183.38, Robust: true, Side: "long", StopMultiplier: 3},
}

// RSI2OvernightRiskPct is the RSI2-overnight equivalent of HalfKellyRiskPct.
func RSI2OvernightRiskPct(symbol string) (float64, bool) {
	return riskPct(MeasuredRSI2OvernightStats, symbol)
}

// MeasuredRSI2OvernightStats: a SECOND intraday (15m) strategy, found
// 2026-11-28 the same session as the late-session (16-21 UTC) RSI2
// strategy, per the user's "continue to look for more intraday
// variations" request. Different session entirely: 21:00-01:00 UTC
// (NY close into Tokyo open), where a thin-liquidity mean-reversion
// pattern shows up strongly across multiple pairs.
//
// Needs its OWN stats map (not MeasuredRSI2Stats) because EURJPY/
// EURGBP/USDCAD are already live DAILY RSI2 symbols with completely
// different sizing numbers -- reusing the same map would have
// overwritten the daily strategy's sizing for these symbols. Routed
// via the rsi2OvernightDetector wrapper (cmd/tradex/watch.go), which
// reports Name()=="RSI2OVERNIGHT" instead of "RSI2".
//
// Config: entry RSI2<=2.5 (rsi2_long_below_bps=250), exit RSI2>=75
// (rsi2_strength_exit_bps=7500), hold<=32 bars (8h), session hours
// 21-01 UTC (wraps midnight), 15m interval, real spread AND real
// overnight swap included (-0.411bp/day, charged once if the trade
// holds through the 22:00 UTC rollover -- verified live via GET
// /api/v1/markets/{epic}'s overnightFee field, same rate on all 3).
// ~2.9 years of data (2024-01-01 to present, Capital.com's real 15m-FX
// cap). EURJPY n=294 (147/half, +2.45bp/+1.23bp); EURGBP n=264
// (132/half, +1.83bp/+3.46bp); USDCAD n=218 (109/half, +0.10bp/+1.91bp,
// half1 thin-margin but still nominally positive -- weakest of the 3,
// worth re-checking as more live data accrues). EURUSD and AUDJPY were
// ALSO tested here: EURUSD not robust at this exact entry (half1
// negative); AUDJPY looked very strong (71-80% win rate at every
// threshold) but its real live spread could not be verified (FX
// markets were closed/weekend at measurement time, and the one
// available quote showed an implausibly wide 20bp vs. these pairs'
// 0.6-4.5bp) -- EXCLUDED pending a live re-check during active hours,
// not deployed on an unverified cost assumption.
//
// GBPJPY added 2026-11-28 (same session): 80.9% win rate, n=272
// (136/half, +2.96bp/+4.58bp), robust at EVERY entry (1.0-3.0), exit
// (65-95) and hold (8-96) tested -- the most parameter-insensitive of
// the 4 symbols checked in this session. Real overnight fee confirmed
// live, same -0.411bp/day as the other 3.
var MeasuredRSI2OvernightStats = map[string]IBSStats{
	"EURJPY": {WinRate: 0.711, AvgWinBps: 6.57, AvgLossBps: 9.79, Robust: true, Side: "long", StopMultiplier: 4},
	"GBPJPY": {WinRate: 0.809, AvgWinBps: 8.35, AvgLossBps: 15.62, Robust: true, Side: "long", StopMultiplier: 4},
	"EURGBP": {WinRate: 0.663, AvgWinBps: 5.50, AvgLossBps: 2.98, Robust: true, Side: "long", StopMultiplier: 4},
	"USDCAD": {WinRate: 0.661, AvgWinBps: 4.19, AvgLossBps: 5.18, Robust: true, Side: "long", StopMultiplier: 4},
}

// MeasuredBreakoutStats: the trend-following Donchian breakout family
// (internal/features/breakout.go), deliberately the opposite structural
// bet from IBS/RSI2/Confluence -- low win rate, winners run far longer
// than losers. Found 2026-11-20 specifically hunting for a genuinely
// different (not mean-reversion) edge for forex/crypto per the user's
// request not to restrict the search to the existing framework.
//
// BTCUSD on Capital.com's own daily feed, 2020-11 to 2026-11, entry=10-bar
// high breakout / exit=5-bar low break or 90-bar cap, real swap included
// (2.226bps/day, same rate as BTCUSD RSI2): 63 trades, 33.3% win rate,
// avg win 1693.9bp / avg loss 684.5bp (payoff ratio 2.47), both halves
// positive (+118.9bp / +98.1bp). Robust across every (entry,exit) window
// combination tried (10/5, 15/7, 20/10, 10/10, 15/10, 20/20 all stayed
// both-halves-positive) -- not a fitted fluke on one parameter choice.
// EURUSD, XAUUSD, AUDCAD, ETHUSD were also tested under the same model:
// XAUUSD confirmed too (+40.3/+248.0bp, n=30, thin) but EURUSD, AUDCAD
// and ETHUSD all flipped sign between halves and are NOT deployed.
var MeasuredBreakoutStats = map[string]IBSStats{
	"BTCUSD": {WinRate: 0.333, AvgWinBps: 1693.9, AvgLossBps: 684.5, Robust: true, Side: "long", StopMultiplier: 4},
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

// init asserts every Measured*Stats entry sets StopMultiplier explicitly
// -- added 2026-11-28. ProtectiveStopBps silently substitutes a default
// of 3 whenever StopMultiplier<=0 (the zero value for an unset field),
// which is EXACTLY the bug class that caused a real live incident: the
// 2026-11-23 stop-width widening (3x -> 4x) was applied only to
// MeasuredRSI2Stats, and the other tables silently kept trading on the
// old 3x width with no error, no log line, and no test failure -- US30
// and JPM's worst live losses (including the single worst loss in the
// account's history, -$19.66 on US30) traded through that gap. Every
// entry currently sets the field explicitly, so this panic should never
// fire today; it exists purely so the NEXT missed field -- a one-line
// omission, easy to miss in review -- fails loudly at process startup
// instead of silently reintroducing the same incident.
func init() {
	check := func(name string, table map[string]IBSStats) {
		for symbol, s := range table {
			if s.StopMultiplier <= 0 {
				panic(fmt.Sprintf("execution: %s[%q].StopMultiplier is unset (<=0) -- this would silently fall back to the old, narrower default width; see sizing.go's init() comment for the live incident this already caused once", name, symbol))
			}
		}
	}
	check("MeasuredIBSStats", MeasuredIBSStats)
	check("MeasuredRSI2Stats", MeasuredRSI2Stats)
	check("MeasuredConfluenceStats", MeasuredConfluenceStats)
	check("MeasuredMondayGapStats", MeasuredMondayGapStats)
	check("MeasuredBreakoutStats", MeasuredBreakoutStats)
}
