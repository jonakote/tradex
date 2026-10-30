package providers

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"strconv"
	"sync"
	"time"

	"tradex/internal/broker/ctrader"
)

const (
	ctraderApplicationAuthReq       = 2100
	ctraderApplicationAuthRes       = 2101
	ctraderAccountAuthReq           = 2102
	ctraderAccountAuthRes           = 2103
	ctraderSubscribeSpotsReq        = 2127
	ctraderSubscribeSpotsRes        = 2128
	ctraderSpotEvent                = 2131
	ctraderSubscribeTrendbarReq     = 2135
	ctraderGetTrendbarsReq          = 2137
	ctraderGetTrendbarsRes          = 2138
	ctraderErrorRes                 = 2142
	ctraderAccountsByTokenReq       = 2149
	ctraderAccountsByTokenRes       = 2150
	ctraderSubscribeTrendbarRes     = 2165
	ctraderHeartbeatEvent           = 51
	ctraderDefaultMaxFramePayload   = 8 << 20
	ctraderDefaultRequestTimeout    = 15 * time.Second
	ctraderDefaultHeartbeatInterval = 10 * time.Second
)

// CTraderAccessTokenFunc supplies an OAuth access token without exposing the
// token to configuration files, provider metadata, or logs.
type CTraderAccessTokenFunc func(context.Context) (string, error)

// CTraderJSONTCPConfig configures the read-only JSON/TCP Open API transport.
// Endpoint should be a cTrader host and port such as demo.ctraderapi.com:5036.
type CTraderJSONTCPConfig struct {
	Endpoint        string
	ClientID        string
	ClientSecret    string
	AccountID       string
	AccessToken     CTraderAccessTokenFunc
	DialContext     func(context.Context, string, string) (net.Conn, error)
	RequestTimeout  time.Duration
	HeartbeatPeriod time.Duration
	MaxFramePayload int
}

func (c CTraderJSONTCPConfig) Validate() error {
	if c.Endpoint == "" || c.ClientID == "" || c.ClientSecret == "" || c.AccountID == "" || c.AccessToken == nil {
		return errors.New("cTrader JSON/TCP endpoint, client identity, account, and access-token callback are required")
	}
	if _, err := strconv.ParseInt(c.AccountID, 10, 64); err != nil {
		return errors.New("cTrader JSON/TCP account ID must be an integer")
	}
	if c.RequestTimeout < 0 || c.HeartbeatPeriod < 0 || c.MaxFramePayload < 0 {
		return errors.New("cTrader JSON/TCP timing and frame limits cannot be negative")
	}
	if c.HeartbeatPeriod > 10*time.Second {
		return errors.New("cTrader JSON/TCP heartbeat period cannot exceed 10 seconds")
	}
	return nil
}

// CTraderJSONTCPDataClient implements CTraderDataClient over the documented
// JSON/TCP Open API. It deliberately implements market-data operations only.
type CTraderJSONTCPDataClient struct {
	config CTraderJSONTCPConfig
	clock  func() time.Time

	connectMu sync.Mutex
	writeMu   sync.Mutex
	mu        sync.Mutex
	conn      net.Conn
	done      chan struct{}
	closed    bool
	nextID    uint64
	pending   map[string]chan jsonTCPResponse

	subMu              sync.Mutex
	spotSubscribed     map[int64]bool
	trendbarSubscribed map[trendbarSubscription]bool
	spotHandlers       map[uint64]spotHandler
	trendbarHandlers   map[uint64]trendbarHandler
	spotWaiters        map[int64]map[uint64]chan CTraderSpot
	nextHandlerID      uint64
}

type trendbarSubscription struct {
	symbolID int64
	period   CTraderPeriod
}

type spotHandler struct {
	ids map[int64]struct{}
	fn  func(CTraderSpot) error
}

type trendbarHandler struct {
	sub trendbarSubscription
	fn  func(CTraderTrendbar) error
}

type jsonTCPEnvelope struct {
	ClientMsgID string          `json:"clientMsgId,omitempty"`
	PayloadType int             `json:"payloadType"`
	Payload     json.RawMessage `json:"payload,omitempty"`
}

type jsonTCPResponse struct {
	envelope jsonTCPEnvelope
	err      error
}

func NewCTraderJSONTCPDataClient(config CTraderJSONTCPConfig, clock func() time.Time) (*CTraderJSONTCPDataClient, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if clock == nil {
		clock = time.Now
	}
	if config.RequestTimeout == 0 {
		config.RequestTimeout = ctraderDefaultRequestTimeout
	}
	if config.HeartbeatPeriod == 0 {
		config.HeartbeatPeriod = ctraderDefaultHeartbeatInterval
	}
	if config.MaxFramePayload == 0 {
		config.MaxFramePayload = ctraderDefaultMaxFramePayload
	}
	return &CTraderJSONTCPDataClient{
		config:             config,
		clock:              clock,
		pending:            make(map[string]chan jsonTCPResponse),
		spotSubscribed:     make(map[int64]bool),
		trendbarSubscribed: make(map[trendbarSubscription]bool),
		spotHandlers:       make(map[uint64]spotHandler),
		trendbarHandlers:   make(map[uint64]trendbarHandler),
		spotWaiters:        make(map[int64]map[uint64]chan CTraderSpot),
	}, nil
}

func (c *CTraderJSONTCPDataClient) GetSpot(ctx context.Context, accountID string, symbolID int64) (CTraderSpot, error) {
	if err := c.validateAccount(accountID); err != nil {
		return CTraderSpot{}, err
	}
	if symbolID <= 0 {
		return CTraderSpot{}, errors.New("cTrader spot symbol ID must be positive")
	}
	if err := c.ensureSession(ctx); err != nil {
		return CTraderSpot{}, err
	}
	waiterID, waiter := c.addSpotWaiter(symbolID)
	defer c.removeSpotWaiter(symbolID, waiterID)
	if err := c.ensureSpotSubscription(ctx, []int64{symbolID}); err != nil {
		return CTraderSpot{}, err
	}
	waitCtx, cancel := c.requestContext(ctx)
	defer cancel()
	select {
	case response := <-waiter:
		return response, nil
	case <-waitCtx.Done():
		return CTraderSpot{}, fmt.Errorf("wait for cTrader spot %d: %w", symbolID, waitCtx.Err())
	}
}

func (c *CTraderJSONTCPDataClient) GetTrendbars(ctx context.Context, accountID string, symbolID int64, period CTraderPeriod, from, to time.Time, limit int) ([]CTraderTrendbar, error) {
	if err := c.validateAccount(accountID); err != nil {
		return nil, err
	}
	if symbolID <= 0 {
		return nil, errors.New("cTrader trendbar symbol ID must be positive")
	}
	periodValue, ok := cTraderPeriodValue(period)
	if !ok {
		return nil, fmt.Errorf("unsupported cTrader trendbar period %q", period)
	}
	if !from.IsZero() && !to.IsZero() && !to.After(from) {
		return nil, errors.New("cTrader trendbar end time must be after start time")
	}
	if limit < 0 || limit > math.MaxUint32 {
		return nil, errors.New("cTrader trendbar limit is outside uint32 range")
	}
	if err := c.ensureSession(ctx); err != nil {
		return nil, err
	}
	if err := c.ensureSpotSubscription(ctx, []int64{symbolID}); err != nil {
		return nil, err
	}
	payload := map[string]any{
		"ctidTraderAccountId": mustInt64(c.config.AccountID),
		"period":              periodValue,
		"symbolId":            symbolID,
	}
	if !from.IsZero() {
		payload["fromTimestamp"] = from.UTC().UnixMilli()
	}
	if !to.IsZero() {
		payload["toTimestamp"] = to.UTC().UnixMilli()
	}
	if limit > 0 {
		payload["count"] = limit
	}
	response, err := c.request(ctx, ctraderGetTrendbarsReq, payload, ctraderGetTrendbarsRes)
	if err != nil {
		return nil, err
	}
	var decoded struct {
		AccountID int64           `json:"ctidTraderAccountId"`
		Period    json.RawMessage `json:"period"`
		SymbolID  int64           `json:"symbolId"`
		Trendbars []jsonTrendbar  `json:"trendbar"`
	}
	if err := json.Unmarshal(response.Payload, &decoded); err != nil {
		return nil, fmt.Errorf("decode cTrader trendbar response: %w", err)
	}
	if decoded.AccountID != mustInt64(c.config.AccountID) || (decoded.SymbolID != 0 && decoded.SymbolID != symbolID) {
		return nil, errors.New("cTrader trendbar response account or symbol mismatch")
	}
	result := make([]CTraderTrendbar, 0, len(decoded.Trendbars))
	for _, raw := range decoded.Trendbars {
		bar, err := raw.normalize(symbolID, period)
		if err != nil {
			return nil, err
		}
		result = append(result, bar)
	}
	return result, nil
}

func (c *CTraderJSONTCPDataClient) SubscribeSpots(ctx context.Context, accountID string, symbolIDs []int64, handler func(CTraderSpot) error) error {
	if err := c.validateAccount(accountID); err != nil {
		return err
	}
	if handler == nil {
		return errors.New("cTrader spot handler is required")
	}
	if len(symbolIDs) == 0 {
		return errors.New("cTrader spot subscription requires symbols")
	}
	if err := c.ensureSession(ctx); err != nil {
		return err
	}
	ids := make(map[int64]struct{}, len(symbolIDs))
	for _, symbolID := range symbolIDs {
		if symbolID <= 0 {
			return errors.New("cTrader spot symbol IDs must be positive")
		}
		ids[symbolID] = struct{}{}
	}
	handlerID := c.addSpotHandler(ids, handler)
	defer c.removeSpotHandler(handlerID)
	if err := c.ensureSpotSubscription(ctx, symbolIDs); err != nil {
		return err
	}
	return c.waitForSubscription(ctx)
}

func (c *CTraderJSONTCPDataClient) SubscribeTrendbars(ctx context.Context, accountID string, symbolID int64, period CTraderPeriod, handler func(CTraderTrendbar) error) error {
	if err := c.validateAccount(accountID); err != nil {
		return err
	}
	if handler == nil {
		return errors.New("cTrader trendbar handler is required")
	}
	periodValue, ok := cTraderPeriodValue(period)
	if !ok {
		return fmt.Errorf("unsupported cTrader trendbar period %q", period)
	}
	if symbolID <= 0 {
		return errors.New("cTrader trendbar symbol ID must be positive")
	}
	if err := c.ensureSession(ctx); err != nil {
		return err
	}
	if err := c.ensureSpotSubscription(ctx, []int64{symbolID}); err != nil {
		return err
	}
	subscription := trendbarSubscription{symbolID: symbolID, period: period}
	handlerID, err := c.addTrendbarHandler(subscription, handler)
	if err != nil {
		return err
	}
	defer c.removeTrendbarHandler(handlerID)
	c.subMu.Lock()
	alreadySubscribed := c.trendbarSubscribed[subscription]
	c.subMu.Unlock()
	if !alreadySubscribed {
		payload := map[string]any{
			"ctidTraderAccountId": mustInt64(c.config.AccountID),
			"period":              periodValue,
			"symbolId":            symbolID,
		}
		if _, err := c.request(ctx, ctraderSubscribeTrendbarReq, payload, ctraderSubscribeTrendbarRes); err != nil {
			return err
		}
		c.subMu.Lock()
		c.trendbarSubscribed[subscription] = true
		c.subMu.Unlock()
	}
	return c.waitForSubscription(ctx)
}

// Close terminates the current connection and wakes all pending requests.
// A network failure allows a later data call to establish a fresh connection
// and repeat read-only authorization/subscription setup. Explicit Close is
// permanent.
func (c *CTraderJSONTCPDataClient) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	c.closeConnection(errors.New("cTrader JSON/TCP client closed"))
	return nil
}

func (c *CTraderJSONTCPDataClient) ensureSession(ctx context.Context) error {
	if ctx == nil {
		return errors.New("cTrader JSON/TCP context is required")
	}
	c.connectMu.Lock()
	defer c.connectMu.Unlock()
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errors.New("cTrader JSON/TCP client is closed")
	}
	if c.conn != nil {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	conn, err := c.dial(ctx)
	if err != nil {
		return fmt.Errorf("dial cTrader JSON/TCP endpoint: %w", err)
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = conn.Close()
		return errors.New("cTrader JSON/TCP client is closed")
	}
	c.conn = conn
	c.done = make(chan struct{})
	c.pending = make(map[string]chan jsonTCPResponse)
	done := c.done
	c.mu.Unlock()
	go c.readLoop(conn, done)
	go c.heartbeatLoop(conn, done)

	if err := c.authenticate(ctx); err != nil {
		c.closeConnection(err)
		return err
	}
	return nil
}

func (c *CTraderJSONTCPDataClient) authenticate(ctx context.Context) error {
	accessToken, err := c.config.AccessToken(ctx)
	if err != nil {
		return fmt.Errorf("load cTrader access token: %w", err)
	}
	if accessToken == "" {
		return errors.New("cTrader access token is empty")
	}
	if _, err := c.request(ctx, ctraderApplicationAuthReq, map[string]any{
		"clientId":     c.config.ClientID,
		"clientSecret": c.config.ClientSecret,
	}, ctraderApplicationAuthRes); err != nil {
		return fmt.Errorf("cTrader application authorization: %w", err)
	}
	accountsResponse, err := c.request(ctx, ctraderAccountsByTokenReq, map[string]any{
		"accessToken": accessToken,
	}, ctraderAccountsByTokenRes)
	if err != nil {
		return fmt.Errorf("cTrader account list: %w", err)
	}
	var accounts struct {
		Accounts []struct {
			AccountID int64 `json:"ctidTraderAccountId"`
		} `json:"ctidTraderAccount"`
	}
	if err := json.Unmarshal(accountsResponse.Payload, &accounts); err != nil {
		return fmt.Errorf("decode cTrader account list: %w", err)
	}
	wanted := mustInt64(c.config.AccountID)
	found := false
	for _, account := range accounts.Accounts {
		if account.AccountID == wanted {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("cTrader account %s was not granted to the access token", c.config.AccountID)
	}
	if _, err := c.request(ctx, ctraderAccountAuthReq, map[string]any{
		"ctidTraderAccountId": wanted,
		"accessToken":         accessToken,
	}, ctraderAccountAuthRes); err != nil {
		return fmt.Errorf("cTrader account authorization: %w", err)
	}
	return nil
}

func (c *CTraderJSONTCPDataClient) dial(ctx context.Context) (net.Conn, error) {
	if c.config.DialContext != nil {
		return c.config.DialContext(ctx, "tcp", c.config.Endpoint)
	}
	host, _, err := net.SplitHostPort(c.config.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid cTrader endpoint %q: %w", c.config.Endpoint, err)
	}
	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: c.config.RequestTimeout},
		Config:    &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host},
	}
	return dialer.DialContext(ctx, "tcp", c.config.Endpoint)
}

func (c *CTraderJSONTCPDataClient) requestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= c.config.RequestTimeout {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, c.config.RequestTimeout)
}

func (c *CTraderJSONTCPDataClient) request(ctx context.Context, payloadType int, payload any, expectedResponse int) (jsonTCPEnvelope, error) {
	requestContext, cancel := c.requestContext(ctx)
	defer cancel()
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return jsonTCPEnvelope{}, fmt.Errorf("encode cTrader payload %d: %w", payloadType, err)
	}
	c.mu.Lock()
	if c.conn == nil {
		c.mu.Unlock()
		return jsonTCPEnvelope{}, errors.New("cTrader JSON/TCP connection is not established")
	}
	c.nextID++
	clientMsgID := fmt.Sprintf("tradex-%08d", c.nextID)
	responseChannel := make(chan jsonTCPResponse, 1)
	c.pending[clientMsgID] = responseChannel
	c.mu.Unlock()
	envelope := jsonTCPEnvelope{ClientMsgID: clientMsgID, PayloadType: payloadType, Payload: payloadBytes}
	if err := c.writeEnvelope(envelope); err != nil {
		c.removePending(clientMsgID)
		return jsonTCPEnvelope{}, err
	}
	select {
	case response := <-responseChannel:
		if response.err != nil {
			return jsonTCPEnvelope{}, response.err
		}
		if response.envelope.PayloadType == ctraderErrorRes {
			return jsonTCPEnvelope{}, decodeCTraderError(response.envelope.Payload)
		}
		if response.envelope.PayloadType != expectedResponse {
			return jsonTCPEnvelope{}, fmt.Errorf("unexpected cTrader response payload type %d, want %d", response.envelope.PayloadType, expectedResponse)
		}
		return response.envelope, nil
	case <-requestContext.Done():
		c.removePending(clientMsgID)
		return jsonTCPEnvelope{}, fmt.Errorf("cTrader request %d: %w", payloadType, requestContext.Err())
	}
}

func (c *CTraderJSONTCPDataClient) writeEnvelope(envelope jsonTCPEnvelope) error {
	data, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("encode cTrader JSON envelope: %w", err)
	}
	frame, err := ctrader.EncodeTCPFrame(data)
	if err != nil {
		return err
	}
	c.mu.Lock()
	conn := c.conn
	done := c.done
	c.mu.Unlock()
	if conn == nil {
		return errors.New("cTrader JSON/TCP connection is not established")
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := conn.Write(frame); err != nil {
		c.closeConnectionFor(conn, done, fmt.Errorf("write cTrader JSON/TCP frame: %w", err))
		return err
	}
	return nil
}

func (c *CTraderJSONTCPDataClient) readLoop(conn net.Conn, done chan struct{}) {
	for {
		payload, err := readTCPFrame(conn, c.config.MaxFramePayload)
		if err != nil {
			c.closeConnectionFor(conn, done, fmt.Errorf("read cTrader JSON/TCP frame: %w", err))
			return
		}
		var envelope jsonTCPEnvelope
		if err := json.Unmarshal(payload, &envelope); err != nil {
			c.closeConnectionFor(conn, done, fmt.Errorf("decode cTrader JSON envelope: %w", err))
			return
		}
		if envelope.PayloadType == ctraderHeartbeatEvent {
			continue
		}
		if envelope.ClientMsgID != "" {
			c.mu.Lock()
			responseChannel := c.pending[envelope.ClientMsgID]
			if responseChannel != nil {
				delete(c.pending, envelope.ClientMsgID)
			}
			c.mu.Unlock()
			if responseChannel != nil {
				responseChannel <- jsonTCPResponse{envelope: envelope}
				continue
			}
		}
		if envelope.PayloadType == ctraderSpotEvent {
			c.dispatchSpotEvent(envelope.Payload)
		}
		select {
		case <-done:
			return
		default:
		}
	}
}

func (c *CTraderJSONTCPDataClient) heartbeatLoop(conn net.Conn, done chan struct{}) {
	ticker := time.NewTicker(c.config.HeartbeatPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			if err := c.writeEnvelope(jsonTCPEnvelope{PayloadType: ctraderHeartbeatEvent, Payload: json.RawMessage(`{}`)}); err != nil {
				c.closeConnectionFor(conn, done, err)
				return
			}
		}
	}
}

func (c *CTraderJSONTCPDataClient) closeConnection(err error) {
	c.closeConnectionFor(nil, nil, err)
}

func (c *CTraderJSONTCPDataClient) closeConnectionFor(expectedConn net.Conn, expectedDone chan struct{}, err error) {
	c.mu.Lock()
	conn := c.conn
	done := c.done
	if expectedConn != nil && (conn != expectedConn || (expectedDone != nil && done != expectedDone)) {
		c.mu.Unlock()
		return
	}
	if conn == nil {
		c.mu.Unlock()
		return
	}
	c.conn = nil
	c.done = nil
	pending := c.pending
	c.pending = make(map[string]chan jsonTCPResponse)
	c.mu.Unlock()
	if done != nil {
		close(done)
	}
	_ = conn.Close()
	for _, channel := range pending {
		channel <- jsonTCPResponse{err: err}
	}
	c.subMu.Lock()
	c.spotSubscribed = make(map[int64]bool)
	c.trendbarSubscribed = make(map[trendbarSubscription]bool)
	c.subMu.Unlock()
}

func (c *CTraderJSONTCPDataClient) removePending(clientMsgID string) {
	c.mu.Lock()
	delete(c.pending, clientMsgID)
	c.mu.Unlock()
}

func (c *CTraderJSONTCPDataClient) validateAccount(accountID string) error {
	if accountID == "" || accountID != c.config.AccountID {
		return fmt.Errorf("cTrader account mismatch: got %q", accountID)
	}
	return nil
}

func (c *CTraderJSONTCPDataClient) waitForSubscription(ctx context.Context) error {
	c.mu.Lock()
	done := c.done
	c.mu.Unlock()
	if done == nil {
		return errors.New("cTrader JSON/TCP connection is not established")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return errors.New("cTrader JSON/TCP connection disconnected")
	}
}

func (c *CTraderJSONTCPDataClient) ensureSpotSubscription(ctx context.Context, symbolIDs []int64) error {
	missing := make([]int64, 0, len(symbolIDs))
	c.subMu.Lock()
	for _, symbolID := range symbolIDs {
		if !c.spotSubscribed[symbolID] {
			missing = append(missing, symbolID)
		}
	}
	c.subMu.Unlock()
	if len(missing) == 0 {
		return nil
	}
	payload := map[string]any{
		"ctidTraderAccountId":      mustInt64(c.config.AccountID),
		"symbolId":                 missing,
		"subscribeToSpotTimestamp": true,
	}
	if _, err := c.request(ctx, ctraderSubscribeSpotsReq, payload, ctraderSubscribeSpotsRes); err != nil {
		return err
	}
	c.subMu.Lock()
	for _, symbolID := range missing {
		c.spotSubscribed[symbolID] = true
	}
	c.subMu.Unlock()
	return nil
}

func (c *CTraderJSONTCPDataClient) addSpotWaiter(symbolID int64) (uint64, chan CTraderSpot) {
	c.subMu.Lock()
	defer c.subMu.Unlock()
	c.nextHandlerID++
	id := c.nextHandlerID
	if c.spotWaiters[symbolID] == nil {
		c.spotWaiters[symbolID] = make(map[uint64]chan CTraderSpot)
	}
	channel := make(chan CTraderSpot, 1)
	c.spotWaiters[symbolID][id] = channel
	return id, channel
}

func (c *CTraderJSONTCPDataClient) removeSpotWaiter(symbolID int64, id uint64) {
	c.subMu.Lock()
	defer c.subMu.Unlock()
	delete(c.spotWaiters[symbolID], id)
	if len(c.spotWaiters[symbolID]) == 0 {
		delete(c.spotWaiters, symbolID)
	}
}

func (c *CTraderJSONTCPDataClient) addSpotHandler(ids map[int64]struct{}, handler func(CTraderSpot) error) uint64 {
	c.subMu.Lock()
	defer c.subMu.Unlock()
	c.nextHandlerID++
	id := c.nextHandlerID
	c.spotHandlers[id] = spotHandler{ids: ids, fn: handler}
	return id
}

func (c *CTraderJSONTCPDataClient) removeSpotHandler(id uint64) {
	c.subMu.Lock()
	delete(c.spotHandlers, id)
	c.subMu.Unlock()
}

func (c *CTraderJSONTCPDataClient) addTrendbarHandler(subscription trendbarSubscription, handler func(CTraderTrendbar) error) (uint64, error) {
	c.subMu.Lock()
	defer c.subMu.Unlock()
	for _, existing := range c.trendbarHandlers {
		if existing.sub == subscription {
			return 0, errors.New("cTrader trendbar subscription already has a handler")
		}
	}
	c.nextHandlerID++
	id := c.nextHandlerID
	c.trendbarHandlers[id] = trendbarHandler{sub: subscription, fn: handler}
	return id, nil
}

func (c *CTraderJSONTCPDataClient) removeTrendbarHandler(id uint64) {
	c.subMu.Lock()
	delete(c.trendbarHandlers, id)
	c.subMu.Unlock()
}

func (c *CTraderJSONTCPDataClient) dispatchSpotEvent(payload json.RawMessage) {
	var event jsonSpotEvent
	if err := json.Unmarshal(payload, &event); err != nil || event.SymbolID <= 0 {
		return
	}
	if event.AccountID != mustInt64(c.config.AccountID) {
		return
	}
	spot, hasSpot, err := event.normalizeSpot()
	if err != nil {
		return
	}
	c.subMu.Lock()
	waiters := make([]chan CTraderSpot, 0, len(c.spotWaiters[event.SymbolID]))
	for _, waiter := range c.spotWaiters[event.SymbolID] {
		waiters = append(waiters, waiter)
	}
	handlers := make([]func(CTraderSpot) error, 0, len(c.spotHandlers))
	for _, handler := range c.spotHandlers {
		if _, ok := handler.ids[event.SymbolID]; ok {
			handlers = append(handlers, handler.fn)
		}
	}
	trendbarHandlers := make([]trendbarHandler, 0, len(c.trendbarHandlers))
	for _, handler := range c.trendbarHandlers {
		trendbarHandlers = append(trendbarHandlers, handler)
	}
	c.subMu.Unlock()
	if hasSpot {
		for _, waiter := range waiters {
			select {
			case waiter <- spot:
			default:
			}
		}
		for _, handler := range handlers {
			_ = handler(spot)
		}
	}
	for _, raw := range event.Trendbars {
		period, err := raw.period()
		if err != nil {
			continue
		}
		bar, err := raw.normalize(event.SymbolID, period)
		if err != nil {
			continue
		}
		for _, handler := range trendbarHandlers {
			if handler.sub.symbolID == bar.SymbolID && handler.sub.period == bar.Period {
				_ = handler.fn(bar)
			}
		}
	}
}

type jsonSpotEvent struct {
	AccountID int64          `json:"ctidTraderAccountId"`
	SymbolID  int64          `json:"symbolId"`
	Bid       *uint64        `json:"bid"`
	Ask       *uint64        `json:"ask"`
	Timestamp int64          `json:"timestamp"`
	Trendbars []jsonTrendbar `json:"trendbar"`
}

func (e jsonSpotEvent) normalizeSpot() (CTraderSpot, bool, error) {
	if e.Bid == nil || e.Ask == nil || *e.Bid == 0 || *e.Ask < *e.Bid || e.Timestamp == 0 {
		return CTraderSpot{}, false, nil
	}
	if *e.Bid > math.MaxInt64 || *e.Ask > math.MaxInt64 {
		return CTraderSpot{}, false, errors.New("cTrader spot price exceeds int64")
	}
	return CTraderSpot{SymbolID: e.SymbolID, Bid: int64(*e.Bid), Ask: int64(*e.Ask), Timestamp: unixTimestamp(e.Timestamp)}, true, nil
}

type jsonTrendbar struct {
	Period             json.RawMessage `json:"period"`
	Volume             int64           `json:"volume"`
	Low                int64           `json:"low"`
	DeltaOpen          uint64          `json:"deltaOpen"`
	DeltaClose         uint64          `json:"deltaClose"`
	DeltaHigh          uint64          `json:"deltaHigh"`
	TimestampInMinutes uint32          `json:"utcTimestampInMinutes"`
}

func (b jsonTrendbar) period() (CTraderPeriod, error) {
	if len(b.Period) == 0 {
		return CTraderPeriodM1, nil
	}
	var value int
	if err := json.Unmarshal(b.Period, &value); err == nil {
		return cTraderPeriodFromValue(value)
	}
	var text string
	if err := json.Unmarshal(b.Period, &text); err == nil {
		period := CTraderPeriod(text)
		if _, ok := cTraderPeriodValue(period); ok {
			return period, nil
		}
	}
	return "", errors.New("invalid cTrader trendbar period")
}

func (b jsonTrendbar) normalize(symbolID int64, fallback CTraderPeriod) (CTraderTrendbar, error) {
	period := fallback
	if len(b.Period) > 0 {
		parsed, err := b.period()
		if err != nil {
			return CTraderTrendbar{}, err
		}
		period = parsed
	}
	if symbolID <= 0 || b.Low <= 0 || b.Volume <= 0 || b.TimestampInMinutes == 0 {
		return CTraderTrendbar{}, errors.New("invalid cTrader trendbar event")
	}
	return CTraderTrendbar{
		SymbolID:   symbolID,
		Period:     period,
		OpenTime:   time.Unix(int64(b.TimestampInMinutes)*60, 0).UTC(),
		Low:        b.Low,
		DeltaOpen:  b.DeltaOpen,
		DeltaHigh:  b.DeltaHigh,
		DeltaClose: b.DeltaClose,
		Volume:     uint64(b.Volume),
	}, nil
}

func cTraderPeriodValue(period CTraderPeriod) (int, bool) {
	switch period {
	case CTraderPeriodM1:
		return 1, true
	case CTraderPeriodM5:
		return 5, true
	case CTraderPeriodM15:
		return 7, true
	case CTraderPeriodM30:
		return 8, true
	case CTraderPeriodH1:
		return 9, true
	case CTraderPeriodH4:
		return 10, true
	case CTraderPeriodD1:
		return 12, true
	case CTraderPeriodW1:
		return 13, true
	default:
		return 0, false
	}
}

func cTraderPeriodFromValue(value int) (CTraderPeriod, error) {
	switch value {
	case 1:
		return CTraderPeriodM1, nil
	case 5:
		return CTraderPeriodM5, nil
	case 7:
		return CTraderPeriodM15, nil
	case 8:
		return CTraderPeriodM30, nil
	case 9:
		return CTraderPeriodH1, nil
	case 10:
		return CTraderPeriodH4, nil
	case 12:
		return CTraderPeriodD1, nil
	case 13:
		return CTraderPeriodW1, nil
	default:
		return "", fmt.Errorf("unsupported cTrader trendbar period value %d", value)
	}
}

func unixTimestamp(value int64) time.Time {
	if value >= 1_000_000_000_000 {
		return time.UnixMilli(value).UTC()
	}
	return time.Unix(value, 0).UTC()
}

func readTCPFrame(reader io.Reader, maxPayload int) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(header[:])
	if length == 0 || int64(length) > int64(maxPayload) {
		return nil, fmt.Errorf("invalid cTrader JSON/TCP payload length %d", length)
	}
	payload := make([]byte, int(length))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func decodeCTraderError(payload json.RawMessage) error {
	var response struct {
		ErrorCode   string `json:"errorCode"`
		Description string `json:"description"`
		RetryAfter  uint64 `json:"retryAfter"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return fmt.Errorf("cTrader error response: %w", err)
	}
	if response.Description == "" {
		return fmt.Errorf("cTrader API error %s", response.ErrorCode)
	}
	if response.RetryAfter > 0 {
		return fmt.Errorf("cTrader API error %s: %s (retry after %ds)", response.ErrorCode, response.Description, response.RetryAfter)
	}
	return fmt.Errorf("cTrader API error %s: %s", response.ErrorCode, response.Description)
}

func mustInt64(value string) int64 {
	parsed, _ := strconv.ParseInt(value, 10, 64)
	return parsed
}

var _ CTraderDataClient = (*CTraderJSONTCPDataClient)(nil)
