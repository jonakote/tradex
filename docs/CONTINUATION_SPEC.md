# Break-and-retest continuation — `continuation-0.1.0`

Status: **measured, negative. Must not be promoted.**

Detector: `internal/features/continuation.go`
Harness: `internal/research/contlab`, `cmd/contlab`
Live path: `cmd/tradex/watch.go` (monitor only, no order authority)

## What it claims

A deterministic version of a discretionary trend-continuation read, built to
carry the filters whose absence made `crt-0.1.0` unmeasurable:

1. **Drift is not a trend.** A run of same-direction closes can be positioning
   rather than intent, and it reverses hardest on the next catalyst. Net
   displacement alone never qualifies: the move must also *take out a prior
   structural extreme* (default, the low or high of the preceding 24 bars).
2. **Never chase the broken level.** The order is a resting limit at the broken
   level itself, on the premise that failed support is tested as resistance.
3. **The stop must be wider than the cost of being there.** Risk is floored at
   a multiple (default 4x) of the measured spread.
4. **The target must be a price that actually traded** — the extreme of a
   48-bar window. Reward/risk is an output to filter on, not an input.

Stop placement: above the high (below the low) of the leg that broke the level.
If price reclaims that extreme, the break failed and the premise is void.

## Determinism rules the harness encodes

- A setup is knowable only at its confirming bar's **close**; a fill is
  rejected at or before `DetectedAt`.
- When one bar spans both stop and target, the trade resolves to the **stop**.
  The count of such bars is reported (`ambiguous_bars_resolved_to_stop`) so the
  result's dependence on that assumption is visible.
- Costs are charged on every closed trade: spread once, slippage on entry and
  exit. Results are in R.
- One position at a time. Concurrent positions would make the equity curve a
  function of an unspecified sizing policy.

## Measured evidence

Capital.com bid candles, 1h, 2018-06-01 to 2026-10-30, ~52,000 bars per
instrument. FX costs: 7-tick spread + 2-tick slippage each side. XAUUSD:
500-tick spread + 100-tick slippage (Dukascopy-scale points).

### Entry mode `retest` (limit at the broken level)

| Instrument | Trades | Full | First half | Second half | Win% | Max DD |
| ---------- | ------ | -------- | ---------- | ----------- | ---- | ------ |
| EURUSD     | 199    | −0.172 R | −0.150 R   | −0.193 R    | 25.1 | 53.5 R |
| GBPUSD     | 199    | −0.123 R | −0.063 R   | −0.181 R    | 24.6 | 33.3 R |
| AUDUSD     | 203    | −0.147 R | −0.010 R   | −0.283 R    | 26.1 | 42.1 R |
| XAUUSD     | 174    | −0.090 R | −0.150 R   | −0.038 R    | 28.7 | 25.4 R |

### Entry mode `break_close` (market at the confirming close, measured-move target)

| Instrument | Trades | Full | First half | Second half | Win% | Max DD  |
| ---------- | ------ | -------- | ---------- | ----------- | ---- | ------- |
| EURUSD     | 1658   | −0.148 R | −0.048 R   | −0.245 R    | 25.0 | 268.1 R |
| GBPUSD     | 1696   | −0.093 R | −0.038 R   | −0.147 R    | 24.8 | 205.7 R |
| AUDUSD     | 1786   | −0.192 R | −0.157 R   | −0.232 R    | 24.9 | 346.6 R |
| XAUUSD     | 1501   | +0.008 R | +0.035 R   | −0.016 R    | 30.8 |  60.5 R |

**Verdict: negative in both entry modes, on every FX instrument, in both
halves. XAUUSD break_close is flat (profit factor 1.011), i.e. noise around
zero, not an edge.** The two entry modes were a genuine test, not a sweep: the
retest and break_close fill disjoint subsets of the armed setups (retest fills
only pullbacks, break_close fills everything), and both lose. The pattern does
not predict direction.

## Why it fails — the diagnosis worth keeping

The retest booked a 61% unfilled rate, which looked like adverse selection:
the setups that fill are the ones where price retraced all the way back, which
is itself evidence the break failed. break_close was the control — it fills
every armed setup at the confirming close, removing the fill filter entirely.
It is also negative, at the same ~25% win rate.

So the fault is not the entry. The break-and-retest pattern, as defined here —
displacement plus a structural-extreme break — carries no forward directional
information at the 1h timeframe on FX majors. A ~25% win rate against ~3.5R
geometry needs ~29% to break even, and across ~13,000 trades it never gets
there. No entry rescheme changes a signal with no signal in it.

## Open questions, in the order worth testing

The pattern has no edge at 1h on FX majors in either entry mode. Before
spending more on this pattern family, the higher-information moves are:

1. **Change the timeframe, not the parameters.** 1h FX is close to a random
   walk net of cost. The daily/4h timeframe carries more of whatever trend
   persistence exists, and cost is a far smaller share of a wider stop.
   Re-measure the same detector on 4h and daily before touching its rules.
2. **Change the instrument class.** Crypto (Binance, keyless, already wired)
   trends more persistently than FX majors and is the cheapest place to find
   out whether the pattern works anywhere. Metals were the least-bad here,
   which is weak corroboration that trendier instruments matter.
3. **Add a regime filter that is itself measured.** Only trade the break when
   a longer-timeframe trend agrees. This is one parameter, so it must be split
   out of sample, and it can only ever reduce the sample — it cannot create an
   edge that is not present in the unfiltered population.

What is NOT worth doing: sweeping the existing parameters on 1h FX. A 40-cell
sweep will surface a winner by chance, and the two entry modes already measured
show the underlying signal is empty. That is the CRT lesson, repeated.

## Negative results already recorded, so they are not repeated

- `continuation-0.1.0`, entry `retest` and `break_close`: negative on EURUSD,
  GBPUSD, AUDUSD and XAUUSD at 1h across 2018-2026.
- 4h and daily FX/metals: full-sample numbers turn positive but the two halves
  disagree in sign (EURUSD daily −0.509 R then +0.736 R; XAUUSD 4h −0.665 R
  then +0.429 R) on 20-63 trades. That is small-sample variance, not an edge —
  an edge does not change sign between halves.
- Crypto BTCUSDT/ETHUSDT 4h `break_close` looked positive in both halves
  (+0.14 R / +0.20 R) — but ONLY because the run charged a fixed 2-4 tick cost,
  which is $0.02-0.04 on a $40,000 instrument. Binance's real taker fee is
  0.1% per side, percentage-based. Re-run with a representative 0.3% all-in
  cost, BTC falls to −0.088 R and ETH to −0.005 R. The edge was the missing
  cost.
- `crt-0.1.0` (see `CANDLE_RANGE_SPEC.md`): no demonstrated edge across five
  instruments, two vendors and 14 years.

### Cost-model lesson for crypto

`contlab` charges cost in fixed instrument ticks, which is correct for FX
(spread is roughly constant in pips) but wrong for crypto, where the fee is a
percentage of notional and the price ranges 30x across the sample. A fixed
tick cost silently approaches zero as a share of a large stop, manufacturing
an edge. Until the harness models percentage fees, a crypto result must be
sanity-checked by re-running at a cost representing the fee at the median
price, and the fragile ones discarded. The honest BTC/ETH figures above are
flat-to-negative.
