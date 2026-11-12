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

## RSI(2) mean reversion: a genuinely new, confirmed, DEPLOYED strategy (2026-11-12)

The first new strategy family to clear this project's promotion bar since
IBS. Connors' 2-period RSI, same v0.2 overlay structure as IBS (200-day
trend filter, exit-on-strength, 10-day max hold) so the two are directly
comparable: `features.RSI2Bps`, `scan.RSI2`, measured with a new tool
`cmd/rsi2v2` (mirrors `cmd/ibsv2` exactly).

**A real self-correction mid-measurement, worth recording as a lesson**:
a first-pass check (ad hoc Python, no drift-adjustment) showed 6
instruments confirming (EURUSD, US100, US500, NATGAS, PLATINUM, BRENT).
Re-measured properly in Go with the SAME drift-adjustment IBS's own
numbers always use (forward return minus the unconditional baseline for
the same hold length) plus real swap cost, only **3 survive**:

| Instrument | Trades | Win% | Full edge (net of swap) | 1st half | 2nd half |
|---|---|---|---|---|---|
| US100 | 260 | 71.9% | +25.50bp | +24.42bp | +26.13bp |
| **US500** | 272 | 71.7% | +19.33bp | +4.99bp (thin) | +28.49bp |
| PLATINUM | 156 | 71.2% | +74.67bp | +26.84bp | +97.23bp |

EURUSD flips sign between halves once real swap is included (+5.00bp /
−4.35bp) — excluded. NATGAS and BRENT were only "confirmed" in the
non-drift-adjusted first pass and don't survive proper measurement —
excluded. **US500 is a genuinely new instrument** — not in any live IBS
config.

Signal-day overlap with IBS on the shared instruments (US100, PLATINUM) is
only 21-39% — most RSI2 trade days are genuinely new, not a restatement of
IBS's own signal (checked explicitly before trusting this as incremental
frequency rather than double-counting).

**A serious bug caught and fixed while wiring this in**: `cmd/tradex
execute.go`'s entry-detection loop checked `det.Name() != "ibs"`
(lowercase), but `scan.IBS.Name()` returns `"IBS"` (uppercase) — the
comparison has NEVER matched since `execute.go` was built. Every signal
this whole time was correctly managed on the exit side (existing open
positions) but **the live demo system has never been capable of detecting
a single new entry**, regardless of real market conditions. This explains
why zero trades fired even when EURUSD was within ~2 percentage points of
its IBS threshold. Fixed with a case-insensitive, two-strategy-aware check.

**Deployed**: `docs/examples_watch_forward_daily_rsi2_v2.json` (US100,
US500, PLATINUM, long only) now runs alongside the two IBS configs on
demo. `internal/execution/sizing.go` extended with `MeasuredRSI2Stats` and
a generic `riskPct` helper shared by `HalfKellyRiskPct`/`RSI2RiskPct`.
`cmd/tradex/execute.go` generalized to size and manage either strategy
(oscillator dispatch via `currentOscillatorBps`, matching on the
`ibs_`/`rsi2_` prefix of each position's `HoldExitRule`).

## Confluence mean reversion: IBS and RSI(2) agreeing (2026-11-12)

A genuinely new idea, not another parallel strategy: split every IBS-or-
RSI2 signal day into three buckets (IBS fires alone, RSI2 fires alone,
both fire on the same symbol same day) and measure each separately. On
every instrument tested, the "both fire" bucket beat BOTH individual
signals, both halves positive:

| Instrument | IBS alone | RSI2 alone | **Confluence (both fire)** |
|---|---|---|---|
| US100 | 107 trades, +18.45bp | 163 trades, +13.28bp | **96 trades, +30.49bp** (halves +26.35/+34.63) |
| PLATINUM | 78 trades, +56.84bp | 107 trades, +48.81bp | **50 trades, +84.08bp** (halves +35.97/+132.19 — thinner, ~25/half, right at the floor) |
| XAUUSD (gold) | sign flip, not robust | +11.75bp, consistent | **89 trades, +22.32bp** (halves +27.68/+17.07) |
| GBPUSD | negative | negative, sign flip | **74 trades, +11.66bp** (halves +8.28/+15.05 — ~37/half, at the floor) |
| USDJPY | negative | sign flip | sign flip (+16.94/-9.17) — **excluded** |
| BTCUSDT (Binance) | negative | +44.30bp, consistent | sign flip (-1.62/+53.39) — **excluded** |

**XAUUSD and GBPUSD are the interesting cases**: neither IBS nor RSI2
confirms alone on either instrument, but their agreement does — confluence
rescued two instruments neither individual signal could confirm by
itself. US100 and PLATINUM get a size BOOST on top of their existing
single-signal trades; XAUUSD and GBPUSD are CONFLUENCE-ONLY (never trade
on either signal alone, since neither is individually confirmed there).

**What was explicitly tried and rejected before this**: a confirmation-
entry refinement (wait for an actual up-close before entering, the same
LTF-confirmation idea that helped CRT/APA's win rate) was tested on
IBS/RSI2 directly and made 6 of 7 cases WORSE, flipping PLATINUM's IBS
and RSI2 both from strongly positive to negative — mean reversion's edge
lives in entering AT the extreme, not after proof it's turning. Also
tested and rejected: whether ICT/APA's edge is just trend-following in
disguise (replicated only on EURUSD, contradicted on XAUUSD/US100).

**Implementation** (`internal/execution/sizing.go`,
`cmd/tradex/execute.go`): `MeasuredConfluenceStats`,
`ConfluenceOnlySymbols` = {XAUUSD, GBPUSD}, `ConfluenceBoostSymbols` =
{US100, PLATINUM}, `ConfluenceRiskPct`. The entry-detection loop now
evaluates BOTH detectors per symbol (not break-on-first-match) and
branches: confluence-only symbols require both to fire; boost symbols
trade normally on either alone and get the confluence size when both
agree; a combined position exits on EITHER oscillator's own threshold
(`confluence_strength_above`, hardcoded OR check — a single generic
threshold can't express two different oscillators' two different
thresholds).

**Deployed**: `docs/examples_watch_forward_daily_confluence_v2.json`
(US100, PLATINUM, XAUUSD, GBPUSD; strategies `["ibs","rsi2"]`) replaces
the former separate `ibs_v2`/`rsi2_v2` coverage of US100/PLATINUM (those
two symbols moved out of `ibs_v2.json`/`rsi2_v2.json`, which now only
covers EURUSD/NATGAS and US500 respectively). GOLD's real swap/margin
verified live (-1.56/+0.74bp/day, margin 5%, min deal 0.01); GBPUSD's
too (-0.40/-0.42bp/day, margin 3.33%, min deal 100).

**Operational note**: the watchdog cron job still referenced the
retired `ibs_v2.json` at the moment of the cutover and restarted it
once before the script update landed — caught and stopped within
minutes; `scripts/watchdog.sh` is now current.

## Crypto RSI2, re-verified on Capital.com's own feed (2026-11-12)

RSI2 looked strong on BTCUSDT/ETHUSDT via Binance's data (+41-68bp,
consistent both halves) — but Binance and Capital.com's BTCUSD/ETHUSD
CFDs are genuinely different instruments (different venue, different
price series), and this project already has a standing rule never to
conflate them. Re-measured on Capital.com's OWN 6-year feed, with real
daily swap cost (0.0616%/day both symbols -> 6.16bps/day):

| Instrument (Capital.com feed) | Trades | Win% | Edge | Halves |
|---|---|---|---|---|
| **BTCUSD** | 257 | 63.8% | **+39.26bp** | **+25.04 / +53.38 — CONFIRMS** |
| ETHUSD | 227 | 64.3% | +42.63bp | -1.99 / +86.85 — sign flip, rejected |

ETHUSD's Capital.com result does NOT match its Binance result — exactly
the scenario the existing "crypto research stays on Binance, never
conflated with Capital.com" rule exists to catch. Only BTCUSD is real on
the actual tradable venue.

**Deployed**: `BTCUSD`/`ETHUSD` added to `internal/instrument` as new,
separate symbols from `BTCUSDT`/`ETHUSDT` (never merged). Only BTCUSD
added to `MeasuredRSI2Stats` and traded, via
`docs/examples_watch_forward_daily_rsi2_btc_v2.json`.

**Live demo processes as of 2026-11-12, end of day**: `ibs_longonly_v2`
(EURUSD, NATGAS), `rsi2_v2` (US500), `confluence_v2` (US100, PLATINUM,
XAUUSD, GBPUSD), `rsi2_btc_v2` (BTCUSD). Watchdog covers all four.
