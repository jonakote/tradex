# Broker session calendars and swap schedules

Tradex accepts a strict `tradex.broker-schedule/v1` artifact for a specific broker, account type, symbol, and effective-date range. The artifact contains:

- provenance: dataset ID, vendor/feed, broker, retrieval time, source timezone, raw-artifact SHA-256, and licence reference;
- seven weekly local-time calendar entries with open/close windows;
- date-specific exceptions for holidays, maintenance, and early closes;
- the exact signed paper rollover configuration, including UTC charge boundary, charge weekdays, triple day, and long/short rates.

Compile it into a replay scenario with:

```sh
go run ./cmd/tradex compile-schedule \
  base-scenario.json \
  pepperstone-eurusd-schedule.json \
  scheduled-scenario.json
```

Compilation removes existing hand-authored `MARKET_STATUS` events, injects deterministic status transitions across the scenario's effective range, attaches the schedule for manifest provenance, and copies its rollover settings into paper execution. Calendar windows use the artifact's IANA timezone, so daylight-saving transitions are resolved by the timezone database. Overnight windows are supported.

The importer does not infer broker hours from a generic FX calendar. A schedule must be sourced for the exact broker, account type, symbol, and period. Exness, Trade Nation, and Pepperstone can have different holiday closures, metal hours, maintenance windows, triple-swap conventions, and rate tables. Effective dates and raw checksums make schedule changes visible in frozen experiment identity.
