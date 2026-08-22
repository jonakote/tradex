package ctrader

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// MessageWriter is the only I/O dependency required by ProtocolClient. A TCP
// or WebSocket implementation owns TLS, socket deadlines, and actual framing.
type MessageWriter interface {
	WriteMessage(context.Context, MessageEnvelope) error
}

// ProtocolClient coordinates authorization gates and request correlation while
// leaving serialization and network I/O to injected implementations.
type ProtocolClient struct {
	session  *OpenAPISession
	tracker  *RequestTracker
	writer   MessageWriter
	safety   *SafetyGate
	recorder AuditRecorder
}

func NewProtocolClient(session *OpenAPISession, tracker *RequestTracker, writer MessageWriter) (*ProtocolClient, error) {
	if session == nil || tracker == nil || writer == nil {
		return nil, errors.New("cTrader protocol client requires session, tracker, and writer")
	}
	return &ProtocolClient{session: session, tracker: tracker, writer: writer}, nil
}

func (c *ProtocolClient) AttachSafetyGate(gate *SafetyGate)          { c.safety = gate }
func (c *ProtocolClient) AttachAuditRecorder(recorder AuditRecorder) { c.recorder = recorder }

func (c *ProtocolClient) Send(ctx context.Context, payloadType uint32, payload []byte, mutating bool) (PendingRequest, error) {
	if ctx == nil {
		return PendingRequest{}, errors.New("cTrader protocol context is required")
	}
	if c.session.State() == SessionDisconnected {
		return PendingRequest{}, errors.New("cTrader protocol session is disconnected")
	}
	if c.safety != nil {
		if err := c.safety.Allow(); err != nil {
			return PendingRequest{}, err
		}
	}
	if mutating && !c.session.CanMutate() {
		return PendingRequest{}, errors.New("cTrader mutation requires account authorization")
	}
	request, err := c.tracker.Register(payloadType)
	if err != nil {
		return PendingRequest{}, err
	}
	envelope := MessageEnvelope{PayloadType: payloadType, Payload: append([]byte(nil), payload...), ClientMsgID: request.ClientMsgID}
	if err := envelope.Validate(); err != nil {
		c.tracker.Complete(request.ClientMsgID)
		return PendingRequest{}, err
	}
	if err := c.writer.WriteMessage(ctx, envelope); err != nil {
		c.tracker.Complete(request.ClientMsgID)
		return PendingRequest{}, fmt.Errorf("write cTrader protocol message: %w", err)
	}
	return request, nil
}

// SendOperation applies typed business validation before entering the generic
// protocol path. The payload is already serialized by the caller for now.
func (c *ProtocolClient) SendOperation(ctx context.Context, operation OperationRequest) (PendingRequest, error) {
	if err := operation.Validate(c.session.config.AccountID); err != nil {
		return PendingRequest{}, err
	}
	mutating := operation.Kind == OperationSubmitOrder || operation.Kind == OperationCancelOrder
	return c.Send(ctx, operation.PayloadType, operation.Payload, mutating)
}

// HandleResponse correlates responses. Events without a client message ID are
// intentionally ignored by the request tracker and can be routed separately.
func (c *ProtocolClient) HandleResponse(response MessageEnvelope) (PendingRequest, error) {
	if err := response.Validate(); err != nil {
		return PendingRequest{}, err
	}
	if response.ClientMsgID == "" {
		return PendingRequest{}, nil
	}
	request, ok := c.tracker.Complete(response.ClientMsgID)
	if !ok {
		return PendingRequest{}, fmt.Errorf("unknown cTrader client message ID %q", response.ClientMsgID)
	}
	return request, nil
}

func (c *ProtocolClient) HandleOperationResponse(response OperationResponse, expected OperationKind, expectedPayloadType uint32) (PendingRequest, error) {
	if err := response.Validate(expected, c.session.config.AccountID, expectedPayloadType); err != nil {
		return PendingRequest{}, err
	}
	request, ok := c.tracker.Complete(response.ClientMsgID)
	if !ok {
		return PendingRequest{}, fmt.Errorf("unknown cTrader client message ID %q", response.ClientMsgID)
	}
	return request, nil
}

func (c *ProtocolClient) OnDisconnected() []PendingRequest {
	c.session.OnDisconnected()
	if c.recorder != nil {
		c.recorder.Record(AuditEvent{At: time.Now().UTC(), Kind: AuditDisconnected, AccountID: c.session.config.AccountID})
	}
	return c.tracker.Reset()
}

func (c *ProtocolClient) HeartbeatDue(at time.Time) bool {
	return c.session.HeartbeatDue(at)
}

func (c *ProtocolClient) RecordHeartbeat(at time.Time) error {
	return c.session.RecordHeartbeat(at)
}

// HandleHeartbeatTimeout disconnects the session and abandons all pending
// requests when the configured heartbeat deadline has elapsed.
func (c *ProtocolClient) HandleHeartbeatTimeout(at time.Time) []PendingRequest {
	if !c.session.HeartbeatDue(at) {
		return nil
	}
	if c.recorder != nil {
		c.recorder.Record(AuditEvent{At: at.UTC(), Kind: AuditHeartbeatTimeout, AccountID: c.session.config.AccountID})
	}
	return c.OnDisconnected()
}

func (c *ProtocolClient) Reconnect(at time.Time) error {
	err := c.session.OnConnected(at)
	if err == nil && c.recorder != nil {
		c.recorder.Record(AuditEvent{At: at.UTC(), Kind: AuditReconnected, AccountID: c.session.config.AccountID})
	}
	return err
}
