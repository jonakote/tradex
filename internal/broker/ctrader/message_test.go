package ctrader

import (
	"reflect"
	"testing"
	"time"
)

func TestMessageEnvelopeValidation(t *testing.T) {
	if err := (MessageEnvelope{PayloadType: 1, Payload: []byte{1}, ClientMsgID: "x"}).Validate(); err != nil {
		t.Fatal(err)
	}
	for _, message := range []MessageEnvelope{{Payload: []byte{1}}, {PayloadType: 1}} {
		if err := message.Validate(); err == nil {
			t.Fatal("expected invalid envelope")
		}
	}
}

func TestRequestTrackerCorrelationAndReset(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	tracker := NewRequestTracker(func() time.Time { return now })
	one, err := tracker.Register(100)
	if err != nil {
		t.Fatal(err)
	}
	two, err := tracker.Register(200)
	if err != nil {
		t.Fatal(err)
	}
	if one.ClientMsgID != "tradex-00000001" || two.ClientMsgID != "tradex-00000002" {
		t.Fatalf("IDs = %q, %q", one.ClientMsgID, two.ClientMsgID)
	}
	if got, ok := tracker.Complete(one.ClientMsgID); !ok || !reflect.DeepEqual(got, one) {
		t.Fatalf("complete = %#v, %v", got, ok)
	}
	if _, ok := tracker.Complete(one.ClientMsgID); ok {
		t.Fatal("duplicate completion should be absent")
	}
	if got := tracker.Reset(); !reflect.DeepEqual(got, []PendingRequest{two}) {
		t.Fatalf("reset = %#v", got)
	}
	if tracker.PendingCount() != 0 {
		t.Fatal("tracker still has pending requests")
	}
}

func TestProtocolError(t *testing.T) {
	err := ProtocolError{Code: 42, Description: "rate limited", Retriable: true}
	if err.Error() != "cTrader protocol error code 42: rate limited" {
		t.Fatalf("error = %q", err.Error())
	}
}
