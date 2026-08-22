package mt5

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"tradex/internal/domain"
)

// fakeEA simulates the MQL5 Expert Advisor side of the bridge for testing.
type fakeEA struct {
	accountID string
	conn      net.Conn
	mu        sync.Mutex
	orders    map[string]int64 // intentID -> sequence
	seq       int64
}

func newFakeEA(accountID string) *fakeEA {
	return &fakeEA{accountID: accountID, orders: make(map[string]int64)}
}

// connect dials the Go transport listener and performs handshake.
func (ea *fakeEA) connect(addr string) error {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return err
	}
	ea.conn = conn

	// Send handshake
	hs := Handshake{
		ProtocolVersion: ProtocolVersion,
		AccountID:       ea.accountID,
		Terminal:        "MetaTrader 5 Build 4500",
		Broker:          "Exness",
		ServerTime:      time.Now().UTC().Format(time.RFC3339),
	}
	if err := EncodeMessage(conn, MsgHandshake, hs); err != nil {
		return err
	}

	// Read handshake ack
	frame, err := ReadFrame(conn)
	if err != nil {
		return err
	}
	if frame.Type != MsgHandshakeAck {
		return fmt.Errorf("expected handshake ack, got %s", frame.Type)
	}
	var ack HandshakeAck
	if err := DecodePayload(frame, &ack); err != nil {
		return err
	}
	if !ack.Accepted {
		return fmt.Errorf("handshake rejected: %s", ack.RejectReason)
	}
	return nil
}

// serve handles requests from the Go transport in a loop.
func (ea *fakeEA) serve(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		_ = ea.conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		frame, err := ReadFrame(ea.conn)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return
		}

		switch frame.Type {
		case MsgHeartbeat:
			// Echo heartbeat back
			hb := HeartbeatMsg{Timestamp: time.Now().UnixMilli(), SequenceN: 0}
			_ = EncodeMessage(ea.conn, MsgHeartbeat, hb)

		case MsgSubmitIntent:
			ea.handleSubmit(frame)

		case MsgCancelOrder:
			ea.handleCancel(frame)

		case MsgSnapshotRequest:
			ea.handleSnapshot(frame)
		}
	}
}

func (ea *fakeEA) handleSubmit(frame Frame) {
	var msg SubmitIntentMsg
	if err := DecodePayload(frame, &msg); err != nil {
		return
	}

	ea.mu.Lock()
	ea.seq++
	orderID := fmt.Sprintf("mt5-ticket-%d", ea.seq)
	ea.orders[msg.IntentID] = ea.seq
	ea.mu.Unlock()

	ack := OrderAckMsg{
		RequestID:     msg.RequestID,
		IntentID:      msg.IntentID,
		BrokerOrderID: orderID,
		Symbol:        msg.Symbol,
		Side:          msg.Side,
		Quantity:      msg.Quantity,
		Remaining:     msg.Quantity,
		SubmittedAt:   time.Now().UTC(),
	}
	_ = EncodeMessage(ea.conn, MsgOrderAck, ack)
}

func (ea *fakeEA) handleCancel(frame Frame) {
	var msg CancelOrderMsg
	if err := DecodePayload(frame, &msg); err != nil {
		return
	}

	cancelled := OrderCancelledMsg{
		RequestID:     msg.RequestID,
		BrokerOrderID: msg.BrokerOrderID,
		CancelledAt:   time.Now().UTC(),
	}
	_ = EncodeMessage(ea.conn, MsgOrderCancelled, cancelled)
}

func (ea *fakeEA) handleSnapshot(frame Frame) {
	var msg SnapshotRequestMsg
	if err := DecodePayload(frame, &msg); err != nil {
		return
	}

	resp := SnapshotResponseMsg{
		RequestID:   msg.RequestID,
		Orders:      []SnapshotOrder{},
		Positions:   []SnapshotPosition{},
		EquityMinor: 1000000,
		CapturedAt:  time.Now().UTC(),
	}
	_ = EncodeMessage(ea.conn, MsgSnapshotResponse, resp)
}

func (ea *fakeEA) close() {
	if ea.conn != nil {
		ea.conn.Close()
	}
}

// ---------- Tests ----------

func TestTCPTransportConfigValidation(t *testing.T) {
	valid := TCPTransportConfig{
		ListenAddr:        ":0",
		AccountID:         "test-account",
		HeartbeatInterval: 5 * time.Second,
		HeartbeatTimeout:  10 * time.Second,
		RequestTimeout:    5 * time.Second,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	t.Run("missing addr", func(t *testing.T) {
		c := valid
		c.ListenAddr = ""
		if err := c.Validate(); err == nil {
			t.Fatal("accepted")
		}
	})
	t.Run("missing account", func(t *testing.T) {
		c := valid
		c.AccountID = ""
		if err := c.Validate(); err == nil {
			t.Fatal("accepted")
		}
	})
	t.Run("zero heartbeat", func(t *testing.T) {
		c := valid
		c.HeartbeatInterval = 0
		if err := c.Validate(); err == nil {
			t.Fatal("accepted")
		}
	})
	t.Run("timeout not greater than interval", func(t *testing.T) {
		c := valid
		c.HeartbeatTimeout = valid.HeartbeatInterval
		if err := c.Validate(); err == nil {
			t.Fatal("accepted")
		}
	})
}

func startTransportAndEA(t *testing.T, accountID string) (*TCPTransport, *fakeEA, context.CancelFunc) {
	t.Helper()
	cfg := TCPTransportConfig{
		ListenAddr:        "127.0.0.1:0",
		AccountID:         accountID,
		HeartbeatInterval: 2 * time.Second,
		HeartbeatTimeout:  5 * time.Second,
		RequestTimeout:    3 * time.Second,
	}
	transport, err := NewTCPTransport(cfg)
	if err != nil {
		t.Fatal(err)
	}

	// Start listener on a random port
	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Accept in background
	listenDone := make(chan error, 1)
	go func() {
		listenDone <- transport.ListenOn(ctx, ln)
	}()

	// Connect fake EA
	ea := newFakeEA(accountID)
	time.Sleep(50 * time.Millisecond) // Let listener start
	if err := ea.connect(ln.Addr().String()); err != nil {
		cancel()
		t.Fatalf("EA connect failed: %v", err)
	}

	// Wait for transport to accept and handshake
	time.Sleep(100 * time.Millisecond)

	// Start EA serve loop
	go ea.serve(ctx)

	t.Cleanup(func() {
		cancel()
		ea.close()
		transport.Close()
	})

	return transport, ea, cancel
}

func TestTCPTransportConnectAndHandshake(t *testing.T) {
	transport, _, _ := startTransportAndEA(t, "test-account")
	if !transport.Connected() {
		t.Fatal("transport not connected after handshake")
	}
}

func TestTCPTransportRejectsWrongAccount(t *testing.T) {
	cfg := TCPTransportConfig{
		ListenAddr:        "127.0.0.1:0",
		AccountID:         "expected-account",
		HeartbeatInterval: 2 * time.Second,
		HeartbeatTimeout:  5 * time.Second,
		RequestTimeout:    3 * time.Second,
	}
	transport, err := NewTCPTransport(cfg)
	if err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	go func() {
		_ = transport.ListenOn(ctx, ln)
	}()
	time.Sleep(50 * time.Millisecond)

	// Connect with wrong account
	ea := newFakeEA("wrong-account")
	err = ea.connect(ln.Addr().String())
	ea.close()
	transport.Close()

	if err == nil {
		t.Fatal("accepted connection with wrong account")
	}
}

func TestTCPTransportSubmit(t *testing.T) {
	transport, _, _ := startTransportAndEA(t, "test-account")

	now := time.Now().UTC()
	approved := domain.ApprovedOrder{
		ApprovalID: "approval-1",
		ApprovedAt: now,
		Intent: domain.OrderIntent{
			ID: "intent-1", StrategyID: "icc", StrategyVersion: "v1",
			AccountID: "test-account", Symbol: "EURUSD",
			Side: domain.SideBuy, Type: domain.OrderTypeMarket,
			Quantity: 100000, StopLoss: 108000, TakeProfit: 109500,
			SignalTime: now.Add(-time.Second), ExpiresAt: now.Add(time.Minute),
		},
	}

	order, err := transport.Submit(context.Background(), approved)
	if err != nil {
		t.Fatalf("submit failed: %v", err)
	}
	if order.Status != domain.OrderAcknowledged {
		t.Fatalf("unexpected status: %s", order.Status)
	}
	if order.BrokerOrderID == "" {
		t.Fatal("missing broker order ID")
	}
	if order.Symbol != "EURUSD" || order.Quantity != 100000 {
		t.Fatalf("unexpected order: %+v", order)
	}
}

func TestTCPTransportCancel(t *testing.T) {
	transport, _, _ := startTransportAndEA(t, "test-account")

	err := transport.Cancel(context.Background(), "mt5-ticket-1")
	if err != nil {
		t.Fatalf("cancel failed: %v", err)
	}
}

func TestTCPTransportSnapshot(t *testing.T) {
	transport, _, _ := startTransportAndEA(t, "test-account")

	snap, err := transport.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if snap.CapturedAt.IsZero() {
		t.Fatal("snapshot has no capture time")
	}
	if snap.EquityMinor != 1000000 {
		t.Fatalf("unexpected equity: %d", snap.EquityMinor)
	}
}

func TestTCPTransportNotConnectedRejectsRequests(t *testing.T) {
	cfg := TCPTransportConfig{
		ListenAddr:        "127.0.0.1:0",
		AccountID:         "test-account",
		HeartbeatInterval: 2 * time.Second,
		HeartbeatTimeout:  5 * time.Second,
		RequestTimeout:    3 * time.Second,
	}
	transport, err := NewTCPTransport(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()

	// No connection established — requests should fail
	now := time.Now().UTC()
	_, err = transport.Submit(context.Background(), domain.ApprovedOrder{
		ApprovalID: "a", ApprovedAt: now,
		Intent: domain.OrderIntent{
			ID: "i", StrategyID: "s", StrategyVersion: "v1",
			AccountID: "test-account", Symbol: "EURUSD",
			Side: domain.SideBuy, Type: domain.OrderTypeMarket,
			Quantity: 1000, StopLoss: 100, TakeProfit: 200,
			SignalTime: now.Add(-time.Second), ExpiresAt: now.Add(time.Minute),
		},
	})
	if err == nil {
		t.Fatal("submit succeeded without connection")
	}
}

func TestTCPTransportCancelEmptyID(t *testing.T) {
	transport, _, _ := startTransportAndEA(t, "test-account")
	if err := transport.Cancel(context.Background(), ""); err == nil {
		t.Fatal("accepted empty order ID")
	}
}

func TestTCPTransportFullLifecycle(t *testing.T) {
	transport, _, _ := startTransportAndEA(t, "test-account")

	// 1. Snapshot (reconciliation)
	snap, err := transport.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.CapturedAt.IsZero() {
		t.Fatal("no capture time")
	}

	// 2. Submit order
	now := time.Now().UTC()
	approved := domain.ApprovedOrder{
		ApprovalID: "approval-1",
		ApprovedAt: now,
		Intent: domain.OrderIntent{
			ID: "lifecycle-intent", StrategyID: "icc", StrategyVersion: "v1",
			AccountID: "test-account", Symbol: "XAUUSD",
			Side: domain.SideSell, Type: domain.OrderTypeMarket,
			Quantity: 100, StopLoss: 200000, TakeProfit: 190000,
			SignalTime: now.Add(-time.Second), ExpiresAt: now.Add(time.Minute),
		},
	}
	order, err := transport.Submit(context.Background(), approved)
	if err != nil {
		t.Fatal(err)
	}
	if order.BrokerOrderID == "" || order.Status != domain.OrderAcknowledged {
		t.Fatalf("unexpected: %+v", order)
	}

	// 3. Cancel order
	if err := transport.Cancel(context.Background(), order.BrokerOrderID); err != nil {
		t.Fatal(err)
	}
}

func TestExtractRequestID(t *testing.T) {
	tests := []struct {
		payload  string
		expected string
	}{
		{`{"request_id":"abc-123","other":"val"}`, "abc-123"},
		{`{"other":"val","request_id":"xyz"}`, "xyz"},
		{`{"no_match":"val"}`, ""},
		{`{}`, ""},
	}
	for _, tt := range tests {
		got := extractRequestID([]byte(tt.payload))
		if got != tt.expected {
			t.Errorf("extractRequestID(%s) = %q, want %q", tt.payload, got, tt.expected)
		}
	}
}
