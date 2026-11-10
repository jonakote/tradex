# Cross-sectional ranking (momentum / reversal) — tested, not confirmed

`spec: xsect-0.1.0` · tool: `cmd/xsectlab`

## The idea

Every strategy measured elsewhere in this project picks a side on ONE
instrument from its own pattern. Cross-sectional ranking is structurally
different: at each rebalance, score every instrument in a universe from its
own trailing return (momentum: recent winners keep winning; reversal:
recent losers bounce), rank them, go long the top K and short the bottom K
in equal weight, hold to the next rebalance. Because the basket is
long-short and equal-weighted, common-mode drift across the universe
cancels by construction — unlike IBS's single-instrument test, no separate
drift baseline is needed; the mean basket return net of cost is the edge
test directly.

This was built specifically to answer a prior question honestly: does this
instrument universe have ANY exploitable relative-strength structure, with
the simplest possible auditable score, before considering a fancier
forecaster (e.g. a pretrained price-forecasting model) to rank it instead.
If a trivial return-based score finds nothing, a better forecast doesn't
obviously fix that — the problem would be the universe/frequency, not the
scoring function.

## Measured (daily, 7-instrument universe: EUR/GBP/AUD/USDCAD/USDJPY vs USD,
XAUUSD, XAGUSD; ~2 years; top 2 long / bottom 2 short; 3bp/leg cost)

| mode | lookback/hold | periods | full | first half | second half | verdict |
|---|---|---|---|---|---|---|
| momentum | 60d / 10d | 56 | +25.45bp | +57.39bp | **−6.49bp** | not robust |
| momentum | 120d / 20d | 25 | +150.12bp | +244.70bp | +62.83bp | not measurable (too few periods) |
| reversal | 5d / 5d | 123 | −14.68bp | +18.62bp | **−47.46bp** | negative |
| reversal | 21d / 10d | 60 | −56.84bp | −18.51bp | −95.18bp | negative, consistent |

**Verdict: not promotable, and the one eye-catching number is a red flag,
not a lead.** The 120d/20d momentum full-sample +150bp/period looks
exciting, but inspecting the actual rebalances shows the long leg is
XAUUSD+XAGUSD in nearly every period — this is not a repeating
cross-sectional signal, it is one concentrated bet on the 2025-2026 gold/
silver bull market dressed up as a 7-instrument factor. Exactly the
single-regime trap this project has hit before (Continuation's
single-instrument confirms, StructureTrend's USDJPY-only result). The
60d/10d config, with more periods and no single dominant trade, is the
fairer read and it fails outright (halves disagree).

## What this says about adding a forecasting model (e.g. Kronos) instead

The simplest possible cross-sectional score — plain lookback return — finds
no robust structure in this universe at these frequencies, and the one
result that looked positive was a single concentrated regime, not a
repeatable factor. A better per-instrument forecaster would only help a
cross-sectional ranking strategy if the universe has persistent, rankable
dispersion to begin with. This test is weak evidence that it mostly
doesn't, at least not at daily rebalance on 7 FX/metals instruments —
swapping the scoring function for a transformer's forecast is unlikely to
manufacture structure that a basic return score can't find any trace of.
Not conclusive on its own (one universe, one frequency, one cost
assumption), but it does not support spending the integration cost of a
pretrained forecasting model as the next move.
