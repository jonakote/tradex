# Execution realism model

Tradex paper replay models execution conditions deterministically so an experiment can be reproduced from its frozen manifest. These controls make a simulation less optimistic; they do not prove that an order would have received the same treatment at Exness, Pepperstone, Trade Nation, or any other venue.

## Quote liquidity and partial fills

A `QUOTE` event may set `available_quantity`. The value is a quantity-step-aligned cap applied independently to each eligible order at that quote:

- omitted means unlimited simulated liquidity;
- zero means the quote is valid but no entry quantity is executable;
- a positive value may create or extend a partial entry.

Successive partial fills share one broker position. Its entry price is quantity-weighted and rounded adversely: upward for buys and downward for sells. Entry commission accumulates per fill. If the stop or target closes the filled portion, any unfilled order remainder is cancelled.

This is a deterministic top-of-book stress input, not a queue-position or order-book simulator. It does not model competing orders, market impact, rejection probability, or liquidity depletion across different orders.

## Submission latency

`paper.submission_latency` delays broker eligibility from submission time. A quote before the eligibility time cannot fill the order. If the configured latency extends beyond the intent expiry, the broker records a rejected order with `LATENCY_EXCEEDS_INTENT_TTL`.

Latency is currently fixed per instrument scenario. It does not model a measured distribution, network path, server processing phases, or broker-specific last-look behavior.

## Variable spread

Native `QUOTE` events always execute against their supplied bid and ask, so spread can vary at every observation. Candle-only execution may set `half_spread_ticks` on an `EXECUTION_CANDLE`; when omitted, the scenario's `half_spread_ticks_for_candle` default is used.

Candle spread remains synthetic because OHLC data has no historical bid/ask path. Broker-native bid/ask datasets are the next data-quality slice.

## Market state

A `MARKET_STATUS` event requires `market_open: true` or `false`. While closed:

- newly submitted approved orders become broker-rejected with `MARKET_CLOSED`;
- quotes and execution candles do not fill orders or close positions;
- pending orders remain pending and may become eligible after a later open event, subject to expiry.

Tradex does not invent a universal holiday or weekend calendar. Market-status events must be generated from the broker, instrument, account, and date-specific session calendar used by the experiment. This avoids treating normal FX, metal, holiday, and maintenance hours as interchangeable.

## Rollover/swap

Rollover is disabled by default. An enabled `paper.rollover` definition contains:

- `minute_utc`, the daily charge boundary as minutes after 00:00 UTC;
- `charge_weekdays`, seven booleans ordered Sunday through Saturday;
- `triple_weekday` and `triple_multiplier`;
- separate signed long and short charges in account-currency minor units per quantity step.

Each open position accrues at crossed configured boundaries. The amount appears as `rollover_minor` on the closing fill, contributes to realized and closed-trade net P&L, and is reported separately from commission. Positive values are credits and negative values are charges.

Broker swap schedules, triple-swap days, symbols, account types, currencies, daylight offsets, and rates can change. Every production-grade experiment must source and freeze those inputs for the exact broker account and tested period. A disabled rollover block must be entirely zero-valued so assumptions cannot be silently ignored.

## Versioning and review

The single and shared replay engines and paper cost model are version 2. Manifests identify the fill model as `paper-realism-v2`; changing any event condition or paper setting changes the frozen content fingerprints.

Research reports expose rejected, cancelled, open-partial, and partially executed order counts plus commission and rollover totals. Review these alongside completed-trade metrics: fills alone cannot reveal missed entries or broker-side rejections.

Before any live promotion, replace illustrative assumptions with measured broker-native data, run latency/spread/liquidity stress grids, reconcile demo fills, and retain the independent account-level kill switches. No win-rate or profit target—including 80%—is accepted without untouched holdout evidence and live-forward validation.
