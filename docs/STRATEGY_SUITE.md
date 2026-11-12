# Strategy Suite & Multi-Strategy Signal Engine

`spec: strategy-suite-0.1.0`

Tradex runs an arbitrary set of strategies through one interface and tags every
emitted signal with the strategy that produced it. A strategy is a pure
`scan.Detector` over a trailing window of closed candles; the live monitor
(`tradex watch`) and the measurement harness (`stratlab`) consume the exact
same detectors, so nothing is measured one way and traded another.

## Architecture

- `internal/strategy/scan/scan.go` — `Setup` (strategy-agnostic signal:
  side, entry, stop/target OR hold horizon) and the `Detector` interface
  (`Name`, `Spec`, `Validated`, `RequiredBars`, `Detect`).
- `internal/strategy/scan/detectors.go` — adapters wrapping the pure detectors
  in `internal/features`.
- `internal/strategy/scan/backtest.go` — one deterministic backtest for every
  detector. Signal knowable only at the confirming bar's close; a bar spanning
  both stop and target resolves to the STOP; costs charged per trade; results
  in R; full sample + both halves. Validated equivalent to `contlab` on
  continuation (EURUSD 1h: −0.172R, 199 trades, identical).
- `cmd/stratlab` — `-strategy {continuation|ict|amn|structuretrend|trianglebreakout|ibs}` measurement CLI.
- `cmd/tradex watch` — runs every enabled strategy against every instrument;
  each signal carries `Strategy` + `SpecVersion` + a `Validated` flag.
- Gate (`internal/notify/gate.go`) dedupe and symbol-cooldown keys include the
  strategy (two strategies may each signal one instrument); the correlated-group
  cap is per group across all strategies.

A signal ships to Telegram as a real trade only when its detector's
`Validated()` is true: positive after realistic cost, in both time halves, on
more than one instrument. Until then every signal is labelled UNVALIDATED.

## Integrated strategies and measured status

All strategies are currently UNVALIDATED. Measured net expectancy per trade
(R), realistic cost (FX 7+2 ticks, XAU 500+100, crypto 300+100 on 4h):

| strategy          | EURUSD 1h | XAUUSD 1h  | BTC 4h   | ETH 4h   | verdict |
|-------------------|-----------|------------|----------|----------|---------|
| Continuation      | −0.172R   | −0.090R    | —        | —        | negative |
| ICT (FVG) v0.1    | −0.047R   | +0.003R*   | −0.151R  | −0.182R  | no edge |
| AMN (Adeel) v0.1  | −0.113R   | −0.184R    | −0.473R  | −0.343R  | negative |
| IBS (mean rev.)   | signal-vs-drift test below — correctly signed on XAU/ETH only |

`*` XAUUSD ICT is not robust: halves disagree (−0.020 / +0.016R).

### v0.2 mechanism-fix round (1h, 2y, EURUSD/XAUUSD/USDJPY exposed data +
one genuine per-strategy holdout; see each section for detail)

| strategy              | EURUSD    | XAUUSD    | USDJPY    | holdout (fresh instrument) | verdict |
|-----------------------|-----------|-----------|-----------|------------------------------|---------|
| AMN v0.2              | +0.285R   | +0.180R   | —         | USDJPY: −0.087R (halves disagree) | not promoted |
| ICT v0.2              | +0.283R   | −0.086R   | +0.298R   | USDCAD: −0.039R (halves disagree) | not promoted |
| StructureTrend v0.2   | −0.047R   | −0.239R   | +0.185R   | USDCAD: −0.159R (both halves negative) | not promoted |

Win rate roughly doubled for AMN and ICT after the stop-buffer fix (details
below) — a real, keepable correctness improvement — but no strategy gained a
demonstrated directional edge: every holdout is negative and the AMN/ICT
holdouts disagree in sign between halves (no information, per the project's
CRT lesson), not "close."

### ICT — `ict-0.2.0` (`internal/features/ict.go`)

Fair value gap continuation: a 3-candle imbalance whose displacement broke the
local structural extreme; limit entry on the retrace into the proximal edge of
the gap, stop beyond the far edge, target prior liquidity, ≥1.5R, cost-floored.
Built because it was requested. The evidence against ICT is extensive and the
measurement above agrees: no edge on this universe. Public tests (tick-stream
7.5y NQ, statoasis 648 backtests, an SSRN G10 FX study) find order blocks,
liquidity sweeps and the Silver Bullet flat-to-negative and FVG indistinct from
noise; the one survivor, Optimal Trade Entry, reduces to "buy a pullback in a
trend" with its Fibonacci precision shown irrelevant.

**v0.2 mechanism fix**: diagnosed the same bug found in AMN — MFE-before-stop
analysis on EURUSD/XAUUSD/USDJPY 1h 2y showed 61-69% of v0.1's stop-losses
filled and stopped on the SAME bar (fixed 2-tick buffer beyond the gap's far
edge, inside ordinary post-fill noise), and roughly half of the genuine
multi-bar losers still reached ≥1.5R favorable before fully reversing. Fix:
stop buffer now scales to the measured ATR of the structure window, floored
at the old fixed minimum. Win rate roughly doubled (EURUSD 25%→46%, XAUUSD
23%→33%, USDJPY 25%→47%, USDCAD holdout 36%). EURUSD and USDJPY turned
positive in both halves on the already-exposed data, but the USDCAD holdout
(never touched before this test) came back −0.039R with halves disagreeing
in sign (−0.175R / +0.207R) — not promotable. Entry, gap detection,
structure-break filter and target unchanged from v0.1.

### AMN — `amn-0.2.0` (`internal/features/amn.go`)

The "Adeel Trades" AMN model mechanized: a clear trend (close vs SMA), a
liquidity sweep of a prior structural extreme that is then reclaimed, limit
entry at the reclaimed level, stop beyond the sweep, target prior liquidity at
≥1.5R. Adeel's discretionary "point of interest" is modelled as the reclaimed
swept level. v0.1 measured strongly negative with 9–20% win rates.

**v0.2 mechanism fix**: MFE-before-stop analysis (EURUSD/XAUUSD 1h 2y) found
61-67% of v0.1's stop-losses filled and stopped on the SAME bar — the fixed
2-tick stop buffer sat inside ordinary post-reclaim noise, not a real
invalidation distance. Even the genuine multi-bar losers reached ≥1.5R
favorable 58-81% of the time before fully reversing: a real directional read
with no way to bank the move. Fix: stop buffer now scales to the measured
ATR of the structure window, floored at the old fixed minimum (does not
touch target, entry or trend/sweep logic). Win rate roughly tripled (EURUSD
14%→42%, XAUUSD 9%→38%, USDJPY holdout 29%). EURUSD/XAUUSD turned positive
in both halves on the already-exposed data, but the USDJPY holdout (fresh,
never touched before this test) came back −0.087R with halves disagreeing in
sign (+0.064R / −0.238R) — not promotable. A follow-up multi-timeframe
variant (1h structure + 5m entry confirmation, cmd/amnmtf, research-only, not
wired into scan.Detector) gave mixed, too-small-sample results (25-32 trades
over 3 months; one instrument-half was a flat 0% win rate) and is not
trusted in either direction — likely a breakout-chase stop-placement issue,
unconfirmed.

A 5-minute-timeframe retest of AMN v0.2 (EURUSD/XAUUSD/USDJPY, ~3 months,
167/163/152 trades) was uniformly negative on all three with both halves
negative on all three (−0.122R/−0.037R/−0.329R) — shrinking the timeframe did
not help and plausibly hurt (fixed per-trade cost is a larger share of a
5m-scale stop).

### StructureTrend — `structuretrend-0.2.0` (`internal/features/structuretrend.go`)

Ports the live monitor's original "Structure Break + Trend" logic
(`internal/signal/engine.go`) into a measurable `scan.Detector`: a structure
break aligned with the prevailing swing trend, market entry at the close,
stop beyond the 10-bar recent extreme.

**v0.2 mechanism fix — different from AMN/ICT.** An MFE/whipsaw diagnostic
(`cmd/stmfe`) found only 4-9% of v0.1's stop-losses were an immediate-next-bar
stop; the tight-stop bug that hit AMN/ICT was NOT the dominant failure here.
Instead mean favorable excursion before a stop was only ~0.5R across
EURUSD/XAUUSD/USDJPY while the target sat a fixed 2.0R away — because v0.1's
target was `entry ± stop*RR`, a stop-multiple, never a traded price. That is
the exact anti-pattern this project's own geometry rules reject elsewhere
("a target must be a price that actually traded, never a multiple of the
stop") — v0.1 kept it "for parity" with the legacy live-engine logic it
replaced, explicitly flagged in the code as not yet fixed. v0.2 replaces the
target with the extreme of a trailing TargetBars (40) window, matching
AMN/ICT. Entry, stop and trend/break logic unchanged.

Result: mixed, mostly still negative. EURUSD −0.047R, XAUUSD −0.239R, USDCAD
holdout −0.159R (both halves negative, consistent sign). USDJPY alone turned
positive in both halves (+0.071R / +0.284R) — a single-instrument result,
the same trap the project's Continuation spec already documented once; the
holdout doesn't confirm it. Not promoted. The target-design fix is kept as a
correctness improvement regardless (a stop-multiple target was never
defensible under this project's own rules), but it did not create an edge
where the underlying structure-break-and-trend premise doesn't have one.

### Continuation — `continuation-0.1.0`

Checked for the AMN/ICT fixed-stop-buffer bug (MFE diagnostic,
EURUSD/XAUUSD/USDJPY 1h 2y) and does NOT have it — same-bar fill+stop was
only 21-24%, in the normal range. Mean MFE before stop (1.2-1.5R) vs target
(2.0-2.5R away) is a milder gap than StructureTrend's, not a single
dominant mechanism. No fix applied — changing code without a diagnosed
mechanism would be the parameter-tuning this project forbids. See
`docs/CONTINUATION_SPEC.md`.

### IBS — `meanrev-0.1.0` (detector) + v0.2 trend filter / exit-on-strength overlay

The only strategy in this project to clear 2 instruments with both halves
positive net of cost (XAGUSD — a genuine fresh holdout — and EURUSD, which
v0.1's raw signal found flat). Win rate 55-68%, a real improvement over
v0.1's raw 45-55%. Not yet `Validated()`: XAUUSD and ETHUSDT, the two
instruments v0.1 found promising, flip sign between halves under this same
v0.2 overlay, and BTCUSDT remains wrong-signed — 2 of 5 confirms, not the
broad confirmation needed before trusting it. See
`docs/MEANREVERSION_SPEC.md` for the full measurement, the drift-baseline
methodology, and the `stratlab` IBS R-unit miscalibration this surfaced for
high-priced instruments (a separate, pre-existing, not-yet-fixed issue).

## Promotion rule

A detector flips `Validated()` to true (and its signals drop the UNVALIDATED
label) only after `stratlab` shows positive expectancy in BOTH halves on at
least two instruments, net of realistic cost, with ≥30 trades per sample. No
strategy currently meets this bar with the confidence to flip it — IBS v0.2
is the closest (2/5 instruments on paper) but was held back deliberately
because the two instruments that confirmed under the OLD raw signal now
disagree under the new overlay; see `docs/MEANREVERSION_SPEC.md`.

## Multi-timeframe (HTF trend filter)

`internal/features/trend.go` (`DetectHTFTrend`, reusing the existing
`NewSwingDetector`/`ClassifyTrend` swing-structure classifier — not a new,
separately-unmeasured trend indicator) + `internal/strategy/scan/htf.go`
(`BuildHTFTrendSeries`, `HTFTrendAdmit`) + `scan.BacktestFiltered` let any
stop/target detector be gated on a higher-timeframe trend: a setup is only
admitted when its side agrees with the HTF trend known at `DetectedAt` (no
lookahead). `cmd/mtflab` measures baseline vs HTF-filtered side by side.

**Measured (15m entry / 1h HTF bias, EURUSD + XAUUSD, ~60 days, swing 3/2,
same costs as the table above):**

| strategy | symbol | baseline (trades, R/trade) | HTF-filtered (trades, R/trade) |
|---|---|---|---|
| Continuation | EURUSD | 14, +0.856R (halves −0.040/+1.752, not robust) | 5, +1.402R — too few trades |
| ICT | EURUSD | 33, +0.168R (halves −0.067/+0.526, not robust) | 8, −0.385R — too few trades |
| AMN | EURUSD | 32, −0.260R (negative) | 17, −0.311R — too few trades |
| Continuation | XAUUSD | 18, −0.194R — too few trades | 4, +0.052R — too few trades |
| ICT | XAUUSD | 22, −0.284R — too few trades | 7, −0.104R — too few trades |
| AMN | XAUUSD | 38, +0.058R (halves −0.244/+0.176, not robust) | 16, −0.102R — too few trades |

**Verdict: not promotable.** The filter mechanically works (verified
no-lookahead, admits/rejects correctly) but on this sample it mostly just
cuts the trade count below the 30-trade measurability floor without fixing
the underlying sign problem, and where a sample survives it doesn't clear
both-halves-positive either. Kept as a measurement tool (`cmd/mtflab`) and a
reusable primitive (`scan.HTFTrendAdmit`/`BacktestFiltered`), not wired into
`tradex watch` — deploying it live would repeat the exact mistake the
forward-test logs already exposed (running unvalidated strategies and
calling the resulting losses a surprise). Re-run with other HTF/LTF pairs,
swing windows or instruments if a real edge is still suspected; the tool
makes that a one-command measurement instead of a redeploy-and-wait.

## CRT follow-up: multi-timeframe sweep/reclaim, tested two ways (2026-11-12)

CRT itself was removed from the live codebase (see the commit that replaced
it with the current IBS/Continuation/ICT/StructureTrend/TriangleBreakout
suite; the original postmortem, `docs/CANDLE_RANGE_SPEC.md`, is preserved in
git history at `035e5a5^:docs/CANDLE_RANGE_SPEC.md`). Two follow-up variants
were tested ad hoc (Python, not merged into the Go codebase — this is a
record of the result, not new production code) after a user proposal to add
a specific multi-timeframe entry refinement: sweep+reclaim on a higher
timeframe, wait one confirmation candle, then drop to a lower timeframe for
entry, stop at the confirmation candle's wick, target at the swept candle's
opposite extreme. EURUSD, XAUUSD, US100; 1h setup / 15m entry; real spread
cost; both-halves split.

**Variant 1 — generic swing-anchored sweep, market entry.** Support/resistance
= nearest confirmed 1h swing high/low (no clock-time or liquidity-confluence
requirement), entry = market order at the next 15m bar after the third
1h candle begins. 680-726 trades per instrument over ~15 months — negative
on all three, and unusually *consistent* between halves (not sign-flipping,
unlike the original CRT v0.1 postmortem):

| Instrument | Trades | Win% | Full edge | 1st half | 2nd half |
|---|---|---|---|---|---|
| EURUSD | 726 | 21.3% | −2.16bp | −2.19bp | −2.13bp |
| XAUUSD | 684 | 25.6% | −6.52bp | −6.67bp | −6.37bp |
| US100 | 680 | 31.6% | −3.02bp | −2.46bp | −3.57bp |

Negative even gross, before any cost (EURUSD −0.16bp, XAUUSD −1.52bp, US100
−1.02bp) — not a cost story, no directional edge in the raw signal.

**Variant 2 — the inventor's actual rules** (Romeotpt/TTrades' "Candle Range
Theory": fixed kill-zone-hour range candle — not a generic swing — an
accumulation/quiet-period filter, a confluence requirement against the prior
day's extreme, and a genuine lower-timeframe market-structure-shift entry,
i.e. price must close through a confirmed 15m swing level in the reversal
direction, not a blind market order). Stacking every filter on one fixed
hour collapsed the sample to 0-9 trades per instrument — unmeasurable (one
9-trade XAUUSD cell showed a flattering 77.8% win rate; not reported as a
result, exactly the small-sample mirage the project's ≥30-trade floor exists
to block). Pooling across six commonly-cited kill-zone hours (2, 3, 5, 8, 9,
13 NY) restored a measurable sample:

| Instrument | Trades | Win% | Full edge | 1st half | 2nd half |
|---|---|---|---|---|---|
| EURUSD | 60 | 50.0% | −1.30bp | −2.22bp | −0.38bp |
| XAUUSD | 69 | 44.9% | −5.51bp | −5.01bp | −5.99bp |
| US100 | 70 | 61.4% | −4.78bp | −4.55bp | −5.00bp |

Still negative, still consistent both halves, on all three. The real
structure-shift entry roughly doubled the win rate versus the blind-entry
variant (21-32% -> 45-61%) — it does filter out a meaningful share of false
reclaims — but expectancy didn't follow: wins got smaller and losses bigger
with the later, tighter entry, netting to the same negative conclusion.
Gross (no cost): EURUSD ≈ 0 (+0.70bp), XAUUSD ≈ 0 (−0.51bp), US100 mildly
negative (−2.78bp) — again not primarily a cost problem.

**Verdict: no promotable edge in either variant.** Recorded so the next
"add multi-timeframe confirmation to CRT" idea isn't re-tested from zero —
the multi-timeframe refinement changes *which* trades you take (higher win
rate, different R distribution) without changing the sign of the answer.
