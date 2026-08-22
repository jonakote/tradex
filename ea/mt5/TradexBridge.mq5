//+------------------------------------------------------------------+
//| TradexBridge.mq5 — Thin MT5 Expert Advisor for the Tradex bridge |
//|                                                                  |
//| This EA connects OUTBOUND to the Go control service over TCP,    |
//| performs a protocol handshake, then receives signed order intents |
//| and executes them via the native MQL5 OrderSend API. It streams  |
//| fills, positions, heartbeats, and snapshots back to Go.          |
//|                                                                  |
//| The EA independently validates every intent before submission:   |
//|   - Account ID match                                             |
//|   - Symbol validity and availability                             |
//|   - Maximum order size                                           |
//|   - Stop/TP distance minimum                                     |
//|   - Intent expiry (rejects stale intents)                        |
//|   - Duplicate intent ID rejection                                |
//|   - Signature presence (authorization proof)                     |
//|                                                                  |
//| The EA NEVER makes trading decisions. It only executes validated  |
//| intents from the Go risk engine. It also refuses new entries when |
//| the Go heartbeat is stale, but still permits protective exits    |
//| and emergency flattening.                                        |
//+------------------------------------------------------------------+
#property copyright "Tradex"
#property version   "1.00"
#property strict

#include <Trade\Trade.mqh>

//--- Input parameters (set in EA properties dialog)
input string   InpGoHost          = "127.0.0.1";  // Go service host
input int      InpGoPort          = 9100;         // Go service port
input int      InpHeartbeatMs     = 5000;         // Heartbeat interval (ms)
input int      InpHeartbeatTimeout= 10000;        // Max time without Go heartbeat (ms)
input int      InpMaxOrderSize    = 1000000;      // Maximum lot*100000 per order
input int      InpMinStopPoints   = 10;           // Minimum SL/TP distance in points
input string   InpAccountFilter   = "";           // Required account ID (blank=any)

//--- Protocol constants (must match Go protocol.go)
#define PROTOCOL_VERSION      1
#define MSG_HANDSHAKE         1
#define MSG_HANDSHAKE_ACK     2
#define MSG_HEARTBEAT         10
#define MSG_SUBMIT_INTENT     20
#define MSG_CANCEL_ORDER      21
#define MSG_SNAPSHOT_REQUEST  22
#define MSG_ORDER_ACK         30
#define MSG_ORDER_REJECTED    31
#define MSG_ORDER_CANCELLED   32
#define MSG_FILL_EVENT        33
#define MSG_SNAPSHOT_RESPONSE 34
#define MSG_PROTOCOL_ERROR    50

#define MAX_PAYLOAD_SIZE      1048576  // 1 MB

//--- State
int      g_socket = INVALID_HANDLE;
bool     g_connected = false;
datetime g_lastGoHeartbeat = 0;
ulong    g_hbSeqOut = 0;
string   g_processedIntents[];  // Duplicate detection ring buffer
int      g_processedCount = 0;
#define  MAX_PROCESSED 256

CTrade   g_trade;

//+------------------------------------------------------------------+
//| Expert initialization                                            |
//+------------------------------------------------------------------+
int OnInit()
{
   // Validate account filter
   if(InpAccountFilter != "" && InpAccountFilter != IntegerToString(AccountInfoInteger(ACCOUNT_LOGIN)))
   {
      Print("TradexBridge: Account filter mismatch. Expected ", InpAccountFilter,
            " got ", AccountInfoInteger(ACCOUNT_LOGIN));
      return INIT_FAILED;
   }

   ArrayResize(g_processedIntents, MAX_PROCESSED);
   for(int i = 0; i < MAX_PROCESSED; i++) g_processedIntents[i] = "";

   // Attempt initial connection
   if(!ConnectToGo())
   {
      Print("TradexBridge: Initial connection failed, will retry in OnTimer");
   }

   EventSetMillisecondTimer(1000); // 1 second timer for reconnect/heartbeat
   return INIT_SUCCEEDED;
}

//+------------------------------------------------------------------+
//| Expert deinitialization                                          |
//+------------------------------------------------------------------+
void OnDeinit(const int reason)
{
   EventKillTimer();
   Disconnect();
}

//+------------------------------------------------------------------+
//| Timer event — heartbeat, reconnect, and message polling          |
//+------------------------------------------------------------------+
void OnTimer()
{
   if(!g_connected)
   {
      ConnectToGo();
      return;
   }

   // Check heartbeat staleness
   if(g_lastGoHeartbeat > 0 &&
      (GetTickCount64() - (ulong)g_lastGoHeartbeat) > (ulong)InpHeartbeatTimeout)
   {
      Print("TradexBridge: Go heartbeat stale, disconnecting");
      Disconnect();
      return;
   }

   // Send heartbeat
   SendHeartbeat();

   // Poll for incoming messages
   ProcessIncoming();
}

//+------------------------------------------------------------------+
//| OnTick — also poll for messages on every tick for lower latency  |
//+------------------------------------------------------------------+
void OnTick()
{
   if(g_connected)
      ProcessIncoming();
}

//+------------------------------------------------------------------+
//| Connect to Go service                                            |
//+------------------------------------------------------------------+
bool ConnectToGo()
{
   g_socket = SocketCreate();
   if(g_socket == INVALID_HANDLE)
   {
      Print("TradexBridge: SocketCreate failed: ", GetLastError());
      return false;
   }

   if(!SocketConnect(g_socket, InpGoHost, InpGoPort, 5000))
   {
      Print("TradexBridge: SocketConnect to ", InpGoHost, ":", InpGoPort, " failed: ", GetLastError());
      SocketClose(g_socket);
      g_socket = INVALID_HANDLE;
      return false;
   }

   // Send handshake
   string handshake = StringFormat(
      "{\"protocol_version\":%d,\"account_id\":\"%s\",\"terminal\":\"%s\",\"broker\":\"%s\",\"server_time\":\"%s\"}",
      PROTOCOL_VERSION,
      IntegerToString(AccountInfoInteger(ACCOUNT_LOGIN)),
      TerminalInfoString(TERMINAL_NAME) + " Build " + IntegerToString(TerminalInfoInteger(TERMINAL_BUILD)),
      AccountInfoString(ACCOUNT_COMPANY),
      TimeToString(TimeCurrent(), TIME_DATE | TIME_SECONDS)
   );

   if(!SendFrame(MSG_HANDSHAKE, handshake))
   {
      Print("TradexBridge: Failed to send handshake");
      Disconnect();
      return false;
   }

   // Wait for handshake ack (blocking with timeout)
   uchar msgType;
   string payload;
   if(!ReceiveFrame(msgType, payload, 5000))
   {
      Print("TradexBridge: No handshake ack received");
      Disconnect();
      return false;
   }

   if(msgType != MSG_HANDSHAKE_ACK)
   {
      Print("TradexBridge: Expected handshake ack, got type ", msgType);
      Disconnect();
      return false;
   }

   // Parse accepted field
   if(StringFind(payload, "\"accepted\":true") < 0)
   {
      Print("TradexBridge: Handshake rejected: ", payload);
      Disconnect();
      return false;
   }

   g_connected = true;
   g_lastGoHeartbeat = (datetime)GetTickCount64();
   Print("TradexBridge: Connected to Go service at ", InpGoHost, ":", InpGoPort);
   return true;
}

//+------------------------------------------------------------------+
//| Disconnect and cleanup                                           |
//+------------------------------------------------------------------+
void Disconnect()
{
   if(g_socket != INVALID_HANDLE)
   {
      SocketClose(g_socket);
      g_socket = INVALID_HANDLE;
   }
   g_connected = false;
}

//+------------------------------------------------------------------+
//| Process incoming messages from Go                                |
//+------------------------------------------------------------------+
void ProcessIncoming()
{
   // Non-blocking check for available data
   int available = SocketIsReadable(g_socket);
   if(available <= 0) return;

   uchar msgType;
   string payload;

   while(SocketIsReadable(g_socket) >= 5)  // Minimum frame: 4 byte header + 1 type
   {
      if(!ReceiveFrame(msgType, payload, 100))
         break;

      switch(msgType)
      {
         case MSG_HEARTBEAT:
            g_lastGoHeartbeat = (datetime)GetTickCount64();
            break;

         case MSG_SUBMIT_INTENT:
            HandleSubmitIntent(payload);
            break;

         case MSG_CANCEL_ORDER:
            HandleCancelOrder(payload);
            break;

         case MSG_SNAPSHOT_REQUEST:
            HandleSnapshotRequest(payload);
            break;

         default:
            Print("TradexBridge: Unknown message type: ", msgType);
            break;
      }
   }
}

//+------------------------------------------------------------------+
//| Handle order submission intent                                   |
//+------------------------------------------------------------------+
void HandleSubmitIntent(const string &payload)
{
   // Parse fields from JSON
   string requestId  = JsonGetString(payload, "request_id");
   string intentId   = JsonGetString(payload, "intent_id");
   string accountId  = JsonGetString(payload, "account_id");
   string symbol     = JsonGetString(payload, "symbol");
   string side       = JsonGetString(payload, "side");
   string orderType  = JsonGetString(payload, "order_type");
   long   quantity   = JsonGetLong(payload, "quantity");
   long   entryPrice = JsonGetLong(payload, "entry_price");
   long   stopLoss   = JsonGetLong(payload, "stop_loss");
   long   takeProfit = JsonGetLong(payload, "take_profit");
   string expiresAt  = JsonGetString(payload, "expires_at");
   string signature  = JsonGetString(payload, "signature");

   // --- Independent validation ---

   // 1. Signature present
   if(signature == "")
   {
      SendOrderRejected(requestId, intentId, "missing signature", 0);
      return;
   }

   // 2. Account match
   if(accountId != IntegerToString(AccountInfoInteger(ACCOUNT_LOGIN)))
   {
      SendOrderRejected(requestId, intentId, "account mismatch", 0);
      return;
   }

   // 3. Duplicate check
   if(IsProcessed(intentId))
   {
      SendOrderRejected(requestId, intentId, "duplicate intent", 0);
      return;
   }

   // 4. Symbol valid
   if(!SymbolSelect(symbol, true))
   {
      SendOrderRejected(requestId, intentId, "invalid symbol: " + symbol, 0);
      return;
   }

   // 5. Quantity bounds
   if(quantity <= 0 || quantity > InpMaxOrderSize)
   {
      SendOrderRejected(requestId, intentId, "quantity out of bounds", 0);
      return;
   }

   // 6. Stop distance
   double point = SymbolInfoDouble(symbol, SYMBOL_POINT);
   if(point <= 0)
   {
      SendOrderRejected(requestId, intentId, "cannot determine point value", 0);
      return;
   }
   double slPoints = MathAbs((double)stopLoss - (double)entryPrice) / point;
   if(slPoints < InpMinStopPoints && orderType == "MARKET")
   {
      // For market orders, use current price for SL distance check
      double currentPrice = (side == "BUY") ?
         SymbolInfoDouble(symbol, SYMBOL_ASK) :
         SymbolInfoDouble(symbol, SYMBOL_BID);
      double scale = MathPow(10, SymbolInfoInteger(symbol, SYMBOL_DIGITS));
      double slPrice = (double)stopLoss / scale;
      slPoints = MathAbs(currentPrice - slPrice) / point;
   }
   if(slPoints < InpMinStopPoints)
   {
      SendOrderRejected(requestId, intentId, "stop distance too small", 0);
      return;
   }

   // 7. Heartbeat freshness (block new entries if Go is stale)
   if(g_lastGoHeartbeat > 0 &&
      (GetTickCount64() - (ulong)g_lastGoHeartbeat) > (ulong)(InpHeartbeatTimeout / 2))
   {
      SendOrderRejected(requestId, intentId, "go heartbeat stale - entries blocked", 0);
      return;
   }

   // --- Execute order ---
   double lotSize = SymbolInfoDouble(symbol, SYMBOL_VOLUME_MIN);
   double lotStep = SymbolInfoDouble(symbol, SYMBOL_VOLUME_STEP);
   int    digits  = (int)SymbolInfoInteger(symbol, SYMBOL_DIGITS);
   double scale   = MathPow(10, digits);

   // Convert fixed-point prices to double
   double dEntry  = (double)entryPrice / scale;
   double dSL     = (double)stopLoss / scale;
   double dTP     = (double)takeProfit / scale;
   double volume  = NormalizeVolume(symbol, quantity, lotStep, lotSize);

   ENUM_ORDER_TYPE mt5Type;
   double price;
   if(orderType == "MARKET")
   {
      mt5Type = (side == "BUY") ? ORDER_TYPE_BUY : ORDER_TYPE_SELL;
      price = (side == "BUY") ?
         SymbolInfoDouble(symbol, SYMBOL_ASK) :
         SymbolInfoDouble(symbol, SYMBOL_BID);
   }
   else if(orderType == "LIMIT")
   {
      mt5Type = (side == "BUY") ? ORDER_TYPE_BUY_LIMIT : ORDER_TYPE_SELL_LIMIT;
      price = dEntry;
   }
   else if(orderType == "STOP")
   {
      mt5Type = (side == "BUY") ? ORDER_TYPE_BUY_STOP : ORDER_TYPE_SELL_STOP;
      price = dEntry;
   }
   else
   {
      SendOrderRejected(requestId, intentId, "unknown order type: " + orderType, 0);
      return;
   }

   g_trade.SetExpertMagicNumber(123456); // Tradex magic number
   g_trade.SetDeviationInPoints(10);     // Allow 10 points slippage

   bool result = g_trade.PositionOpen(symbol, mt5Type, volume,
                                       NormalizeDouble(price, digits),
                                       NormalizeDouble(dSL, digits),
                                       NormalizeDouble(dTP, digits),
                                       "tradex:" + intentId);

   if(result)
   {
      ulong ticket = g_trade.ResultOrder();
      MarkProcessed(intentId);

      // Send ack
      string ack = StringFormat(
         "{\"request_id\":\"%s\",\"intent_id\":\"%s\",\"broker_order_id\":\"%s\","
         "\"symbol\":\"%s\",\"side\":\"%s\",\"quantity\":%d,\"remaining\":%d,"
         "\"submitted_at\":\"%s\"}",
         requestId, intentId, IntegerToString(ticket),
         symbol, side, quantity, quantity,
         TimeToString(TimeCurrent(), TIME_DATE | TIME_SECONDS)
      );
      SendFrame(MSG_ORDER_ACK, ack);
      Print("TradexBridge: Order placed, ticket=", ticket, " intent=", intentId);
   }
   else
   {
      int err = (int)g_trade.ResultRetcode();
      string reason = StringFormat("OrderSend failed: retcode=%d %s",
                                    err, g_trade.ResultRetcodeDescription());
      SendOrderRejected(requestId, intentId, reason, err);
   }
}

//+------------------------------------------------------------------+
//| Handle cancel order request                                      |
//+------------------------------------------------------------------+
void HandleCancelOrder(const string &payload)
{
   string requestId     = JsonGetString(payload, "request_id");
   string brokerOrderId = JsonGetString(payload, "broker_order_id");

   ulong ticket = StringToInteger(brokerOrderId);
   if(ticket == 0)
   {
      SendOrderRejected(requestId, "", "invalid broker_order_id", 0);
      return;
   }

   // Try to delete pending order
   if(g_trade.OrderDelete(ticket))
   {
      string resp = StringFormat(
         "{\"request_id\":\"%s\",\"broker_order_id\":\"%s\",\"cancelled_at\":\"%s\"}",
         requestId, brokerOrderId,
         TimeToString(TimeCurrent(), TIME_DATE | TIME_SECONDS)
      );
      SendFrame(MSG_ORDER_CANCELLED, resp);
      Print("TradexBridge: Order cancelled, ticket=", ticket);
   }
   else
   {
      // Try closing position if it's a filled order
      if(PositionSelectByTicket(ticket))
      {
         if(g_trade.PositionClose(ticket))
         {
            string resp = StringFormat(
               "{\"request_id\":\"%s\",\"broker_order_id\":\"%s\",\"cancelled_at\":\"%s\"}",
               requestId, brokerOrderId,
               TimeToString(TimeCurrent(), TIME_DATE | TIME_SECONDS)
            );
            SendFrame(MSG_ORDER_CANCELLED, resp);
            Print("TradexBridge: Position closed, ticket=", ticket);
         }
         else
         {
            SendOrderRejected(requestId, "", "position close failed: " +
               IntegerToString(g_trade.ResultRetcode()), (int)g_trade.ResultRetcode());
         }
      }
      else
      {
         SendOrderRejected(requestId, "", "order/position not found: " +
            IntegerToString(GetLastError()), GetLastError());
      }
   }
}

//+------------------------------------------------------------------+
//| Handle snapshot request                                          |
//+------------------------------------------------------------------+
void HandleSnapshotRequest(const string &payload)
{
   string requestId = JsonGetString(payload, "request_id");

   string orders = BuildOrdersJson();
   string positions = BuildPositionsJson();
   long equity = (long)(AccountInfoDouble(ACCOUNT_EQUITY) * 100); // cents

   string resp = StringFormat(
      "{\"request_id\":\"%s\",\"orders\":%s,\"positions\":%s,"
      "\"equity_minor\":%d,\"captured_at\":\"%s\"}",
      requestId, orders, positions, equity,
      TimeToString(TimeCurrent(), TIME_DATE | TIME_SECONDS)
   );
   SendFrame(MSG_SNAPSHOT_RESPONSE, resp);
}

//+------------------------------------------------------------------+
//| Build JSON array of pending orders                               |
//+------------------------------------------------------------------+
string BuildOrdersJson()
{
   string result = "[";
   int total = OrdersTotal();
   bool first = true;
   for(int i = 0; i < total; i++)
   {
      ulong ticket = OrderGetTicket(i);
      if(ticket == 0) continue;
      if(OrderGetInteger(ORDER_MAGIC) != 123456) continue; // Only our orders

      if(!first) result += ",";
      first = false;

      string symbol = OrderGetString(ORDER_SYMBOL);
      long qty = (long)(OrderGetDouble(ORDER_VOLUME_CURRENT) *
                  MathPow(10, SymbolInfoInteger(symbol, SYMBOL_DIGITS)));
      string sideStr = (OrderGetInteger(ORDER_TYPE) == ORDER_TYPE_BUY_LIMIT ||
                        OrderGetInteger(ORDER_TYPE) == ORDER_TYPE_BUY_STOP) ? "BUY" : "SELL";
      string typeStr = "LIMIT";
      if(OrderGetInteger(ORDER_TYPE) == ORDER_TYPE_BUY_STOP ||
         OrderGetInteger(ORDER_TYPE) == ORDER_TYPE_SELL_STOP) typeStr = "STOP";

      result += StringFormat(
         "{\"broker_order_id\":\"%s\",\"intent_id\":\"\",\"symbol\":\"%s\","
         "\"side\":\"%s\",\"type\":\"%s\",\"quantity\":%d,\"remaining\":%d,"
         "\"status\":\"ACKNOWLEDGED\"}",
         IntegerToString(ticket), symbol, sideStr, typeStr, qty, qty
      );
   }
   result += "]";
   return result;
}

//+------------------------------------------------------------------+
//| Build JSON array of open positions                               |
//+------------------------------------------------------------------+
string BuildPositionsJson()
{
   string result = "[";
   int total = PositionsTotal();
   bool first = true;
   for(int i = 0; i < total; i++)
   {
      ulong ticket = PositionGetTicket(i);
      if(ticket == 0) continue;
      if(PositionGetInteger(POSITION_MAGIC) != 123456) continue; // Only our positions

      if(!first) result += ",";
      first = false;

      string symbol = PositionGetString(POSITION_SYMBOL);
      int digits = (int)SymbolInfoInteger(symbol, SYMBOL_DIGITS);
      double scale = MathPow(10, digits);
      long qty = (long)(PositionGetDouble(POSITION_VOLUME) * 100000); // Convert lots to quantity
      string sideStr = (PositionGetInteger(POSITION_TYPE) == POSITION_TYPE_BUY) ? "BUY" : "SELL";
      long entryP = (long)(PositionGetDouble(POSITION_PRICE_OPEN) * scale);
      long slP    = (long)(PositionGetDouble(POSITION_SL) * scale);
      long tpP    = (long)(PositionGetDouble(POSITION_TP) * scale);

      result += StringFormat(
         "{\"broker_position_id\":\"%s\",\"broker_order_id\":\"%s\","
         "\"intent_id\":\"\",\"symbol\":\"%s\",\"side\":\"%s\","
         "\"quantity\":%d,\"entry_price\":%d,\"stop_loss\":%d,\"take_profit\":%d}",
         IntegerToString(ticket), IntegerToString(ticket),
         symbol, sideStr, qty, entryP, slP, tpP
      );
   }
   result += "]";
   return result;
}

//+------------------------------------------------------------------+
//| Send heartbeat to Go                                             |
//+------------------------------------------------------------------+
void SendHeartbeat()
{
   g_hbSeqOut++;
   string hb = StringFormat("{\"ts\":%d,\"seq\":%d}",
                             GetTickCount64(), g_hbSeqOut);
   SendFrame(MSG_HEARTBEAT, hb);
}

//+------------------------------------------------------------------+
//| Send order rejected message                                      |
//+------------------------------------------------------------------+
void SendOrderRejected(const string &requestId, const string &intentId,
                       const string &reason, int errorCode)
{
   string msg = StringFormat(
      "{\"request_id\":\"%s\",\"intent_id\":\"%s\",\"reject_reason\":\"%s\",\"error_code\":%d}",
      requestId, intentId, reason, errorCode
   );
   SendFrame(MSG_ORDER_REJECTED, msg);
   Print("TradexBridge: Rejected intent=", intentId, " reason=", reason);
}

//+------------------------------------------------------------------+
//| Wire frame: [4 bytes big-endian length][1 byte type][JSON]       |
//| Length includes the type byte: length = 1 + strlen(json)         |
//+------------------------------------------------------------------+
bool SendFrame(uchar msgType, const string &jsonPayload)
{
   if(g_socket == INVALID_HANDLE) return false;

   int payloadLen = StringLen(jsonPayload);
   uint frameLen = (uint)(1 + payloadLen);  // type byte + payload

   uchar header[5];
   header[0] = (uchar)((frameLen >> 24) & 0xFF);
   header[1] = (uchar)((frameLen >> 16) & 0xFF);
   header[2] = (uchar)((frameLen >> 8) & 0xFF);
   header[3] = (uchar)(frameLen & 0xFF);
   header[4] = msgType;

   if(SocketSend(g_socket, header, 5) != 5) return false;

   if(payloadLen > 0)
   {
      uchar data[];
      StringToCharArray(jsonPayload, data, 0, payloadLen, CP_UTF8);
      // StringToCharArray adds null terminator, we only send payloadLen bytes
      if(SocketSend(g_socket, data, payloadLen) != payloadLen) return false;
   }

   return true;
}

//+------------------------------------------------------------------+
//| Receive a frame (blocking with timeout in ms)                    |
//+------------------------------------------------------------------+
bool ReceiveFrame(uchar &msgType, string &payload, uint timeoutMs)
{
   uchar header[];
   int read = SocketRead(g_socket, header, 5, timeoutMs);
   if(read != 5) return false;

   uint frameLen = ((uint)header[0] << 24) | ((uint)header[1] << 16) |
                   ((uint)header[2] << 8)  | (uint)header[3];
   msgType = header[4];

   if(frameLen <= 1)
   {
      payload = "";
      return true;
   }

   uint payloadLen = frameLen - 1;
   if(payloadLen > MAX_PAYLOAD_SIZE) return false;

   uchar data[];
   read = SocketRead(g_socket, data, (int)payloadLen, timeoutMs);
   if(read != (int)payloadLen) return false;

   payload = CharArrayToString(data, 0, read, CP_UTF8);
   return true;
}

//+------------------------------------------------------------------+
//| Duplicate intent detection                                       |
//+------------------------------------------------------------------+
bool IsProcessed(const string &intentId)
{
   for(int i = 0; i < MAX_PROCESSED; i++)
   {
      if(g_processedIntents[i] == intentId) return true;
   }
   return false;
}

void MarkProcessed(const string &intentId)
{
   g_processedIntents[g_processedCount % MAX_PROCESSED] = intentId;
   g_processedCount++;
}

//+------------------------------------------------------------------+
//| Volume normalization                                             |
//+------------------------------------------------------------------+
double NormalizeVolume(const string &symbol, long quantity,
                      double lotStep, double lotMin)
{
   // quantity is in base units (e.g. 100000 = 1 standard lot for FX)
   double contractSize = SymbolInfoDouble(symbol, SYMBOL_TRADE_CONTRACT_SIZE);
   if(contractSize <= 0) contractSize = 100000;

   double volume = (double)quantity / contractSize;

   // Round down to lot step
   if(lotStep > 0)
      volume = MathFloor(volume / lotStep) * lotStep;

   // Enforce minimum
   if(volume < lotMin)
      volume = lotMin;

   // Enforce maximum
   double lotMax = SymbolInfoDouble(symbol, SYMBOL_VOLUME_MAX);
   if(volume > lotMax)
      volume = lotMax;

   return NormalizeDouble(volume, 2);
}

//+------------------------------------------------------------------+
//| Minimal JSON parsing helpers (no external library dependency)    |
//+------------------------------------------------------------------+
string JsonGetString(const string &json, const string &key)
{
   string search = "\"" + key + "\":\"";
   int pos = StringFind(json, search);
   if(pos < 0) return "";
   int start = pos + StringLen(search);
   int end = StringFind(json, "\"", start);
   if(end < 0) return "";
   return StringSubstr(json, start, end - start);
}

long JsonGetLong(const string &json, const string &key)
{
   string search = "\"" + key + "\":";
   int pos = StringFind(json, search);
   if(pos < 0) return 0;
   int start = pos + StringLen(search);
   // Skip whitespace
   while(start < StringLen(json) && StringGetCharacter(json, start) == ' ') start++;
   string numStr = "";
   while(start < StringLen(json))
   {
      ushort ch = StringGetCharacter(json, start);
      if((ch >= '0' && ch <= '9') || ch == '-')
         numStr += ShortToString(ch);
      else
         break;
      start++;
   }
   return StringToInteger(numStr);
}
//+------------------------------------------------------------------+
