# Go Trading Bot: Draft Design and Research Plan

Status: Draft v0.7; milestones 1–10 through shared-account exposure-bucket enforcement implemented  
Date: 2026-08-19  
Scope: Research, backtest, paper trade, then cautiously automate rule-based price-action strategies for major FX pairs, gold, and silver.

## 1. Objective and non-objectives

Build a deterministic, auditable Go system that can:

1. ingest and validate market data;
2. express price action, ICC, ICT, SMC, CRT, and liquidity ideas as precise, versioned rules;
3. backtest those rules without look-ahead bias and with realistic costs;
4. apply portfolio-level risk limits independently of strategy code;
5. paper trade using the same decision and execution paths as live trading;
6. support broker/exchange adapters only after research and safety gates pass.

The system will **not** guarantee an 80% win rate, profitability, or a fixed risk-to-reward ratio. Those are measured outputs. An 80% win rate with an average 3R winner and 1R loser implies:

`expectancy = 0.80 × 3R - 0.20 × 1R = 2.20R per trade, before costs`.

That is an exceptional claim. It should trigger more skepticism and validation, not larger sizing. The CFTC warns that automated systems cannot consistently predict the future and that leverage magnifies losses ([CFTC forex warning](https://www.cftc.gov/LearnAndProtect/forexfrauds), [CFTC leverage advisory](https://www.cftc.gov/LearnAndProtect/AdvisoriesAndArticles/understand_risks_of_virtual_currency.html)).

## 2. Terminology and working assumptions

These names do not have universally accepted, machine-readable specifications. This draft uses the following working definitions:

| Name | Working interpretation | Quantifiable proxy |
|---|---|---|
| Price action (APA) | Price action trading strategy | swing structure, support/resistance, displacement, volatility, candle/range rules |
| ICC | Trades by Sci's Indication, Correction, Continuation | higher-timeframe break/indication, pullback/correction, lower-timeframe continuation |
| ICT | Inner Circle Trader concepts, kept separate from ICC | session windows, liquidity sweeps, fair-value gaps, order blocks, market-structure shift |
| SMC | Smart Money Concepts | break/change of structure, imbalances, supply/demand zones, premium/discount |
| CRT | Candle Range Theory | prior higher-timeframe range, one-side sweep, close/reclaim inside, opposite-side target |
| Liquidity | observable or inferred available interest | prior highs/lows, equal highs/lows, session extremes; order-book depth when available |

CRT sources commonly describe a prior candle/range whose high or low is swept, followed by re-entry and a projected move toward the other side ([CRT overview](https://candlerangetheory.com/), [independent CRT explanation](https://tradingwyckoff.com/en/crt/)). These are practitioner descriptions, not evidence of profitability.

The economic intuition behind “liquidity” should not be confused with proof of a particular retail pattern. Market-microstructure research does support relationships among order-flow imbalance, depth, and short-horizon price impact ([Cont, Kukanov & Stoikov](https://arxiv.org/abs/1011.6402)), and research has examined stop-loss clustering and FX price cascades ([Federal Reserve Bank of New York paper](https://fraser.stlouisfed.org/files/docs/publications/frbnysr/frbny_sr150.pdf)). OHLC candles alone, however, do not reveal the actual order book or institutional intent.

Trades by Sci describes the approach through market structure and multi-timeframe alignment ([Trades by Sci market-structure lesson](https://www.youtube.com/watch?v=Y7Q37vTbuNg&vl=en)). Public summaries describe ICC as **Indication, Correction, Continuation**, commonly marking structure on H4/H1 and confirming on M15/M5. This is sufficient for a research prototype, but exact entry, invalidation, and exit rules must be frozen from agreed source examples before a holdout test.

### ICC state machine v1

ICC is implemented independently from ICT:

1. **Context:** classify completed H4 and H1 structure as bullish, bearish, or neutral. Require alignment for the strict baseline.
2. **Indication:** a completed H1 candle closes beyond a previously confirmed structural swing by a configurable tick/ATR buffer. Store the broken level, origin swing, impulse extreme, and timestamp.
3. **Correction:** price retraces toward the indication/broken-structure zone without closing beyond the setup's structural invalidation. No trade occurs during correction.
4. **Continuation:** a completed M15 candle first, and M5 only as a later experiment, resumes the H1 direction by breaking a confirmed correction swing or reclaiming the indication level with minimum displacement.
5. **Entry:** next-bar market or predefined limit-retest model; these are separate experiments.
6. **Stop:** beyond the correction extreme plus spread/volatility buffer.
7. **Target:** next H1/H4 external liquidity level, previous impulse projection, or fixed R multiple selected before testing.
8. **Expiry:** cancel if continuation does not occur within a configured number of H1 bars, the opposite H1 structure breaks, the allowed session ends, or spread/data controls fail.

The prototype will save screenshots/structured evidence for each transition so human ICC chart markups can be compared with the mechanical interpretation.

## 3. Market universe, sessions, and liquidity timeframes

“USD, JPY, AUD, GBP, and EUR” are currencies, so the bot trades them as pairs. Gold and silver are initially quoted in USD.

### 3.1 Instrument rollout

| Tier | Symbols | Purpose |
|---|---|---|
| Core | EUR/USD, GBP/USD, USD/JPY, AUD/USD, XAU/USD, XAG/USD | one representative for each requested currency plus metals; build and validate here first |
| Cross-validation | EUR/JPY, GBP/JPY, AUD/JPY, EUR/GBP | test whether an edge generalizes beyond USD-major behavior |
| Later research | EUR/AUD, GBP/AUD | additional regional crosses after cost and correlation analysis |

The system must use broker-specific symbol metadata rather than assume names, pip values, contract sizes, or quote precision. It will not open several effectively identical USD or JPY positions as if they were independent. Exposure is aggregated by currency and risk factor—for example, long EUR/USD plus long GBP/USD both create material short-USD exposure.

### 3.2 Session engine

Spot FX is an OTC, nearly 24-hour weekday market; “sessions” are liquidity labels rather than separate exchanges. The scheduler uses IANA timezones and local wall-clock definitions so daylight-saving changes cannot silently shift a strategy:

| Session label | Configured local window | Indicative Accra/UTC window | Primary instruments |
|---|---|---|---|
| Sydney | 08:00–17:00 `Australia/Sydney` | 21:00–06:00 or 22:00–07:00 | AUD/USD, AUD/JPY, EUR/AUD, GBP/AUD |
| Tokyo | 09:00–17:00 `Asia/Tokyo` | 00:00–08:00 | USD/JPY, EUR/JPY, GBP/JPY, AUD/JPY |
| London | 08:00–17:00 `Europe/London` | 07:00–16:00 or 08:00–17:00 | EUR/USD, GBP/USD, EUR/GBP, European/JPY crosses, XAU/USD, XAG/USD |
| New York | 08:00–17:00 `America/New_York` | 12:00–21:00 or 13:00–22:00 | USD pairs, XAU/USD, XAG/USD |
| London–New York overlap | intersection computed dynamically | normally four to five hours, DST-dependent | EUR/USD, GBP/USD, USD/JPY, gold, silver |

These are configurable research windows, not assumptions that every minute inside them is equally liquid. The UK and US dominate global FX dealing, while Singapore/Hong Kong are also major centres; the US dollar was on one side of 89% of FX trades in the BIS April 2025 survey ([BIS 2025 FX survey](https://www.bis.org/press/p250930.htm)).

Session features recorded for every eligible signal:

- session open, high, low, midpoint, range and time since open;
- previous session/day/week high and low;
- Asia-range sweep during London and London-range sweep during New York;
- spread, realized volatility, tick count/volume proxy, and distance to rollover;
- session overlap, holidays, early closes, and DST transition flags;
- scheduled high-impact event blackout state, once a licensed calendar source is selected.

For gold and silver, London and New York are primary study windows, with Asia retained as a separate regime rather than discarded. Gold's LBMA benchmarks occur at 10:30 and 15:00 London time; silver's occurs at 12:00 London time ([LBMA benchmark schedule](https://www.lbma.org.uk/prices-and-data/about-lbma-daily-auction-prices)). These times are tagged for analysis, not automatically traded. CME also identifies London, New York, and Shanghai as major gold centres and documents meaningful Asian-hours liquidity ([CME metals overview](https://www.cmegroup.com/education/articles-and-reports/trading-comex-gold-and-silver)).

### 3.3 Multi-timeframe liquidity map

| Layer | Timeframes | Bot use |
|---|---|---|
| Macro liquidity | Monthly, weekly | previous highs/lows, broad range and long-term targets; never direct entries |
| Direction/context | Daily, H4 | trend/range regime, premium/discount, external liquidity, ICC context |
| Setup/indication | H4, H1 | structural swings, ICC indication, CRT anchor range, session/day levels |
| Confirmation | M15 | correction structure, sweep/reclaim, displacement, continuation |
| Execution refinement | M5 | optional later experiment after M15 baseline succeeds |
| Microstructure | tick/bid-ask, optional M1 | fill/slippage modeling; not a directional signal in v1 |

Liquidity levels are created only from completed bars and versioned definitions:

1. previous month/week/day high and low;
2. previous Sydney/Tokyo/Asia, London, and New York session extremes;
3. confirmed H4/H1 swing highs and lows;
4. equal highs/lows within a volatility- and tick-normalized tolerance;
5. ICC indication levels, CRT range boundaries, and unmitigated mechanically defined gaps;
6. actual order-book depth only when the selected broker provides a representative feed.

Each level has a creation time, first-available time, expiry rule, number of touches, sweep state, and source timeframe. This prevents future information from leaking into a backtest.

### 3.4 Session-specific research hypotheses

- AUD setups should be evaluated separately in Sydney/Tokyo and London/New York.
- JPY setups should distinguish Tokyo structure from later London continuation or reversal.
- EUR and GBP setups prioritize London and the London–New York overlap.
- Gold and silver compare Asia, London, New York, benchmark windows, and overlap rather than assuming one universal behavior.
- No session filter is promoted to a rule until its out-of-sample improvement survives costs and multiple-testing correction.

## 4. Strategy model: confluence, not a pile of indicators

Each concept becomes a small feature detector. A strategy combines features through an explicit state machine. No detector may submit orders directly.

### 4.1 Normalized inputs

- UTC timestamp plus named trading session/calendar
- bid, ask, last, OHLCV and timeframe
- tick size, lot/contract size, minimum quantity and price precision
- spread, fees, funding/rollover, estimated slippage
- optional Level 1/Level 2 order-book events
- instrument metadata and market status

### 4.2 Candidate feature detectors

- **Swing structure:** confirmed pivot highs/lows using only bars available at decision time.
- **BOS/CHOCH/MSS:** close beyond a confirmed swing plus configurable displacement threshold.
- **Displacement:** candle body/range normalized by ATR or rolling volatility.
- **FVG/imbalance:** three-candle gap with minimum tick/volatility size; track partial/full mitigation.
- **Order block proxy:** last opposing candle before qualifying displacement and structure break.
- **Liquidity pool proxy:** prior day/week/session high/low, repeated highs/lows within a tick tolerance, range boundaries.
- **Sweep:** trade beyond a pool by a minimum buffer, then close/reclaim within a maximum time.
- **CRT:** anchor range → one-side sweep → reclaim → optional lower-timeframe structure confirmation.
- **Context:** higher-timeframe trend/range, premium/discount, session, volatility and spread regime.

Every detector returns evidence, not just `true/false`: price levels, timestamps, confidence inputs, invalidation reason, and the exact rule version.

### 4.3 First testable strategy: CRT liquidity-reclaim v1

Use one narrow strategy first so results remain attributable.

**Long setup**

1. A completed higher-timeframe anchor candle defines `[rangeLow, rangeHigh]`.
2. Current price trades below `rangeLow + sweepBuffer` during an allowed session.
3. A completed execution-timeframe candle closes back above `rangeLow`.
4. Optional confirmation: bullish structure shift and displacement; any FVG must be defined mechanically.
5. Entry is either the next-bar market price or a limit retrace rule selected before testing.
6. Stop is below the observed sweep extreme plus a volatility/tick buffer.
7. Targets are fixed before entry: range midpoint, opposite boundary, or a specified external pool.

The short setup is exactly symmetric. A setup expires after a fixed number of bars. No repainting pivots, intrabar hindsight, or discretionary overrides are allowed in a backtest.

### 4.4 Strategy and confluence experiments

Start with independent baselines so overlapping terminology does not hide which rule creates value:

1. price-action structure baseline;
2. ICC Indication–Correction–Continuation baseline;
3. CRT sweep-and-reclaim baseline;
4. ICT liquidity/FVG execution baseline;
5. SMC structure/imbalance baseline.

For each baseline, test additions one at a time: session filter, higher-timeframe regime, structure-shift confirmation, imbalance/FVG entry, spread/volatility filter, and genuine order-flow/depth only where the venue supplies representative data. Hybrid strategies are evaluated only after their components pass independently.

Record every attempted variant. This trial ledger is required to correct for selection bias; the Deflated Sharpe Ratio was designed to account for multiple testing, non-normal returns, and short samples ([Bailey & López de Prado](https://doi.org/10.2139/ssrn.2460551)).

## 5. Risk management: an independent veto layer

Strategy code proposes an `OrderIntent`; the risk engine may resize or reject it but can never increase it.

### 5.1 Conservative research defaults

These are starting limits, not recommendations:

| Control | Draft default |
|---|---:|
| Risk per trade | 0.25% of current equity |
| Maximum aggregate open risk | 0.75% |
| Daily realized + unrealized loss stop | 1.0% |
| Weekly loss stop | 2.5% |
| Research/paper drawdown kill switch | 5.0% |
| Maximum correlated exposure | one risk bucket per currency/underlying |
| Minimum planned reward:risk | 2.0R after estimated costs |
| Maximum consecutive losses | 4, then strategy disabled pending review |
| Averaging down / martingale / grid recovery | prohibited |

Position size is based on stop distance, not confidence:

`riskBudget = equity × riskFraction`

`quantity = floorToLot(riskBudget / lossPerUnitAtStop)`

Reject the trade if quantity is below venue minimum, stop distance is invalid, spread is too large, market data is stale, or the worst-case loss including fees/slippage exceeds the budget. Gap risk means a stop is not a guaranteed exit price.

### 5.2 Mandatory live safety controls

- global and per-strategy kill switches;
- maximum order rate, size, notional, leverage and open-order count;
- stale-price, crossed-market, abnormal-spread and volatility halts;
- duplicate-order prevention using durable idempotency keys;
- broker reconciliation on startup and continuously thereafter;
- cancel-on-disconnect policy where supported;
- no new entries near configured market close/news windows; exits remain available;
- secrets outside config files and logs; least-privilege API keys with withdrawals disabled;
- append-only decision, risk, order, fill and reconciliation audit events;
- alerting for rejects, partial fills, position mismatch, drawdown and process restart.

The BIS notes that FX execution algorithms shift execution risk to users and require suitable skills, tools, and data architecture ([BIS report](https://www.bis.org/publ/mktc13.htm)).

## 6. Proposed Go architecture

Use a modular monolith first. This keeps backtest and live behavior consistent without premature distributed-system complexity.

```text
cmd/
  tradex/             CLI: backtest, replay, paper, live, reconcile
internal/
  domain/             Candle, Tick, Quote, Signal, OrderIntent, Fill, Position
  marketdata/         ingestion, normalization, validation, bar construction
  calendar/           sessions, holidays, timezone/DST handling
  features/           swings, structure, FVG, ranges, sweeps, volatility
  strategy/           versioned state machines; no broker calls
  risk/               sizing, limits, exposure, drawdown, vetoes
  portfolio/          positions, P&L, correlation buckets
  execution/          order state machine, retry/idempotency, reconciliation
  broker/             paper, cTrader, MT4-bridge and MT5-bridge adapters
  tradingview/        optional webhook intake and signal-parity exporter
  backtest/           event replay, fill models, costs, corporate/funding events
  analytics/          metrics, trial ledger, reports, Monte Carlo
  journal/            append-only decisions and lifecycle events
  config/             validated, versioned experiment/runtime config
```

### 6.1 Broker and platform integration plan

The selected brokers are **Exness, Trade Nation, and Pepperstone**. One Go strategy/risk core drives separate execution adapters:

| Broker | Confirmed platform capability | Planned Go integration | Initial role |
|---|---|---|---|
| Pepperstone | cTrader, MT4/MT5, TradingView, and API support | direct cTrader Open API adapter over JSON WebSocket or generated Protobuf | primary API prototype and demo execution |
| Exness | MT5/MT4 and algorithmic trading through MetaTrader | thin MQL5 Expert Advisor bridge between MT5 terminal and Go | second demo adapter; validate metals and broker-specific execution |
| Trade Nation | MT4 with Expert Advisors; TN Trader connected to TradingView | thin MQL4 Expert Advisor bridge; TradingView/TN Trader used for human validation | third demo adapter and cross-broker comparison |

Pepperstone advertises cTrader among its automation/API platforms ([Pepperstone platforms](https://pepperstone.com/en-gb/platforms)). cTrader Open API is language-neutral, supports market data and trading, separates demo/live endpoints, and accepts JSON or Protobuf ([cTrader Open API](https://help.ctrader.com/open-api/)). A Go adapter therefore does not require a C# or Python sidecar, although Spotware's official SDKs are currently C# and Python. API application registration and OAuth approval are required before account access.

Exness supports MT5 automated trading and the requested FX/metals instruments ([Exness MT5](https://www.exness.com/metatrader-5/)). Trade Nation officially supports automated Expert Advisors on MT4 ([Trade Nation MT4](https://tradenation.com/en-gb/metatrader-4/)). Neither broker's public material reviewed for this draft documents a general-purpose, retail Go trading API, so the safe initial design is a small terminal-resident EA rather than undocumented endpoints or UI automation.

The MT4/MT5 bridge is deliberately thin:

- Go creates a signed, expiring `OrderIntent` after portfolio risk approval;
- the EA independently checks account ID, symbol, maximum size, price age, stop distance, intent expiry and duplicate ID;
- the EA submits/modifies/cancels through MetaTrader and streams acknowledgements, fills, positions, margin and heartbeat events back to Go;
- both sides persist the same correlation/idempotency ID;
- the EA refuses new entries when Go heartbeat is stale, but still permits protective exits and emergency flattening;
- terminal and account reconciliation runs at startup, reconnect, and on a fixed interval;
- one EA build supports broker symbol aliases and account-specific contract metadata through configuration.

No browser clicking, password scraping, DLL injection, or unofficial broker endpoint is part of the design.

The production topology separates the portable Go service from terminal-dependent workers:

```text
Go control/risk service (Linux or Windows VPS)
  ├── Pepperstone cTrader Open API (direct TLS/WebSocket)
  ├── secure outbound channel ── Exness Windows node: MT5 + MQL5 bridge
  └── secure outbound channel ── Trade Nation Windows node: MT4 + MQL4 bridge
```

Trade Nation documents only MT4 Web access as the native Mac path, while desktop EAs require a terminal environment ([Trade Nation account connection](https://support.tradenation.com/connecting-mt4-accounts)). Exness offers Windows VPS hosting for EAs subject to its eligibility and service terms ([Exness VPS](https://www.exness.com/vps/)). Therefore continuous automation will use supervised Windows VPS nodes for MetaTrader rather than depend on the development Mac remaining awake. The bridges connect outbound to Go using mutual authentication; MetaTrader ports are not exposed publicly.

### 6.2 TradingView role

TradingView is optional and serves three controlled purposes:

1. **Visual rulebook:** Pine indicators display ICC/ICT/SMC/CRT states, session boxes and liquidity levels so human chart markups can be compared with Go output.
2. **Signal parity:** alerts are ingested in paper/shadow mode and compared with the Go engine; disagreements become test cases.
3. **Advisory trigger:** if later enabled, a webhook may propose an intent, but Go must reconstruct the signal from trusted market data and re-run all risk checks before execution.

TradingView documents that webhook delivery can fail, has a three-second receiver timeout, and may retry qualifying failures up to three times ([webhook configuration](https://www.tradingview.com/support/solutions/43000529348-how-to-configure-webhook-alerts/), [webhook resubmission](https://www.tradingview.com/support/solutions/43000735201-webhook-resubmission/)). Therefore every webhook requires a unique event ID, timestamp/expiry, schema version, strategy version, symbol/timeframe, bar-close time and payload signature. The receiver acknowledges only after durable storage, deduplicates all retries, and never accepts credentials or risk/quantity authority in the payload.

Trade Nation's TN Trader and Pepperstone accounts can be connected to TradingView for chart-based trading ([Trade Nation integration](https://tradenation.com/tradingview/), [Pepperstone platforms](https://pepperstone.com/en-gb/platforms)). This is useful for manual supervision, but it is not a substitute for the broker adapter's order and position reconciliation.

### 6.3 Multi-broker routing and risk

The same signal must **not** automatically become three leveraged positions. The default rollout is:

1. one broker is `ACTIVE_DEMO` for order submission;
2. the other two are `SHADOW`, recording executable quotes and simulated fills;
3. reports compare spread, commission, swap, slippage, rejection, latency and symbol availability;
4. a broker can become active only through configuration promotion and an audit event;
5. live mode initially permits exactly one active broker and one account;
6. later multi-broker routing, if justified, allocates one risk budget across accounts rather than duplicating it.

Portfolio risk aggregates all broker accounts by currency and metal exposure. A reconciliation mismatch, unknown manual position, disconnected active adapter, stale quote, or account-equity discrepancy blocks new entries globally until resolved. Broker-specific feeds are stored separately: strategy research will quantify whether a signal survives feed differences rather than assuming Exness, Trade Nation, Pepperstone and TradingView candles are identical.

### 6.4 Core boundaries

```go
type Strategy interface {
    OnEvent(Context, MarketEvent, PortfolioView) []OrderIntent
}

type RiskEngine interface {
    Evaluate(Context, OrderIntent, PortfolioView, MarketView) RiskDecision
}

type Broker interface {
    Submit(context.Context, Order) (BrokerOrder, error)
    Cancel(context.Context, BrokerOrderID) error
    OpenOrders(context.Context) ([]BrokerOrder, error)
    Positions(context.Context) ([]BrokerPosition, error)
}
```

The event loop uses an injected clock and ordered events. Decimal/fixed-point values are used for money and quantity; `float64` is restricted to statistics where rounding rules are explicit. The order lifecycle is a persisted state machine: `proposed → risk-approved → submitted → acknowledged → partially-filled → filled/cancelled/rejected`, with recovery from every state.

Start with SQLite or PostgreSQL for configuration, trials, audit events, orders and fills; use Parquet/partitioned files for larger historical datasets. Store broker-native order IDs and the bot's broker-independent correlation ID together.

## 7. Backtest and validation protocol

### 7.1 Data controls

- use point-in-time data and immutable raw snapshots;
- store source, checksum, venue timezone, session rules and adjustments;
- detect duplicates, gaps, non-monotonic timestamps and impossible OHLC values;
- never build a signal from the closing value of a bar and fill at an earlier price;
- for ambiguous OHLC bars where both stop and target are touched, assume the adverse path or replay finer data;
- model bid/ask spread, commission, slippage, partial fills, latency, funding/rollover and market impact where relevant.

Transaction costs must be applied inside each trial; omitting them can create false discoveries ([research on trading costs and data snooping](https://www.sciencedirect.com/science/article/pii/S0165176522001720)).

### 7.2 Dataset split and robustness

1. Freeze rules before viewing holdout results.
2. Use chronological train/validation/untouched test periods.
3. Add purging/embargo around overlapping labels and trades.
4. Test across multiple instruments, volatility regimes, sessions and years.
5. Run parameter-sensitivity grids; prefer broad stable plateaus over a sharp optimum.
6. Bootstrap trades/blocks and run Monte Carlo sequences for drawdown and risk-of-ruin estimates.
7. Compare against simple baselines: no-trade, buy-and-hold where appropriate, random entries with identical exits, and a simple trend/range rule.
8. Report all attempted variants and calculate multiple-testing-aware statistics such as DSR.

### 7.3 Required report metrics

- net return and expectancy in R after all costs;
- win rate with a confidence interval, not just a point estimate;
- average/median win, loss and realized reward:risk;
- profit factor, Sharpe/Sortino, Deflated Sharpe Ratio;
- maximum and duration of drawdown, time under water;
- exposure, turnover, trade count and capacity assumptions;
- MAE/MFE, slippage distribution and rejection/partial-fill rates;
- results by instrument, long/short, session, year and volatility regime.

### 7.4 Evidence gates

The 80% figure is not a gate. Robustness is.

- **Backtest gate:** positive net expectancy on untouched data, sufficient trades for useful confidence intervals, costs stress-tested at 2×, no single market/regime responsible for most profits, and acceptable drawdown under bootstrap/Monte Carlo.
- **Replay gate:** identical decisions for repeated input streams; no look-ahead; deterministic audit trail.
- **Paper gate:** at least 8–12 weeks and 200 eligible signals (longer if frequency is low), with live-data signal behavior and simulated fills consistent with the historical model.
- **Shadow gate:** send no orders; compare intended orders with real executable quotes and venue constraints for at least 4 weeks.
- **Micro-live gate:** smallest venue size, one strategy/instrument, hard capital cap, manual supervision and instant kill switch.
- **Scale gate:** increase risk only in small steps after new out-of-sample evidence; never scale merely to recover a loss.

Any major rule or parameter change returns the strategy to the backtest gate.

## 8. Testing and operations

- unit/property tests for candle aggregation, sizing, P&L and every risk invariant;
- golden replay tests for complete strategy scenarios;
- fuzz parsers, decimal conversions and order-state transitions using Go's built-in fuzzing ([Go fuzzing](https://go.dev/doc/security/fuzz/));
- integration tests against a fake broker covering timeouts, retries, duplicate acknowledgements and partial fills;
- crash/restart and broker-desynchronization drills;
- `go test -race ./...` for concurrent paths ([Go race detector](https://go.dev/doc/articles/race_detector));
- structured logs, metrics, traces and alerts; all decisions carry strategy/config/data versions;
- deterministic builds and a promotion record from research to paper to live.

## 9. Delivery roadmap

### Phase 0 — Product decisions and rulebook (1 week)

- confirm EUR/USD and XAU/USD as the first instruments and Pepperstone cTrader demo as the proposed first execution target;
- record the legal entity/jurisdiction, account type, platform, base currency, leverage and symbol catalogue for every demo account;
- choose holding horizon and historical/live data source;
- select canonical Trades by Sci lessons and freeze mechanical ICC definitions;
- write one-page, example-driven rules for ICC-continuation v1 and CRT-liquidity-reclaim v1;
- define London, New York, overlap, benchmark, rollover, holiday and news-window behavior in configuration;
- agree on risk limits and legal/regulatory constraints for the account jurisdiction.

Deliverable: frozen strategy specification plus sample annotated winning, losing and invalid setups.

### Phase 1 — Research kernel (2–3 weeks)

- Go module, domain types, config validation and deterministic event clock;
- historical data importer/validator and bar builder;
- feature detectors, paper broker and event-driven backtester;
- cost/fill models, risk sizing and first analytics report.
- broker-neutral symbol registry plus Exness, Trade Nation and Pepperstone contract-metadata fixtures.

Deliverable: reproducible baseline backtest from a single command.

### Phase 2 — Robustness lab (2–4 weeks)

- chronological splits, purging/embargo and locked holdout;
- trial registry, sensitivity analysis, DSR and bootstrap/Monte Carlo;
- confluence ablations and regime/instrument breakdowns;
- independent review of data leakage and execution assumptions.

Deliverable: go/no-go research report; weak strategies are retired, not optimized indefinitely.

### Phase 3 — Paper execution (3–5 weeks engineering, then 8–12+ weeks observation)

- Pepperstone cTrader Open API demo adapter;
- Exness MT5 and Trade Nation MT4 EA bridges, first in shadow and then demo mode;
- optional TradingView Pine visualizer and authenticated, idempotent webhook receiver for parity testing;
- durable order state machine, reconciliation and idempotency;
- risk daemon, kill switches, dashboards and alerts;
- compare modeled versus observed spreads, slippage and latency.

Deliverable: paper-trading evidence report and operational runbook.

### Phase 4 — Guarded live pilot (only after gates pass)

- least-privilege live credentials and smallest order size;
- deployment, backup/restore, incident and manual-close procedures;
- shadow/canary rollout, daily reconciliation and weekly risk review.

Deliverable: limited live pilot with explicit loss budget; no automatic scaling.

## 10. Confirmed scope and remaining decisions

Confirmed: ICC means Trades by Sci's Indication–Correction–Continuation; ICT is also in scope as a separate module; APA means price action; the market is FX plus gold and silver; session/liquidity-timeframe context is required; and the broker targets are Exness, Trade Nation, and Pepperstone with TradingView available.

Remaining decisions:

1. Which legal entity/jurisdiction, account type, base currency and leverage apply to each broker account?
2. Are Pepperstone cTrader, Exness MT5 and Trade Nation MT4 accounts available, and can demo credentials be created for integration testing?
3. Should the first deployment target intraday holds, multi-session swing holds, or support both as separately tested strategies?
4. What historical and live data is available (OHLCV, tick, bid/ask, and possibly depth)?
5. Is 80% meant as win rate, percentage of profitable days, or return? It remains a measured hypothesis in every case.
6. What absolute capital-at-risk and maximum acceptable drawdown can the pilot lose without creating financial harm?
7. Which Trades by Sci lessons/examples are the canonical ICC rule source when public explanations differ?

## 11. Recommended first milestone

Do not begin with all concepts. Begin with two isolated baselines on **EUR/USD and XAU/USD**: **ICC continuation v1 (H4/H1 context, M15 confirmation)** and **CRT liquidity-reclaim v1 (H1 anchor, M15 confirmation)**. Use a paper broker and the independent risk engine. Produce reproducible reports for London, New York, overlap, and off-session control groups, with and without each confirmation filter. This establishes whether each concept and session contributes out-of-sample value rather than making historical charts look more convincing.

## Sources and research notes

- CFTC: [Forex frauds and automated-system warning](https://www.cftc.gov/LearnAndProtect/forexfrauds)
- CFTC: [Leverage amplifies risk](https://www.cftc.gov/LearnAndProtect/AdvisoriesAndArticles/understand_risks_of_virtual_currency.html)
- Exness: [MT5 platform and instruments](https://www.exness.com/metatrader-5/)
- Trade Nation: [MT4 and Expert Advisors](https://tradenation.com/en-gb/metatrader-4/), [TradingView integration](https://tradenation.com/tradingview/)
- Trade Nation: [MT4 account and Mac access](https://support.tradenation.com/connecting-mt4-accounts)
- Pepperstone: [Platforms and API options](https://pepperstone.com/en-gb/platforms)
- Exness: [VPS for Expert Advisors](https://www.exness.com/vps/)
- cTrader: [Open API](https://help.ctrader.com/open-api/), [authentication](https://help.ctrader.com/open-api/account-authentication/)
- TradingView: [Webhook behavior](https://www.tradingview.com/support/solutions/43000529348-how-to-configure-webhook-alerts/), [resubmission](https://www.tradingview.com/support/solutions/43000735201-webhook-resubmission/)
- Trades by Sci: [Market structure lesson](https://www.youtube.com/watch?v=Y7Q37vTbuNg&vl=en)
- BIS: [2025 Triennial FX survey](https://www.bis.org/press/p250930.htm)
- BIS Markets Committee: [FX execution algorithms and market functioning](https://www.bis.org/publ/mktc13.htm)
- LBMA: [Gold and silver benchmark schedule](https://www.lbma.org.uk/prices-and-data/about-lbma-daily-auction-prices)
- CME Group: [Global gold and silver trading centres](https://www.cmegroup.com/education/articles-and-reports/trading-comex-gold-and-silver)
- Bailey & López de Prado: [The Deflated Sharpe Ratio](https://doi.org/10.2139/ssrn.2460551)
- Research: [Trading costs and false discoveries](https://www.sciencedirect.com/science/article/pii/S0165176522001720)
- Cont, Kukanov & Stoikov: [The Price Impact of Order Book Events](https://arxiv.org/abs/1011.6402)
- Federal Reserve Bank of New York: [Stop-Loss Orders and Price Cascades in Currency Markets](https://fraser.stlouisfed.org/files/docs/publications/frbnysr/frbny_sr150.pdf)
- Practitioner references: [Candle Range Theory](https://candlerangetheory.com/), [CRT/Wyckoff explanation](https://tradingwyckoff.com/en/crt/)
- Go: [Fuzzing](https://go.dev/doc/security/fuzz/), [Race detector](https://go.dev/doc/articles/race_detector)

Practitioner references define the terminology only. They do not substantiate win-rate or profitability claims.
