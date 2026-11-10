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
