# Candle Range Theory Strategy Specification v0.1

Candle Range Theory (CRT) treats a completed candle's range as a liquidity
container: the next candle purges one side of it, then price expands toward the
opposite side. This document fixes that idea as a deterministic contract.

Detection is a feature detector and a research hypothesis. It is not an
automatic trade signal, and it cannot bypass context, trigger, risk, portfolio,
execution, or kill-switch gates.

Spec version: `crt-0.1.0` (`features.CandleRangeSpecVersion`). Any change to
detection semantics must bump it, because frozen manifests record it.

## Supported forms

- `BULLISH_CANDLE_RANGE`: the range candle's low is purged and reclaimed.
- `BEARISH_CANDLE_RANGE`: the range candle's high is purged and reclaimed.

## Required evidence

The detector reads exactly two adjacent completed candles on the setup
timeframe: a range candle and a purge candle. All of the following must hold,
or the pair is rejected.

1. The purge candle opens exactly at the range candle's close. A gap in the
   stream would change which liquidity was swept, so a non-adjacent pair is
   refused rather than patched.
2. The range candle's height is at least `min_range_ticks`.
3. The purge candle penetrates exactly one range extreme by at least
   `min_purge_ticks`. A touch is not a purge.
4. The purge is no deeper than `max_purge_bps` of the range height. A deeper
   penetration is displacement, not a sweep.
5. The purge candle closes back inside the range, at least `min_reclaim_bps` of
   the range height away from the swept extreme, and not beyond the opposite
   extreme. This is the "close back inside" rule made numeric.
6. Reward/risk of the resulting geometry is at least `min_reward_risk_bps`.

A purge candle that penetrates **both** extremes is ambiguous: its direction is
unknowable from the pair alone. The detector rejects it instead of classifying
it by guesswork.

## Derived levels

All three levels are fixed at detection time and never move:

| Level  | Bullish                              | Bearish                               |
| ------ | ------------------------------------ | ------------------------------------- |
| Entry  | purge candle low + `entry_bps` of its height | purge candle high − `entry_bps` of its height |
| Stop   | purge low − `stop_buffer_ticks`      | purge high + `stop_buffer_ticks`      |
| Target | range candle high                    | range candle low                      |

`entry_bps` defaults to 5000 (the purge candle's equilibrium). Entry is always
a limit; the model never takes a market fill.

## Configuration

`features.CandleRangeConfig`, all values frozen before a backtest:

- `min_range_ticks`, `min_purge_ticks`, `max_purge_bps`, `min_reclaim_bps`;
- `entry_bps`, `stop_buffer_ticks`, `min_reward_risk_bps`.

`crt.Config` adds the lifecycle envelope: `setup_timeframe`,
`confirmation_timeframe` (must divide the setup timeframe),
`entry_expiry_setup_bars`, `max_hold_setup_bars`, and
`require_trend_alignment`.

Every ratio is basis points of an integer tick distance and every computation
uses integer truncation, so detection is bit-reproducible across platforms.

## Strategy lifecycle

`SEEKING_RANGE -> ARMED -> FILLED -> COMPLETED | INVALIDATED | EXPIRED`

The machine is a research interpretation; it never submits an order. Position
size comes from the independent risk engine using the objective stop distance.

## Determinism rules

These four rules exist so that no replay result can depend on evaluation
order, and so no backtest can flatter itself:

1. **Knowability.** A pattern exists only at the purge candle's close.
   `DetectedAt` is that close time. Nothing earlier may reference it.
2. **Limit entry.** Entry fills only when a confirmation bar trades through
   the equilibrium. There is no market entry and no assumed fill.
3. **Pessimistic intrabar resolution.** If one confirmation bar spans both the
   stop and the target, the trade resolves to the **stop**. The intrabar path
   is unknown, so the unfavourable branch is mandatory. Relaxing this rule is
   the single largest source of fake backtest edge in this model.
4. **Mechanical expiry.** An unfilled limit dies when the expansion candle
   closes (`entry_expiry_setup_bars`). An open position is closed at the setup
   close after `max_hold_setup_bars`. Neither is a judgment call.

Ordering in the research harness follows from rule 3: a confirmation bar that
closes at the same instant as a setup candle is processed **first**, so fills
and exits resolve before the containing setup candle can age or time-stop the
position.

## Measured evidence (v0.1)

Run with `cmd/crtlab` over free vendor candles. Costs are charged on every
closed trade: spread once plus slippage on entry and exit. Risk is 1R per
trade, so expectancy is directly in R.

The window is bounded by **confirmation** coverage, not setup coverage: a
pattern whose fill window has no lower-timeframe data cannot be adjudicated,
so the harness counts it as `uncovered` and stands the machine down rather
than booking a phantom expiry. Free FX/metal 1h history is capped at roughly
two years, which is why those windows are shorter than their daily data.

Baseline (no context filter):

| Instrument | Setup/confirm | Window    | Source  | Trades | Win   | Expectancy | Profit factor | Max DD |
| ---------- | ------------- | --------- | ------- | ------ | ----- | ---------- | ------------- | ------ |
| BTCUSDT    | 4h / 15m      | 2023–2026 | Binance | 372    | 37.4% | +0.023 R   | 1.038         | 32.8 R |
| ETHUSDT    | 4h / 15m      | 2023–2026 | Binance | 353    | 33.4% | −0.079 R   | 0.878         | 40.9 R |

### FX and metals, re-derived from Dukascopy

The original FX and metal measurements came from Yahoo Finance and are
withdrawn: Yahoo duplicates the open into the close on 73% of EURUSD daily
bars, and the detector's reclaim test reads the purge candle's close
directly, so those runs measured a vendor defect rather than the strategy.
They reported EURUSD −0.331 R, GBPUSD −0.282 R and XAUUSD −0.512 R.

Re-measured on Dukascopy integer-point candles over 14 years — roughly seven
times the window and three times the sample:

| Instrument | Setup/confirm | Window    | Source     | Trades | Win   | Expectancy | Profit factor | Max DD |
| ---------- | ------------- | --------- | ---------- | ------ | ----- | ---------- | ------------- | ------ |
| EURUSD     | 1d / 1h       | 2012–2026 | Dukascopy  | 146    | 34.9% | −0.069 R   | 0.898         | 27.3 R |
| GBPUSD     | 1d / 1h       | 2012–2026 | Dukascopy  | 115    | 35.7% | −0.088 R   | 0.861         | 27.6 R |
| XAUUSD     | 1d / 1h       | 2012–2026 | Dukascopy  | 128    | 34.4% | −0.112 R   | 0.832         | 18.4 R |

Clean data moved every instrument the same way: still negative, but far
milder than the corrupt-data figures. The defect had exaggerated the losses
roughly fivefold. The conclusion is unchanged; the magnitude was never real.

A 21-cell sweep across these three instruments produced no filter that is
positive on all of them. `min R:R 2.5` is the best candidate at +0.275 R on
EURUSD and +0.151 R on XAUUSD, but −0.158 R on GBPUSD, on 24–26 trades each.
Split at mid-2019, the per-half estimates flip sign in **both** directions:

| Instrument | Variant      | In-sample (2012–2019) | Out-of-sample (2019–2026) |
| ---------- | ------------ | --------------------- | ------------------------- |
| EURUSD     | baseline     | +0.056 R (81)         | −0.225 R (65)             |
| EURUSD     | min R:R 2.5  | +0.906 R (12)         | −0.266 R (14)             |
| GBPUSD     | baseline     | −0.305 R (68)         | **+0.224 R** (47)         |
| GBPUSD     | min R:R 2.5  | −0.498 R (11)         | +0.108 R (14)             |
| XAUUSD     | baseline     | −0.075 R (68)         | −0.155 R (60)             |
| XAUUSD     | min R:R 2.5  | +0.053 R (14)         | +0.289 R (10)             |

GBPUSD's baseline swinging from −0.305 R to +0.224 R is the clearest
evidence available that these half-sample estimates carry no information
about the next half. A real edge does not reverse sign; noise does, in
whichever direction the sample happens to fall.

A 40-cell filter sweep (trend gate, reward/risk floors, purge depth, reclaim
depth, hold budget) produced exactly one attractive cell: BTCUSDT with the
swing-trend gate, 115 trades, +0.234 R per trade, profit factor 1.434. Split
in half by time, that cell does not survive:

| BTCUSDT + trend gate | Trades | Win   | Expectancy | Profit factor | Max DD | Worst streak |
| -------------------- | ------ | ----- | ---------- | ------------- | ------ | ------------ |
| 2023-11 … 2025-05    | 60     | 51.7% | +0.408 R   | 1.890         | 4.4 R  | 4            |
| 2025-05 … 2026-11    | 55     | 32.7% | +0.043 R   | 1.070         | 11.7 R | 11           |

The entire result sits in the first half. The same filter on ETHUSDT — the
most similar instrument available — is negative in every variant.

**Verdict: v0.1 has no demonstrated edge and must not be promoted.**

The evidence across five instruments, two data vendors, and 14 years:

- every baseline is negative except BTCUSDT at +0.023 R, which is
  indistinguishable from zero at profit factor 1.038 against a 32.8 R
  drawdown;
- no filter is positive across instruments; the best candidate on FX/metals
  fails on GBPUSD, and the best on crypto fails on ETHUSDT;
- every attractive cell decays or reverses out of sample, and GBPUSD's
  baseline reverses sign in the favourable direction, which rules out a
  one-sided bug as the explanation.

This is a negative result recorded on purpose. The detector, the lifecycle,
and the determinism rules are now fixed and testable, so later work is
measured against a known baseline instead of a fresh opinion. The most likely
defect is the entry condition, not the data and not the arithmetic: the
baseline arms on every qualifying pair, with no requirement that the purge
take validated liquidity.

## Live signal path

The live signal engine can emit CRT setups from a real-time feed
(`enable_crt`, default off). It reports the **ARMED** state only:

- the pattern is published at the purge candle's close, the first instant it
  is knowable;
- the entry is a resting limit at the purge equilibrium, flagged
  `EntryType: LIMIT`, because entering at market changes the trade;
- the signal expires one setup bar later, matching
  `entry_expiry_setup_bars`;
- only completed candles are processed, so an in-progress bar cannot repaint
  a setup;
- `max_signal_age_bars` must be set on a live feed that backfills history,
  or every historical pattern is announced as if it were current.

FILLED, COMPLETED and EXPIRED are not reported live. Adjudicating them needs
the confirmation-timeframe stream and the pessimistic intrabar rule, which
live in the research harness.

Because v0.1 has no demonstrated edge, a live signal is research evidence,
not a trade recommendation.

## Research questions

- Does the purge need validated liquidity context (prior session extreme, HTF
  imbalance, external range) rather than a swing-trend proxy? The baseline
  arms on every qualifying pair, which is the most likely defect.
- Is the opposite range extreme the right objective, or should the target be
  validated liquidity with a measured fallback?
- Does equilibrium entry beat a close-of-purge entry once the unfilled-expiry
  population is accounted for? It is 30% of armed setups on BTCUSDT, 33% on
  XAUUSD, but only 7–15% on EURUSD/GBPUSD.
- Which, if any, instrument/session subset survives walk-forward and an
  untouched holdout, with the multiple-comparison cost paid explicitly?
- Should a partial objective at the range midpoint replace the single target?
