# Calendar/session anomalies — both tested, neither confirmed

`spec: season-0.1.0` · tool: `cmd/seasonlab`

Two specific, well-cited, falsifiable academic claims, tested the same way
as everything else in this project: full sample + both halves, net of
cost, a trade-count floor before trusting any verdict.

## 1. Overnight vs intraday (Cooper & Cliff 2008; Lou/Polk/Skouras)

**Claim**: the US equity premium is earned almost entirely overnight
(close-to-open); intraday (open-to-close) returns are flat or negative.
Only meaningful for instruments with a real session close — tested on the
three US indices, not FX (near-continuous 24/5).

6 years, US500/US100/US30, net of 2bp cost each leg:

| Instrument | Overnight (full) | Intraday (full) |
|---|---|---|
| US500 | **−2.03bp** | +2.54bp |
| US100 | **−1.94bp** | +3.51bp |
| US30 | **−1.89bp** | +1.36bp |

**Verdict: contradicted, not just unconfirmed.** Overnight is consistently
*negative* here and intraday *positive* — the exact opposite sign from the
2008-era literature, holding in both halves on all three indices. Most
likely explanation: this is a documented, published anomaly from 15+ years
ago; anomalies this well-known tend to get arbitraged away or structurally
change (CFD overnight mechanics differ from the cash-equity session the
original papers used, and don't even include financing/swap cost, which
would make real overnight carry on a CFD worse still). Good reminder that a
citation alone is not evidence for THIS market, THIS instrument, THIS
decade — it still has to be measured, same as everything homegrown here.

## 2. Turn-of-month (Ariel 1987; Lakonishok & Smidt 1988)

**Claim**: returns are concentrated in a ~4-day window (last trading day
of the month through the first 3 of the next) vs the rest of the month.

Tested on US500/US100/US30 (6y) and EURUSD/GBPUSD/AUDUSD/USDCAD/USDJPY/
XAUUSD/XAGUSD (2y), edge = mean(TOM) − mean(non-TOM):

| Instrument | Full edge | First half | Second half | Verdict |
|---|---|---|---|---|
| US500 | +1.86bp | +6.93bp | **−3.23bp** | not robust |
| US100 | +1.44bp | +6.35bp | **−3.49bp** | not robust |
| US30 | +1.36bp | +4.79bp | **−2.08bp** | not robust |
| EURUSD | −0.84bp | +1.87bp | −3.63bp | does not confirm |
| GBPUSD | −1.69bp | −4.27bp | +0.94bp | does not confirm |
| AUDUSD | +2.95bp | +5.74bp | +0.15bp | "confirms" — but see below |
| USDCAD | −0.68bp | −5.46bp | +4.19bp | does not confirm |
| USDJPY | −7.29bp | −5.72bp | −8.87bp | does not confirm |
| XAUUSD | −7.82bp | +3.29bp | −19.27bp | does not confirm |
| XAGUSD | −36.65bp | −3.74bp | −70.15bp | does not confirm |

**Verdict: not promotable.** All three US indices show the exact
halves-flip pattern seen everywhere else in this project (strong in the
first half, negative in the second). AUDUSD is the only "both halves
positive" result out of 10 instruments tested — with a second-half edge of
+0.15bp, i.e. noise, and exactly the ~1-in-10 false positive rate you'd
expect from testing 10 instruments on one hypothesis by chance (multiple-
comparisons, not a lead).

## Takeaway

Two citable, specific, "this should obviously work" academic claims, both
fail on real measurement — one flips sign entirely from its own source
paper. Reinforces the project's standing lesson: a narrative or a citation
is not a substitute for `stratlab`/`ibsv2`/`seasonlab` on this venue's
actual data. The daily IBS result (XAGUSD, EURUSD, now US100 — see
`docs/MEANREVERSION_SPEC.md`) remains the only thing in this project that
has survived that bar.
