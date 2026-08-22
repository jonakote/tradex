## Market Data Provider Management System

Design and implement a production-ready **Market Data Provider Manager** that allows a trading bot to consume market data from multiple providers without unnecessarily consuming API quotas.

### Providers

Support these providers behind a common abstraction:

- Finnhub
- Twelve Data
- Alpha Vantage
- Polygon/Massive
- Yahoo Finance

Do not allow trading strategies or bot components to call providers directly.

All market-data requests must go through a central `MarketDataManager`.

---

## Architecture

Implement the following layers:

```text
Trading Strategies
        │
        ▼
MarketDataManager
        │
        ├── Cache
        ├── Request Deduplication
        ├── Rate Limiter
        ├── Provider Selection
        ├── Provider Health Tracking
        ├── Circuit Breaker
        ├── Usage Tracking
        └── Retry / Backoff
                │
        ┌───────┼────────┬──────────┐
        ▼       ▼        ▼          ▼
    Finnhub  TwelveData AlphaVantage Polygon/Massive
                                  │
                                Yahoo
```

---

## Provider abstraction

Create a common interface similar to:

```dart
abstract interface class MarketDataProvider {
  String get name;

  Future<Quote> getQuote(String symbol);

  Future<List<Candle>> getCandles({
    required String symbol,
    required Timeframe timeframe,
    required DateTime from,
    required DateTime to,
  });

  Future<List<String>> getSymbols();

  bool supports(Instrument instrument);
}
```

The exact language/framework can be adapted to the existing project.

---

## Rate limiting

Every provider must have its own rate limiter.

Do NOT use a single global rate limiter.

The rate limiter must support:

- Requests per second
- Requests per minute
- Requests per hour
- Requests per day
- Provider-specific quotas
- Configurable limits
- Remaining quota tracking
- Automatic cooldown
- HTTP 429 handling
- Retry-After handling when supplied by the provider

Never assume that all providers have the same limits.

Provider limits must be configurable rather than hard-coded.

---

## Request caching

Implement caching before making an API request.

Cache keys should include:

```text
provider-independent data key
symbol
data type
timeframe
request parameters
```

Example:

```text
quote:XAUUSD
candles:XAUUSD:5m:2026-08-20
```

Use configurable TTLs.

Suggested defaults:

```text
Quote:             1-5 seconds
1m candles:        5-15 seconds
5m candles:        30 seconds
15m candles:       1-2 minutes
1h candles:        5 minutes
Daily candles:     30-60 minutes
Symbol metadata:   24 hours
```

Make these values configurable.

Never fetch data from a provider if valid cached data exists.

---

## Request deduplication

Implement in-flight request deduplication.

If five strategies simultaneously request:

```text
XAUUSD 5m candles
```

the system must make only ONE provider request.

All five callers should await the same in-flight Future/Promise.

Example:

```text
Strategy A ─┐
Strategy B ─┤
Strategy C ─┼──> MarketDataManager
Strategy D ─┤
Strategy E ─┘
                 │
                 ▼
             ONE API call
                 │
                 ▼
             FIVE results
```

Remove the in-flight request after completion or failure.

---

## Provider selection

Implement intelligent provider selection.

The manager should consider:

1. Whether the provider supports the requested instrument.
2. Whether the provider has remaining quota.
3. Provider health.
4. Current cooldown state.
5. Current rate-limit availability.
6. Data freshness.
7. Provider priority.
8. Cost/quota efficiency.

Example configuration:

```text
XAUUSD:
  primary: TwelveData
  secondary: Finnhub
  tertiary: Polygon

EURUSD:
  primary: Finnhub
  secondary: TwelveData
  tertiary: Polygon

BTCUSD:
  primary: Finnhub
  secondary: TwelveData
  tertiary: Yahoo
```

Do not blindly fail over on every error.

---

## Circuit breaker

Implement a circuit breaker per provider.

States:

```text
CLOSED
OPEN
HALF_OPEN
```

Open the circuit after configurable consecutive failures.

While OPEN:

- Do not send requests to the provider.
- Route requests to another healthy provider.
- Wait for the configured recovery timeout.
- Perform a limited health request during HALF_OPEN.

Successful recovery should return the provider to CLOSED.

---

## Retry strategy

Implement bounded retries.

Do not retry:

- Authentication failures
- Invalid requests
- Unsupported symbols
- Permanent provider errors

Retry selectively for:

- Network failures
- Timeouts
- HTTP 429
- Temporary 5xx responses

Use exponential backoff with jitter.

Respect `Retry-After` when available.

Never retry indefinitely.

---

## Usage monitoring

Track per-provider:

```text
requests
successfulRequests
failedRequests
rateLimitedRequests
timeouts
quotaUsed
quotaRemaining
lastRequestAt
lastSuccessfulRequestAt
consecutiveFailures
circuitState
```

Expose this through a monitoring interface.

Example:

```text
Provider        Requests    Errors    RateLimited    Remaining
Finnhub             42         1           0             18
TwelveData           7         0           1              1
AlphaVantage        17         0           0              8
Polygon              9         0           0             91
Yahoo               12         0           0              -
```

The actual limits must come from configuration/provider documentation rather than assumptions.

---

## Candle optimization

Avoid repeatedly requesting completed candles.

For example, if the bot needs 5-minute candles:

```text
10:00 ───────── 10:05
       candle closes
```

Once the 10:00-10:05 candle has been stored, do not repeatedly request it unless:

- The provider may revise the candle.
- The data is missing.
- The application explicitly requests a refresh.

For historical requests, first query the local database/cache and only request missing ranges from the provider.

---

## Historical data

Implement a historical data repository.

Request flow:

```text
Strategy
   │
   ▼
MarketDataManager
   │
   ▼
Local Database
   │
   ├── Complete → return data
   │
   └── Missing
          │
          ▼
      Provider API
          │
          ▼
      Store locally
          │
          ▼
      Return data
```

Never download the same historical candles repeatedly.

---

## Data normalization

Normalize all provider responses into common domain models.

For example:

```dart
class Quote {
  final String symbol;
  final double bid;
  final double ask;
  final double price;
  final DateTime timestamp;
  final String provider;
}

class Candle {
  final String symbol;
  final Timeframe timeframe;
  final DateTime timestamp;
  final double open;
  final double high;
  final double low;
  final double close;
  final double? volume;
  final String provider;
}
```

Do not expose provider-specific response models to trading strategies.

---

## Stale-data protection

Every returned market-data object must contain a timestamp.

The manager must be able to determine:

```text
fresh
stale
expired
```

Never allow a strategy to unknowingly trade on stale data.

Provide configurable maximum acceptable age per data type.

Example:

```text
Live quote:       max 10 seconds
1m candle:        max 2 minutes
5m candle:        max 7 minutes
15m candle:       max 20 minutes
```

These should be configurable.

---

## Concurrency

The manager must be safe under high concurrency.

Prevent:

- Duplicate requests
- Race conditions in quota tracking
- Cache stampedes
- Simultaneous provider failovers
- Concurrent circuit-state corruption

Use appropriate locking/semaphore/atomic mechanisms for the chosen language.

---

## Observability

Add structured logs for:

```text
provider selected
cache hit
cache miss
request started
request completed
request failed
rate limit reached
provider cooldown
provider failover
circuit opened
circuit closed
stale data detected
```

Do not log API keys or sensitive credentials.

---

## Configuration

Provider credentials and limits must come from environment/configuration.

Never hard-code:

```text
API keys
API secrets
provider quotas
```

Example:

```text
FINNHUB_API_KEY
TWELVE_DATA_API_KEY
ALPHA_VANTAGE_API_KEY
POLYGON_API_KEY
```

Use dependency injection so providers can be mocked during testing.

---

## Testing

Write unit tests for:

- Cache hits
- Cache expiration
- Request deduplication
- Rate limiting
- HTTP 429
- Retry-After
- Exponential backoff
- Provider failover
- Circuit breaker
- Provider recovery
- Stale data detection
- Historical data caching
- Concurrent requests
- Quota tracking

Create integration tests using mocked provider APIs.

No test should consume real provider quota.

---

## Critical requirement

The system must optimize for **minimum external API usage**.

Before making ANY provider request, execute this decision flow:

```text
Request
  │
  ▼
Is data in cache?
  │
  ├── YES → Is it fresh?
  │             │
  │             ├── YES → Return cached data
  │             └── NO
  │
  └── NO
       │
       ▼
Is an identical request already running?
       │
       ├── YES → Await existing request
       │
       └── NO
            │
            ▼
      Select provider
            │
            ▼
      Has quota available?
            │
       ┌────┴────┐
       │         │
      YES        NO
       │         │
       ▼         ▼
    Request   Next provider
       │
       ▼
    Success?
       │
   ┌───┴────┐
   │        │
  YES       NO
   │        │
   ▼        ▼
 Cache    Retry/failover
   │
   ▼
 Return
```

The final implementation should be production-ready, modular, testable, provider-agnostic, and designed so that adding another market-data provider later does not require changes to trading strategies.