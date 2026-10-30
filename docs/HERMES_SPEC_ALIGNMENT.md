# Hermes specification alignment

The external Hermes specification pack is the canonical product contract. The
current TradeX implementation is a mature deterministic research and paper
execution kernel, but it is not yet the complete Hermes pipeline.

## Current mapping

| Hermes stage | TradeX implementation | Status |
|---|---|---|
| Data and validation | `internal/marketdata`, feed clients, quote/candle validators | Implemented for current providers and replay inputs |
| Confirmed swings and structure | `internal/features`, `internal/strategy/icc` | Implemented for the ICC baseline; internal/external structure separation and full MSS semantics remain |
| Session/time context | `internal/session`, broker schedule support | Implemented and DST-aware |
| ICC methodology | `internal/strategy/icc`, raw compiler, replay | Implemented as versioned ICC v1 research behavior |
| Triangle pattern | `internal/features/triangle.go`, versioned triangle spec | Deterministic geometry detector added; breakout strategy validation pending |
| Liquidity map and sweeps | session/swing inputs exist, but no complete Hermes liquidity engine | Gap |
| Price delivery, FVG, displacement, POI | partial execution/feature primitives only | Gap |
| Composite strategy pipeline | no AMN/SLP/APA/ICT composition yet | Gap |
| Risk and portfolio risk | `internal/risk`, `internal/execution`, exposure buckets, kill-switch controls | Implemented for the current paper/replay contract |
| Execution and broker boundary | paper broker plus gated MT4/MT5/cTrader adapters | Paper implemented; live promotion remains gated |
| Research and reproducibility | replay, manifests, walk-forward, robustness, reports | Implemented |
| Hermes client boundary | `tradex-mcp` stdio server | Implemented as research-only MCP access |

## Non-negotiable invariants

- The default outcome is `NO_TRADE`/rejection when a hard gate fails.
- AI and MCP clients cannot bypass risk, exposure, stop, stale-data, duplicate,
  or kill-switch controls.
- Strategy output is separate from execution authorization.
- All historical features use only information available at their event time.
- New behavior requires a versioned specification, tests, and reproducible
  research before paper or live promotion.

## Next implementation order

1. Define versioned Hermes domain contracts for context, liquidity, POI, setup,
   trigger, risk decision, and trade intent.
2. Integrate triangle evidence into the setup/trigger state machine and validate
   its breakout, retest, target, and failure hypotheses.
3. Add the deterministic liquidity engine: BSL/SSL, EQH/EQL, session/day/week
   levels, sweeps, expiry, and target candidates.
4. Add price-delivery and POI engines with explicit FVG, displacement,
   mitigation, and invalidation rules.
5. Introduce the `NO_TRADE -> WATCH -> SETUP -> ARMED -> TRIGGERED -> EXECUTABLE`
   state machine and keep AMN/SLP/APA/ICC/ICT as independent evidence fields,
   not votes.
6. Add fundamentals/news-firewall inputs and a versioned Hermes signal-schema
   adapter without giving the AI layer order authority.
7. Validate each increment with walk-forward, holdout, transaction-cost, and
   robustness tests before combining hypotheses.

Ambiguous creator-specific methodology rules remain research hypotheses; they
must not be silently invented in code.
