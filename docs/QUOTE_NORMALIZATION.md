# Quote normalization and conversion streams

Broker exports often use aliases such as `EUR/USD`, timestamps in Unix milliseconds, or local wall-clock strings. Tradex keeps that raw layer separate from the canonical `tradex.quotes/v1` dataset.

Normalize a raw quote artifact with an explicit configuration:

```sh
go run ./cmd/tradex normalize-quotes \
  raw-quotes.json normalization.json normalized-quotes.json
```

Supported timestamp bases are `RFC3339`, `LOCAL_RFC3339`, `UNIX_MILLIS`, and `UNIX_NANOS`. Symbol aliases are explicit and collisions are rejected. The normalized result is UTC, sorted by event time and source sequence, and carries the original artifact provenance. Price integers are not rescaled implicitly; the instrument price scale must be documented and validated separately.

Account-currency conversion streams use `tradex.conversions/v1`. Each stream contains a directed currency pair and time-ordered rational observations. `RateAt` selects the latest observation at or before the requested time and rejects stale data. `CompileConversionDataset` can attach a conversion dataset to a replay scenario and derive the rational `PriceValue` used by risk and realized P&L.

Conversion direction matters: for a USD account trading USD/JPY, the required stream is `JPY -> USD`. Missing, future, or stale rates fail closed. Do not silently use a chart quote, a different broker, or a later observation as a conversion rate.
