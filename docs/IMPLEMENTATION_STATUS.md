# Implementation status

Last updated: 2026-08-20

## Milestone 1: research kernel foundation

Implemented:

- Go 1.23 module with no third-party dependencies;
- broker-neutral instruments, market snapshots, order intents, orders and positions;
- fixed-point price representation suitable for broker-specific precision;
- strict JSON runtime configuration and single-active-broker enforcement;
- deterministic event replay that rejects out-of-order input;
- injectable system/fixed clocks;
- Sydney, Tokyo, London and New York sessions using IANA timezones and DST-aware windows;
- London–New York overlap detection;
- independent, fail-closed pre-trade risk engine;
- risk-based quantity sizing rounded down to broker quantity steps.

The risk engine currently rejects:

- malformed, expired, incorrectly sized or directionally invalid orders;
- stale or invalid quotes;
- broker disconnection or reconciliation mismatch;
- per-trade and aggregate exposure breaches;
- daily/weekly loss stops and drawdown kill-switch breaches;
- excessive consecutive losses;
- reward/risk below the configured minimum;
- spreads above the configured maximum.

## Verification

Verified in the official `golang:1.23` container:

```text
go test ./...       PASS
go test -race ./... PASS
go vet ./...        PASS
```

## Milestone 2: market structure and ICC research engine

Implemented:

- canonical bid/ask quote and completed-candle domain events;
- OHLC geometry, stream identity, ordering, overlap, alignment and gap validation;
- deterministic quote-to-candle aggregation using mid-prices without synthesizing missing bars;
- duplicate sequence and out-of-order quote rejection;
- non-repainting swing-high/low detection with configurable left/right windows;
- separate pivot time and confirmation/known time on every swing;
- higher-high/higher-low and lower-high/lower-low trend classification;
- close-based bullish/bearish structure-break events emitted once per confirmed swing;
- mechanical, versioned ICC v1 state machine;
- aligned H4/H1 context, H1 indication/correction and M15 continuation flow;
- session eligibility, correction expiry and structural invalidation;
- symmetric bullish/bearish ICC behavior;
- immutable transition evidence and signal references for replay audits.

The ICC implementation is a research specification, not a claim that discretionary chart interpretation has been perfectly reproduced. Changes to its mechanics require a new strategy version and a new out-of-sample evaluation.

## Milestone 3: risk-approved paper execution

Implemented:

- compile-time separation between raw `OrderIntent` and `ApprovedOrder`;
- risk approval identity and validity-window enforcement;
- deterministic, concurrency-safe paper broker implementing the broker interface;
- idempotent intent submission and rejection of conflicting intent-ID reuse;
- acknowledged, filled, cancelled and rejected order lifecycle states;
- market, limit and stop-entry trigger rules using executable bid/ask sides;
- configurable adverse slippage and per-quantity-step commission;
- protective stop-loss and take-profit processing;
- conservative stop-first handling when one OHLC candle can reach both protective levels;
- fixed-point gross, fill-level net and closed-trade net P&L;
- checked arithmetic for commission and P&L overflow;
- ICC execution planner that converts a completed signal into a market intent;
- stop buffering, cost-aware net reward/risk targeting and risk-budget sizing;
- full signal → sizing → risk approval/veto → paper fill integration test.

The paper economics require a broker/account-currency value for one price unit per quantity step. That value is straightforward for USD-quoted instruments in a USD account but must be updated using a conversion quote for crosses such as USD/JPY, EUR/JPY and GBP/JPY. Until dynamic conversion is implemented, those crosses cannot be promoted beyond research fixtures.

## Milestone 4: currency valuation, audit integrity, and analytics

Implemented:

- rational account-currency value per price unit and quantity step;
- direct valuation for account-quoted instruments such as EUR/USD and XAU/USD in USD accounts;
- explicit quote-to-account conversion for crosses such as USD/JPY;
- loss rounding upward, reward rounding downward, and realized P&L rounding only after full quantity;
- rational valuation integrated into ICC sizing and paper realized P&L;
- EUR/USD, USD/JPY and XAU/USD valuation fixtures;
- disk-backed JSONL audit journal opened in append-only mode;
- monotonic audit sequence and correlation IDs;
- SHA-256 hash chaining with verification on reopen/read to detect modification or truncation discontinuity;
- optional `fsync` after each audit event;
- concurrent append serialization;
- first trade report with wins/losses/breakeven, win rate, 95% Wilson interval, net result, expectancy, profit factor, commissions, maximum drawdown and losing streak.

## Milestone 5: deterministic audited strategy replay

Implemented:

- timestamped cross-currency conversion snapshots with source provenance;
- fail-closed rejection of missing, stale, or future-dated conversion rates at signal execution time;
- monotonic manual clock for deterministic integration runs;
- strict JSON scenario loader with unknown-field and trailing-document rejection;
- ICC context → indication → correction → confirmation coordination;
- cost-aware sizing → independent risk approval → paper order/fill coordination;
- equity updates from closed trades and final analytics generation;
- hash-chained audit events for strategy state, quotes, risk decisions, orders, fills, and reports;
- deterministic EUR/USD winning fixture with exact spread, slippage, commission, quantity, target, and net result;
- deterministic XAU/USD gap-through-stop fixture proving realized loss may exceed planned stop risk;
- JSON-safe representation of infinite profit factor for all-winning samples;
- `tradex replay <scenario.json> <manifest.json> <audit.jsonl>` command for repeatable, manifest-verified research runs.

The fixture inputs are synthetic and prove engineering behavior only. They do not estimate an edge or support an 80% win-rate claim. Raw-data experiments must use a separate calibration set, frozen strategy/configuration versions, and an untouched holdout set.

## Milestone 6: immutable experiment identity

Implemented:

- deterministic SHA-256 fingerprints for the dataset, replay engine, strategy, risk rules, cost model, instrument metadata, and complete normalized scenario;
- a self-hashed `tradex.experiment/v1` manifest with strict validation;
- semantic engine and component versions alongside content fingerprints;
- exclusive manifest creation that refuses to overwrite a frozen experiment;
- strict manifest loading with unknown-field, trailing-document, digest, and self-hash checks;
- verified replay that rejects any dataset or configuration change made after the manifest was frozen;
- rejection occurs before an audit file is created or an order can be considered;
- the verified manifest is the first event in every replay audit chain;
- exclusive audit creation so separate runs cannot silently share one journal;
- CLI workflow to freeze a manifest first and execute only against that manifest.

Content fingerprints make accidental or undisclosed changes visible. They do not prove that a dataset is accurate, representative, licensed, or free of selection bias; those properties require source provenance and experimental controls.

## Milestone 7: raw candle provenance and structure derivation

Implemented:

- strict `tradex.candles/v1` multi-timeframe candle-dataset schema;
- required dataset ID, vendor, feed, broker, retrieval time, source timezone, timestamp basis, price basis, raw-artifact SHA-256, and licence reference;
- UTC, retrieval-time, stream-identity, duplicate, ordering, overlap, alignment, and explicit gap-policy validation;
- deterministic full-dataset fingerprints that change with candle or provenance changes;
- completed-candle swing, trend, and structure-break derivation for every timeframe;
- higher-timeframe-first ordering when different candles close simultaneously;
- prefix-invariance test proving future candles cannot rewrite already-derived events;
- raw H4/H1/M15 compiler that derives ICC context, indication, correction, and continuation without caller-labelled structure events;
- DST-aware allowed-session evaluation during raw compilation;
- explicit MID-price requirement and disclosed synthetic bid/ask spread for candle-only research;
- future confirmation candles routed through the adverse paper fill model;
- raw dataset retained in the compiled scenario and fingerprinted directly by its experiment manifest;
- exclusive `tradex compile-raw <raw.json> <scenario.json>` workflow;
- end-to-end raw candles → derived ICC → risk approval → paper fill → audited report integration test.

The raw compiler accepts either separate completed H4/H1/M15 streams or the validated base-timeframe path added in milestone 8. It does not reconstruct historical bid/ask. Synthetic spreads are suitable only for conservative research experiments and must be replaced by broker bid/ask history where available.

## Milestone 8: validated timeframe resampling

Implemented:

- exact OHLC and tick-count aggregation from one validated base timeframe;
- target timeframe must be a larger whole multiple of the source timeframe;
- UTC-aligned aggregation boundaries for the M5 → M15/H1/H4 research path;
- incomplete edge buckets and internally gapped buckets are dropped rather than padded;
- entirely missing target buckets are counted without creating synthetic candles;
- checked tick-count accumulation with overflow rejection;
- per-target input, output, and dropped-bucket reports;
- parent dataset fingerprint and versioned `OHLC_RESAMPLE` transformations retained in the derived dataset;
- derived dataset receives a distinct identity while preserving original raw-artifact provenance;
- resampling reports retained in compiled scenarios and frozen into experiment manifests;
- optional raw-compiler base timeframe that automatically constructs M15, H1, and H4 before structure derivation;
- positive OHLC/lineage fixtures plus gap and whole-bucket omission tests.

Resampling changes representation, not information quality. It cannot recover missing M5 observations, historical spread, tick ordering, or intrabar paths. Dropped-bucket counts must be reviewed before an experiment is accepted.

## Milestone 9: multi-cycle strategy and portfolio safety

Implemented:

- ICC state-machine replacement only after `COMPLETED` or `INVALIDATED` terminal states;
- transition history retained across every strategy cycle;
- raw compiler scans and derives multiple independent indication/correction/continuation lifecycles;
- confirmation-timeframe candles continue managing existing positions while the next setup is sought;
- correction-bar expiry mirrored during raw compilation;
- thread-safe portfolio tracker with current equity and peak-equity drawdown;
- per-intent worst-case risk reservation from approval until closing fill;
- aggregate reserved risk, open-position count, and same-symbol position count;
- UTC daily and ISO-week realized P&L periods;
- consecutive-loss tracking across strategy cycles;
- all portfolio state supplied to every independent risk evaluation;
- approved and rejected decisions retain their estimated loss and reward for auditability;
- configurable global open-position and same-symbol position limits;
- explicit `OPEN_POSITION_LIMIT` and `SYMBOL_POSITION_LIMIT` veto codes;
- sequential two-trade fixture proving state reset and carried equity;
- overlap fixture proving a second EUR/USD position is rejected while the first remains open;
- raw-candle fixture deriving and executing two complete ICC cycles.

The replay scenario at this milestone still represents one instrument/account. Milestone 10 extends its account boundary with cross-symbol currency and metal exposure buckets while retaining the same single-symbol replay file format.

## Milestone 10: shared-account exposure buckets

Implemented:

- each approved FX or metal reservation allocates worst-case risk to both base/underlying and quote-currency buckets;
- gross bucket risk does not net away merely because directional exposures oppose;
- signed directional risk is retained separately for analysis and future hedge rules;
- long EUR/USD and long GBP/USD correctly accumulate a common short-USD direction;
- configurable exposure-bucket limit expressed in equity basis points;
- conservative default bucket limit of 0.50% versus 0.75% aggregate open risk;
- explicit `EXPOSURE_BUCKET_LIMIT` rejection code and breached-bucket identity;
- exposure limit and estimated economics retained in the risk decision audit payload;
- reservation release removes aggregate, bucket, and directional risk atomically;
- shared-account coordinator serializes portfolio snapshot, risk evaluation, and reservation;
- concurrent strategies cannot approve independently against the same stale capacity;
- the normal replay coordinator now uses the same shared-account approval boundary;
- multi-instrument fixture approves EUR/USD and GBP/USD, rejects a third AUD/USD trade on the USD bucket, then restores capacity after release;
- concurrent correlated-signal fixture proves exactly two approvals and one USD-bucket veto under the default limits.

The account/risk boundary is multi-instrument capable. Milestone 11 connects that boundary to a shared replay stream.

## Milestone 11: deterministic multi-instrument replay

Implemented:

- a strict shared-scenario schema containing two or more fully specified instrument legs;
- one paper-broker account, account-risk coordinator, portfolio ledger, and audit chain for every leg;
- UTC event merging with a documented lexical-symbol tie-break for equal timestamps;
- independent ICC state-machine lifecycle and intent sequence per symbol;
- event-time cross-symbol risk evaluation, so earlier reservations constrain later correlated signals;
- multi-instrument paper fills with symbol-specific price scale, quantity rules, slippage, spread, commission, and value economics;
- combined account analytics plus per-symbol completed-trade reports;
- final shared portfolio state with aggregate, currency/underlying bucket, and signed directional risk;
- a frozen shared experiment manifest that fingerprints every leg and the event-ordering rule;
- `tradex manifest-shared <shared-scenario.json> <manifest.json>` and `tradex replay-shared <shared-scenario.json> <manifest.json> <audit.jsonl>` commands;
- integration coverage proving that EUR/USD and GBP/USD reservations cause a later AUD/USD setup to fail the USD exposure cap;
- deterministic equal-timestamp coverage proving that symbol order in the input file cannot change event priority.

The current shared replay accepts already compiled event legs. A later ingestion slice should compile multiple raw broker-native streams into a single shared scenario while preserving each stream's provenance.

## Milestone 12: versioned research-report artifacts

Implemented:

- deterministic `tradex.research-report/v1` machine-readable JSON output;
- human-readable Markdown output generated from the exact same report model;
- frozen experiment ID and manifest SHA-256 carried into every report;
- SHA-256 fingerprint of the complete source replay result;
- self-verifying report SHA-256 that detects subsequent metric or metadata changes;
- account performance including Wilson win-rate interval, net return, expectancy, costs, profit factor, drawdown, and loss streak;
- execution funnel totals for decisions, approvals, rejections, orders, entry fills, and closing fills;
- rejection-code counts plus per-symbol approval/rejection counts when symbol identity is available;
- final equity, open positions, aggregate risk, gross bucket risk, and signed directional-risk snapshot;
- stable lexical ordering for symbol, rejection, and exposure tables in Markdown;
- explicit interpretation guardrails in both JSON and Markdown;
- new-only bundle writes that refuse to overwrite either report artifact;
- `tradex report <replay-result.json> <report.json> <report.md>` and `tradex report-shared <shared-result.json> <report.json> <report.md>` commands;
- integrity, deterministic rendering, tamper detection, overwrite refusal, and single/shared CLI coverage.

The report hash provides integrity and reproducibility, not authorship authentication. Signed attestations can be added later if reports cross a trust boundary.

## Milestone 13: frozen walk-forward and untouched-holdout evaluation

Implemented:

- strict `tradex.walk-forward/v1` study schema with two or more embedded strategy candidates;
- identical event-data, instrument, account, risk, and paper-cost assumptions enforced across candidates;
- candidate variation limited to versioned ICC and execution definitions;
- complete-cycle splitting at context boundaries, with exactly one confirmed setup required per eligible cycle;
- chronological-order validation before any evaluation artifacts are created;
- configurable initial training, test, step, final holdout, and minimum-training-trade counts;
- anchored expanding training windows and non-overlapping out-of-sample test windows;
- deterministic development score of net profit minus maximum drawdown;
- candidates below the minimum completed-trade count marked ineligible rather than promoted from sparse results;
- lexical candidate-ID tie-breaking for reproducible selection;
- final candidate selection on all pre-holdout development cycles;
- holdout evaluation invoked only after the final candidate has been fixed;
- frozen experiment manifests fingerprinting the dataset, candidates, split configuration, risk, costs, and instrument;
- immutable hash-chained audit journal for every candidate/segment evaluation;
- SHA-256 fingerprints for every audit, candidate definition, dataset, study manifest, and final result;
- self-verifying result structure that reproduces fold selection and validates segment geometry;
- `tradex manifest-study <study.json> <manifest.json>` and `tradex walk-forward <study.json> <manifest.json> <audit-dir> <result.json>` commands;
- deterministic integration coverage over five chronological ICC cycles, including freeze rejection and tamper detection.

Current limitations are deliberate: the evaluator handles single-instrument replay candidates and confirmed ICC cycles only; it does not yet aggregate shared-account candidates or invalidated/no-trade opportunities. A technically untouched holdout also requires researcher discipline—the software prevents holdout metrics from selecting a candidate within a frozen run, but it cannot prove that a human never inspected the underlying period before freezing the study.

## Milestone 14: stationary bootstrap and Deflated Sharpe analysis

Implemented:

- strict robustness configuration with at least 1,000 simulations, explicit seed, mean block length, drawdown threshold, risk-free return, complete trial-Sharpe list, and effective independent-trial count;
- combined-account chronological closed-trade P&L extraction for single- and shared-replay results;
- fixed SplitMix64 pseudo-random stream for reproducible simulations independent of Go's library RNG implementation;
- circular stationary bootstrap with geometric block lengths and restart probability `1 / mean_block_length`;
- IID bootstrap as the explicit mean-block-length-one special case;
- observed and simulated ending P&L, maximum drawdown, drawdown basis points, and maximum loss-streak statistics;
- nearest-rank P05/P50/P95/P99 empirical quantiles;
- simulated probabilities of ending loss and crossing a configured drawdown threshold;
- per-trade excess returns using equity immediately before each completed trade;
- non-annualized Sharpe, skewness, kurtosis, cross-trial Sharpe variance, expected maximum Sharpe, and DSR probability;
- explicit estimator and statistical-method identities in every result;
- source-result, trade-P&L, and final-result SHA-256 fingerprints;
- fail-closed rejection of undersized, zero-variance, non-finite, inconsistent, or overwritten analyses;
- `tradex robustness <replay-result.json> <config.json> <result.json>` and `tradex robustness-shared <shared-result.json> <config.json> <result.json>` commands;
- deterministic, seed-sensitivity, numerical-normal-CDF, DSR, tamper, new-only, and combined-account test coverage.

The method and limitations are documented in [ROBUSTNESS_METHOD.md](ROBUSTNESS_METHOD.md). The stationary bootstrap follows the random-length block method introduced by [Politis and Romano](https://doi.org/10.1080/01621459.1994.10476870). The DSR inputs and probability follow [Bailey and López de Prado](https://www.davidhbailey.com/dhbpapers/deflated-sharpe.pdf).

## Milestone 15: deterministic execution realism

Implemented:

- fixed per-instrument submission latency with first-eligible-quote enforcement;
- explicit broker rejections for closed-market submission and latency beyond intent expiry;
- quote-level executable-quantity caps, including zero-liquidity observations;
- quantity-step-aligned partial entry fills and cumulative entry commission;
- one position per order with adversely rounded quantity-weighted entry prices;
- cancellation of unfilled order remainder after the filled portion exits;
- protection validation at actual fill price so gap-invalid stops or targets fail closed;
- quote-native variable bid/ask spread and per-execution-candle synthetic spread overrides;
- explicit chronological market-open/closed events without inventing a universal calendar;
- configurable UTC rollover boundaries, charge weekdays, triple-day multiplier, and signed long/short rates;
- rollover included in realized and closed-trade net P&L and exposed separately from commission;
- terminal broker rejection/cancellation releases reserved account risk when no live position remains;
- final order snapshots retained in single and shared results so partial and terminal outcomes are auditable;
- research-report counts for rejected, cancelled, open-partial, and partially executed orders;
- execution-realism engine, event, fill, and paper-cost model version 2 identities;
- walk-forward cycle support for market-status events preceding the first strategy context;
- deterministic broker and full-replay coverage for latency, closure, partial fills, variable spread, rollover, and reservation release.

The model boundaries and input semantics are documented in [EXECUTION_REALISM.md](EXECUTION_REALISM.md). Liquidity is a deterministic per-order cap rather than an order-book simulation; latency is fixed rather than sampled; candle spreads are synthetic; and market closures and swap schedules must be supplied from the exact broker, account type, instrument, and historical period. These controls reduce optimistic bias but cannot establish an 80% win rate or guarantee live execution.

## Milestone 16: broker-native bid/ask datasets

Implemented:

- strict `tradex.quotes/v1` dataset schema for executable bid/ask observations;
- broker/feed/vendor/account-period provenance, raw-artifact checksum, licence reference, UTC retrieval time, and timestamp basis;
- symbol-stream identity, source-sequence monotonicity, chronological ordering, UTC validation, and retrieval cutoff checks;
- optional maximum inter-quote gap with explicit declared-gap policy;
- deterministic quote-dataset fingerprints included in compiled scenario manifests;
- replacement of synthetic quote events while preserving strategy and execution-candle events;
- same-timestamp ordering rule: broker quote before strategy event;
- `tradex compile-quotes <base-scenario.json> <quotes.json> <compiled-scenario.json>` workflow;
- validation, fingerprint mutation, sequence/gap, replacement, and tie-order integration tests.

The quote stream models top-of-book observations only. It does not infer depth, queue priority, impact, venue rejection behavior, or missing ticks. Symbol aliases, broker timestamps, session closures, conversion quotes, and account-specific swap schedules remain explicit normalization inputs. See [QUOTE_DATASET.md](QUOTE_DATASET.md).

## Milestone 17: opportunity-level analytics

Implemented:

- one auditable opportunity record per ICC indication;
- lifecycle capture for unconfirmed, structurally invalidated, risk-rejected, broker-rejected, no-fill, pending, open, and closed outcomes;
- linkage from opportunity to risk decision, intent, broker order, entry quantity, and closed quantity;
- single- and shared-account replay persistence;
- research-report opportunity funnel separate from completed-trade performance;
- deterministic invalidation, closed-trade, pending/no-fill classification, and report coverage.

Opportunity counts expose survivorship and execution bias but are not trades and do not create P&L. A strategy's completed-trade win rate must never be presented without the opportunity funnel and the corresponding rejected, invalidated, no-fill, and holdout evidence. See [OPPORTUNITY_ANALYTICS.md](OPPORTUNITY_ANALYTICS.md).

## Milestone 18: shared-account walk-forward evaluation

Implemented:

- strict shared-study schema with at least two multi-leg candidates;
- candidate invariants for account equity, risk, paper account/costs, value assumptions, instruments, symbols, and event data;
- per-leg strategy variation while freezing cross-candidate market data;
- equal cycle-count validation across every shared leg;
- cycle-wise joining followed by deterministic UTC/symbol event merging through one shared account;
- anchored expanding training, non-overlapping tests, final development selection, and untouched holdout;
- account-level selection score of net profit minus maximum drawdown with minimum-trade eligibility;
- frozen `tradex-shared-walk-forward` manifests and exclusive result artifacts;
- `tradex manifest-study-shared` and `tradex walk-forward-shared` CLI workflows;
- shared fold selection, holdout isolation, tamper/hash, and multi-leg integration coverage.

Shared walk-forward scores are account-level research diagnostics. They do not imply that each symbol is profitable or that correlated exposure is safe; review bucket limits, opportunity outcomes, rejected orders, and per-symbol reports before any promotion decision. See [SHARED_WALK_FORWARD.md](SHARED_WALK_FORWARD.md).

## Milestone 19: broker/account schedule importers

Implemented:

- strict `tradex.broker-schedule/v1` artifact for one broker, account type, symbol, and effective date range;
- provenance, raw-artifact checksum, licence reference, and retrieval metadata;
- seven-day local-time weekly windows with IANA timezone/DST handling;
- date-specific holiday, maintenance, and early-close exceptions;
- overnight session-window support;
- deterministic market-status boundary generation over a replay interval;
- exact signed rollover configuration imported alongside the calendar;
- replay scenario provenance attachment and paper rollover propagation;
- `tradex compile-schedule <base-scenario.json> <schedule.json> <scenario.json>` workflow;
- effective-date, exception, boundary, fingerprint, and replay integration coverage.

Schedules are never inferred from a universal FX calendar. They must be sourced for the exact Exness, Trade Nation, or Pepperstone account type, instrument, and historical period. See [BROKER_SCHEDULES.md](BROKER_SCHEDULES.md).

## Milestone 20: quote normalization and conversion streams

Implemented:

- strict raw quote layer with explicit alias and timestamp normalization configuration;
- RFC3339, local RFC3339, Unix-millisecond, and Unix-nanosecond parsing;
- UTC canonicalization, deterministic time/sequence ordering, and alias-collision rejection;
- normalized quote output preserving raw-feed provenance;
- strict `tradex.conversions/v1` directed currency-rate dataset;
- rational numerator/denominator observations with provenance and UTC ordering;
- latest-fresh observation lookup with stale/future/missing-rate rejection;
- replay conversion compilation into account-currency `PriceValue` and manifest provenance;
- `tradex normalize-quotes` CLI workflow;
- alias, timestamp, collision, freshness, and conversion validation coverage.

Price integers are never rescaled implicitly, and conversion direction is never inferred. USD/JPY-style instruments require an explicit JPY-to-USD stream for a USD account. See [QUOTE_NORMALIZATION.md](QUOTE_NORMALIZATION.md).

## Milestone 21: Pepperstone cTrader adapter boundary

Implemented:

- strict cTrader adapter identity with endpoint, account, environment, mode, credential reference, and frozen manifest hash;
- explicit live promotion approval requiring matching manifest, operator, timestamp, and reason;
- transport-injected broker interface for demo/shadow/live integration without embedding protocol assumptions;
- remote snapshot reconciliation for order IDs/status/remaining quantity and position IDs/quantity;
- fail-closed submission and cancellation when reconciliation has not succeeded or has mismatched;
- account identity enforcement on approved orders;
- fake-transport tests proving live-promotion and reconciliation gates;
- adapter boundary documentation and explicit production-transport limitations.

No cTrader network protocol or live order path is enabled yet. A separately reviewed demo transport, authentication rotation, symbol mapping, rate limits, server-time handling, and durable remote-order correlation are required before any live promotion. See [CTRADER_ADAPTER.md](CTRADER_ADAPTER.md).

## Milestone 22: deterministic cTrader demo transport

Implemented:

- deterministic in-memory demo transport behind the cTrader `Transport` interface;
- account-checked acknowledged order submission;
- idempotent intent resubmission;
- cancellation and open-order snapshots;
- position snapshots and reconciliation capture;
- end-to-end adapter test covering reconcile → submit → cancel;
- explicit separation between demo contract testing and the unimplemented production cTrader protocol.

The demo transport does not connect to Pepperstone, model fills, authenticate, or validate Open API wire behavior. Production connectivity remains gated behind a separately reviewed protocol transport and demo-account forward testing.

## Milestone 23: cTrader Open API protocol session contract

Implemented:

- explicit JSON/protobuf serialization selection;
- OAuth credential and access-token references without secret persistence;
- connection → application authorization → account authorization state machine;
- account-ID mismatch rejection;
- mutation permission only after account authorization;
- configurable heartbeat deadline with ten-second maximum;
- disconnect and heartbeat state transitions;
- tests for authorization ordering, account mismatch, mutation gating, and heartbeat timing.

The contract follows the current official cTrader Open API guidance but is not a wire client. Protobuf generation, OAuth exchange/refresh, WebSocket/TCP framing, error-message mapping, rate limits, and broker symbol translation remain required before demo connectivity. See [CTRADER_ADAPTER.md](CTRADER_ADAPTER.md).

## Next implementation slice (cTrader — deferred)

Pending Spotware/cTrader Open API application approval:

1. Implement and contract-test a Pepperstone cTrader Open API transport behind this adapter boundary.
2. Implement Exness MT5 and Trade Nation MT4 bridge adapters, then reconciliation and operational kill switches.

## Milestone 24: OAuth lifecycle and transport framing contracts

Implemented network-free protocol utilities:

- atomic OAuth access-token refresh policy with configurable expiry skew and injected refresh exchange;
- bounded, fragmentation-safe four-byte TCP length-prefix encoding/decoding;
- detached WebSocket protobuf payload handling;
- tests for refresh success/failure, partial frames, invalid lengths, and payload ownership.

These utilities do not create sockets, call broker endpoints, or enable live orders.

## Milestone 25: cTrader message and request-correlation contracts

Implemented:

- opaque `MessageEnvelope` validation for payload type, payload bytes, and client message IDs;
- deterministic request IDs and pending-request correlation;
- disconnect reset behavior for outstanding requests;
- transport-neutral `ProtocolError` representation;
- tests for envelope validation, response correlation, reset behavior, and error formatting.

Generated protobuf serialization and broker-specific error-code classification remain transport work.

## Milestone 26: injected protocol client boundary

Implemented:

- `MessageWriter` seam for TCP or WebSocket implementations;
- session authorization and mutation gates at send time;
- request registration, response correlation, and write-failure cleanup;
- disconnect handling that resets outstanding requests;
- deterministic fake-writer tests.

No socket, TLS, protobuf runtime, or broker endpoint is enabled by this milestone.

## Milestone 27: injected TCP/WebSocket writers

Implemented:

- `TCPMessageWriter` using the cTrader four-byte length envelope;
- `WebSocketMessageWriter` delegating WebSocket framing to an injected sink;
- context cancellation checks, serialized writes, short-write handling, and payload validation;
- deterministic byte-sink tests.

These writers accept already-connected sinks only. They do not dial, negotiate TLS, authenticate, or enable live orders.

## Milestone 28: deterministic demo protocol harness

Implemented an in-memory `DemoProtocolHarness` that routes requests through `ProtocolClient`, invokes an injected responder, and correlates responses. It provides end-to-end authorization and request-lifecycle coverage without serialization, sockets, TLS, broker accounts, or live orders.

## Milestone 29: typed cTrader operation contracts

Implemented `OperationRequest` validation for application authorization, account authorization, order submission, cancellation, and snapshots. Submission requires a validated risk-approved order and all operations require the configured account. `ProtocolClient.SendOperation` applies these checks before the generic protocol path.

## Milestone 30: typed cTrader response contracts

Implemented `OperationResponse` validation for authorization results, order acknowledgements, cancellation responses, and snapshots. Responses enforce client correlation IDs, account identity, payload type, broker order/position IDs, and snapshot capture time. Protocol errors remain explicit and are returned before state is consumed.

## Milestone 31: typed demo transport bridge

`DemoTransport` now exposes typed submit, cancel, and snapshot operation methods. They validate `OperationRequest`, invoke the existing deterministic state simulator, and return `OperationResponse` values suitable for response-contract tests. Adapter reconciliation and live promotion gates remain unchanged.

## Milestone 32: retry and idempotency policy

Implemented explicit retry classification: only protocol errors marked retriable become `AFTER_RECONNECT`; unknown, validation, and broker-rejection errors default to `NEVER`. Added duplicate intent tests proving demo submission returns the existing broker order instead of creating a second order.

## Milestone 33: heartbeat and reconnect lifecycle

`ProtocolClient` now exposes heartbeat due/record operations, disconnects and resets pending requests on heartbeat timeout, and supports explicit reconnect transitions. This keeps stale request state from crossing connection boundaries without opening sockets.

## Milestone 34: circuit breaker and kill switch

Implemented a thread-safe circuit breaker with closed/open/half-open recovery, plus a manual kill switch requiring an operator reason. `SafetyGate` combines both controls, and `ProtocolClient` can attach it to block new requests. Existing pending requests are still handled by timeout/reconnect policy.

## Milestone 35: operational audit events

Added metadata-only audit events and an in-memory recorder for safety blocks, recorded failures, heartbeat timeouts, disconnects, and reconnects. Events exclude credentials and serialized payloads so they can support later operational evidence without leaking secrets.

## Milestone 36: journal-backed operational evidence

Added `JournalAuditRecorder`, adapting cTrader operational events to the existing hash-chained journal with `CTRADER_` event kinds and correlation IDs. Append failures are retained for process-level health checks.

## Milestone 37: operational evidence report

Added `BuildOperationalEvidence`, aggregating journal-backed cTrader safety blocks, failures, heartbeat timeouts, disconnects, and reconnects. The report exposes last-event time, health status, and recorder errors for operator review.

## Milestone 38: CLI operational evidence check

Added `tradex ctrader-evidence <audit.jsonl> <evidence.json>`, a read-only journal verification and report command. It refuses to overwrite output, fails on invalid/empty journals, and writes a structured evidence summary for deployment review.

## Milestone 39: deployment readiness evaluator

Added `CheckDeploymentReadiness`, which fails closed unless adapter configuration, reconciliation, operational evidence, circuit-breaker state, kill-switch state, and live-promotion approval (when applicable) all pass.

## Milestone 40: CLI deployment readiness check

Added `tradex ctrader-readiness <readiness.json> <report.json>`. It strictly loads a readiness snapshot, writes a non-overwriting report, and exits non-zero when any deployment gate fails.

## Milestone 41: signed readiness handoff artifact

Added canonical `ReadinessArtifact` creation and verification with manifest/evidence hashes, reconciliation time, approval metadata, artifact hash, and HMAC signature. Artifacts cannot be created from an unready document and tampering is detected before handoff.

## Milestone 42: CLI signed-artifact workflow

Added `ctrader-artifact-create` and `ctrader-artifact-verify`. Signing keys are supplied through protected files, artifacts refuse overwrite, and verification fails on tampering or incorrect keys.

## Milestone 43: signer rotation policy

Readiness artifacts now carry signer key ID and signer-version metadata. `VerifyWithPolicy` supports allow-lists for active key IDs and versions, enabling key rotation and revocation without changing artifact hashing or transport code.

## Milestone 44: CLI signer rotation workflow

CLI artifact creation accepts explicit key ID and signer version. Verification can load a strict policy JSON containing allowed key IDs and versions, enabling deployment-time rotation and revocation checks.

## Milestone 45: readiness freshness policy

Artifact verification now supports maximum artifact age and maximum reconciliation age, plus future-timestamp rejection. The CLI policy file accepts these limits in seconds and evaluates them against current UTC time.

## Milestone 46: artifact scope binding

Readiness artifacts now bind broker name, account ID, environment, and manifest hash into a signed scope. Verification rejects scope mismatches before accepting the artifact.

## Milestone 47: artifact audience and purpose restrictions

Artifacts now carry signed audience and purpose metadata. Verification policies can require exact audience/purpose values, preventing reuse of a valid handoff in an unrelated workflow.

## Milestone 48: artifact schema versioning

Artifacts now include a signed schema version. Verification policies can allow-list schema versions, causing unknown or unsupported formats to fail closed.

## Milestone 49: artifact revocation

Added thread-safe `RevocationList` entries with artifact hash, reason, and timestamp. Verification policies can reject revoked artifact hashes, and CLI policy documents can provide a revoked-hash map.

## Milestone 50: journal-backed revocation replay

Added `CTRADER_ARTIFACT_REVOKED` journal events plus append/replay helpers. A verifier can reconstruct the revocation list from the tamper-evident journal before evaluating readiness artifacts.

## Milestone 51: CLI journal revocation workflow

Added `ctrader-revocations` to export reconstructed revocations and `ctrader-artifact-verify-journal` to verify artifacts against revocations loaded directly from the journal.

## Milestone 52: revocation precedence and conflict detection

Revocation updates are now deterministic: identical events are idempotent, stale events are rejected, same-time conflicting reasons fail, and newer events supersede older records.

## Milestone 53: Pepperstone cTrader demo connector configuration

Added a broker-specific Pepperstone demo configuration seam. It validates demo-only endpoints, shadow/active-demo modes, serialization choice, account identity, and external credential references. No network connector or live mode is enabled.

The repository now includes `.env.example` for local cTrader identifiers and callback configuration. Real `.env` values remain ignored and must be rotated if exposed.

## Milestone 54: cTrader OAuth API client

Added environment-backed OAuth configuration, authorization URL generation, authorization-code exchange, refresh-token exchange, token expiry handling, and injectable HTTP tests. The client never logs credentials and has not been connected to an actual account yet.

## Milestone 55: OAuth CLI workflow

Added `ctrader-oauth-url <state> <scope>` and `ctrader-oauth-exchange <authorization-code> <token-file>`. The exchange writes tokens with mode `0600`, refuses overwrite, and never prints token contents.

## Milestone 56: Exness MT5 bridge adapter boundary

Implemented:

- strict MT5 adapter identity with terminal host/port, account, environment, mode, credential reference, and frozen manifest hash;
- explicit live promotion approval requiring matching manifest, operator, timestamp, and reason;
- transport-injected broker interface for demo/shadow/live integration without embedding MQL5 protocol assumptions;
- remote snapshot reconciliation for order IDs/status/remaining quantity and position IDs/quantity;
- fail-closed submission and cancellation when reconciliation has not succeeded or has mismatched;
- account identity enforcement on approved orders;
- deterministic in-memory demo transport with idempotent intent submission and cancellation;
- fake-transport tests proving live-promotion and reconciliation gates.

No MQL5 EA code, TCP socket dialing, or MetaTrader terminal behavior is embedded. The bridge design uses a thin EA that independently validates account, symbol, size, price age, stop distance, expiry and duplicates before submission. A Windows VPS node hosts the terminal; the EA connects outbound to Go using mutual authentication.

## Milestone 57: Trade Nation MT4 bridge adapter boundary

Implemented:

- strict MT4 adapter identity with terminal host/port, account, environment, mode, credential reference, and frozen manifest hash;
- explicit live promotion approval requiring matching manifest, operator, timestamp, and reason;
- transport-injected broker interface for demo/shadow/live integration without embedding MQL4 protocol assumptions;
- remote snapshot reconciliation for order IDs/status/remaining quantity and position IDs/quantity;
- fail-closed submission and cancellation when reconciliation has not succeeded or has mismatched;
- account identity enforcement on approved orders;
- deterministic in-memory demo transport with idempotent intent submission and cancellation;
- fake-transport tests proving live-promotion, reconciliation gates, and shadow mode.

No MQL4 EA code, TCP socket dialing, or MetaTrader terminal behavior is embedded. Trade Nation documents only MT4 Web access as the native Mac path; desktop EAs require a supervised Windows VPS node. The bridge connects outbound to Go using mutual authentication; MetaTrader ports are not exposed publicly.

## Current checkpoint: cTrader deferred, MT bridge adapters ready

The Pepperstone cTrader integration (milestones 21–55) is paused pending Spotware/cTrader Open API application approval. No broker request has been sent and no live capability is enabled.

Milestones 56–57 established the Exness MT5 and Trade Nation MT4 adapter boundaries independently, allowing integration work to continue without the cTrader external dependency.

When cTrader approval arrives, resume with:

1. Confirm the application status is active and the registered redirect URI matches `.env`.
2. Rotate/update the client secret if required, then reload `.env` into the shell.
3. Run `ctrader-oauth-url local-state accounts`.
4. Approve the Pepperstone demo account and exchange the callback code with `ctrader-oauth-exchange`.
5. Use the resulting token to implement application authorization, account-list retrieval, account authorization, and demo snapshot reconciliation.

## Milestone 58: MT5 bridge wire protocol and TCP transport

Implemented:

- versioned binary wire protocol: 4-byte big-endian length prefix, 1-byte message type, JSON payload;
- 12 message types: handshake, handshake ack, heartbeat, submit intent, cancel order, snapshot request, order ack, order rejected, order cancelled, fill event, snapshot response, protocol error;
- strict validation for handshake (protocol version, account, terminal, broker), submit intent (request ID, intent ID, account, symbol, side, type, quantity, stop/TP, expiry, signature), and cancel;
- maximum 1 MB payload size enforcement on read and write;
- Go TCP transport implementing `mt5.Transport` with the Go service as listener and EA connecting outbound;
- handshake with account-ID verification and heartbeat interval negotiation;
- request-ID correlation: pending requests tracked with channels, routed on response arrival;
- bidirectional heartbeat send loop and read loop with heartbeat-timeout disconnection;
- asynchronous fill event channel for EA-initiated position updates;
- connection loss detection, pending-request cancellation, and graceful close;
- `extractRequestID` fast-path for JSON routing without full deserialization;
- fake EA in-process test server proving connect, submit, cancel, snapshot, wrong-account rejection, not-connected blocking, and full lifecycle;
- race-detector clean under concurrent heartbeat and request paths.

The transport does not dial — the EA connects outbound so MetaTrader ports are never publicly exposed.

## Milestone 59: MQL5 Expert Advisor and operational safety

Implemented:

- `ea/mt5/TradexBridge.mq5`: thin MQL5 Expert Advisor connecting outbound to the Go service;
- protocol handshake on connect with automatic reconnect every second on failure;
- independent intent validation: account ID match, duplicate ring-buffer (256 entries), symbol availability, quantity bounds, minimum stop distance in points, Go heartbeat freshness, and signature presence;
- order execution via `CTrade.PositionOpen` with magic number, slippage tolerance, and volume normalization;
- pending-order cancellation via `OrderDelete` with position-close fallback;
- terminal snapshot building from `OrdersTotal`/`PositionsTotal` filtered by magic number;
- bidirectional heartbeat with stale-Go detection and automatic disconnect;
- minimal JSON parser (no external MQL5 library dependency);
- deployment documentation in `ea/mt5/README.md`;
- Go-side `CircuitBreaker` with closed/open/half-open states, configurable threshold and cooldown;
- `KillSwitch` requiring an operator reason to engage, with clear and check operations;
- `AuditRecorder` interface and `MemoryAuditRecorder` for operational events;
- MT5-specific audit kinds: safety blocked, safety failure, heartbeat timeout, disconnected, reconnected, order submitted, order rejected, order cancelled, fill received;
- `SafetyGate` combining circuit breaker and kill switch with audit recording;
- `SafeTransport` wrapping any `Transport` with safety-gated mutations while allowing read-only operations (snapshot, open orders, positions) to bypass safety;
- 16 safety/safe-transport tests plus 9 TCP transport tests, all race-clean.

The EA does not make trading decisions. It validates and executes intents from the Go risk engine only. New entries are blocked when the Go heartbeat is stale; protective exits remain available.

## Next implementation slice

1. Implement the MQL4 EA communication protocol: equivalent framing and validation for the Trade Nation MT4 terminal.
2. Implement terminal reconciliation and deployment readiness checks for both MT bridges.
3. Add journal-backed operational evidence and CLI readiness commands for MT5/MT4 (mirroring cTrader milestones 36–40).
4. When cTrader approval completes, resume with demo account authorization and snapshot reconciliation.
5. End-to-end integration test: Go service → TCP transport → real Exness MT5 demo terminal.
