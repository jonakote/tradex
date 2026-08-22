# Shared-account walk-forward evaluation

Tradex supports frozen walk-forward studies where each candidate is a complete multi-instrument shared scenario. Every candidate is evaluated through one account, one portfolio, one risk coordinator, and one paper broker, so correlated reservations affect candidate results at event time.

The shared study schema uses `candidates`, each containing a candidate ID and a `replay.SharedScenario`. Candidate differences are limited to per-leg strategy/ICC and execution definitions. Equity, risk limits, account ID, paper costs, value assumptions, instruments, symbols, and event streams must remain identical.

Commands:

```sh
go run ./cmd/tradex manifest-study-shared shared-study.json shared-study-manifest.json
go run ./cmd/tradex walk-forward-shared \
  shared-study.json shared-study-manifest.json \
  shared-walk-forward-audits shared-walk-forward-result.json
```

Each leg is split into the same number of ICC cycles. A segment joins the corresponding cycle from every leg, then `RunShared` merges all events by UTC time and lexical symbol order for ties. Training and test windows are non-overlapping; selection uses only development windows, and the final candidate is fixed before the untouched holdout is run.

The selection score remains net profit minus maximum drawdown, with a minimum completed-trade requirement. This is an account-level selection metric, not a guarantee that every leg is profitable. Review exposure-bucket vetoes, opportunity outcomes, rejected orders, no-fills, and per-symbol reports alongside the aggregate score.
