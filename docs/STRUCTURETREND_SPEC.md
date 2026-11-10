# Structure break + trend — `structuretrend-0.1.0`

Status: **measured, mixed. Does NOT clear the promotion bar. Must not be promoted.**

Detector: `internal/features/structuretrend.go`
Migrated from: `internal/signal/engine.go`'s original ad hoc, never-measured
`evaluateSignal` ("Structure Break + Trend")
Harness: `cmd/stratlab`
Live path: `cmd/tradex/watch.go` (monitor only, no order authority)

## What it claims

A deterministic, stateless port of the live monitor's original idea: a
structure break (close beyond the last confirmed swing extreme) that aligns
with the prevailing swing-high/swing-low trend is a continuation signal.

1. **Swings, trend and structure are recomputed fresh from the window every
   call** — a brand-new `features.NewSwingDetector` and
   `features.StructureDetector` fed the entire window, bar by bar. No
   cross-call state, matching `Continuation`'s pattern.
2. **A break goes stale.** The live engine required the break to be known
   within the last 2 bars; this is kept but made configurable
   (`MaxBreakAgeBars`).
3. **Entry is a market fill at the last candle's close**, matching the live
   engine (no resting limit for this strategy).
4. **Stop is the 10-bar recent extreme plus a tick buffer** — ported from
   the live engine's `findRecentLow`/`findRecentHigh`, with a buffer the
   live engine never had.
5. **Target is a stop-multiple** (entry +/- risk × reward:risk), kept for
   parity with the live engine's `scaleRiskTicks`, per the migration's
   explicit allowance (a traded-extreme target was not substituted).
6. **Risk is floored at a multiple of the measured spread** (default 4x),
   which `Continuation` has and the live engine never did — a real missing
   cost control added here, not a tuned result.

## A real defect this migration found and fixed before measuring

The stop-multiple target construction can project a target at or below
zero during an extreme move (BTCUSDT's March 2020 crash: entry ~$4,800,
stop-multiple target computed below $0). A price that can never trade is
not an ambiguous setup, it is an impossible one. `DetectStructureTrend` now
rejects `Stop <= 0` or `Target <= 0` before returning a setup. This was
found and fixed BEFORE any measurement run was recorded, not discovered by
sweeping for a better number.

## Determinism rules the harness encodes

- A setup is knowable only at its confirming bar's close (`DetectedAt`).
- Costs are charged on every closed trade: spread once, slippage on entry
  and exit. Results are in R.
- One position at a time.
- `StructureTrend` entries are MARKET, so there is no unfilled-limit rate to
  report — every armed setup fills.

## Measured evidence

CSV fixtures in `data/*.csv` (`open_time_ms,open,high,low,close,volume`).
FX costs: 7-tick spread + 2-tick slippage each side (matching
`CONTINUATION_SPEC.md`). XAUUSD: 500-tick spread + 100-tick slippage
(Dukascopy-scale points, matching `CONTINUATION_SPEC.md`). Crypto: no fixed
tick spread is representative of a percentage taker fee, so the spread was
set to a round-trip-equivalent of ~0.2% of the sample's median close,
expressed in that instrument's ticks — BTCUSDT 7760 ticks (median
~$38,797, tick 0.01), ETHUSDT 380 ticks (median ~$1,903.78, tick 0.01).
Default `-slippage-ticks 2`, `-expiry-bars 8`, `-max-hold-bars 48`
(irrelevant here since entries are MARKET and exits are stop/target, not
hold-based) were left at `cmd/stratlab`'s defaults; the config in
`DefaultStructureTrendConfig()` was fixed BEFORE any of these runs and was
not adjusted afterward except for the zero/negative-price defect above,
which was a bug fix, not a tune.

| Instrument | TF | Trades | Full | First half | Second half | Win% | Max DD | Verdict |
| ---------- | -- | ------ | -------- | ---------- | ----------- | ---- | ------ | ------- |
| EURUSD     | 1h | 1012   | −0.059 R | −0.088 R   | −0.035 R    | 37.4 | 67.0 R  | negative |
| GBPUSD     | 1h | 1063   | −0.077 R | −0.054 R   | −0.097 R    | 36.9 | 85.5 R  | negative |
| AUDUSD     | 1h | 1033   | −0.100 R | −0.094 R   | −0.106 R    | 37.7 | 106.2 R | negative |
| XAUUSD     | 1h | 1009   | +0.024 R | −0.028 R   | +0.070 R    | 42.0 | 43.8 R  | not robust (sign flips) |
| BTCUSDT    | 1h | 1014   | −0.081 R | −0.123 R   | −0.051 R    | 37.7 | 104.4 R | negative |
| ETHUSDT    | 1h | 1038   | −0.038 R | −0.031 R   | −0.042 R    | 39.8 | 64.1 R  | negative |
| EURUSD     | 4h | 339    | +0.024 R | −0.039 R   | +0.093 R    | 40.7 | 24.9 R  | not robust (sign flips) |
| XAUUSD     | 4h | 310    | +0.018 R | −0.007 R   | +0.017 R    | 40.0 | 20.4 R  | not robust (sign flips) |
| BTCUSDT    | 4h | 286    | +0.143 R | +0.114 R   | +0.169 R    | 48.6 | 9.9 R   | **both halves positive** |
| ETHUSDT    | 4h | 293    | +0.021 R | +0.027 R   | +0.023 R    | 39.6 | 16.6 R  | **both halves positive** |
| EURUSD     | 1d | 63     | −0.113 R | −0.129 R   | −0.058 R    | 33.3 | 10.1 R  | negative |
| XAUUSD     | 1d | 74     | +0.173 R | +0.288 R   | +0.083 R    | 41.9 | 11.3 R  | **both halves positive** |
| BTCUSDT    | 1d | 58     | +0.353 R | +0.454 R   | +0.272 R    | 55.2 | 4.3 R   | **both halves positive** |
| ETHUSDT    | 1d | 57     | +0.553 R | +0.538 R   | +0.534 R    | 59.6 | 3.8 R   | **both halves positive** |

## Verdict

**Lead with the number: 1h is negative on every instrument tested (EURUSD
−0.059 R, GBPUSD −0.077 R, AUDUSD −0.100 R, BTCUSDT −0.081 R, ETHUSDT
−0.038 R, ≥1000 trades each), and daily EURUSD is also negative
(−0.113 R, 63 trades). XAUUSD 1h and both FX pairs' 4h results flip sign
between halves — variance, not an edge, exactly the CRT/continuation
lesson.**

Four cells DO clear "positive in both halves" on this sample: BTCUSDT 4h
(+0.114 R / +0.169 R, 286 trades), ETHUSDT 4h (+0.027 R / +0.023 R, 293
trades), XAUUSD 1d (+0.288 R / +0.083 R, 74 trades) and BTCUSDT/ETHUSDT 1d
(+0.454/+0.272 R and +0.538/+0.534 R, 58 and 57 trades). That is exactly
the promotion skill's bar language ("both halves positive on ≥2
instruments at ≥30 trades net of cost") stated per-cell, and on its face
four different instruments clear it. It must NOT be read as a green light
for promotion for three reasons, stated plainly rather than hedged away:

1. **The daily and crypto-4h cells are small samples by the skill's own
   floor language.** 57-293 trades is not nothing, but it is an order of
   magnitude below the FX 1h sample sizes (1000+) that are unambiguously
   negative on the SAME detector and SAME default config. A detector that
   loses on >4,000 pooled FX/crypto 1h trades and wins on four thinner
   crypto/metal daily-and-4h cells is the classic small-sample-survivor
   pattern the skill warns against (see CRT's BTCUSDT +0.234 R cell that
   dropped to +0.043 R out of sample).
2. **This has not been cost-sanity-checked for crypto the way the skill
   requires.** The crypto spread used here (a fixed-tick approximation of
   a ~0.2% round-trip fee at the SAMPLE'S median price) is the same
   known-fragile approximation `CONTINUATION_SPEC.md` already documented
   manufacturing a fake edge on BTC/ETH 4h break_close — a fixed tick cost
   silently shrinks as a share of risk when price has ranged 10-20x across
   the sample, which it has here (BTC $3,800 to $112,000+ across this
   fixture). The BTCUSDT/ETHUSDT 4h and 1d "positive" cells are exactly the
   configuration this known defect would manufacture.
3. **No holdout has been taken.** This is the full available sample split
   in half by time, not a post-measurement out-of-sample window.

**Verdict: does not clear the promotion bar.** The large, unambiguous
1h/1d-FX samples are negative. The positive cells are small, concentrated
in crypto (the one instrument class with a known cost-measurement defect
in this codebase), and have not been cost- or holdout-checked. `Validated()`
is hardcoded `false` in this change regardless, as required: promotion is a
deliberate operator decision after further scrutiny (a percentage-fee cost
model for crypto, then a genuine holdout), not something this migration
grants automatically.

## Open questions, in the order worth testing

1. **Add a percentage-fee cost model for crypto to `cmd/stratlab`** before
   trusting any crypto cell on this detector — the fixed-tick cost
   approximation is the same defect `CONTINUATION_SPEC.md` already caught
   once.
2. **Re-run the four positive cells against a genuine post-measurement
   holdout**, not a mid-sample split, before considering promotion.
3. **1h FX is unambiguously negative and large-sample; do not re-sweep its
   parameters.** That result is settled, matching `Continuation`'s own
   1h-FX conclusion.
