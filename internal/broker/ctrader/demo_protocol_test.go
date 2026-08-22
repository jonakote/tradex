package ctrader

import (
	"context"
	"errors"
	"testing"
)

func TestDemoProtocolHarnessRoundTrip(t *testing.T) {
	harness, err := NewDemoProtocolHarness(testSession(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	request, response, err := harness.RoundTrip(context.Background(), 10, []byte{3, 4}, true)
	if err != nil {
		t.Fatal(err)
	}
	if response.PayloadType != 11 || response.ClientMsgID != request.ClientMsgID {
		t.Fatalf("response = %#v", response)
	}
	requests := harness.Requests()
	if len(requests) != 1 || requests[0].ClientMsgID != request.ClientMsgID {
		t.Fatalf("requests = %#v", requests)
	}
	requests[0].Payload[0] = 99
	if harness.Requests()[0].Payload[0] == 99 {
		t.Fatal("requests leaked internal payload")
	}
}

func TestDemoProtocolHarnessUsesResponderAndPropagatesErrors(t *testing.T) {
	harness, err := NewDemoProtocolHarness(testSession(t), func(request MessageEnvelope) (MessageEnvelope, error) {
		return MessageEnvelope{PayloadType: 99, Payload: []byte{7}, ClientMsgID: request.ClientMsgID}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, response, err := harness.RoundTrip(context.Background(), 1, []byte{2}, false)
	if err != nil || response.PayloadType != 99 {
		t.Fatalf("response = %#v, err %v", response, err)
	}
	failing, err := NewDemoProtocolHarness(testSession(t), func(MessageEnvelope) (MessageEnvelope, error) {
		return MessageEnvelope{}, errors.New("demo response failure")
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := failing.RoundTrip(context.Background(), 1, []byte{2}, false); err == nil {
		t.Fatal("expected responder failure")
	}
}
