# Mean reversion — Internal Bar Strength — `meanrev-0.1.0` (detector unchanged,
v0.2 adds a trend filter + exit-on-strength overlay, see below)

Status: **the strongest signal measured in this project to date, but still short of
full promotion.** Real win-rate improvement (55-68%, up from v0.1's raw
45-55%) and a net-of-cost edge over the drift baseline confirmed on one
genuine fresh holdout (XAGUSD) and one mechanism retest on a previously-flat
instrument (EURUSD). Not promoted: the two instruments v0.1 found positive
(XAUUSD, ETHUSDT) now disagree in sign between halves under the same v0.2
overlay, so only 2 of 5 tested instruments confirm — not yet the breadth the
project's own promotion rule expects before trusting it in practice.

Detector: `internal/features/meanreversion.go` (`IBSBps`)
Harness: `internal/research/mrlab`, `cmd/mrlab`

## The idea

Internal Bar Strength is where the close sits inside the bar's own range:
`IBS = (close − low) / (high − low)`, 0 at the low, 1 at the high. The
documented effect (Pagonidis 2013, US equity ETFs): low-IBS closes tend to
bounce, high-IBS closes tend to fade. One bar, no window, pure integer
arithmetic — the cheapest possible mean-reversion signal to test.

The literature warns twice over: the effect is documented only on US equity
ETFs (absent in UK/German/French/Australian index ETFs), and a high win rate
is not profit — mean reversion's per-trade edge is small, so cost takes a
larger share than from a trend trade.

## The decisive test: signal vs drift

A fixed-hold long after a low-IBS close looks profitable on any instrument
with upward drift (crypto, gold) for the same reason RSI-2 "works" on SPY: the
drift, not the signal. So the measurement that matters is the forward return
of signal bars MINUS the all-bar baseline (the drift). Daily bars, full
history to 2026-11:

| Instrument | All-bar next-day | IBS≤0.2 edge | IBS≥0.8 edge | Verdict |
| ---------- | ---------------- | ------------ | ------------ | ------- |
| EURUSD     | −0.1 bps         | −0.0 bps     | −0.9 bps     | no effect |
| XAUUSD     | +3.8 bps         | **+6.3 bps** | **−2.3 bps** | real, correctly signed |
| BTCUSDT    | +15.6 bps        | −2.6 bps     | −10.2 bps    | wrong sign — daily BTC continues, not reverts |
| ETHUSDT    | +18.7 bps        | **+16.7 bps**| **−16.5 bps**| strong, correctly signed |

"Edge" is the signal's forward return minus the drift. A real IBS effect needs
the low-IBS edge clearly positive AND the high-IBS edge clearly negative. That
holds on XAUUSD and ETHUSDT, not on EURUSD (flat) or BTCUSDT (wrong sign).

## Tradeability (why it is not promotable yet)

- **EURUSD**: no effect. Consistent with the "US-only" literature; FX majors
  do not show it.
- **BTCUSDT**: the daily positive is pure drift; the reversion edge is the
  wrong sign (low closes keep falling), so BTC daily is momentum, not
  reversion.
- **XAUUSD**: a genuine +6.3 bp reversion edge, but round-trip cost on gold is
  ~5 bp, so the net is marginal and the `mrlab` hold-1 run split +9 bp / −4 bp
  across halves — not robust as specified.
- **ETHUSDT**: the strongest signal (+16.7 / −16.5 bp), both halves positive
  net of a 20 bp crypto fee (+20 / +8 bp per trade, 55% win). But the long
  side leans on ETH's exceptional 2019-2026 drift as much as on reversion, and
  a 135%-of-return drawdown shows the risk profile is ugly. One instrument,
  one historic bull market — not yet an edge to trade.

The win rates observed (45-55% on a fixed 1-bar hold) are NOT the 70-85% the
marketing cites; those come from RSI-2 on SPY with a 200-SMA filter and an
exit-on-strength rule, i.e. equity drift plus a favourable exit, not IBS alone.

## What would make this promotable

1. Confirm the XAUUSD/ETHUSDT effect is not just 2020-2021 by re-measuring on a
   clean holdout after 2023.
2. Long-only with a trend filter (the literature's required overlay;
   `mrlab -trend-filter-bars`), which should cut the drawdown.
3. Tune the exit: Connors exits on a strength signal, not a fixed hold; a
   fixed hold understates both win rate and expectancy.
4. Stress the crypto cost at the fee for the median price, not a flat bp — the
   same gap that produced a fake continuation edge.

This is the first signal worth developing further. It is also still a long way
from a high-win-rate tradeable bot, and nothing emits to Telegram until a
holdout confirms it.

## v0.2 — trend filter + exit-on-strength, measured honestly

Implemented points 2-3 of the list above (`internal/strategy/scan.IBS`'s
`TrendFilterBars`/`StrengthExitBps` fields; `cmd/stratlab -ibs-trend-filter-bars
-ibs-strength-exit-bps`): long-only above a 200-bar SMA, exit as soon as a
later bar's own IBS crosses back to >=50% (instead of always holding a fixed
1 bar), capped at a 10-bar max hold. All three parameters are stated priors
from the published Connors RSI-2 methodology, decided before looking at any
new result, not fitted to this data.

Measured with `cmd/ibsv2` (percentage-of-price returns and a proper
unconditional-baseline drift comparison per holding length — `stratlab`'s
fixed `RiskUnitTicks` R-conversion is MISCALIBRATED for IBS on high-priced
instruments: it produced +24R and +119R on XAUUSD/BTCUSDT, nonsense numbers,
because a fixed tick risk unit doesn't scale with a $3,900 gold price or a
$76,000 BTC price. This is a known, separate, not-yet-fixed issue — use
`ibsv2`'s bps-of-price convention for any IBS measurement until `stratlab`'s
R-unit handling is corrected for this strategy):

| Instrument | Status | Full | 1st half | 2nd half | Win% |
|---|---|---|---|---|---|
| XAGUSD | **genuine fresh holdout, never used before** | +12.78bp edge | **+19.66bp** | **+4.37bp** | 56-60% |
| EURUSD | previously "no effect" under v0.1's raw signal | +6.89bp edge | +9.88bp | +3.32bp | 54-67% |
| XAUUSD | previously confirmed under v0.1 | +1.69bp edge | −1.83bp | +7.16bp | 57-62% |
| ETHUSDT | previously confirmed under v0.1 | +13.56bp edge | +40.94bp | −4.51bp | 60-65% |
| BTCUSDT | previously wrong-signed under v0.1 | −25.82bp edge | −37.92bp | −18.40bp | ~62%* |

`*` BTCUSDT's win rate is misleadingly high because mean return is small and
positive while drift is large — most of the "wins" are smaller than the
baseline, which the edge column (mean return minus baseline) correctly shows
as strongly negative. Win rate alone would be the wrong read here.

**Honest verdict: 2 of 5 instruments confirm (XAGUSD, EURUSD), both halves
positive net of cost — the project's cross-instrument bar on paper, but not
confident enough to flip `Validated()`.** The two instruments v0.1 found
promising (XAUUSD, ETHUSDT) flip sign between halves under this exact same
v0.2 overlay, and BTCUSDT remains wrong-signed. A result that confirms on a
genuine holdout plus one previously-negative instrument, while the
previously-positive instruments stop confirming, is not yet the stable,
broad effect needed before trusting it with real signals — it is the best
lead this project has produced, not a finished edge. `scan.IBS`'s
`Validated()` stays `false`; `TrendFilterBars`/`StrengthExitBps` default to 0
(disabled) so existing callers see no behavior change unless they opt in.

This is the strongest lever found this session. It is also still a long way
from a stable, broadly-confirmed tradeable bot, and nothing emits to Telegram
until more instruments confirm it.

## Negative results recorded elsewhere

- `continuation-0.1.0` (break-and-retest): negative, both entry modes — see
  `docs/CONTINUATION_SPEC.md`.
- `crt-0.1.0` (Candle Range Theory): no edge — see `docs/CANDLE_RANGE_SPEC.md`.
- ICT named patterns (order blocks, fair value gaps, liquidity sweeps, Silver
  Bullet): no public mechanical test shows an edge, and several lose with
  significance; the one survivor (Optimal Trade Entry) reduces to "buy a
  pullback in a trend" with its Fibonacci precision shown to be irrelevant.
  Not built here; recorded so it is not re-litigated without new evidence.

## US equity indices — US100 confirms, US500/US30 do not survive a longer sample

The literature this whole detector is based on (Pagonidis 2013) documents
the IBS effect on US equity ETFs/indices specifically — an asset class this
project had never actually tested; `instrument.go` carried US500/US100/US30
as `Unverified` leftovers outside the research universe. Verified their
real Capital.com epics (`GET /api/v1/markets`: tickSize 0.1, pipPosition 0,
scalingFactor 1 on all three — 1 decimal place, corrected from the carried-
over 2-digit guess) and tested the same v0.2 config as the confirmed FX/
metals result (`trend_bars=200`, `ibs_long_below=2000bps`,
`ibs_exit_above=5000bps`, `max_hold=10`, 2bp cost):

| Instrument | Sample | Full edge | Pre-split | Post-split | Verdict |
|---|---|---|---|---|---|
| US500 | 2y (60 trades) | +20.82bp | +38.42bp (13 trades) | +15.95bp (47) | looked great, too few trades/half |
| US500 | **6y (228 trades)** | +6.91bp | **−6.81bp** (89) | +15.70bp (139) | **not robust** |
| US100 | 2y (56 trades) | +19.38bp | +47.04bp (13) | +11.02bp (43) | looked great, too few trades/half |
| US100 | **6y (205 trades)** | +14.34bp | **+15.95bp** (77) | **+13.37bp** (128) | **confirms, both halves, proper sample** |
| US30 | 2y (68 trades) | +29.17bp | +15.03bp (17) | +33.89bp (51) | looked great, too few trades/half |
| US30 | **6y (219 trades)** | +4.49bp | **−12.79bp** (86) | +15.66bp (133) | **not robust** |

The lesson repeats one more time, in the opposite direction from usual: a
short 2-year window made ALL THREE indices look like a clean confirmation
(60-85% win rates, every half positive) — but that sample only had 13-17
trades per half, under this project's own 30-trade floor. Re-run on 6 years
with proper statistical power (77-139 trades/half), US500 and US30 both
flip negative in their first half and don't survive; only **US100
(Nasdaq)** holds up in both halves on a real sample.

**US100 is a genuine third confirming instrument**, alongside XAGUSD and
EURUSD — a different asset class entirely, which is the kind of breadth
the project's promotion rule is actually looking for. It is not perfectly
independent of US500/US30 (same equity-index family), so one more fresh
confirmation outside all three families already tried (FX, metals, US
equity indices) would still be the stronger bar to clear before flipping
`Validated()`. Added to the live daily IBS forward test
(`docs/examples_watch_forward_daily_ibs_v2.json`) alongside XAGUSD/EURUSD —
still labelled UNVALIDATED, now collecting real forward data on 3
confirmed instruments instead of 2.

## Breadth search, round 2 — non-US indices and more FX crosses: nothing new

Tried to widen the confirmed set further, same locked v0.2 config, same
discipline:

- **GBPUSD, AUDUSD, USDCAD, USDJPY** (2y daily, same threshold): all
  negative full-sample edge (−2.35 to −12.35bp). No new FX confirmations.
- **DE40 (DAX), J225 (Nikkei), FR40 (CAC40), UK100 (FTSE)** — verified
  real Capital.com epics (same `GET /api/v1/markets` check, tickSize 0.1),
  6-year sample: DE40 outright negative; J225/FR40/UK100 all show the
  identical halves-flip pattern as US500/US30 (negative first half,
  positive second) — not robust. The "equity index" asset class edge does
  NOT generalize beyond US100 specifically.

## Important correction — XAGUSD's confirmation does not survive a harder retest

Re-tested XAGUSD on a uniform 6-year window (2020-11 to 2026-11, split at
the midpoint, same as every other instrument in this document) instead of
whatever window/split produced the original "genuine fresh holdout"
result above. Result: full-sample edge is still positive (+4.24bp,
152 trades), but **the first half is strongly negative (−35.72bp, 59
trades)** — it does not clear the both-halves bar on this window. This
contradicts the earlier claimed confirmation and must be stated plainly
rather than quietly kept: XAGUSD's result is split-point-sensitive, the
same fragility every other "lead" in this project has shown. It stays in
the live forward test (more real data is the only way to settle this), but
is NOT treated as equally confirmed to EURUSD/US100 going forward — see
`internal/execution/sizing.go`, where it is explicitly flagged
`Robust: false` and sized down accordingly.

## Threshold widening (more frequent signals) — tested, dilutes the edge

Asked whether loosening the IBS trigger (30%/70% instead of 20%/80%, i.e.
acting on more "near-extreme" readings) gets more trades without losing
the edge, same 6-year windows:

| Instrument | Config | Trades | Full edge | First half | Second half |
|---|---|---|---|---|---|
| EURUSD | 20/80 (baseline) | 149 | +6.32bp | +7.58bp | +5.60bp |
| EURUSD | 30/70 (widened) | 231 | +2.67bp | +1.76bp | +3.08bp |
| US100 | 20/80 (baseline) | 205 | +14.34bp | +15.95bp | +13.37bp |
| US100 | 30/70 (widened) | 326 | +3.01bp | **+0.85bp** | +4.24bp |
| XAGUSD | 20/80 (baseline) | 152 | +4.24bp | −35.72bp | +29.60bp |
| XAGUSD | 30/70 (widened) | 249 | −4.26bp | −35.35bp | +13.64bp |

**More trades, but every instrument's edge shrinks, and US100's first half
nearly vanishes (+0.85bp — noise).** This is exactly the expected
mean-reversion tradeoff (a more extreme IBS reading is a stronger signal;
a near-average one is weaker), not a surprise, but it means the threshold
is NOT a free lever for frequency — not deployed. The live config stays at
the tight 20%/80% threshold.

## Realistic trade frequency on what is actually confirmed

EURUSD: 149 trades / 6y ≈ 25/yr ≈ 1 every 2 weeks. US100: 205/6y ≈ 34/yr ≈
1 every 1.5 weeks. XAGUSD: unstable, don't count on it. Combined across
all 3 (independent fire conditions, so roughly additive): realistically
**1-3 signals a week**, not multiple a day — this is a mean-reversion
signal on daily bars; it fires when a bar closes at an extreme, which by
definition does not happen every day. There is no version of this specific
edge that fires more often without diluting itself, per the threshold test
above.

## Position sizing (`internal/execution/sizing.go`)

Every daily IBS signal now carries a `suggested_risk_pct` (also shown in
the delivered text as "risk X% of equity"), computed per-instrument from
real `ibsv2` win-rate/avg-win/avg-loss stats via half-Kelly, hard-capped
at 2% of equity, and discounted to 25% of that for any instrument whose
backtest halves disagree (currently just XAGUSD, per the correction
above). EURUSD and US100's raw half-Kelly outputs (8.4% and 14.4%) both
hit the 2% ceiling — a reminder that literal Kelly off a ~150-200 trade
sample routinely overstates what's sane to risk, which is exactly why the
ceiling exists rather than trusting the formula on its own.

## Breadth search, round 3 — crypto, commodities, more FX crosses

Continued the breadth search with the three asset classes not yet tried.

- **Crypto (BTCUSDT, ETHUSDT, ~7.8y Binance daily)**: matches the original
  v0.1 finding exactly, now re-measured with the real avg-win/avg-loss
  numbers — BTCUSDT wrong-signed (edge −25.82bp), ETHUSDT halves disagree
  (+47.74bp / −31.06bp). No change.
- **Commodities beyond gold/silver**: verified real Capital.com epics
  (`OIL_CRUDE`, `OIL_BRENT`, `NATURALGAS`, `COPPER` — none matched the old
  2-digit "Unverified" energy guess; corrected precision to 3/3/4/5 digits
  respectively). 6-year sample:

  | Instrument | Trades | Full edge | First half | Second half | Verdict |
  |---|---|---|---|---|---|
  | WTI | 124 | −11.13bp | +23.57bp | **−50.60bp** | not robust |
  | BRENT | 115 | +42.37bp | +126.61bp | **−43.33bp** | not robust |
  | **NATGAS** | **185** | **+9.22bp** | **+9.31bp** | **+9.14bp** | **confirms — both halves close and positive** |
  | COPPER | 188 | +15.03bp | **−6.32bp** | +26.32bp | not robust |

  **NATGAS is a fourth confirming instrument** — a commodity outside every
  family tried so far (FX, metals, US equity index), with the most
  internally consistent halves of anything measured in this document
  (+9.31 / +9.14bp — nearly identical, not just both-positive). Added to
  the live daily IBS forward test and the sizing table.

- **More FX crosses (EURGBP, EURJPY, GBPJPY, AUDJPY, NZDUSD, USDCHF, 2y)**:
  all have too few trades (16-34 total, well under the ≥30-per-HALF floor)
  to say anything. Several look exciting full-sample (AUDJPY +11.08bp,
  USDCHF +7.29bp) but with 7-16 trades per half that is noise, not
  evidence — not pursued further; would need several more years of data
  before these crosses are even measurable, let alone confirmed.

Updated live config: `["XAGUSD", "EURUSD", "US100", "NATGAS"]`, still all
labelled UNVALIDATED pending one more instrument confirmation outside
these four families (FX, metals, US equity index, energy) before
`Validated()` is considered.

## Breadth search, round 4 — more commodities: PLATINUM confirms (5th instrument)

Continued the commodity sweep that found NATGAS: verified 6 more real
Capital.com epics (PLATINUM, PALLADIUM, WHEAT, CORN, SOYBEAN, COTTON via
`USCOTTON`). Note: WHEAT's own Capital.com history has a persistent data
defect — `tradex candles` rejected it with "invalid candle OHLC geometry"
at three different start dates tried (2020, 2022, 2023); skipped rather
than worked around, since silently patching a vendor's bad bar is exactly
the kind of thing this project's candle validation exists to catch, not
bypass.

| Instrument | Sample | Trades | Full edge | First half | Second half | Verdict |
|---|---|---|---|---|---|---|
| **PLATINUM** | 6y | **128** | **+58.32bp** | **+22.22bp** (40) | **+74.72bp** (88) | **confirms — both halves positive** |
| PALLADIUM | 6y | 94 | −6.78bp | −135.05bp | +42.26bp | not robust |
| CORN | ~4.4y | 77 | −28.82bp | — | — | negative |
| SOYBEAN | ~4.4y | 88 | −2.89bp | — | — | negative |
| COTTON | ~4.4y | 101 | −48.58bp | −77.78bp | +23.92bp | not robust |

**PLATINUM is a fifth confirming instrument** (pre-split 40 trades is
just over the 30-trade floor — thinner than the others, worth another
look once more data accumulates, but both halves genuinely positive, not
a coin flip). Added to the live daily IBS forward test and the sizing
table. Live config is now 5 instruments: `["XAGUSD", "EURUSD", "US100",
"NATGAS", "PLATINUM"]`, spanning FX, metals (two: silver AND platinum),
US equity index, and energy.

## Critical correction — the short side was never measured, and real swap cost matters

Asked to check real overnight financing before trusting any of the above
with money. Two things surfaced, the first bigger than the second:

**`cmd/ibsv2` only ever measured the LONG side.** The live `scan.IBS`
detector fires both long (`LongBelowBps`) AND short (`ShortAboveBps`), and
every live config this whole session has had `ibs_short_above_bps: 8000`
enabled — meaning roughly half the signals actually delivered to Telegram
all week (every SELL IBS signal) had NO measured backtest behind them at
all. Extended `ibsv2` with `-side long|short|both` to fix this gap, then
measured the short side on all 5 instruments (6y, same config):

| Instrument | Short trades | Full edge | First half | Second half | Verdict |
|---|---|---|---|---|---|
| **US100** | 111 | +43.49bp | +46.84bp (91) | +28.25bp (20, thin) | confirms |
| **PLATINUM** | 147 | +10.60bp | +4.00bp (83) | +19.17bp (64) | confirms |
| EURUSD | 179 | +1.26bp | +10.23bp | **−11.18bp** | not robust |
| NATGAS | 169 | −4.54bp | −18.05bp | +3.70bp | not robust |
| XAGUSD | 139 | **−36.99bp** | −40.01bp | −30.45bp | negative, both halves |

**Real overnight swap rates** (pulled live from `GET
/api/v1/markets/{epic}` on 2026-11-10, in bps/day, positive = cost):

| Instrument | Long swap | Short swap |
|---|---|---|
| EURUSD | +1.584 | −0.762 (credit) |
| US100 | +2.221 | +0.002 |
| NATGAS | +1.096 | +1.096 |
| PLATINUM | +1.363 | −0.541 (credit) |
| XAGUSD (SILVER) | +3.799 | −2.977 (credit) |

None of the bp numbers reported anywhere in this document before today
included this — only spread + slippage. Re-measured every instrument with
real swap applied (avg hold ~1.7-2.0 days = 1-2 overnight charges/trade):

- **US100 (both sides + swap)**: 316 trades, edge +22.44bp, halves
  +31.09/+12.63bp — survives.
- **PLATINUM (both sides + swap)**: 275 trades, edge +31.87bp, halves
  +9.35/+50.10bp — survives.
- **EURUSD (long only + swap)**: 149 trades, edge +2.95bp (down from
  +6.32 without swap), halves +4.03/+2.35bp — survives, but thinner.
- **NATGAS (long only + swap)**: 185 trades, edge +7.32bp, halves
  +7.54/+7.13bp — survives, barely changed.
- **XAGUSD (long only + swap)**: edge flips to **−2.50bp full-sample**
  (was +4.24 without swap), halves still disagree (−43.76/+23.67bp).

**Action taken:**
- **XAGUSD removed entirely** from both the live forward test and the
  sizing table. Three independent checks have now gone against it (the
  uniform-6y-window retest, the short-side test, and now real swap cost)
  — that is no longer "shaky," it is rejected.
- **EURUSD and NATGAS**: kept LONG ONLY. Short-side signals disabled
  (`ibs_short_above_bps: 0`) — they were firing live with no measured
  basis, and now measured, they don't confirm. Moved to a separate config,
  `docs/examples_watch_forward_daily_ibs_longonly_v2.json`.
- **US100 and PLATINUM**: kept trading BOTH sides — the only two
  instruments where both long and short independently confirm, and both
  survive real swap cost. `docs/examples_watch_forward_daily_ibs_v2.json`.
- `internal/execution/sizing.go` rewritten to match exactly what each live
  config now emits, with stats net of real swap cost, not just spread.

This is the single most consequential check performed on this strategy to
date — it is the difference between "a measured edge" and "a measured
edge on one side of the trade, running both sides live by accident."

## Intraday IBS (1h / 15m) — tested, not confirmed

The literature's IBS effect is documented on DAILY bars only; it was worth
checking whether it survives at the timeframes `tradex watch` can actually
poll fast on (15m, 1h), same parameters as the v0.2 daily config
(`trend_bars=200`, `ibs_long_below=2000bps`, `ibs_exit_above=5000bps`,
`max_hold=10` — bar counts carried over unchanged, not recalibrated per
timeframe):

| Instrument | Timeframe | Sample | Full edge | Pre-split | Post-split | Verdict |
|---|---|---|---|---|---|---|
| EURUSD | 15m | 327 trades, ~60d | −2.63bp | −2.93bp | −2.54bp | negative, consistent |
| EURUSD | 1h | 115 trades, ~60d | −2.18bp | −0.93bp | −3.50bp | negative, consistent |
| XAGUSD | 15m | 334 trades, ~60d | −0.91bp | −4.16bp | +1.68bp | halves disagree |
| XAGUSD | 1h | 188 trades, ~180d | +4.36bp | **−0.01bp** | **+8.46bp** | halves disagree |

EURUSD's daily edge does not transfer intraday at all (negative at both 15m
and 1h, both halves). XAGUSD's 1h run looked promising on a short ~60-day
sample (85 trades, +8.25bp, pre-split +7.36bp/77 trades) — but re-run on a
proper ~180-day split, the first half flattens to +/-0bp and only the
second half carries the edge. Same pattern as every other "confirms on one
cut of the data" result in this project: not promotable. The daily
timeframe remains the only one with a measured, if still narrow (2/5
instruments), IBS edge.

## First real demo execution (2026-11-11)

Built `tradex execute` — the first code in this project with order
authority at all. `internal/feed` stays read-only (unchanged, still
guarded by its own test); a brand new `internal/broker` package is the
only thing allowed to place or close a position, and it hard-refuses to
construct unless the configured base URL contains "demo" — not a config
flag, a code-level refusal.

**Risk controls** (`internal/execution/risk.go`), all hard gates, not
advisory:
- Per-trade risk: existing half-Kelly sizing, capped at 2% of equity.
- Max 4 open positions at once, max 25% of equity committed as margin.
- Soft daily loss cap (5% of equity): blocks new entries for the rest of
  the UTC day, persisted across restarts so a crash/restart can't reset
  the counter to zero.
- Hard daily loss cap (9%): force-closes every open position immediately,
  in addition to blocking entries.
- Manual kill switch: presence of `data/KILL_SWITCH` blocks all new
  entries, independent of everything else working correctly.
- A second, independent notional backstop (never >15% of equity in one
  trade) in case the margin-factor math itself has a unit error.
- Every protective stop is MANDATORY and broker-side — `PlaceOrder`
  refuses to submit without one. This is NOT the backtested exit rule
  (IBS has no price stop at all, by design, only time/condition exits) —
  it's a safety floor added specifically for real orders, sized at 3x the
  instrument's measured average loss so it essentially never fires under
  normal conditions.

**A real bug caught during verification, not a hypothetical one**:
Capital.com's `/api/v1/confirms/{dealReference}` endpoint returned a
`dealId` that did NOT match the actual open position's `dealId` — closing
by that ID would have silently failed to find the position, every time.
Caught by placing one real $111-notional EURUSD demo order, confirming it
in `/api/v1/positions`, and discovering the IDs didn't match. Fixed by
having `PlaceOrder` look the real position up by epic/direction/size
match instead of trusting the confirm's own field. Re-verified end to end:
placed, confirmed open with the correct ID and stop level, closed,
confirmed gone. Deleted the one-off manual test afterward; the fix and a
permanent demo-URL-refusal test are what's committed.

**Live now, both switched from `watch` to `execute`** (places real demo
orders in addition to the same Telegram/journal delivery `watch` always
did): `docs/examples_watch_forward_daily_ibs_v2.json` (US100, PLATINUM,
both sides) and `docs/examples_watch_forward_daily_ibs_longonly_v2.json`
(EURUSD, NATGAS, long only). Only symbols present in
`execution.MeasuredIBSStats` are ever traded — an unmeasured symbol in a
config gets no order, by construction.

**Known limitations, stated plainly:**
- Margin-factor and min-deal-size values are hardcoded from one live API
  check (2026-11-11) for exactly these 4 instruments — not re-verified
  periodically, could drift if Capital.com changes contract terms.
- `EURUSD`'s min deal size unit semantics (`POINTS`, value 100) are not
  fully understood — the notional backstop exists specifically to catch a
  misunderstanding here before it becomes an oversized order.
- There is a pre-existing, more mature broker abstraction
  (`internal/broker/ctrader`, `internal/broker/paper` — a different
  broker, with its own circuit breaker/safety-gate/idempotency machinery)
  already in this codebase, apparently from earlier unrelated work. Not
  consolidated with this new Capital.com client — worth doing later, not
  done now.
- This is demo money. Nothing here has been asked to run on a live
  account, and `internal/broker` cannot be pointed at one without a
  deliberate code change.
