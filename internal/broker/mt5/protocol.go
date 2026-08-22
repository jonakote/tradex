package mt5

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"tradex/internal/domain"
)

// Protocol version. Both sides must agree on version during handshake.
const ProtocolVersion = 1

// Maximum message payload size (1 MB). Prevents unbounded allocation from
// malformed or adversarial frames.
const MaxPayloadSize = 1 << 20

// MessageType identifies the kind of message on the wire.
type MessageType uint8

const (
	// Handshake: EA → Go on connection establishment.
	MsgHandshake MessageType = 1
	// HandshakeAck: Go → EA confirming accepted connection.
	MsgHandshakeAck MessageType = 2
	// Heartbeat: bidirectional keepalive.
	MsgHeartbeat MessageType = 10
	// SubmitIntent: Go → EA, requesting order placement.
	MsgSubmitIntent MessageType = 20
	// CancelOrder: Go → EA, requesting order cancellation.
	MsgCancelOrder MessageType = 21
	// SnapshotRequest: Go → EA, requesting current terminal state.
	MsgSnapshotRequest MessageType = 22
	// OrderAck: EA → Go, order accepted by terminal.
	MsgOrderAck MessageType = 30
	// OrderRejected: EA → Go, order rejected by terminal.
	MsgOrderRejected MessageType = 31
	// OrderCancelled: EA → Go, order cancelled.
	MsgOrderCancelled MessageType = 32
	// FillEvent: EA → Go, partial or full fill occurred.
	MsgFillEvent MessageType = 33
	// SnapshotResponse: EA → Go, terminal state dump.
	MsgSnapshotResponse MessageType = 34
	// ProtocolError: either direction, unrecoverable protocol violation.
	MsgProtocolError MessageType = 50
)

// String returns a human-readable name for the message type.
func (m MessageType) String() string {
	switch m {
	case MsgHandshake:
		return "HANDSHAKE"
	case MsgHandshakeAck:
		return "HANDSHAKE_ACK"
	case MsgHeartbeat:
		return "HEARTBEAT"
	case MsgSubmitIntent:
		return "SUBMIT_INTENT"
	case MsgCancelOrder:
		return "CANCEL_ORDER"
	case MsgSnapshotRequest:
		return "SNAPSHOT_REQUEST"
	case MsgOrderAck:
		return "ORDER_ACK"
	case MsgOrderRejected:
		return "ORDER_REJECTED"
	case MsgOrderCancelled:
		return "ORDER_CANCELLED"
	case MsgFillEvent:
		return "FILL_EVENT"
	case MsgSnapshotResponse:
		return "SNAPSHOT_RESPONSE"
	case MsgProtocolError:
		return "PROTOCOL_ERROR"
	default:
		return fmt.Sprintf("UNKNOWN(%d)", m)
	}
}

// Valid returns true if the message type is a known value.
func (m MessageType) Valid() bool {
	switch m {
	case MsgHandshake, MsgHandshakeAck, MsgHeartbeat,
		MsgSubmitIntent, MsgCancelOrder, MsgSnapshotRequest,
		MsgOrderAck, MsgOrderRejected, MsgOrderCancelled, MsgFillEvent, MsgSnapshotResponse,
		MsgProtocolError:
		return true
	}
	return false
}

// ---------- Wire frame ----------
//
// Every message on the wire uses the following binary envelope:
//
//   [4 bytes] payload length (big-endian uint32, excludes header)
//   [1 byte]  message type
//   [N bytes] JSON payload
//
// Total frame size = 5 + payload length.
// The 4-byte length prefix includes the type byte, so: length = 1 + len(json).

// Frame represents a decoded wire frame.
type Frame struct {
	Type    MessageType
	Payload []byte
}

// WriteFrame writes a single frame to the writer. It is not concurrency-safe;
// callers must serialize writes externally.
func WriteFrame(w io.Writer, msgType MessageType, payload []byte) error {
	if !msgType.Valid() {
		return errors.New("invalid message type")
	}
	size := uint32(1 + len(payload))
	if size > MaxPayloadSize+1 {
		return errors.New("payload exceeds maximum size")
	}
	header := make([]byte, 5)
	binary.BigEndian.PutUint32(header[0:4], size)
	header[4] = byte(msgType)
	if _, err := w.Write(header); err != nil {
		return fmt.Errorf("write frame header: %w", err)
	}
	if len(payload) > 0 {
		if _, err := w.Write(payload); err != nil {
			return fmt.Errorf("write frame payload: %w", err)
		}
	}
	return nil
}

// ReadFrame reads a single frame from the reader. It blocks until a complete
// frame arrives or an error occurs.
func ReadFrame(r io.Reader) (Frame, error) {
	header := make([]byte, 5)
	if _, err := io.ReadFull(r, header); err != nil {
		return Frame{}, fmt.Errorf("read frame header: %w", err)
	}
	size := binary.BigEndian.Uint32(header[0:4])
	if size == 0 {
		return Frame{}, errors.New("frame size is zero")
	}
	if size > MaxPayloadSize+1 {
		return Frame{}, fmt.Errorf("frame size %d exceeds maximum", size)
	}
	msgType := MessageType(header[4])
	if !msgType.Valid() {
		return Frame{}, fmt.Errorf("unknown message type: %d", header[4])
	}
	payloadLen := size - 1
	var payload []byte
	if payloadLen > 0 {
		payload = make([]byte, payloadLen)
		if _, err := io.ReadFull(r, payload); err != nil {
			return Frame{}, fmt.Errorf("read frame payload: %w", err)
		}
	}
	return Frame{Type: msgType, Payload: payload}, nil
}

// ---------- Message payloads ----------

// Handshake is sent by the EA on connection to identify itself.
type Handshake struct {
	ProtocolVersion int    `json:"protocol_version"`
	AccountID       string `json:"account_id"`
	Terminal        string `json:"terminal"`
	Broker          string `json:"broker"`
	ServerTime      string `json:"server_time"`
}

func (h Handshake) Validate() error {
	if h.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("unsupported protocol version %d, expected %d", h.ProtocolVersion, ProtocolVersion)
	}
	if h.AccountID == "" || h.Terminal == "" || h.Broker == "" {
		return errors.New("handshake requires account_id, terminal, and broker")
	}
	return nil
}

// HandshakeAck is sent by Go to confirm the connection.
type HandshakeAck struct {
	Accepted          bool   `json:"accepted"`
	RejectReason      string `json:"reject_reason,omitempty"`
	HeartbeatInterval int    `json:"heartbeat_interval_ms"`
}

// HeartbeatMsg is sent bidirectionally as a keepalive.
type HeartbeatMsg struct {
	Timestamp int64 `json:"ts"`
	SequenceN uint64 `json:"seq"`
}

// SubmitIntentMsg is sent from Go to EA requesting order placement.
type SubmitIntentMsg struct {
	RequestID  string          `json:"request_id"`
	IntentID   string          `json:"intent_id"`
	AccountID  string          `json:"account_id"`
	Symbol     string          `json:"symbol"`
	Side       domain.Side     `json:"side"`
	OrderType  domain.OrderType `json:"order_type"`
	Quantity   int64           `json:"quantity"`
	EntryPrice int64           `json:"entry_price"`
	StopLoss   int64           `json:"stop_loss"`
	TakeProfit int64           `json:"take_profit"`
	ExpiresAt  time.Time       `json:"expires_at"`
	Signature  string          `json:"signature"`
}

func (s SubmitIntentMsg) Validate() error {
	if s.RequestID == "" || s.IntentID == "" || s.AccountID == "" || s.Symbol == "" {
		return errors.New("submit intent requires request_id, intent_id, account_id, and symbol")
	}
	if !s.Side.Valid() || !s.OrderType.Valid() {
		return errors.New("invalid side or order type in submit intent")
	}
	if s.Quantity <= 0 || s.StopLoss <= 0 || s.TakeProfit <= 0 {
		return errors.New("submit intent requires positive quantity, stop_loss, and take_profit")
	}
	if s.ExpiresAt.IsZero() {
		return errors.New("submit intent requires expires_at")
	}
	if s.Signature == "" {
		return errors.New("submit intent requires signature")
	}
	return nil
}

// CancelOrderMsg is sent from Go to EA requesting order cancellation.
type CancelOrderMsg struct {
	RequestID     string `json:"request_id"`
	BrokerOrderID string `json:"broker_order_id"`
}

func (c CancelOrderMsg) Validate() error {
	if c.RequestID == "" || c.BrokerOrderID == "" {
		return errors.New("cancel requires request_id and broker_order_id")
	}
	return nil
}

// SnapshotRequestMsg is sent from Go to EA.
type SnapshotRequestMsg struct {
	RequestID string `json:"request_id"`
}

// OrderAckMsg is sent from EA to Go when the terminal accepts an order.
type OrderAckMsg struct {
	RequestID     string    `json:"request_id"`
	IntentID      string    `json:"intent_id"`
	BrokerOrderID string    `json:"broker_order_id"`
	Symbol        string    `json:"symbol"`
	Side          domain.Side `json:"side"`
	Quantity      int64     `json:"quantity"`
	Remaining     int64     `json:"remaining"`
	SubmittedAt   time.Time `json:"submitted_at"`
}

// OrderRejectedMsg is sent from EA to Go when the terminal rejects an order.
type OrderRejectedMsg struct {
	RequestID    string `json:"request_id"`
	IntentID     string `json:"intent_id"`
	RejectReason string `json:"reject_reason"`
	ErrorCode    int    `json:"error_code"`
}

// OrderCancelledMsg is sent from EA to Go when cancellation succeeds.
type OrderCancelledMsg struct {
	RequestID     string    `json:"request_id"`
	BrokerOrderID string    `json:"broker_order_id"`
	CancelledAt   time.Time `json:"cancelled_at"`
}

// FillEventMsg is sent from EA to Go on partial or full fill.
type FillEventMsg struct {
	IntentID         string        `json:"intent_id"`
	BrokerOrderID    string        `json:"broker_order_id"`
	BrokerPositionID string        `json:"broker_position_id"`
	Symbol           string        `json:"symbol"`
	Side             domain.Side   `json:"side"`
	Role             domain.FillRole `json:"role"`
	Quantity         int64         `json:"quantity"`
	Price            int64         `json:"price"`
	CommissionMinor  int64         `json:"commission_minor"`
	FilledAt         time.Time     `json:"filled_at"`
}

// SnapshotResponseMsg is sent from EA to Go with the terminal state.
type SnapshotResponseMsg struct {
	RequestID   string              `json:"request_id"`
	Orders      []SnapshotOrder     `json:"orders"`
	Positions   []SnapshotPosition  `json:"positions"`
	EquityMinor int64               `json:"equity_minor"`
	CapturedAt  time.Time           `json:"captured_at"`
}

// SnapshotOrder is a single order in the terminal snapshot.
type SnapshotOrder struct {
	BrokerOrderID string             `json:"broker_order_id"`
	IntentID      string             `json:"intent_id"`
	Symbol        string             `json:"symbol"`
	Side          domain.Side        `json:"side"`
	Type          domain.OrderType   `json:"type"`
	Quantity      int64              `json:"quantity"`
	Remaining     int64              `json:"remaining"`
	Status        domain.OrderStatus `json:"status"`
}

// SnapshotPosition is a single position in the terminal snapshot.
type SnapshotPosition struct {
	BrokerPositionID string      `json:"broker_position_id"`
	BrokerOrderID    string      `json:"broker_order_id"`
	IntentID         string      `json:"intent_id"`
	Symbol           string      `json:"symbol"`
	Side             domain.Side `json:"side"`
	Quantity         int64       `json:"quantity"`
	EntryPrice       int64       `json:"entry_price"`
	StopLoss         int64       `json:"stop_loss"`
	TakeProfit       int64       `json:"take_profit"`
}

// ProtocolErrorMsg signals an unrecoverable protocol violation.
type ProtocolErrorMsg struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ---------- Encoding helpers ----------

// EncodeMessage marshals a payload and writes it as a frame.
func EncodeMessage(w io.Writer, msgType MessageType, msg any) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", msgType, err)
	}
	return WriteFrame(w, msgType, payload)
}

// DecodePayload unmarshals a frame's JSON payload into the target.
func DecodePayload(frame Frame, target any) error {
	if len(frame.Payload) == 0 {
		return fmt.Errorf("empty payload for %s", frame.Type)
	}
	dec := json.NewDecoder(jsonReader(frame.Payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return fmt.Errorf("decode %s: %w", frame.Type, err)
	}
	return nil
}

type byteReader struct {
	data []byte
	pos  int
}

func jsonReader(data []byte) io.Reader { return &byteReader{data: data} }

func (r *byteReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}
