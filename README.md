# Tradex

Tradex is a planned Go trading-research and automation system for major USD, EUR, GBP, JPY and AUD FX pairs plus XAU/USD and XAG/USD. It will research price action, Trades by Sci's ICC, ICT, SMC, CRT and liquidity rules, targeting Pepperstone, Exness and Trade Nation with optional TradingView validation. The project begins in research/paper-trading mode; live execution is deliberately gated behind statistical, operational, and risk controls.

Start with the [draft design and research plan](docs/TRADING_BOT_PLAN.md).

Current engineering progress is tracked in [implementation status](docs/IMPLEMENTATION_STATUS.md).

The implemented path currently reaches deterministic, shared-account multi-instrument paper fills with latency, partial liquidity, variable spreads, explicit market state, rollover, hash-chained audit output, and cost-adjusted analytics. No live broker adapter is enabled.

## Development

The module targets Go 1.23 and currently has no third-party dependencies.

```sh
go test ./...
go test -race ./...
go vet ./...
```

If Go is not installed locally, the same checks can run with Docker:

```sh
docker run --rm -v "$PWD:/src" -w /src golang:1.23 go test ./...
```

Freeze the experiment inputs, then run the fixture against that immutable manifest and write a new append-only audit journal:

```sh
go run ./cmd/tradex manifest \
  internal/research/replay/testdata/eurusd_icc_win.json \
  /tmp/eurusd-manifest.json

go run ./cmd/tradex replay \
  internal/research/replay/testdata/eurusd_icc_win.json \
  /tmp/eurusd-manifest.json \
  /tmp/eurusd-audit.jsonl \
  > /tmp/eurusd-result.json

go run ./cmd/tradex report \
  /tmp/eurusd-result.json \
  /tmp/eurusd-report.json \
  /tmp/eurusd-report.md
```

Both output paths must be new. Tradex refuses to overwrite a frozen manifest, reuse an audit journal, or run a scenario that no longer matches its manifest.

To replay several instruments against one account, place two or more complete single-instrument scenarios in the top-level `legs` array, with common `initial_equity_minor`, `risk`, paper account ID, and maximum quote age. Then freeze and run the shared scenario:

```sh
go run ./cmd/tradex manifest-shared shared-scenario.json shared-manifest.json
go run ./cmd/tradex replay-shared shared-scenario.json shared-manifest.json shared-audit.jsonl > shared-result.json
go run ./cmd/tradex report-shared shared-result.json shared-report.json shared-report.md
```

The shared engine merges events by UTC timestamp and then lexical symbol order for ties. All legs use one paper broker, portfolio, and risk coordinator, so an earlier EUR/USD reservation can constrain a later GBP/USD or AUD/USD decision through their common USD exposure bucket. Output contains an account-wide report and per-symbol reports.

Report export writes a versioned, self-verifying JSON artifact and a deterministic Markdown review. The report fingerprints both the frozen manifest and exact replay result, summarizes decisions and rejections, and includes account, symbol, execution, and open-risk metrics. Both report paths must be new; a report hash detects modification but is not a digital signature.

Reports also include an opportunity funnel for every ICC indication: unconfirmed, invalidated, risk-rejected, broker-rejected, no-fill, pending, open, and closed. These are not extra trades and do not change win-rate calculations. See [opportunity analytics](docs/OPPORTUNITY_ANALYTICS.md).

Execution conditions can be frozen directly into replay inputs. `submission_latency` delays eligibility; a quote's optional `available_quantity` caps entry quantity; an execution candle's optional `half_spread_ticks` overrides its synthetic spread; and `MARKET_STATUS` events explicitly open or close the simulated market. Optional signed rollover rates accrue at configured UTC boundaries and are included in net P&L. See [the execution realism model](docs/EXECUTION_REALISM.md) for the schema semantics and limitations. Broker-specific session calendars and swap schedules must be sourced for the exact historical account and period rather than inferred from a generic FX calendar.

For candidate selection, define a walk-forward study containing at least two full scenarios with identical event data and common account, risk, instrument, and cost assumptions. Reserve the final cycles as holdout, freeze the study, and run it into a new audit directory:

```sh
go run ./cmd/tradex manifest-study study.json study-manifest.json
go run ./cmd/tradex walk-forward \
  study.json \
  study-manifest.json \
  walk-forward-audits \
  walk-forward-result.json
```

The evaluator selects on anchored expanding development windows, tests the selected candidate on the next non-overlapping cycle window, then selects once on all development data before evaluating the reserved holdout. Candidate ties are broken lexically. The current fixed selection score is net profit minus maximum drawdown, subject to a configured minimum number of completed training trades.

For multi-instrument candidate selection, use the shared-study schema and commands. Each candidate must contain the same symbols, instruments, account, risk, costs, and event streams, while its per-leg ICC/execution definitions may vary:

```sh
go run ./cmd/tradex manifest-study-shared shared-study.json shared-study-manifest.json
go run ./cmd/tradex walk-forward-shared shared-study.json shared-study-manifest.json shared-walk-forward-audits shared-walk-forward-result.json
```

Shared cycles are joined across legs and replayed through one account-level risk coordinator. See [shared-account walk-forward evaluation](docs/SHARED_WALK_FORWARD.md).

Freezing prevents silent study changes and the engine never feeds holdout metrics into selection. It cannot prevent a researcher from manually examining the holdout period before creating the study, so dataset governance remains part of the research protocol.

After a replay has at least three completed trades, generate a seeded stationary-bootstrap and Deflated Sharpe analysis with an explicit configuration:

```json
{
  "iterations": 10000,
  "mean_block_length": 3,
  "seed": 20260819,
  "ruin_drawdown_bps": 1000,
  "risk_free_return_per_trade": 0,
  "independent_trials": 2,
  "trial_sharpe_ratios": [0.12, 0.31]
}
```

```sh
go run ./cmd/tradex robustness replay-result.json robustness-config.json robustness-result.json
# For a shared-account replay result:
go run ./cmd/tradex robustness-shared shared-result.json robustness-config.json robustness-result.json
```

The trial Sharpe values must include every attempted candidate and use the same non-annualized per-trade return estimator. Tradex cannot discover omitted experiments or infer the true number of independent trials. See [the robustness methodology](docs/ROBUSTNESS_METHOD.md) for formulas, estimator definitions, and limitations.

For provenance-bearing H4/H1/M15 MID-candle research, compile mechanically derived ICC events before freezing the experiment:

```sh
go run ./cmd/tradex compile-raw raw-scenario.json compiled-scenario.json
go run ./cmd/tradex manifest compiled-scenario.json manifest.json
go run ./cmd/tradex replay compiled-scenario.json manifest.json audit.jsonl
```

The raw input must identify its vendor, broker/feed, retrieval time, source timezone, price basis, licence reference, and original artifact checksum. Candle-only execution uses an explicit synthetic spread; it is not equivalent to historical broker bid/ask data.

Raw compilation can accept a single base stream below M15 by setting `base_timeframe`. Tradex then derives M15, H1, and H4 on UTC boundaries, records transformation lineage, and drops every incomplete or gapped aggregate bucket. The resulting drop reports are part of the frozen scenario.

For broker-native execution research, validate and compile bid/ask observations with `tradex compile-quotes base-scenario.json quotes.json compiled-scenario.json`. This replaces synthetic quote events, preserves the strategy events, and freezes quote-feed provenance into the resulting manifest. See [the quote dataset contract](docs/QUOTE_DATASET.md).

For broker/account-specific hours and swaps, compile a provenance-bearing schedule with `tradex compile-schedule base-scenario.json schedule.json compiled-scenario.json`. This injects market-status transitions and applies the exact rollover configuration for the schedule's effective period. See [broker schedules](docs/BROKER_SCHEDULES.md).

Normalize broker aliases and timestamp formats with `tradex normalize-quotes raw-quotes.json normalization.json quotes.json`. Account-currency conversion streams are validated separately and can be compiled into a scenario for rational USD/JPY-style valuation. See [quote normalization](docs/QUOTE_NORMALIZATION.md).

Repeated ICC setups are evaluated as independent state-machine cycles. Equity, drawdown, daily/weekly P&L, loss streak, aggregate reserved risk, and position counts carry forward. The default risk policy permits at most three total reservations and one position per symbol, so overlapping EUR/USD signals are vetoed rather than stacked.

Shared-account approvals also reserve gross risk in both instrument currencies or underlyings. The default exposure-bucket cap is 0.50% of equity: long EUR/USD and long GBP/USD therefore accumulate in the same USD bucket even though they are different symbols. Signed direction is recorded separately, but opposite trades do not automatically erase gross risk.

The included fixtures are engineering tests, not historical performance evidence.

The Pepperstone cTrader adapter boundary is documented in [CTRADER_ADAPTER.md](docs/CTRADER_ADAPTER.md). It requires explicit promotion approval and successful reconciliation before order mutations; a production cTrader transport is not enabled yet.

> This repository is for engineering and research, not a promise of returns or financial advice. Leveraged trading can lose more than the amount initially funded.
