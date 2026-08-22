# Broker-native quote datasets

Tradex accepts historical bid/ask observations through `tradex.quotes/v1`. A quote dataset is not a candle proxy: every observation contains both executable sides, a UTC event timestamp, a source sequence, and feed identity.

Required provenance includes dataset ID, vendor, feed, broker, retrieval time, source timezone, `UTC_EVENT_TIME` timestamp basis, raw artifact SHA-256, and licence reference. Streams are symbol-specific and require strictly increasing source sequence and chronological timestamps. Optional `max_gap` records the maximum expected interval; gaps above it must be explicitly allowed with `allow_gaps`.

Compile a validated quote stream into an existing strategy scenario:

```sh
go run ./cmd/tradex compile-quotes \
  base-scenario.json \
  pepperstone-eurusd-quotes.json \
  broker-quotes-scenario.json
```

Compilation removes any existing synthetic `QUOTE` events, preserves all strategy and execution-candle events, attaches the quote dataset for manifest provenance, and merges broker quotes by UTC time. At an equal timestamp, the broker quote is ordered before the strategy event. The resulting scenario must be frozen into a new manifest; the quote dataset fingerprint therefore becomes part of experiment identity.

Quote data still has limitations. A top-of-book quote stream does not reveal depth, queue position, market impact, or all venue-side rejection logic. Missing-data policy must be reviewed, and broker symbol aliases, bid/ask conventions, timestamps, weekend/holiday closures, and account-currency conversion rates must be normalized before compilation. Never combine quotes from one broker with candle or swap assumptions from another without recording that deliberate experimental boundary.
