package mt5

import (
	"bytes"
	"testing"
	"time"

	"tradex/internal/domain"
)

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	payload := []byte(`{"ts":1234567890,"seq":1}`)
	if err := WriteFrame(&buf, MsgHeartbeat, payload); err != nil {
		t.Fatal(err)
	}
	frame, err := ReadFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if frame.Type != MsgHeartbeat {
		t.Fatalf("got type %s, want HEARTBEAT", frame.Type)
	}
	if !bytes.Equal(frame.Payload, payload) {
		t.Fatalf("payload mismatch: %s", frame.Payload)
	}
}

func TestFrameEmptyPayload(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, MsgHeartbeat, nil); err != nil {
		t.Fatal(err)
	}
	frame, err := ReadFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if frame.Type != MsgHeartbeat || len(frame.Payload) != 0 {
		t.Fatalf("unexpected frame: %+v", frame)
	}
}

func TestFrameRejectsOversized(t *testing.T) {
	var buf bytes.Buffer
	oversized := make([]byte, MaxPayloadSize+1)
	if err := WriteFrame(&buf, MsgHeartbeat, oversized); err == nil {
		t.Fatal("accepted oversized payload")
	}
}

func TestFrameRejectsInvalidType(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, MessageType(255), []byte("x")); err == nil {
		t.Fatal("accepted invalid message type")
	}
}

func TestFrameRejectsZeroSize(t *testing.T) {
	// Manually write a zero-size header
	var buf bytes.Buffer
	buf.Write([]byte{0, 0, 0, 0, byte(MsgHeartbeat)})
	if _, err := ReadFrame(&buf); err == nil {
		t.Fatal("accepted zero-size frame")
	}
}

func TestEncodeDecodeHandshake(t *testing.T) {
	var buf bytes.Buffer
	hs := Handshake{
		ProtocolVersion: ProtocolVersion,
		AccountID:       "12345",
		Terminal:        "MetaTrader 5",
		Broker:          "Exness",
		ServerTime:      "2026-08-20T10:00:00Z",
	}
	if err := EncodeMessage(&buf, MsgHandshake, hs); err != nil {
		t.Fatal(err)
	}
	frame, err := ReadFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if frame.Type != MsgHandshake {
		t.Fatalf("got type %s", frame.Type)
	}
	var decoded Handshake
	if err := DecodePayload(frame, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.AccountID != "12345" || decoded.Broker != "Exness" {
		t.Fatalf("decoded handshake mismatch: %+v", decoded)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestHandshakeRejectsWrongVersion(t *testing.T) {
	hs := Handshake{ProtocolVersion: 99, AccountID: "a", Terminal: "t", Broker: "b"}
	if err := hs.Validate(); err == nil {
		t.Fatal("accepted wrong protocol version")
	}
}

func TestEncodeDecodeSubmitIntent(t *testing.T) {
	var buf bytes.Buffer
	msg := SubmitIntentMsg{
		RequestID:  "req-1",
		IntentID:   "intent-1",
		AccountID:  "12345",
		Symbol:     "EURUSD",
		Side:       domain.SideBuy,
		OrderType:  domain.OrderTypeMarket,
		Quantity:   100000,
		EntryPrice: 0,
		StopLoss:   108000,
		TakeProfit: 109000,
		ExpiresAt:  time.Date(2026, 8, 20, 10, 1, 0, 0, time.UTC),
		Signature:  "hmac-sha256:abc123",
	}
	if err := EncodeMessage(&buf, MsgSubmitIntent, msg); err != nil {
		t.Fatal(err)
	}
	frame, err := ReadFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	var decoded SubmitIntentMsg
	if err := DecodePayload(frame, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatal(err)
	}
	if decoded.Symbol != "EURUSD" || decoded.Quantity != 100000 {
		t.Fatalf("unexpected: %+v", decoded)
	}
}

func TestSubmitIntentValidation(t *testing.T) {
	valid := SubmitIntentMsg{
		RequestID: "r", IntentID: "i", AccountID: "a", Symbol: "s",
		Side: domain.SideBuy, OrderType: domain.OrderTypeMarket,
		Quantity: 1, StopLoss: 1, TakeProfit: 1,
		ExpiresAt: time.Now().Add(time.Minute), Signature: "sig",
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid rejected: %v", err)
	}
	t.Run("missing request_id", func(t *testing.T) {
		m := valid
		m.RequestID = ""
		if err := m.Validate(); err == nil {
			t.Fatal("accepted")
		}
	})
	t.Run("invalid side", func(t *testing.T) {
		m := valid
		m.Side = "INVALID"
		if err := m.Validate(); err == nil {
			t.Fatal("accepted")
		}
	})
	t.Run("zero quantity", func(t *testing.T) {
		m := valid
		m.Quantity = 0
		if err := m.Validate(); err == nil {
			t.Fatal("accepted")
		}
	})
	t.Run("missing signature", func(t *testing.T) {
		m := valid
		m.Signature = ""
		if err := m.Validate(); err == nil {
			t.Fatal("accepted")
		}
	})
}

func TestEncodeDecodeSnapshotResponse(t *testing.T) {
	var buf bytes.Buffer
	msg := SnapshotResponseMsg{
		RequestID: "snap-1",
		Orders: []SnapshotOrder{
			{BrokerOrderID: "o1", IntentID: "i1", Symbol: "EURUSD", Side: domain.SideBuy, Type: domain.OrderTypeMarket, Quantity: 1000, Remaining: 1000, Status: domain.OrderAcknowledged},
		},
		Positions: []SnapshotPosition{
			{BrokerPositionID: "p1", BrokerOrderID: "o0", IntentID: "i0", Symbol: "XAUUSD", Side: domain.SideSell, Quantity: 100, EntryPrice: 195000, StopLoss: 196000, TakeProfit: 193000},
		},
		EquityMinor: 500000,
		CapturedAt:  time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC),
	}
	if err := EncodeMessage(&buf, MsgSnapshotResponse, msg); err != nil {
		t.Fatal(err)
	}
	frame, err := ReadFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	var decoded SnapshotResponseMsg
	if err := DecodePayload(frame, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.RequestID != "snap-1" || len(decoded.Orders) != 1 || len(decoded.Positions) != 1 {
		t.Fatalf("unexpected: %+v", decoded)
	}
	if decoded.EquityMinor != 500000 {
		t.Fatalf("equity mismatch: %d", decoded.EquityMinor)
	}
}

func TestCancelOrderValidation(t *testing.T) {
	valid := CancelOrderMsg{RequestID: "r", BrokerOrderID: "o"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid rejected: %v", err)
	}
	m := CancelOrderMsg{}
	if err := m.Validate(); err == nil {
		t.Fatal("accepted empty cancel")
	}
}

func TestMessageTypeString(t *testing.T) {
	if MsgHeartbeat.String() != "HEARTBEAT" {
		t.Fatalf("got %s", MsgHeartbeat.String())
	}
	if MessageType(200).String() != "UNKNOWN(200)" {
		t.Fatal("unknown type not formatted")
	}
}

func TestMultipleFramesInSequence(t *testing.T) {
	var buf bytes.Buffer
	for i := 0; i < 5; i++ {
		hb := HeartbeatMsg{Timestamp: int64(i), SequenceN: uint64(i)}
		if err := EncodeMessage(&buf, MsgHeartbeat, hb); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 5; i++ {
		frame, err := ReadFrame(&buf)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		var hb HeartbeatMsg
		if err := DecodePayload(frame, &hb); err != nil {
			t.Fatal(err)
		}
		if hb.SequenceN != uint64(i) {
			t.Fatalf("frame %d: seq %d", i, hb.SequenceN)
		}
	}
}
