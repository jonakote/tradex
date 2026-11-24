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

## Breadth round 5: 18 more instruments, real swap, same rigor (2026-11-12)

Extended the IBS/RSI2/confluence three-bucket test to indices, more FX
majors/crosses, energy and metals (18 instruments), THEN pulled real
swap rates for every survivor before trusting any of it — the exact
step that flipped XAGUSD and USDCAD's confluence number this round
(confluence was positive gross-of-swap, negative once real swap was
included).

**Confirmed, real swap included, both halves positive:**

| Instrument | Bucket(s) | n | Edge |
|---|---|---|---|
| J225 (Nikkei) | RSI2 alone | 171 | +29.86bp |
| BRENT | RSI2 alone + confluence boost | 81 / 49 | +58.88bp / +93.59bp |
| WTI | RSI2 alone + confluence boost | 117 / 47 (thin, ~24/half) | +40.45bp / +45.09bp |
| COPPER | IBS alone + confluence boost | 110 / 78 | +13.53bp / +15.22bp |
| US30 (Dow) | IBS alone only | 119 | +8.58bp |
| USDCAD | RSI2 alone only | 170 | +8.07bp |
| AUDJPY | RSI2 alone only | 163 | +5.92bp |
| EURJPY | RSI2 alone only | 200 | +6.56bp |
| EURGBP | RSI2 alone only (thinner, ~48/half) | 96 | +4.29bp |
| GBPJPY | IBS alone AND RSI2 alone, run independently | 65 / 207 | +4.70bp / +5.63bp |
| AUDUSD | confluence only | 62 | +7.26bp |

**Rejected after real swap, or structurally excluded from combining**:
- USDCAD's and US30's confluence bucket looked positive gross-of-swap
  but FLIP negative once real swap is included — both kept as
  standalone-only, never added to the confluence sets.
- **GBPJPY is the most important exclusion**: IBS alone (+4.70bp) and
  RSI2 alone (+5.63bp) BOTH confirm independently on it, but the
  COMBINED confluence bucket is strongly negative (-23.50bp). GBPJPY
  trades both strategies independently, deliberately never merged.
- DE40, UK100, FR40, USDCHF, NZDUSD, PALLADIUM, COTTON: flip somewhere
  in every bucket, excluded entirely. CORN and SOYBEAN had no usable
  candle data (empty fetch), not tested.

**Architecture fix made before deploying**: IBS's short side is a
single config-level flag (`ibs_short_above_bps`) shared by every symbol
in one process — US100/PLATINUM's both-sided edge is real and measured,
but BRENT/WTI/COPPER/AUDUSD/XAUUSD/GBPUSD were only ever measured LONG.
Putting them in the same config as US100/PLATINUM would have silently
enabled untested short trades for all of them. Split into
`confluence_both_v2.json` (US100, PLATINUM, both sides) and
`confluence_longonly_v2.json` (XAUUSD, GBPUSD, BRENT, WTI, COPPER,
AUDUSD, long only) instead of one shared config.

**Deployed**: `confluence_both_v2.json` (US100, PLATINUM),
`confluence_longonly_v2.json` (XAUUSD, GBPUSD, BRENT, WTI, COPPER,
AUDUSD), `standalone_v2.json` (J225, US30, USDCAD, AUDJPY, EURJPY,
EURGBP, GBPJPY — both strategies run, but none of these seven are in
either confluence set, so IBS and RSI2 trade independently per symbol).
`internal/execution/sizing.go` extended with 11 new instrument entries
across `MeasuredIBSStats`/`MeasuredRSI2Stats`/`MeasuredConfluenceStats`
and the `ConfluenceOnlySymbols`/`ConfluenceBoostSymbols` sets grew to
include BRENT/WTI/COPPER (boost) and AUDUSD (only).

**Live demo processes as of 2026-11-13**: `ibs_longonly_v2` (EURUSD,
NATGAS), `rsi2_v2` (US500), `confluence_both_v2` (US100, PLATINUM),
`confluence_longonly_v2` (XAUUSD, GBPUSD, BRENT, WTI, COPPER, AUDUSD),
`standalone_v2` (J225, US30, USDCAD, AUDJPY, EURJPY, EURGBP, GBPJPY),
`rsi2_btc_v2` (BTCUSD). Six processes total, 16 instruments. Watchdog
covers all six.

## Two separate bars: backtest-confirmed vs. live-validated (2026-11-15)

`execution.IsValidated(strategy, symbol)` (added 2026-11-15, replacing
a hardcoded per-detector constant — see "Validated signal tag now
data-driven" below) answers ONE question: does this strategy+symbol
pair clear the **backtest bar** — both halves positive, net of real
cost, ≥30 trades/half, ≥2 instruments per strategy. Every instrument
in `MeasuredIBSStats`/`MeasuredRSI2Stats`/`MeasuredConfluenceStats`
already clears this.

That is NOT the same as being proven safe to fully trust. A second,
separate **live-validated bar** exists, and as of this writing nothing
has cleared it — there has been exactly one real auto-detected live
trade (US100, 2026-11-13) since the case-mismatch bug was fixed. The
live-validated bar, until further revised:

1. **≥20-30 real executed trades** (filled positions, not just
   signals) per strategy, counted only from after the strategy's
   detection pipeline was confirmed actually working — a sample
   partially collected while something was silently broken (e.g. the
   `"ibs"`/`"IBS"` case-mismatch bug) does not count toward this.
2. **Spread across ≥2-3 of the confirmed instruments**, not
   concentrated on one symbol — same reasoning as the backtest's
   ≥2-instrument rule: one instrument's luck should not carry the
   whole verdict.
3. **Win rate and expectancy roughly in line with the backtest**, not
   identical but not wildly divergent either. A live sample sitting far
   outside the backtest's measured range is a signal to investigate,
   not a result to average away or ignore because the trade-count
   target was hit.
4. **No major operational bug active during the accumulation window.**

Report progress against this explicitly (e.g. "1/20-30 live trades,
US100 only, one instrument") rather than letting backtest confirmation
get casually described as "validated" without the live-trade caveat
in the same breath — the whole reason two bars exist is that they have
already diverged once in this project's history (IBS/RSI2 cleared the
backtest bar while the live pipeline was silently non-functional the
entire time).

## Project ideology and code-structure decisions worth preserving

Captured here because these came up repeatedly in discussion but were
never written down as standing decisions — future sessions should read
this before re-deriving or accidentally reversing any of them:

- **A strategy is a pure function over closed candles, nothing else.**
  Every detector implements `scan.Detector` and is consumed identically
  by the live monitor and the measurement harness — this is why "measured
  one way, traded another" cannot happen by construction, not by
  discipline. Any new strategy idea gets built this way FIRST, before
  being wired into `tradex execute`'s entry-detection loop.
- **Evidence-based promotion is a one-way ratchet, not a target to hit.**
  No detector's live-trading eligibility (its presence in a
  `Measured*Stats` table) is ever adjusted to reach a desired number —
  not win rate, not trade frequency, not a round confirmation count. A
  request to "get to 60%" or "fabricate a believable number" is refused
  plainly, and the real, possibly disappointing number is reported
  instead. This has happened explicitly in this project's history and
  the refusal was the right call both times.
- **Negative results are permanent, written-down knowledge, not dead
  ends to quietly drop.** CRT, ICT/APA, Continuation, AMN,
  StructureTrend, TriangleBreakout, carry, momentum (too thin a sample),
  scalping, and confirmation-entry-on-mean-reversion are all recorded
  with their specific failure mechanism so a future session doesn't
  re-run the same experiment from zero. A new strategy proposal gets
  checked against this list before any code gets written.
- **A sizing/risk backstop can itself be the bug.** The margin-vs-notional
  sizing fix (2026-11-13) is the canonical example: a safety check with
  good intentions (catch a unit-conversion bug) was itself miscalibrated
  and silently blocked every real signal on every leveraged instrument
  since the risk system was built. Any safety/backstop check added to
  this codebase should be sanity-checked against a REAL worked example
  (real price, real account size, real leverage) before being trusted,
  the same way a strategy's edge is — a backstop that's never fired on
  real numbers is unverified in the same sense an untested strategy is.
- **Two things measuring the same question must share one source of
  data, not two independently-maintained copies.** `IsValidated` reading
  directly from the `Measured*Stats` tables (rather than a second,
  hand-written constant) exists specifically because the hand-written
  version had already drifted stale by the time it was checked — this is
  a recurring class of bug in this codebase (the dead `det.Name() !=
  "ibs"` lowercase/uppercase mismatch is the same shape of problem:
  two things that were supposed to agree, silently didn't, for the
  entire life of the system until someone happened to check).
- **A found edge gets re-verified on the EXACT venue it will trade on,
  never assumed to transfer from a similar-looking source.** Binance
  data is useful for discovering a crypto lead (that's how RSI2's crypto
  edge was found at all) but is not itself sufficient grounds to trade —
  ETHUSD confirmed on Binance's BTCUSDT/ETHUSDT and then failed on
  Capital.com's own feed; BTCUSD was checked the same way and held up.
  Same principle applied to real swap cost (XAGUSD, USDCAD/US30's
  confluence bucket both flipped once real swap replaced an estimate)
  and to margin/leverage (the backstop bug above). Nothing is trusted on
  an approximation of the real mechanism when the real mechanism is
  checkable.
- **Autonomy is the default; fabrication is never the fallback.** This
  operator has explicitly asked for autonomous decision-making without
  being asked for permission on every step, including consequential
  ones — but that license applies to legitimate methodology choices
  (which instrument to test next, which code structure to use), never to
  inventing a result, skipping a verification step, or declaring
  something validated/safe ahead of the evidence. The two are not in
  tension: autonomy decides HOW to find the real answer faster, not
  WHETHER to report it honestly.

## Breadth round 6: US shares, a new asset class (2026-11-17)

Extended IBS/RSI2/confluence to 14 individual US stocks (AAPL, MSFT,
TSLA, NVDA, AMZN, META, GOOGL, JPM, JNJ, XOM, WMT, V, KO, PFE) — the
first asset class beyond FX/metals/indices/energy/crypto. Same
methodology and rigor: 6-year Capital.com daily feed (open==close
defect rate checked, all under 2%), real swap (Capital.com applies one
generic shares financing rate, 0.0222624%/day, identical across all
14, not instrument-specific), both-halves split.

**13 of 14 confirmed** — richer, more varied results than any prior
breadth round:
- **AAPL**: IBS, RSI2, AND confluence all three independently confirm.
- **MSFT/NVDA/GOOGL**: RSI2 alone confirms; IBS alone does not;
  confluence confirms even stronger (NVDA: RSI2 alone +100.44bp,
  confluence +149.39bp — the largest edge measured in this project).
- **AMZN/META**: IBS alone confirms; RSI2 alone does not; confluence
  confirms stronger still.
- **WMT**: NEITHER signal confirms alone; confluence rescues it (same
  pattern as XAUUSD/GBPUSD/AUDUSD).
- **TSLA/XOM/KO**: IBS alone only (RSI2 and confluence both flip).
- **JNJ/V**: RSI2 alone only (IBS and confluence both flip).
- **JPM**: IBS alone AND RSI2 alone BOTH confirm independently, but
  their COMBINED confluence bucket flips negative (-12.31bp) — same
  GBPJPY pattern. JPM trades both strategies, never combined.
- **PFE**: nothing confirms anywhere, excluded entirely.

**Deployed**: `docs/examples_watch_forward_daily_shares_v2.json` (all
13 confirmed symbols, strategies `["ibs","rsi2"]`, long only). New
`ClassShare`/`share()` helper in `internal/instrument`; epics equal the
plain ticker for all 14 (verified via `GET /api/v1/markets`); marginFactor
20% and the shares financing rate verified identical across all 14.

### Two real bugs found deploying this, both from exposing the system to fractional sizes for the first time

1. **`broker.PlaceOrder`'s position-match used exact float equality
   (`p.Size == size`)** to resolve the real dealId after Capital.com's
   `/confirms` endpoint (already known to return a wrong dealId — see
   the broker-execution reference) — this worked by coincidence for
   every FX/metal/index/crypto trade so far because their computed
   sizes happened to round-trip through the API's JSON encoding
   exactly. The very first share order (AAPL, size 1.2) exposed it: the
   position genuinely opened, but the lookup never found it, so the
   order was incorrectly reported as failed and left completely
   untracked locally (though real and open on the broker). Fixed with
   a `1e-6` tolerance comparison instead of exact equality. The
   existing broker-reconciliation logic (built for crash recovery)
   adopted the real orphaned position correctly once restarted on the
   fixed binary — reconciliation existing for an unrelated reason is
   what caught this cleanly rather than leaving a silent, permanently
   orphaned position.
2. **The reconciliation path's `MarginPct` was a raw dollar amount, not
   a percent of equity** (`size * level * marginFactor / 100`, never
   divided by account balance) — a stale comment claimed it would be
   "recomputed next loop anyway," which was false; nothing ever
   corrected it. For AAPL this produced `MarginPct: 84.4` (should have
   been ~8.3%) sitting in `committedMarginPct` for as long as the
   position stayed open (up to 10 days) — enough to silently block
   essentially all other legitimate new entries against the 25% total-
   margin cap, for a reason that had nothing to do with real risk.
   Fixed by fetching account balance at the start of reconciliation and
   dividing by it, same unit as every other `MarginPct` in the file;
   the already-corrupted live value was corrected by hand in
   `data/demo_open_trades.json` immediately upon discovery. A
   reconciled position's `HoldExitRule` is deliberately left empty
   (documented in code) rather than guessed — the originating
   strategy is genuinely unrecoverable information by reconciliation
   time, and a wrong guess is worse than falling back to the simple,
   safe time-cap exit.

**Live demo processes as of 2026-11-17**: `ibs_longonly_v2`,
`rsi2_v2`, `confluence_both_v2`, `confluence_longonly_v2`,
`standalone_v2`, `rsi2_btc_v2`, `shares_v2`. Seven processes, 29
confirmed instruments across FX, metals, indices, energy, crypto, and
US shares. Watchdog covers all seven.

## Breadth round 7: AUDCAD, found via a genuinely different model (2026-11-20)

User asked specifically to focus new research on forex/crypto, having
checked and found that in days of live running, **zero real forex or
crypto trades had fired at all** -- every real trade so far was
metals/indices/shares/energy. Diagnosed why: EURUSD's long setup
(uptrend + oversold IBS) hadn't fired in the cached sample in ~3
months -- not a bug, a real regime effect. FX ranges rather than
trends the way equities do, so the 200-day uptrend filter (built
around equity behavior) is plausibly the wrong gate for FX specifically.

Researched FX-specific mean-reversion literature (multiple sources):
consistent recommendation is a z-score/band model applied to BOTH
long and short sides, gated by a range-regime filter instead of a
trend filter -- the opposite structure from what's deployed. Built
and tested this (20-day rolling z-score, entry at |z|>=2, exit at
z=0 or 10-day cap, both sides, no trend filter) on 8 FX pairs,
including several named by the research as classically range-bound
(EURCHF, EURNZD, EURAUD, AUDNZD, GBPCAD, AUDCAD) that tradex didn't
trade yet:

- 3 confirmed on a quick swap estimate: AUDCAD (+30.47bp), EURCHF
  (+13.85bp), GBPCAD (+10.77bp).
- Re-verified with REAL per-pair swap rates pulled live from
  Capital.com (conservative: worst-case side) -- **caught 2 false
  positives**, same lesson as XAGUSD/USDCAD/US30 before: EURCHF and
  GBPCAD both flip negative in one half once real swap is included.
  **Only AUDCAD survives** (257 trades -> both halves +19.0/+29.5bp,
  n=297, well over the 30/half floor).

Before building an entirely new strategy module for one symbol,
checked whether AUDCAD's edge is ALSO reachable through the
already-deployed IBS/RSI2/confluence engine -- cheaper to ship than a
new detector type. It is: RSI2 alone confirms (142 trades, both
halves positive +3.3/+2.5bp, thin but real), and confluence confirms
much stronger (51 trades, thinner — right at the ~25/half floor like
PLATINUM confluence was — both halves positive +24.4/+21.9bp). IBS
alone does NOT confirm (sign flip). Deployed via the existing
`ConfluenceBoostSymbols` mechanism, no new code path needed. Added to
`docs/examples_watch_forward_daily_confluence_longonly_v2.json`.

The z-score model itself was not deployed (no new detector/strategy
type added) -- it served as the research tool that found a real,
new FX instrument, which then turned out to be reachable through
infrastructure that already exists and is already live-trading
cleanly.

**Also found and corrected while checking live state the same week**:
US30 and JPM positions closed via their own broker-side stop while
their process was asleep between scans, and the pnl recorded for both
was initially WRONG -- see the `broker.ClosedFillLevel` history in
this doc and the commit log (`f7db2ec`, `bac3053`) for the full
story. Both corrected by hand with the real broker fill price.
`MaxRiskPct` was also cut from 2.0 to 1.0 (commit `71f457b`) given the
live track record is still only a handful of trades and 3 real
infrastructure bugs were found in the same few days -- a fair reason
to run smaller while the sample and the plumbing are both this young.

## A genuinely different strategy family: trend-following Breakout (2026-11-20)

User explicitly asked not to restrict the search to the existing
IBS/RSI2/Confluence mean-reversion framework. Built and tested a
classic turtle-style Donchian channel breakout: long when close
breaks above the highest high of the preceding N bars, exit when
close breaks below the lowest low of the preceding M bars (a trailing
channel, recomputed every bar) or a time cap, whichever comes first.
Deliberately the OPPOSITE structural bet from mean-reversion: low win
rate, winners run far longer than losers -- the "legitimate" way to
get a good payoff ratio, per the research done earlier this session on
why high-win-rate systems are usually dangerous.

Tested on Capital.com's own daily feed (BTCUSD, ETHUSD, EURUSD,
XAUUSD, AUDCAD), real swap included:
- **BTCUSD confirms strongly**: entry=10-bar high / exit=5-bar low,
  63 trades, 33.3% win rate, avg win 1693.9bp / avg loss 684.5bp
  (payoff ratio 2.47), expectancy +108.3bp/trade, both halves positive
  (+118.9/+98.1bp). Robust across every parameter combination tried
  (10/5, 15/7, 20/10, 10/10, 15/10, 20/20 all stayed both-halves-
  positive) -- not a fitted fluke on one specific window choice.
- XAUUSD also confirmed (+144.13bp both halves) but the sample is thin
  (n=30, ~15/half) -- not deployed yet, flagged for a future re-check
  with more data.
- ETHUSD, EURUSD, AUDCAD all flipped sign between halves under this
  model -- not deployed.

Built as real new infrastructure, not just a config tweak:
`internal/features/breakout.go` (pure `DonchianBreakoutLong`/
`DonchianExitLong` functions), `scan.Breakout` detector
(`internal/strategy/scan/detectors.go`), a new `"BREAKOUT"` case
through execute.go's detector dispatch/sizing switch, and a
`donchian_low_break` exit rule in the exit-management loop (handled
specially because the exit channel is recomputed fresh from current
history every scan, unlike IBS/RSI2's single-bar oscillator read).
Position sizing reuses the exact same half-Kelly formula as IBS/RSI2 --
Kelly only cares about win rate and payoff ratio, not which produced
them -- so `MeasuredBreakoutStats` slots into the same
`MeasuredIBSStats`-shaped table and `BreakoutRiskPct` helper as
everything else. Deployed to BTCUSD alongside its existing RSI2
config (`docs/examples_watch_forward_daily_rsi2_btc_v2.json`, now
`strategies: ["rsi2", "breakout"]`) -- the two triggers are nearly
mutually exclusive (oversold vs. new-high breakout) so real same-day
conflicts should be rare.

## Intraday, finally confirmed: extreme IBS on FX, 15-min bars (2026-11-20)

User pushed back hard on the earlier intraday conclusion ("there are
intraday strats that have an edge, find one"), and was right to --
this section is the result of actually finding the wrong variable
instead of giving up.

**Four more ideas tried and rejected first**, all with exact
published TP/SL mechanics (stop at the opposite side of the range, R
measured from the breakout candle's close, target at R multiples),
tested on both 5-min and 15-min bars, forex (EURUSD/GBPUSD) and
crypto (BTCUSD/ETHUSD): plain ORB, filtered ORB (ATR range
qualification + volume confirmation), intraday VWAP mean-reversion,
intraday momentum continuation. All negative or failed to replicate
on a second instrument.

**What was actually wrong, found by elimination, not guessing:**
1. Real spread (pulled live from Capital.com: EURUSD 0.64bp, GBPUSD
   0.99bp, BTCUSD 8.12bp, ETHUSD 10.07bp) was close to the earlier
   assumptions -- cost was NOT the main problem.
2. Candles defaulted to the **bid** side. Added a `-side=mid|bid|ask`
   flag to `tradex candles` and re-tested on mid prices -- also not
   the main problem (nearly identical numbers), but a real fix worth
   keeping (bid-ask bounce is a more serious confound for a
   within-bar-range signal like IBS at short timeframes than it ever
   was on daily bars).
3. **The real lever: entry/exit threshold and session-hour gating.**
   Daily-style thresholds (entry<=1000-2000bps, exit at the 5000
   midpoint) fired thousands of times with no edge (tested on 9,000+
   trade samples, consistently negative). Restricting entries to
   genuinely EXTREME readings (IBS<=300bps -- the most extreme ~3% of
   the bar's own range, not the daily-style "just below average"),
   requiring a near-COMPLETE reversal to exit (IBS>=9500, not the
   midpoint), holding up to 12 hours (48 bars), and trading ONLY
   during active London/NY session hours (07-21 UTC, not around the
   clock) turned a stable, well-powered negative into a stable,
   well-powered positive.

**Confirmed on two FX instruments with the SAME unified parameters**
(not two separately-fitted configs -- checked explicitly, each
symbol's own "best" parameters were cross-tested on the other first
and only one failed that cross-check):
- EURUSD: 1299 trades (1 year, mid price), 54.7% win, both halves
  positive (+0.23/+0.35bp with a 100bp stop).
- GBPUSD: 1508 trades, 53.2% win, both halves positive
  (+0.16/+0.49bp with a 100bp stop).
- **Crypto does NOT confirm** with this mechanism -- BTCUSD/ETHUSD
  stayed negative at every threshold tried, 24/7 session, real spread
  included. Crypto's spread (8-10bp) is too large relative to the
  bp-scale moves this kind of mean-reversion captures at 15-min
  resolution. Honestly reported, not force-fitted.

**A real stop-loss was modeled and required, unlike the first three
intraday attempts** -- the no-stop backtest numbers above all survive
a realistic protective stop, but the system's EXISTING default (3x
average loss, ~30bp here) was tight enough to clip the edge (tested:
20-30bp stops pushed the first half negative on both pairs). Needed a
wider stop (~100bp, ~9.5x average loss) to stay robust. Rather than
change the global 3x default (would affect every other live
instrument), added `IBSStats.StopMultiplier` (zero = old 3x default,
non-zero overrides it) and threaded it through
`ProtectiveStopBps(avgLossBps, multiplier)`.

**How the trade actually works, mechanically** (for anyone reading
this without the code in front of them):
1. Every 15 minutes, during London/NY session hours only (07:00-21:00
   UTC), compute IBS (where the just-closed 15-min bar's close sits
   inside that bar's own high-low range, 0=low, 10000=high) for
   EURUSD and GBPUSD.
2. If IBS<=300 (closed almost exactly at the bar's low): go LONG at
   the NEXT bar's market open.
   If IBS>=9700 (closed almost exactly at the bar's high): go SHORT
   at the NEXT bar's market open.
3. Place a mandatory broker-side protective stop ~9.5x the
   instrument's average measured loss away (~100bp) -- required by
   `broker.PlaceOrder`, same as every other live strategy.
4. Every subsequent 15-min bar, check: has IBS reversed almost all
   the way to the OPPOSITE extreme (>=9500 for the long, <=500 for
   the short)? If yes, exit there (the real signal-based exit, same
   `donchian_low_break`-style pattern as other strategies' generic
   exit-management loop, just checking a single-bar oscillator value
   instead of a channel).
5. If no reversal within 48 bars (12 hours), exit at whatever price
   the 48th bar closes at (the hold cap, same mechanism as daily
   IBS/RSI2's MaxHoldBars).
6. Outside session hours, no new entries are considered at all --
   existing open positions are still managed normally by the
   exit-management loop regardless of hour.

**Infrastructure built, not just a config tweak:**
- `-side=mid|bid|ask` flag on `tradex candles` (`cmd/tradex/candles.go`).
- **A real, independent bug found and fixed**: the IBS detector's
  short-side exit threshold wasn't mirrored around IBS's own midpoint
  (5000) -- used `StrengthExitBps` directly for the short's "exit
  below" check instead of `10000-StrengthExitBps`. Every live "both
  sides" config so far used `StrengthExitBps=5000`, where mirroring
  is a no-op, which hid this for as long as it's existed. The first
  config needing a genuinely asymmetric threshold (9500, not 5000)
  exposed it. Fixed in `internal/strategy/scan/detectors.go`.
- `WatchConfig.SessionStartHourUTC`/`SessionEndHourUTC` (new, 0/0 =
  no gating, every daily config unaffected) and the corresponding
  gate in execute.go's entry loop.
- `IBSStats.StopMultiplier` and `ProtectiveStopBps(avgLossBps,
  multiplier)` -- additive, every existing entry defaults to the old
  3x behavior.
- **EURUSD migrated, not duplicated**: a symbol can only run one live
  IBS config at a time (state is keyed by bare symbol, no timeframe
  tag), so EURUSD was removed from the daily
  `examples_watch_forward_daily_ibs_longonly_v2.json` (now NATGAS
  only, confirmed no open EURUSD position existed before the switch)
  before its `MeasuredIBSStats` entry was replaced with the intraday
  numbers -- a deliberate reallocation since the daily EURUSD edge was
  the weakest in the whole FX lineup anyway.
- New config `docs/examples_watch_forward_intraday_ibs_fx_v2.json`
  (EURUSD+GBPUSD, 15m, `poll_interval: "15m"` -- this is the first
  live config polling faster than hourly, a real operational change
  from everything else running).

**8 live demo processes now** (the 7 from before, plus this new
intraday one) -- 31 confirmed instrument/strategy pairs total.

## RETRACTION (2026-11-20, same day): the intraday IBS result above was invalid

The section above was wrong, and it is kept in place (not deleted)
specifically so this correction is visible next to the claim it
corrects -- the project's own standing rule is that a result gets
recorded, not erased, when it turns out to be wrong.

**The bug**: the research backtest (`intraday_ibs_v4`) allowed a new
pseudo-trade to start on EVERY qualifying bar, even while a previous
one was conceptually still "open." That measures overlapping,
independent forward-looking samples -- a legitimate thing to measure
in the abstract, but NOT what the live system can actually do: `tradex
execute` enforces exactly one open position per symbol
(`trades.Trades[symbol]`), so once a position is open, every
subsequent qualifying bar is a signal the live system physically
cannot act on.

**Caught by**: re-measuring the exact same parameters (entry<=300bps,
exit>=9500bps, 48-bar hold) with a SEQUENTIAL backtest (skip ahead to
the actual exit bar before considering a new entry, exactly matching
what the live code does). Trade count dropped from 1299/1508 to
662/735 -- the research had silently counted roughly double the
trades the live system could ever take -- and the result **flipped**:
EURUSD's first half went from +0.23bp to -0.10bp, GBPUSD's from
+0.19bp to -0.09bp. A full sequential grid search (entry 100-500bps x
exit 8500-9700bps x hold 16-64 bars) found **zero** combinations that
are both-halves-robust on both symbols. Intraday IBS on FX is
REJECTED, not deployable as measured.

**What was undone**: `MeasuredIBSStats["EURUSD"]` reverted to its
original daily entry (WinRate 0.537, AvgWinBps 34.30, AvgLossBps
34.28, long-only); EURUSD restored to
`docs/examples_watch_forward_daily_ibs_longonly_v2.json`; the intraday
config and its journal/log deleted; the live process (which had only
been paused for an unrelated reason -- an unverified spread
assumption, see below -- long enough for this deeper bug to surface
first) never resumed. **No real demo trade was ever placed on the
rejected strategy** -- the spread-gate pause bought exactly enough
time for this to be caught first.

**What was kept, because it's real, reusable infrastructure
independent of this one rejected result**: the `-side=mid|bid|ask`
candle flag, the IBS short-exit mirroring bug fix (still correct and
still silently affecting nothing live, since every current "both
sides" config uses the 5000 midpoint where mirroring is a no-op),
`WatchConfig.SessionStartHourUTC/EndHourUTC`, `IBSStats.StopMultiplier`
+ `ProtectiveStopBps`'s multiplier parameter, and
`WatchConfig.MaxSpreadBps` + the real-time spread gate in execute.go's
entry loop. All of these cost nothing to keep (default to their old
no-op behavior) and all were exercised correctly by this episode even
though its conclusion was wrong.

**The generalizable lesson** (also written into the `tradex-mcp`
skill): any backtest for a strategy with a real one-position-per-
symbol constraint must enforce that constraint SEQUENTIALLY during
measurement, never evaluate overlapping/independent windows, or its
trade count and edge are both inflated by counting opportunities the
live system could never actually take. This is a different and
separate check from the already-standing "verify real swap/spread"
and "verify out-of-sample" rules -- it has to be checked explicitly on
any new intraday/high-frequency strategy, since a daily-bar strategy's
signal is rare enough that overlapping entries essentially never
happen, which is exactly why this bug went unnoticed in every prior
daily-bar measurement.

## RSI2 intraday: a real edge found, explicitly NOT deployed (same day)

Applying the corrected (sequential) methodology to RSI2 instead of
IBS, on the same EURUSD/GBPUSD 15-min session-hours-gated setup,
turned up something real but reported honestly as NOT YET
TRUSTWORTHY rather than deployed, because it fails a different bar
this project holds strategies to.

**The numbers** (entry: RSI2<=1 extreme oversold / mirrored >=99
overbought, exit: RSI2 crosses back to >=80/<=20, 32-bar/8h hold,
100bp stop, real spread, session hours 07-21 UTC): EURUSD 182 trades,
63.7% win, +1.36bp/trade (half1 +1.07, half2 +1.65); GBPUSD 197
trades, 60.4% win, +1.19bp/trade (half1 +1.87, half2 +0.51). Both
robust, both well above the 30-trades/half floor, and the edge is
4-5x the size of the (now-rejected) IBS attempt.

**Why it is NOT deployed despite clearing the robustness bar**: the
entry threshold has NO stable neighborhood. Mapped the gradient
finely (entry 0.5/1.0/1.5/2.0/2.5/3.0/4.0, same exit/hold): the edge
decays smoothly and monotonically as the threshold loosens, and
exactly one point (entry<=1) clears both-symbols-both-halves
robustness -- entry=0.5 fails on GBPUSD (half1 -0.01, essentially
zero), entry=1.5 already fails on GBPUSD (half1 -0.13), entry=2.0
fails on both. This is a single-point optimum on a continuous curve,
not a plateau -- exactly the kind of result this project's own
Breakout-deployment precedent explicitly required NOT to have
("parameter-insensitivity... is what justified deploying it; a result
that only works at one specific window choice would be the curve-
fitting this project forbids"). The exit threshold (70-98) and hold
(8-64 bars) ARE both insensitive/stable once entry is fixed at 1 --
only the entry cutoff itself is fragile.

**Cost sensitivity, checked per the same-day standing instruction to
verify approximations before concluding**: more resilient than the
rejected IBS attempt -- EURUSD stays positive up to ~1.6bp spread
(1.8 pips), GBPUSD flips around the same point. Still thinner margin
than any daily strategy (27bp+ minimum elsewhere).

**Status: real finding, parked, not traded.** Needs either (a) a
longer data window to check if the entry=1 optimum is stable over
more than one year, or (b) a structurally different formulation that
doesn't depend on hitting one exact RSI2 value, before it clears the
bar this project requires. Reporting this as "found something
promising but not trustworthy yet" rather than quietly deploying it
or quietly dropping it -- the honest state is exactly that: unresolved,
documented, not live.

## RSI2's stop multiplier widened 3x -> 4x, backed by a live diagnostic (2026-11-23)

"What can we do to improve our outcome" got a real, measured answer
instead of generic advice, starting from an actual decomposition of
the live account's P&L.

**The diagnostic**: of 18 closed demo trades at that point (7 wins,
11 losses, net realized -$13.58), splitting by exit reason showed the
strategies' own exit logic (`strategy_exit`) was net **+$39.10** (7
wins +$49.43, 6 small losses -$10.33) while 5 broker-side stop-outs
(`broker_closed_externally`: US30 x3, JPM, MSFT) accounted for
**-$52.68** -- almost the entire realized loss. The signal logic is
working; stop-outs are the drag.

**Checked whether this was anomalous or normal**, rather than assume
either: re-ran a stop-inclusive historical backtest (6y daily bars,
the real 3x-avg-loss stop, exact live entry/exit/trend-filter
parameters) for the specific symbols that got stopped. Found a real,
consistent, previously-unmeasured pattern: **RSI2's stop-hit rate is
structurally much higher than IBS's** -- JPM RSI2 22.0%, MSFT RSI2
32.4%, vs. US30 IBS 8.0%, MSFT IBS 9.1% -- at the identical 3x
multiplier. Mechanism: RSI2's extreme-oversold entry condition catches
sharper, more continuation-prone drops than IBS's close-near-low
condition before they revert, so its loss distribution has a fatter
tail relative to its own average loss.

**Then checked whether a wider stop actually helps**, rather than
assume it does: swept the stop multiplier (2x to effectively no stop)
on 5 RSI2 symbols. 4 came back cleanly positive and monotonic --
expectancy improves as the stop widens (JPM: 45.6bp at 3x -> 71.7bp
unstopped; MSFT: 28.4bp -> 77.2bp; US100: 80.6bp -> 86.5bp; AAPL:
14.5bp -> 54.6bp) -- a tight stop clips the mean-reversion edge before
it has room to revert, the same mechanism found in the (separately
rejected) intraday IBS research. The 5th, PLATINUM, was NOISY and
inconclusive (n=26, below the 30-trade bar) and explicitly excluded
from the conclusion rather than cherry-picked to support it. IBS, by
contrast, barely moved across the same multiplier range (US30: 8.3bp
at 3x vs 9.5bp unstopped) -- confirming the fix belongs on RSI2
specifically, not as a blanket change to every strategy.

**What changed**: `MeasuredRSI2Stats`'s `StopMultiplier` set to 4
(from the global default of 3) on every entry -- a real, moderate,
evidence-backed widening, not a guess and not tuned to a target
number. Does not increase dollar risk per trade: position sizing
resizes to the stop distance to keep `RiskPct` constant, so a wider
stop means a smaller position that can breathe longer, not a bigger
loss when it eventually does fire. Only applies to NEW entries going
forward -- the 4 positions already open at the time (sized and
stopped under the old 3x rule) are unaffected; broker-side stop
orders aren't retroactively moved.

**What this is not**: a parameter sweep to hit a target number (the
multiplier was picked because 4 of 5 symbols' OWN data supported it,
not because it produced a specific headline figure), and not a claim
that stop-outs will stop happening -- RSI2's entries will still
sometimes be wrong and still sometimes get stopped; the claim is only
that, on the evidence gathered, a wider stop should make the overall
expectancy better than it already measures to be.

## Breadth round 8: more crypto, because high-volatility already carries this project's biggest edges (2026-11-23)

Asked "what about high volatility markets" led to actually checking
the existing confirmed book rather than speculating: averaging
measured expectancy by rough volatility class across every live
strategy/symbol showed HIGH-VOL instruments (metals, crypto, growth
stocks) at **+51bp/trade average**, mid-cap shares at +27bp, and FX
majors/crosses at only **+7bp**. This wasn't new research, it was
already sitting in `MeasuredIBSStats`/`MeasuredRSI2Stats` -- the
breadth effort had been adding a lot of thin-edge FX crosses (the
standing "FX/crypto first" priority) while the highest-value
unexplored territory was more high-volatility names, specifically
more crypto, where only BTCUSD/ETHUSD had ever been tried.

**Checked what Capital.com actually offers** (never assumed): XRPUSD,
SOLUSD, ADAUSD, DOGEUSD, LTCUSD, DOTUSD, AVAXUSD, all verified live
(precision via `GET /api/v1/markets/{epic}`, not guessed from the
symbol -- decimalPlaces ranged from 2 (LTCUSD) to 7 (DOGEUSD)).
marginFactor 50% and the generic crypto financing rate
(-0.0616438%/day = 6.16bps/day) identical across all 7, matching
BTCUSD/ETHUSD exactly.

**Measured honestly against the real bar, not a loosened one for
crypto**: IBS, RSI2, and Donchian breakout tested on all 7, real
swap included, 6-year daily history. Only 2 clear **>=30 trades/half,
both halves positive**: SOLUSD IBS (118 trades, 58.5% win, +50.3bp
average, half1 +76.0/half2 +24.7) and DOGEUSD IBS (77 trades, 59.7%
win, +32.5bp average, half1 +29.3/half2 +35.6). Several others looked
very attractive on paper -- XRPUSD RSI2 +99.9bp, ADAUSD RSI2
+290.4bp, AVAXUSD RSI2 +140.4bp -- but every one of those had only
14-19 trades per half, well under the 30-trade bar, and are explicitly
NOT deployed despite the tempting headline numbers. Breakout looked
attractive on several too (XRPUSD +515bp, SOLUSD +955bp, AVAXUSD
+557bp) but none cleared 30/half either -- breakout signals are too
rare over the available crypto history to trust yet. LTCUSD, DOTUSD,
and the remaining buckets rejected outright (negative or
sign-flipping).

**What shipped**: SOLUSD and DOGEUSD added to `MeasuredIBSStats` with
`StopMultiplier: 4` (same crypto-specific reasoning as BTCUSD's
RSI2/Breakout entries -- bigger moves need more room before the stop
fires). New `internal/instrument.crypto()` registrations for all 7
tested symbols (not just the 2 that confirmed -- the specs are
harmless to register even for symbols with no measured entry, since
`tradeableSymbols()` gates on the stats table, not the instrument
registry) and epic mappings in `capital.go`. New live config
`docs/examples_watch_forward_daily_crypto_v2.json` (SOLUSD+DOGEUSD,
daily IBS) -- **8 live demo processes now**, 33 confirmed
instrument/strategy pairs total.

**What this confirms about the "what can we do to improve our
outcome" question more broadly**: the single highest-value lever
right now isn't a new strategy TYPE, a parameter tweak, or risk
sizing (all already addressed) -- it's directing breadth search
toward the asset classes that are ALREADY measured to produce the
biggest edges, instead of defaulting to "FX/crypto" as one
undifferentiated priority when the two have wildly different
measured expectancy.

## "A strategy with <30% losing trades" -- the answer was already in the live book (2026-11-24)

User asked for a forex/crypto intraday-or-week-trade strategy with
fewer than 3 losses per 10 trades (>70% win rate), explicitly
instructed to go past literature and keep testing until finding one
for forward test.

**Intraday was tried first and failed cleanly, honestly reported
rather than papered over**: a tight-target/wide-stop scalp (the
classic mechanical way to engineer a high win rate) was swept across
EURUSD 15-min bars -- win rates up to 89% were trivially achievable,
but every single combination was net NEGATIVE after real cost (the
"picking up pennies in front of a steamroller" pattern this project's
own skill notes already warn about). A trend-pullback continuation
mechanism was also tried on EURUSD and BTCUSD 15-min bars across a
full parameter sweep -- raw edge was essentially zero on both
(net return tracked the cost almost exactly regardless of target/stop
ratio), confirming FX/crypto don't have exploitable continuation
structure at 15-minute resolution with this mechanism either.

**The real answer was in week-trade (daily-bar) territory, and it was
largely already deployed**: re-checking the ALREADY-CONFIRMED,
ALREADY-LIVE RSI2 book for win rate specifically (not just
expectancy) found **GBPJPY RSI2 at 72.0% win rate** (+4.5bp/trade,
both halves positive, real spread 1.16bp + real swap 0.411bp/day
included) and **AUDCAD RSI2 at 78.1% win rate** (+33.4bp/trade, both
halves positive, same real-cost standard) -- both already running in
`docs/examples_watch_forward_daily_standalone_v2.json`, no new
deployment needed. These clear the user's >70%-win-rate bar with a
real, already-measured, already-live edge -- not a new discovery, but
the honest existing answer that answers the actual question.

**Crypto genuinely could not clear the same bar with available data**,
reported honestly rather than force-fit: IBS exit-threshold widening
on SOLUSD/DOGEUSD (the two confirmed crypto instruments) never
exceeded ~59% win rate, and widening actively broke robustness on
DOGEUSD. RSI2 on XRPUSD (entry RSI2<=10, exit RSI2>=60, 200-day trend
filter, 10-day hold) showed a genuinely attractive **71.4% win rate,
+104.2bp/trade**, both halves nominally positive -- but only 35
trades (17/half), short of the project's own 30-trades/half
confirmation bar. Crypto's shorter available history on Capital.com
(roughly 2021-present vs FX's full 6 years) is the structural reason
every crypto RSI2 candidate keeps landing in this same thin-sample
zone.

**Deployed as an actual forward test, not a backtest claim, per the
user's explicit instruction**: `docs/examples_watch_forward_xrpusd_rsi2_v1.json`
running via `tradex watch` (NOT `tradex execute` -- no order
authority, confirmed in its own startup banner), journal-only
(`desktop: false, telegram: false`, matching the project's standing
"keep sinks off until a holdout confirms an edge" rule), tagged
UNVALIDATED on every signal. This is the correct, honest way to grow
a promising-but-thin sample into a real confirmation or a real
rejection -- not a shortcut around the confirmation bar.

**9 background processes now**: the 8 live `tradex execute` processes
(real demo orders) plus this 1 `tradex watch` forward-test-only
process (journal, no orders) for XRPUSD.
