# Triangle breakout — `trianglebreakout-0.1.0`

Status: **not measurable on any tested instrument/timeframe. Must not be
promoted.**

Detector: `internal/features/trianglebreakout.go` (wraps the existing
`internal/features/triangle.go` geometry — not reimplemented)
Migrated from: `internal/signal/engine.go`'s original ad hoc, never-measured
`evaluateTriangleSignal` ("Triangle Breakout")
Harness: `cmd/stratlab`
Live path: `cmd/tradex/watch.go` (monitor only, no order authority)

## What it claims

A deterministic, stateless port of the live monitor's original idea: a
converging triangle (`features.DetectTriangle`) whose boundary is broken by
a confirmed close is a breakout signal.

1. **Swings and the triangle are recomputed fresh from the window every
   call** — a brand-new `features.NewSwingDetector` feeds the whole window,
   then `features.DetectTriangle` runs on the result. No cross-call state.
2. **A "genuinely new" breakout only.** The live engine signalled a
   triangle pattern once (`triangleSignaled`, cross-call state). Recomputed
   statelessly, that becomes: the breakout condition must NOT already have
   been true on the second-to-last candle — otherwise every bar after a
   real breakout would re-signal.
3. **Entry/stop/target exactly as `evaluateTriangleSignal`**: a market entry
   at the confirming close; stop at the worse of the 10-bar recent extreme
   and the OPPOSITE triangle boundary; target a stop-multiple.
4. **Risk is floored at a multiple of the measured spread** (default 4x),
   which `Continuation` has and the live engine never did.
5. **A target/stop at or below zero is rejected** (same defect class found
   and fixed in `StructureTrend`, carried over defensively here even
   though it was not independently observed on this fixture set).

## Determinism rules the harness encodes

Same as `StructureTrend`: a setup is knowable only at its confirming bar's
close; costs charged per closed trade; one position at a time; entries are
MARKET so every armed setup fills.

## Measured evidence

Same CSV fixtures and cost assumptions as `STRUCTURETREND_SPEC.md`: FX
7-tick spread + 2-tick slippage, XAUUSD 500-tick spread + 100-tick
slippage, BTCUSDT 7760-tick and ETHUSDT 380-tick spread (a round-trip
~0.2%-of-median-price approximation). `cmd/stratlab` defaults otherwise.
The config in `DefaultTriangleBreakoutConfig()` — `features.TriangleConfig`
unchanged from the live engine's own default (`MinTouches: 3,
ToleranceTicks: 5` — a tight 5-tick line-fit tolerance already in the
codebase before this migration, not introduced or tuned here) — was fixed
before any of these runs and was NOT adjusted afterward.

| Instrument | TF | Armed | Trades | Verdict |
| ---------- | -- | ----- | ------ | ------- |
| EURUSD     | 1h | 1     | 1      | not measurable — 1 trade |
| GBPUSD     | 1h | 0     | 0      | not measurable — 0 trades |
| AUDUSD     | 1h | 2     | 2      | not measurable — 2 trades (both losers, −1.081 R) |
| XAUUSD     | 1h | 0     | 0      | not measurable — 0 trades |
| EURUSD     | 4h | 0     | 0      | not measurable — 0 trades |
| XAUUSD     | 4h | 0     | 0      | not measurable — 0 trades |
| EURUSD     | 1d | 0     | 0      | not measurable — 0 trades |
| XAUUSD     | 1d | 0     | 0      | not measurable — 0 trades |
| BTCUSDT    | 1h | 0     | 0      | not measurable — 0 trades |
| BTCUSDT    | 4h | 0     | 0      | not measurable — 0 trades |
| BTCUSDT    | 1d | 0     | 0      | not measurable — 0 trades |
| ETHUSDT    | 1h | 0     | 0      | not measurable — 0 trades |
| ETHUSDT    | 4h | 0     | 0      | not measurable — 0 trades |
| ETHUSDT    | 1d | 0     | 0      | not measurable — 0 trades |

## Verdict

**Lead with the number: zero to two armed setups across every one of the 14
instrument/timeframe combinations tested (EURUSD, GBPUSD, AUDUSD, XAUUSD at
1h/4h/1d where available, BTCUSDT and ETHUSDT at 1h/4h/1d). Not measurable
anywhere — there is no sample to compute an expectancy from.**

This is the honest result of the stated-prior config, not a bug surfaced by
testing: `internal/features/structuretrend_test.go`/`trianglebreakout_test.go`
confirm the detector correctly identifies triangles and breakouts on
constructed fixtures (positive detection, mirror, determinism and every
rejection branch all pass). The scarcity is the combination of three
pre-existing, unchanged constraints:

1. `TriangleConfig.ToleranceTicks: 5` — a 5-tick line-fit tolerance is a
   fraction of a pip on 5-digit FX and essentially nothing against XAUUSD's
   3-digit/crypto's 2-digit noise. Real market swings rarely sit this close
   to a straight line.
2. `TriangleConfig.MinTouches: 3` on each boundary, drawn only from the
   MOST RECENT three confirmed swing highs/lows inside the window.
3. **Recomputing the triangle fresh from only the trailing window on every
   call** (this migration's explicit, required design) means a pattern
   that would form if swing history accumulated across calls instead gets
   rebuilt from scratch each time, and the "genuinely new breakout" check
   additionally suppresses any boundary condition already true one bar
   earlier. The combination makes a confirmed, fresh breakout a very rare
   event in this architecture.

None of these three are config values changed by this migration to chase a
result — they are the live engine's own prior defaults (1 and 2) and the
task's required stateless-recomputation design (3). Per the migration's
explicit instruction not to sweep knobs after seeing the data, they were
NOT loosened after this near-zero result. **Verdict: not measurable on any
tested instrument or timeframe; does not clear, and cannot currently be
evaluated against, the promotion bar.** `Validated()` is hardcoded `false`.

## Open questions, in the order worth testing

1. **Is the triangle geometry itself too strict for 1h-and-coarser FX/crypto
   data, independent of this migration?** `TriangleConfig.ToleranceTicks`
   and `MinTouches` predate this work and are shared with the live engine;
   loosening them is a measured decision for whoever owns `triangle.go`,
   to be split out of sample like any other parameter change — not
   something to do inside this migration just to manufacture a sample.
2. **Does triangle geometry need a WIDER window than the trailing-only
   recompute this migration uses?** The live engine accumulated swing
   history across the whole session; this detector only sees
   `RequiredBars()` bars per call. A future design could widen the window
   substantially (at a real history-cost) to see whether that alone
   produces a measurable sample, which would isolate whether the scarcity
   is the tolerance/MinTouches or the trailing-window recompute.
3. **If a measurable sample does appear, re-run with the same
   crypto-cost caveat as `STRUCTURETREND_SPEC.md`** before trusting any
   crypto cell.
