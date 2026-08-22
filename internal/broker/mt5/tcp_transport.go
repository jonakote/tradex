package mt5

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"tradex/internal/domain"
)

// TCPTransportConfig controls the behavior of the TCP bridge transport.
type TCPTransportConfig struct {
	// ListenAddr is the address to listen on for EA connections (e.g. ":9100").
	ListenAddr string
	// AccountID is the expected account from the connecting EA.
	AccountID string
	// HeartbeatInterval is how often heartbeats should be exchanged.
	HeartbeatInterval time.Duration
	// HeartbeatTimeout is the maximum time to wait for a heartbeat before
	// declaring the connection dead.
	HeartbeatTimeout time.Duration
	// RequestTimeout is the maximum time to wait for a response to a request.
	RequestTimeout time.Duration
}

// Validate checks required fields and sensible defaults.
func (c TCPTransportConfig) Validate() error {
	if c.ListenAddr == "" {
		return errors.New("MT5 TCP transport requires listen address")
	}
	if c.AccountID == "" {
		return errors.New("MT5 TCP transport requires account ID")
	}
	if c.HeartbeatInterval <= 0 {
		return errors.New("MT5 TCP transport requires positive heartbeat interval")
	}
	if c.HeartbeatTimeout <= c.HeartbeatInterval {
		return errors.New("MT5 TCP heartbeat timeout must exceed interval")
	}
	if c.RequestTimeout <= 0 {
		return errors.New("MT5 TCP transport requires positive request timeout")
	}
	return nil
}

// pendingRequest tracks an outstanding request awaiting a response.
type pendingRequest struct {
	ch   chan Frame
	sent time.Time
}

// TCPTransport implements the Transport interface over a TCP connection to
// the MQL5 EA. The Go service listens; the EA connects outbound.
type TCPTransport struct {
	config   TCPTransportConfig
	listener net.Listener

	mu          sync.Mutex
	conn        net.Conn
	connected   bool
	handshake   *Handshake
	pending     map[string]*pendingRequest
	hbSeq       uint64
	lastHBRecv  time.Time
	closed      bool

	// fillCh receives asynchronous fill events from the EA.
	fillCh chan FillEventMsg

	// done signals the read/write loops to exit.
	done chan struct{}
}

// NewTCPTransport creates a new TCP transport. Call Listen() to start accepting
// connections from the EA.
func NewTCPTransport(cfg TCPTransportConfig) (*TCPTransport, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &TCPTransport{
		config:  cfg,
		pending: make(map[string]*pendingRequest),
		fillCh:  make(chan FillEventMsg, 64),
		done:    make(chan struct{}),
	}, nil
}

// Listen starts the TCP listener. It blocks until an EA connects and
// completes the handshake, or the context is cancelled.
func (t *TCPTransport) Listen(ctx context.Context) error {
	ln, err := net.Listen("tcp", t.config.ListenAddr)
	if err != nil {
		return fmt.Errorf("MT5 TCP listen: %w", err)
	}
	t.mu.Lock()
	t.listener = ln
	t.mu.Unlock()

	return t.acceptLoop(ctx)
}

// ListenOn starts the transport on an existing listener. Used for testing.
func (t *TCPTransport) ListenOn(ctx context.Context, ln net.Listener) error {
	t.mu.Lock()
	t.listener = ln
	t.mu.Unlock()
	return t.acceptLoop(ctx)
}

func (t *TCPTransport) acceptLoop(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.done:
			return nil
		default:
		}

		// Set a deadline so we can check ctx periodically.
		if dl, ok := t.listener.(interface{ SetDeadline(time.Time) error }); ok {
			_ = dl.SetDeadline(time.Now().Add(time.Second))
		}

		conn, err := t.listener.Accept()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			select {
			case <-t.done:
				return nil
			default:
			}
			return fmt.Errorf("MT5 TCP accept: %w", err)
		}

		if err := t.handleConnection(ctx, conn); err != nil {
			conn.Close()
			// If context cancelled, stop accepting
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}
		return nil
	}
}

func (t *TCPTransport) handleConnection(ctx context.Context, conn net.Conn) error {
	// Read handshake with timeout
	_ = conn.SetReadDeadline(time.Now().Add(t.config.RequestTimeout))
	frame, err := ReadFrame(conn)
	if err != nil {
		return fmt.Errorf("handshake read: %w", err)
	}
	if frame.Type != MsgHandshake {
		return fmt.Errorf("expected handshake, got %s", frame.Type)
	}
	var hs Handshake
	if err := DecodePayload(frame, &hs); err != nil {
		return err
	}
	if err := hs.Validate(); err != nil {
		// Send rejection
		ack := HandshakeAck{Accepted: false, RejectReason: err.Error()}
		_ = EncodeMessage(conn, MsgHandshakeAck, ack)
		return err
	}
	if hs.AccountID != t.config.AccountID {
		reason := "account ID mismatch"
		ack := HandshakeAck{Accepted: false, RejectReason: reason}
		_ = EncodeMessage(conn, MsgHandshakeAck, ack)
		return errors.New(reason)
	}

	// Accept
	ack := HandshakeAck{
		Accepted:          true,
		HeartbeatInterval: int(t.config.HeartbeatInterval.Milliseconds()),
	}
	_ = conn.SetWriteDeadline(time.Now().Add(t.config.RequestTimeout))
	if err := EncodeMessage(conn, MsgHandshakeAck, ack); err != nil {
		return fmt.Errorf("handshake ack write: %w", err)
	}

	// Clear deadlines for normal operation
	_ = conn.SetReadDeadline(time.Time{})
	_ = conn.SetWriteDeadline(time.Time{})

	t.mu.Lock()
	t.conn = conn
	t.connected = true
	t.handshake = &hs
	t.lastHBRecv = time.Now()
	t.mu.Unlock()

	// Start read loop in background
	go t.readLoop(ctx)
	// Start heartbeat loop in background
	go t.heartbeatLoop(ctx)

	return nil
}

func (t *TCPTransport) readLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.done:
			return
		default:
		}

		t.mu.Lock()
		conn := t.conn
		t.mu.Unlock()
		if conn == nil {
			return
		}

		// Set a read deadline to avoid blocking forever
		_ = conn.SetReadDeadline(time.Now().Add(t.config.HeartbeatTimeout))
		frame, err := ReadFrame(conn)
		if err != nil {
			t.mu.Lock()
			t.connected = false
			t.mu.Unlock()
			// Cancel all pending requests
			t.failAllPending(err)
			return
		}

		switch frame.Type {
		case MsgHeartbeat:
			t.mu.Lock()
			t.lastHBRecv = time.Now()
			t.mu.Unlock()

		case MsgOrderAck, MsgOrderRejected, MsgOrderCancelled, MsgSnapshotResponse:
			t.routeResponse(frame)

		case MsgFillEvent:
			var fill FillEventMsg
			if err := DecodePayload(frame, &fill); err == nil {
				select {
				case t.fillCh <- fill:
				default:
					// Drop if channel full — operator should read fills
				}
			}

		case MsgProtocolError:
			t.mu.Lock()
			t.connected = false
			t.mu.Unlock()
			t.failAllPending(errors.New("protocol error from EA"))
			return
		}
	}
}

func (t *TCPTransport) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(t.config.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.done:
			return
		case <-ticker.C:
			t.mu.Lock()
			if !t.connected {
				t.mu.Unlock()
				return
			}
			t.hbSeq++
			seq := t.hbSeq
			conn := t.conn
			t.mu.Unlock()

			hb := HeartbeatMsg{Timestamp: time.Now().UnixMilli(), SequenceN: seq}
			_ = conn.SetWriteDeadline(time.Now().Add(t.config.HeartbeatInterval))
			if err := EncodeMessage(conn, MsgHeartbeat, hb); err != nil {
				t.mu.Lock()
				t.connected = false
				t.mu.Unlock()
				return
			}
		}
	}
}

func (t *TCPTransport) routeResponse(frame Frame) {
	// Extract request_id from the JSON payload to correlate
	requestID := extractRequestID(frame.Payload)
	if requestID == "" {
		return
	}
	t.mu.Lock()
	req, exists := t.pending[requestID]
	if exists {
		delete(t.pending, requestID)
	}
	t.mu.Unlock()
	if exists {
		select {
		case req.ch <- frame:
		default:
		}
	}
}

func (t *TCPTransport) failAllPending(err error) {
	t.mu.Lock()
	pending := t.pending
	t.pending = make(map[string]*pendingRequest)
	t.mu.Unlock()
	for _, req := range pending {
		close(req.ch)
	}
	_ = err // error logged via audit events in production
}

// sendRequest sends a message and waits for the correlated response.
func (t *TCPTransport) sendRequest(ctx context.Context, requestID string, msgType MessageType, msg any) (Frame, error) {
	t.mu.Lock()
	if !t.connected {
		t.mu.Unlock()
		return Frame{}, errors.New("MT5 transport not connected")
	}
	conn := t.conn
	ch := make(chan Frame, 1)
	t.pending[requestID] = &pendingRequest{ch: ch, sent: time.Now()}
	t.mu.Unlock()

	// Write the request
	_ = conn.SetWriteDeadline(time.Now().Add(t.config.RequestTimeout))
	if err := EncodeMessage(conn, msgType, msg); err != nil {
		t.mu.Lock()
		delete(t.pending, requestID)
		t.mu.Unlock()
		return Frame{}, fmt.Errorf("MT5 send %s: %w", msgType, err)
	}

	// Wait for response
	select {
	case <-ctx.Done():
		t.mu.Lock()
		delete(t.pending, requestID)
		t.mu.Unlock()
		return Frame{}, ctx.Err()
	case frame, ok := <-ch:
		if !ok {
			return Frame{}, errors.New("MT5 connection lost while awaiting response")
		}
		return frame, nil
	case <-time.After(t.config.RequestTimeout):
		t.mu.Lock()
		delete(t.pending, requestID)
		t.mu.Unlock()
		return Frame{}, errors.New("MT5 request timeout")
	}
}

// ---------- Transport interface ----------

// Submit sends a risk-approved order to the EA for execution.
func (t *TCPTransport) Submit(ctx context.Context, approved domain.ApprovedOrder) (domain.BrokerOrder, error) {
	if err := approved.Validate(); err != nil {
		return domain.BrokerOrder{}, err
	}
	requestID := fmt.Sprintf("submit-%s-%d", approved.Intent.ID, time.Now().UnixNano())
	msg := SubmitIntentMsg{
		RequestID:  requestID,
		IntentID:   approved.Intent.ID,
		AccountID:  approved.Intent.AccountID,
		Symbol:     approved.Intent.Symbol,
		Side:       approved.Intent.Side,
		OrderType:  approved.Intent.Type,
		Quantity:   approved.Intent.Quantity,
		EntryPrice: int64(approved.Intent.EntryPrice),
		StopLoss:   int64(approved.Intent.StopLoss),
		TakeProfit: int64(approved.Intent.TakeProfit),
		ExpiresAt:  approved.Intent.ExpiresAt,
		Signature:  approved.ApprovalID, // approval serves as the authorization signature
	}

	frame, err := t.sendRequest(ctx, requestID, MsgSubmitIntent, msg)
	if err != nil {
		return domain.BrokerOrder{}, err
	}

	switch frame.Type {
	case MsgOrderAck:
		var ack OrderAckMsg
		if err := DecodePayload(frame, &ack); err != nil {
			return domain.BrokerOrder{}, err
		}
		return domain.BrokerOrder{
			BrokerOrderID: ack.BrokerOrderID,
			IntentID:      ack.IntentID,
			AccountID:     approved.Intent.AccountID,
			Symbol:        ack.Symbol,
			Side:          ack.Side,
			Type:          approved.Intent.Type,
			Quantity:      ack.Quantity,
			Remaining:     ack.Remaining,
			Status:        domain.OrderAcknowledged,
			SubmittedAt:   ack.SubmittedAt,
			UpdatedAt:     ack.SubmittedAt,
		}, nil

	case MsgOrderRejected:
		var rej OrderRejectedMsg
		if err := DecodePayload(frame, &rej); err != nil {
			return domain.BrokerOrder{}, err
		}
		return domain.BrokerOrder{
			IntentID:     rej.IntentID,
			AccountID:    approved.Intent.AccountID,
			Symbol:       approved.Intent.Symbol,
			Side:         approved.Intent.Side,
			Type:         approved.Intent.Type,
			Quantity:     approved.Intent.Quantity,
			Status:       domain.OrderRejected,
			RejectReason: rej.RejectReason,
		}, nil

	default:
		return domain.BrokerOrder{}, fmt.Errorf("unexpected response type %s for submit", frame.Type)
	}
}

// Cancel requests cancellation of an open order.
func (t *TCPTransport) Cancel(ctx context.Context, brokerOrderID string) error {
	if brokerOrderID == "" {
		return errors.New("broker order ID is required")
	}
	requestID := fmt.Sprintf("cancel-%s-%d", brokerOrderID, time.Now().UnixNano())
	msg := CancelOrderMsg{
		RequestID:     requestID,
		BrokerOrderID: brokerOrderID,
	}

	frame, err := t.sendRequest(ctx, requestID, MsgCancelOrder, msg)
	if err != nil {
		return err
	}

	switch frame.Type {
	case MsgOrderCancelled:
		return nil
	case MsgOrderRejected:
		var rej OrderRejectedMsg
		if err := DecodePayload(frame, &rej); err != nil {
			return err
		}
		return fmt.Errorf("MT5 cancel rejected: %s", rej.RejectReason)
	default:
		return fmt.Errorf("unexpected response type %s for cancel", frame.Type)
	}
}

// OpenOrders returns current open orders from the terminal snapshot.
func (t *TCPTransport) OpenOrders(ctx context.Context) ([]domain.BrokerOrder, error) {
	snap, err := t.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return snap.Orders, nil
}

// Positions returns current open positions from the terminal snapshot.
func (t *TCPTransport) Positions(ctx context.Context) ([]domain.BrokerPosition, error) {
	snap, err := t.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return snap.Positions, nil
}

// Snapshot requests and returns a complete terminal state from the EA.
func (t *TCPTransport) Snapshot(ctx context.Context) (Snapshot, error) {
	requestID := fmt.Sprintf("snap-%d", time.Now().UnixNano())
	msg := SnapshotRequestMsg{RequestID: requestID}

	frame, err := t.sendRequest(ctx, requestID, MsgSnapshotRequest, msg)
	if err != nil {
		return Snapshot{}, err
	}

	if frame.Type != MsgSnapshotResponse {
		return Snapshot{}, fmt.Errorf("unexpected response type %s for snapshot", frame.Type)
	}

	var resp SnapshotResponseMsg
	if err := DecodePayload(frame, &resp); err != nil {
		return Snapshot{}, err
	}

	// Convert to domain types
	orders := make([]domain.BrokerOrder, len(resp.Orders))
	for i, o := range resp.Orders {
		orders[i] = domain.BrokerOrder{
			BrokerOrderID: o.BrokerOrderID,
			IntentID:      o.IntentID,
			AccountID:     t.config.AccountID,
			Symbol:        o.Symbol,
			Side:          o.Side,
			Type:          o.Type,
			Quantity:      o.Quantity,
			Remaining:     o.Remaining,
			Status:        o.Status,
		}
	}
	positions := make([]domain.BrokerPosition, len(resp.Positions))
	for i, p := range resp.Positions {
		positions[i] = domain.BrokerPosition{
			BrokerPositionID: p.BrokerPositionID,
			BrokerOrderID:    p.BrokerOrderID,
			IntentID:         p.IntentID,
			AccountID:        t.config.AccountID,
			Symbol:           p.Symbol,
			Side:             p.Side,
			Quantity:         p.Quantity,
			EntryPrice:       domain.Price(p.EntryPrice),
			StopLoss:         domain.Price(p.StopLoss),
			TakeProfit:       domain.Price(p.TakeProfit),
		}
	}

	return Snapshot{
		Orders:      orders,
		Positions:   positions,
		EquityMinor: resp.EquityMinor,
		CapturedAt:  resp.CapturedAt,
	}, nil
}

// Fills returns the channel receiving asynchronous fill events.
func (t *TCPTransport) Fills() <-chan FillEventMsg {
	return t.fillCh
}

// Connected returns true if the EA is currently connected and handshaked.
func (t *TCPTransport) Connected() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.connected
}

// Close shuts down the transport, closing the listener and connection.
func (t *TCPTransport) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	t.connected = false
	conn := t.conn
	ln := t.listener
	t.mu.Unlock()

	close(t.done)
	if conn != nil {
		conn.Close()
	}
	if ln != nil {
		ln.Close()
	}
	return nil
}

// Addr returns the listener address, useful for tests.
func (t *TCPTransport) Addr() net.Addr {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.listener != nil {
		return t.listener.Addr()
	}
	return nil
}

// ---------- Helpers ----------

// extractRequestID pulls the request_id field from a JSON payload without
// full unmarshalling for routing efficiency.
func extractRequestID(payload []byte) string {
	// Simple JSON field extraction — works for our controlled payloads.
	// A full JSON parse is done by the caller after routing.
	const key = `"request_id":"`
	for i := 0; i < len(payload)-len(key); i++ {
		if string(payload[i:i+len(key)]) == key {
			start := i + len(key)
			for end := start; end < len(payload); end++ {
				if payload[end] == '"' {
					return string(payload[start:end])
				}
			}
		}
	}
	return ""
}
