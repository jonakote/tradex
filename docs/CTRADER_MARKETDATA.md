# cTrader market-data provider

TradeX now exposes a data-only cTrader provider through the central
`MarketDataManager`.

The provider supports:

- broker/account-specific symbol IDs and price digits;
- normalized snapshot bid/ask quotes;
- historical trendbars converted from cTrader low-plus-delta OHLC;
- live quote subscriptions;
- live trendbar subscriptions;
- demo/live configuration separation;
- injected transports for deterministic tests and paper-forward runs.

The provider is implemented in
`internal/marketdata/manager/providers/ctrader.go`. Its `CTraderDataClient`
interface is the only network boundary. The repository now includes a
read-only JSON/TCP implementation in
`internal/marketdata/manager/providers/ctrader_json_tcp.go`; it performs
application authorization, token account lookup, account authorization,
heartbeat, spot subscriptions, historical trendbars, and live trendbar
events. It has no order, cancellation, or account-mutation methods, so adding
it to the manager cannot enable live execution.

The optional `tradex signals` integration is in
`internal/marketdata/ctraderfeed` and is enabled by adding a `ctrader` section
to the signals JSON configuration. It polls normalized cTrader candles into
the existing signal engine; the default signal configuration does not enable
it.

## Configuration

`manager.DefaultManagerConfig()` includes cTrader disabled by default. Enable
it only after a cTrader demo account has been authorized and the transport has
been validated. Register the provider and put `ctrader` first in the relevant
symbol routing chain when it becomes the authoritative feed.

The signal monitor can opt into the direct feed with a `ctrader` section in its
JSON configuration:

```json
{
  "poll_interval": "1m",
  "ctrader": {
    "environment": "demo",
    "account_id": "123456",
    "token_file": "/secure/path/ctrader-token.json",
    "interval": "1h",
    "symbols": [
      {"symbol_id": 1, "symbol": "EUR/USD", "type": "forex", "digits": 5},
      {"symbol_id": 41, "symbol": "XAU/USD", "type": "metal", "digits": 5}
    ]
  }
}
```

`CTRADER_CLIENT_ID` and `CTRADER_CLIENT_SECRET` are required at runtime.
`CTRADER_ACCOUNT_ID`, `CTRADER_ENVIRONMENT`, and `CTRADER_TOKEN_FILE` may also
be supplied through the environment. `CTRADER_REDIRECT_URI` is only needed by
the one-time authorization bootstrap; the running bot does not open a browser
or listen for a callback. When cTrader rotates the refresh token, TradeX
atomically replaces the token file with mode `0600`.

## Terminal-friendly authorization

cTrader requires a redirect URI for the initial user consent step, but that
step happens once during provisioning. Register a loopback URI such as
`http://127.0.0.1:8787/oauth/callback` in the cTrader application, set the
client credentials and URI in the provisioning shell, then run:

```sh
tradex ctrader-oauth-bootstrap /secure/tradex/ctrader-token.json
```

The command prints one authorization URL, waits on the local loopback callback,
exchanges the short-lived authorization code, and stores the access/refresh
tokens without printing them. It refuses to overwrite an existing token file.
After that, the unattended bot only needs the client ID, client secret, and
token-file path:

```sh
export CTRADER_CLIENT_ID=...
export CTRADER_CLIENT_SECRET=...
export CTRADER_TOKEN_FILE=/secure/tradex/ctrader-token.json
tradex signals config.json
```

Use the `accounts` scope for read-only market data. Do not grant the `trading`
scope until a separately approved execution integration exists. If the
provisioning host is remote, use an SSH port forward for the registered
loopback port; the bot itself still remains browser- and redirect-free.

Symbol IDs are account/server specific. They must be obtained from cTrader
symbol metadata and frozen with the deployment configuration; TradeX does not
guess an ID from a display name.

## Remaining connectivity work

The JSON/TCP transport is wired into the opt-in signal CLI but is not enabled
by default or for a broker account yet. The remaining validation work is:

1. validate the terminal bootstrap, token file, and refresh path with the approved Pepperstone demo
   account;
2. validate the Pepperstone demo endpoint and account authorization with a
   real, operator-approved demo account;
3. discover and freeze cTrader symbol metadata, including digits;
4. add reconnect/resubscribe orchestration and raw-event persistence;
5. compare live demo events against replay/paper fixtures before enabling the
   provider in a deployment.

No credentials belong in source, manifests, fixtures, or MCP artifacts. No
live order path is enabled by this provider.
