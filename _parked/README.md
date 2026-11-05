# Parked code

Directories here are **excluded from the Go build**: the toolchain ignores any
path whose name begins with `_`, so `go build ./...`, `go vet ./...` and
`go test ./...` never see them. They remain in version control so nothing is
lost and restoration is a `git mv`.

Code is parked rather than deleted when it represents real, working effort
that is blocked on something external, and keeping it in the active tree would
force the rest of the codebase to carry its abstractions.

## marketdata-manager (was `internal/marketdata/manager` + `.../providers`)

A second market-data abstraction with its own `Candle` type, provider
interface, cache, rate limiter and quota accounting — roughly 5,100 lines
whose only consumer was `marketdata-ctraderfeed`.

It duplicated `internal/feed`, and the duplication was not harmless: three
competing `Candle` types meant three conversions between them, and that is
exactly where a one-bar timestamp shift and a wrong-tick-size defect both
survived unnoticed. The rebuild keeps a single candle type
(`domain.Candle`, integer ticks) and a single feed interface.

## marketdata-ctraderfeed (was `internal/marketdata/ctraderfeed`)

The live cTrader market-data feed. Parked because no broker is connected:
cTrader execution is paused pending a Spotware Open API application that has
not yet been submitted, and real-time feeds are not wired. It is the only
consumer of the manager stack above, so the two are parked together.

The cTrader **broker** package (`internal/broker/ctrader`) stays in the build:
it is OAuth, readiness, evidence and signed-artifact tooling needed to pursue
that approval, it is not duplicated by anything, and it carries its own tests.

## Restoring

```sh
git mv _parked/marketdata-manager internal/marketdata/manager
git mv _parked/marketdata-ctraderfeed internal/marketdata/ctraderfeed
```

Expect compile errors against the current tree: both predate the single-candle
rebuild and still reference `feed.Candle` and the old provider interface.
Porting them onto `domain.Candle` is the work that restoration implies.

## Deleted outright, not parked

`internal/broker/mt4`, `internal/broker/mt5` and `ea/mt5` were unreachable
from every binary. They are recoverable from history at commit `4b1faec`.
