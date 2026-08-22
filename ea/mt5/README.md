# TradexBridge MQL5 Expert Advisor

## What this is

A thin MQL5 Expert Advisor that runs inside MetaTrader 5 and bridges the Tradex
Go control service to the terminal's native order execution API.

The EA **does not make trading decisions**. It receives signed, validated order
intents from the Go service and independently verifies them before calling
`OrderSend()`.

## Architecture

```
┌────────────────────────┐      TCP (outbound)       ┌──────────────────────┐
│  Go control service    │ ◄─────────────────────── │  TradexBridge.mq5    │
│  (Linux/Mac/Windows)   │    signed JSON intents    │  (Windows MT5)       │
│                        │    fills/acks/snapshots    │                      │
│  Listens on port 9100  │                           │  Connects to Go      │
└────────────────────────┘                           └──────────────────────┘
```

The EA connects **outbound** to the Go service. This means:
- The MetaTrader terminal does not need exposed ports
- Firewall rules only need to allow outbound from the Windows VPS
- The Go service controls which account can connect via handshake validation

## Deployment

### Prerequisites

1. **MetaTrader 5** installed on a Windows machine (VPS recommended for 24/5 uptime)
2. **Exness demo account** logged in and connected
3. **Go service** running and listening on a reachable address
4. **Network**: Windows VPS must be able to reach the Go service's IP:port

### Installation

1. Copy `TradexBridge.mq5` to your MT5 data folder:
   ```
   %APPDATA%\MetaQuotes\Terminal\<instance>\MQL5\Experts\TradexBridge.mq5
   ```

2. In MetaTrader 5, open the Navigator panel → Expert Advisors → right-click → Refresh

3. Compile the EA (MetaEditor opens automatically, press F7)

4. Drag `TradexBridge` onto any chart (EURUSD M15 recommended)

5. In the Inputs tab, configure:
   - **InpGoHost**: IP address of the Go service (e.g. `10.0.0.5`)
   - **InpGoPort**: Port the Go service listens on (default `9100`)
   - **InpAccountFilter**: Your Exness account number (for safety)
   - **InpHeartbeatMs**: How often to send keepalives (default 5000ms)
   - **InpMaxOrderSize**: Maximum position size in quantity units

6. Enable:
   - ☑ Allow Algo Trading
   - ☑ Allow DLL imports (not needed currently but may be in future)

7. In Tools → Options → Expert Advisors:
   - ☑ Allow automated trading
   - Add the Go service address to "Allowed URLs" if using WebRequest

### MT5 Terminal Settings

In Tools → Options → Expert Advisors:
- ☑ Allow automated trading
- ☐ Disable automated trading when switching... (uncheck for continuous operation)

### Verifying Connection

When the EA connects successfully, you'll see in the Experts tab:
```
TradexBridge: Connected to Go service at 10.0.0.5:9100
```

If connection fails, it retries every second automatically.

## Safety Features

The EA independently validates every intent:

| Check | What happens on failure |
|-------|------------------------|
| Account ID mismatch | Order rejected immediately |
| Duplicate intent ID | Order rejected (ring buffer of last 256) |
| Invalid/unavailable symbol | Order rejected |
| Quantity exceeds max | Order rejected |
| Stop distance too small | Order rejected |
| Go heartbeat stale | New entries blocked (exits still allowed) |
| Signature missing | Order rejected |
| Intent expired | Not currently checked by EA (Go enforces) |

## Wire Protocol

The EA uses the same binary framing as the Go TCP transport:

```
[4 bytes] payload length (big-endian uint32)
[1 byte]  message type
[N bytes] JSON payload
```

Message types are defined in `protocol.go` on the Go side and as `#define`
constants in the EA.

## Monitoring

- **Experts tab**: Shows connection status, order placements, rejections
- **Journal tab**: Shows MetaTrader-level order execution details
- **Go service logs**: Shows all protocol messages and audit events

## Kill Switch

If the Go heartbeat is stale for more than `InpHeartbeatTimeout` milliseconds,
the EA disconnects and blocks all new entries. It will reconnect automatically
when the Go service is available again.

To manually stop all trading:
1. Remove the EA from the chart, OR
2. Disable AutoTrading in MetaTrader (global button), OR
3. The Go service can close the TCP connection

## Limitations

- JSON parsing is minimal (no external library). Complex nested structures
  are not supported in the current implementation.
- The EA uses `PositionOpen` which is appropriate for hedging accounts.
  For netting accounts, orders may behave differently.
- Magic number 123456 is hardcoded — only Tradex orders are tracked.
- Fill events are not yet streamed asynchronously (future enhancement).
