package ctrader

import (
	"context"
	"errors"
)

// DemoResponder deterministically maps an outbound request to a response.
// It is intended for contract tests and paper simulations, never broker I/O.
type DemoResponder func(MessageEnvelope) (MessageEnvelope, error)

type DemoProtocolHarness struct {
	client    *ProtocolClient
	responder DemoResponder
	requests  []MessageEnvelope
}

func NewDemoProtocolHarness(session *OpenAPISession, responder DemoResponder) (*DemoProtocolHarness, error) {
	if session == nil {
		return nil, errors.New("cTrader demo protocol requires a session")
	}
	if responder == nil {
		responder = func(request MessageEnvelope) (MessageEnvelope, error) {
			return MessageEnvelope{PayloadType: request.PayloadType + 1, Payload: append([]byte(nil), request.Payload...), ClientMsgID: request.ClientMsgID}, nil
		}
	}
	harness := &DemoProtocolHarness{responder: responder}
	client, err := NewProtocolClient(session, NewRequestTracker(nil), harness)
	if err != nil {
		return nil, err
	}
	harness.client = client
	return harness, nil
}

// RoundTrip sends one request, invokes the deterministic responder, and
// correlates the returned response through the same client path used by a real
// transport. No serialization, socket, TLS, or broker endpoint is involved.
func (h *DemoProtocolHarness) RoundTrip(ctx context.Context, payloadType uint32, payload []byte, mutating bool) (PendingRequest, MessageEnvelope, error) {
	request, err := h.client.Send(ctx, payloadType, payload, mutating)
	if err != nil {
		return PendingRequest{}, MessageEnvelope{}, err
	}
	message := h.requests[len(h.requests)-1]
	response, err := h.responder(message)
	if err != nil {
		h.client.tracker.Complete(request.ClientMsgID)
		return PendingRequest{}, MessageEnvelope{}, err
	}
	if _, err := h.client.HandleResponse(response); err != nil {
		return PendingRequest{}, MessageEnvelope{}, err
	}
	return request, response, nil
}

func (h *DemoProtocolHarness) WriteMessage(_ context.Context, message MessageEnvelope) error {
	h.requests = append(h.requests, message)
	return nil
}

func (h *DemoProtocolHarness) Requests() []MessageEnvelope {
	requests := make([]MessageEnvelope, len(h.requests))
	for i, request := range h.requests {
		requests[i] = MessageEnvelope{PayloadType: request.PayloadType, Payload: append([]byte(nil), request.Payload...), ClientMsgID: request.ClientMsgID}
	}
	return requests
}
