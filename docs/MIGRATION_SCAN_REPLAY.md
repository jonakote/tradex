# Scan replay migration — status

**Status: replay path migrated; ICC implementation still present elsewhere.**
Single- and shared-account replay, evaluation, reporting, CLI and MCP now run
on `scan.Stream`/`scan.Config`, not the ICC state machine. `internal/strategy/icc`
itself and its live-signal consumers (`tradex watch` / `internal/signal.Engine`)
were intentionally left untouched — they are a separate migration. This
document must not be read as execution authorization. All strategies remain
**UNVALIDATED**, for research/paper use only.

## Implemented interfaces

`internal/strategy/scan.Config` selects a real `scan.Detector`, not an ICC
wrapper. JSON requires `name` (`continuation`, `ict`, `amn`, or `ibs`). Only
parameters for the chosen detector may be supplied. Unknown fields, unknown
detectors, invalid settings and ICC configurations are rejected. An ICC name
or ICC key produces an explicit migration error.

Examples (detector configuration only; **not yet replay scenario files**):

```json
{"name":"continuation"}
```

```json
{"name":"ibs","hold_bars":2,"long_below_bps":2000,"short_above_bps":8000}
```

Optional `continuation`, `ict`, and `amn` objects are complete feature config
objects; omission chooses feature defaults. They do not merge partial objects
into defaults. IBS omission selects one-bar holding with 2000/8000 thresholds;
when either threshold is supplied the other zero threshold disables that side.

`scan.NewStream` is the incremental lifecycle used by `scan.Backtest`. It
requires a canonical instrument and its exact registry specification. Each
advance takes one validated `domain.Candle` with integer prices, in strictly
non-overlapping chronological order. The detector receives only a copy of the
trailing completed-bar window. Setups must be known exactly at the newest
bar's close, with internally consistent geometry and risk distance.

Lifecycle event kinds are `DETECTED`, `REJECTED`, `ENTRY`, `EXIT`, `EXPIRED`.
Each carries strategy/spec and `validation: UNVALIDATED`. Exit reasons are
`STOP_LOSS`, `TAKE_PROFIT`, and `TIME_EXIT`. IBS holds count bars after the
entry; it has no synthetic stop or target. Stop/target setups time out using
`MaxHoldBars`. Ambiguous stop/target bars resolve to the stop. Limits first
become fillable on a subsequent bar; a touch on the last expiry bar wins over
expiry. Expiry bars do not immediately re-arm, preserving batch semantics.

`AdvanceWithObserver(candle, admit, observe)` delivers events synchronously.
The host must settle exit events and release risk in `observe` before the next
`admit` call. This ordering prevents a shared account from evaluating a new
proposal against the previous position's stale exposure. Observer or admission
errors abort and poison the stream; it cannot continue after an audit failure.

The stream does not authorize orders, size positions, or apply account costs.
Its prices use the existing idealized bar model. `Backtest` still applies its
explicit spread/slippage cost model once per completed trade.

## Implemented (replay migration)

- `replay.Scenario` drops `ICC`/`execution.ICCPlan` for `scan.Config` detector
  selection plus `execution.SetupPlan` (generic sizing/cost/risk policy).
  `EventContext`/`EventIndication`/`EventSetupCandle`/`EventConfirmation` and
  any `"icc"` scenario field are rejected with an explicit migration error.
- Single- and shared-account replay drive `scan.Stream`/`AdvanceWithObserver`;
  `execution.Account.PrepareSetup` replaces `PrepareICC`. Frozen manifests,
  exclusive outputs, strict JSON, journal chaining and quote/liquidity/
  session/conversion inputs are preserved; `EngineVersion` bumped to `v4-scan`
  so old ICC manifests are rejected, not silently reinterpreted.
- Stopless (hold-bars, e.g. IBS) setups get an explicit, symmetric protective
  bracket sized from `SetupPlan.StoplessMaxAdverseExcursionTicks` — a declared
  **policy** parameter, never derived from a detector's `RiskTicks`
  measurement, and never an invented reward. See `internal/execution/setup.go`.
- Evaluation's ICC-cycle partitioning is now per-bar (`EventCandle`), the
  natural `scan.Stream` unit. Robustness needed no source changes (it consumes
  `replay.Result`/`SharedResult` generically).
- `internal/execution/icc.go` (`ICCPlan`/`PrepareICC`) deleted after its
  replay/evaluation/CLI/MCP consumers were migrated. `internal/strategy/icc`
  itself is untouched.
- New fixture `eurusd_continuation_win.json` replaces the deleted ICC
  fixtures; general regression coverage (frozen-manifest rejection, exclusive
  file creation, strict JSON, audit determinism, partial fills, rollover,
  exposure vetoes, concurrent approvals) was retargeted onto it, not dropped.
- Verified end-to-end: `manifest → replay → report` produces real output from
  this fixture; an old ICC-shaped scenario/manifest is rejected.

## Remaining (separate migration, not this one)

- `internal/signal.Engine.ProcessCandle` now takes `domain.Candle` directly;
  the `feedToDomainCandle` round-trip inside the engine is gone, and all
  price-action math (swings, structure, triangle geometry, entry/stop/target)
  stays in integer ticks until the final `Signal` (display-only floats).
  `cmd/tradex/legacy_candle.go` (`domainFromLegacyCandle`) is now a one-way
  float-`feed.Candle` → `domain.Candle` adapter for vendor sources not yet on
  `feed.Source` (Dukascopy's tick-derived live poller, Twelve Data, Finnhub,
  FCS, TradingView webhook). Binance and Capital.com already return
  `domain.Candle` and call `ProcessCandle` with no adapter. Deleting this file
  entirely requires migrating those remaining vendor sources to `feed.Source`.
- `internal/strategy/icc` package has been deleted entirely — no production
  or test consumer remained after the replay migration.
- Raw-compilation/derive paths beyond what replay/evaluation already route
  through `scan.Config` were not re-audited in this pass.
