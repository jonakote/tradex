# Market data sources for research

Backtest conclusions are only as good as the candles behind them. This
document records what each source actually delivers, because two of them ship
data that looks fine and is not.

## Verified defect: Yahoo Finance FX closes are unusable

The Yahoo chart API (`query1.finance.yahoo.com/v8/finance/chart/EURUSD=X`)
returns FX candles whose **close frequently duplicates the open**:

| Series            | Bars   | `open == close` |
| ----------------- | ------ | --------------- |
| EURUSD=X daily    | 1300   | 953 (73.3%)     |
| GBPUSD=X daily    | 1300   | 90 (6.9%)       |
| EURUSD=X hourly   | 17294  | 1700 (9.8%)     |
| GC=F (gold) daily | 1258   | 0 (0.0%)        |
| GC=F hourly       | 13768  | 103 (0.7%)      |
| BTCUSDT (Binance) | 6570   | 0 (0.0%)        |

Highs and lows agree with Dukascopy to a few tenths of a pip; the close does
not. Cross-checked on EURUSD 2024 against Dukascopy over 262 overlapping
days: mean absolute close difference 0.00138 with a maximum of 0.0203 (203
pips), while 44 days differ by more than 30 pips.

This is fatal for any strategy that reads a candle's close. Candle Range
Theory is exactly such a strategy — the purge candle must *close back inside*
the range — so every Yahoo-sourced FX result is void. Yahoo also caps 1h
history at roughly 730 days, which separately starves a daily/1h study.

**Do not use Yahoo for FX or metals research.** It remains acceptable only for
coarse daily high/low context.

## Dukascopy (preferred for FX and metals)

Public, keyless historical feed at
`datafeed.dukascopy.com/datafeed/{SYMBOL}/...`. Implemented in
`internal/feed/dukascopy` with `cmd/dukafetch`.

Properties that matter:

- Prices are published as **integer points** at the instrument's quoted
  precision, which map directly onto `domain.Price` ticks. No floating-point
  conversion touches a price, so decoding is exact and reproducible.
- Payloads are LZMA-alone `.bi5` files. Go's standard library has no LZMA
  decoder and the repository forbids third-party dependencies, so
  `internal/encoding/lzma` implements the LZMA1 decoder directly. It is
  verified against fixtures produced by the reference XZ Utils encoder plus a
  real published payload.
- Record layout is 24 bytes big-endian: `int32` seconds from the file epoch,
  then **open, close, low, high** as `int32` points, then `float32` volume.
  The field order is not OHLC.
- **Months are zero-indexed in URL paths.** `/2024/05/` is June 2024. This
  silently shifts an entire study by one month if missed; it is pinned by a
  test.
- Closed-market periods appear as flat filler bars with zero volume. They are
  dropped; treating them as candles fabricates ranges and purges.
- Digit counts must be verified per instrument, never guessed: 5 for
  non-JPY FX majors, 3 for JPY crosses and for XAU/XAG. A wrong value
  misprices every candle by a factor of 100. `Lookup` refuses unknown
  symbols rather than assuming.
- Requests require a browser `User-Agent`, otherwise the feed answers 429.

Request cost is set by file granularity, and the feed is slow (tens of
seconds per file when cold):

| Timeframe | File covers | Requests for 15 years |
| --------- | ----------- | --------------------- |
| `d1`      | one year    | 15                    |
| `h1`      | one month   | 180                   |
| `m1`      | one day     | ~5,500                |

`Client` therefore fetches through a bounded worker pool (default 6) and
caches raw `.bi5` payloads on disk. Always pass `-cache`. Concurrency never
reaches the result: candles are merged by open time, so output is identical
regardless of completion order.

Minute data is supported but should be treated as a deliberate, cached
investment rather than a default.

### Keyless live-ish FX: tick files

The aggregated candle files publish per month and so lag by up to a month;
November's hourly file does not exist on 5 November. Tick files, however,
publish per completed clock hour, which is the only keyless route to recent
FX and metal prices.

`Client.Ticks` and `Client.TickCandles` decode and aggregate them;
`dukascopy.LiveFeed` polls them and emits `feed.Candle` for the live signal
engine (`"dukascopy"` section in the signals config).

Tick record layout (big-endian, 20 bytes): `uint32` milliseconds from the
hour, `uint32` ask and `uint32` bid in instrument points, then two `float32`
volumes. Prices are integer points, so aggregation never touches a float.

The hard constraint is latency. A bar is visible roughly an hour after it
closes, because its hour's file does not exist until the hour ends. That is:

- adequate for daily and 4h research monitoring;
- **not** adequate for acting on a 15m or 1h limit, whose fill window closes
  before the data arrives.

Two correctness rules the implementation enforces:

- the in-progress hour is never requested — its file does not exist;
- a bar is emitted only when **every** hour it spans has published, so a 4h
  bar missing one hour is withheld rather than published with an understated
  range.

Closed-market hours publish empty files, which is data rather than an error.
Transient `503`s under load are expected and handled with exponential
backoff.

## Binance (crypto only)

`api.binance.com/api/v3/klines`, keyless, 1000 bars per request, paginated by
`startTime`. OHLC is internally consistent — no duplicated closes in 6,570
4h bars checked. Crypto is outside the tradex instrument universe, so it is
useful only as an additional out-of-sample regime for a strategy's geometry.

## Quote precision is a verified fact, not an inference

`internal/instrument` is the single source of truth for quote precision, and
both the live signal engine and the Dukascopy feed read it. A test asserts the
feed and the registry agree, so they cannot drift.

Precision is load-bearing because every price in tradex is an integer tick: a
wrong scale does not merely misformat output, it changes the meaning of every
tick-denominated strategy parameter. The live engine previously inferred the
scale from the symbol string and defaulted to five decimals, which produced:

| Symbol  | Assumed | Verified | Consequence |
| ------- | ------- | -------- | ----------- |
| BTCUSDT | 5 digits, tick 0.00001 | 2 digits, tick 0.01 (Binance `PRICE_FILTER`) | `stop_buffer_ticks: 2` was worth $0.00002 on an $86,000 instrument, so the stop sat on the purge wick with no buffer at all; prices printed as 85999.99998 |
| XAUUSD  | 2 digits | 3 digits (Dukascopy points) | live engine and research pipeline disagreed about a tick |
| XAGUSD  | 5 digits with a 0.01 pip | 3 digits, 0.01 pip | internally inconsistent |
| US30    | unmatched, fell through to FX | 2 digits | index treated as a 5-decimal FX pair |

Provenance of the registry:

- FX and metals: verified against Dukascopy's published integer points.
- Crypto: verified against the Binance `PRICE_FILTER` `tickSize` from
  `/api/v3/exchangeInfo`.
- Energy and indices: carried over from prior repository convention and
  flagged `Unverified`. They resolve so existing symbols keep working, but
  their precision must be confirmed before tick-denominated parameters are
  trusted on them.

`Lookup` fails closed. An unregistered symbol returns false and the live
engine skips it, because a plausible-looking wrong scale is far more damaging
than a refusal. `Normalize` maps every feed spelling — `EUR/USD`, `eur-usd`,
`EURUSD` — onto one spec.

## Bar timestamps: vendors stamp the OPEN

`feed.Candle.Time` is the instant a bar **opened**, and `OpenTime`/`CloseTime`
state the owned half-open interval `[OpenTime, CloseTime)` explicitly. Every
producer sets them; consumers must read them rather than infer.

This was previously undefined and consumers disagreed. Binance and cTrader
set `Time` to the open, the market-data manager read it as the open, and the
signal engine read it as the close and subtracted a timeframe. The engine
therefore labelled every bar with the **previous** bar's window, which:

- stamped a pattern one full bar before its data existed (look-ahead in the
  live path);
- expired the resulting order window at the moment it should have opened,
  making the entry unusable;
- overstated candle age by a bar, so the staleness guard suppressed
  genuinely fresh signals.

Two further rules follow:

- **Derive the close from the open plus the interval.** Binance reports a
  close one millisecond early (`23:59:59.999` for a bar owning up to
  `24:00:00`), and `domain.Candle` requires close to equal open plus
  timeframe exactly.
- **Interval spellings must share one table.** `feed.IntervalDuration`
  covers both vocabularies. The signal engine previously kept a local copy
  that knew only Twelve Data's forms (`15min`, `1day`), so a Binance `15m`
  or `1d` stream resolved to a zero duration and every candle was dropped
  with no error. Only `1h/2h/4h/8h` overlapped.

## Standing rules

1. Before trusting a new source, check `open == close` rates and cross-check
   closes against a second source over an overlapping window.
2. Prefer sources that publish integer prices; a source that forces float
   conversion costs exactness for nothing.
3. Record the source on every candle (`domain.Candle.Source`) so a result can
   always be traced to the feed that produced it.
4. A study's usable window is bounded by its **confirmation** timeframe
   coverage, not by the setup timeframe's.
5. Never infer quote precision from a symbol string. Verify it against the
   venue and record it in `internal/instrument`, or refuse the instrument.
6. Never infer whether a vendor timestamp is a bar's open or close. Have the
   producer declare both boundaries.
7. When a feed silently yields nothing, suspect an interval-spelling
   mismatch before suspecting the market.
