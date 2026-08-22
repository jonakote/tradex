package ctrader

import (
	"context"
	"reflect"
	"testing"
	"time"
)

type recordingWriter struct {
	messages []MessageEnvelope
	err      error
}

func (w *recordingWriter) WriteMessage(_ context.Context, message MessageEnvelope) error {
	w.messages = append(w.messages, message)
	return w.err
}

func testSession(t *testing.T) *OpenAPISession {
	t.Helper()
	session, err := NewOpenAPISession(OpenAPIConfig{
		Endpoint: "demo", UseWebSocket: true, Serialization: SerializationProtobuf,
		OAuthClientID: "client", OAuthCredentialReference: "cred", AccessTokenReference: "token",
		AccountID: "acct", HeartbeatInterval: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.OnConnected(time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if err := session.OnApplicationAuthorized(); err != nil {
		t.Fatal(err)
	}
	if err := session.OnAccountAuthorized("acct"); err != nil {
		t.Fatal(err)
	}
	return session
}

func TestProtocolClientSendsAndCorrelates(t *testing.T) {
	writer := &recordingWriter{}
	client, err := NewProtocolClient(testSession(t), NewRequestTracker(nil), writer)
	if err != nil {
		t.Fatal(err)
	}
	request, err := client.Send(context.Background(), 101, []byte{1, 2}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(writer.messages) != 1 || writer.messages[0].ClientMsgID != request.ClientMsgID {
		t.Fatalf("messages = %#v", writer.messages)
	}
	got, err := client.HandleResponse(MessageEnvelope{PayloadType: 102, Payload: []byte{3}, ClientMsgID: request.ClientMsgID})
	if err != nil || !reflect.DeepEqual(got, request) {
		t.Fatalf("response = %#v, err %v", got, err)
	}
}

func TestProtocolClientBlocksMutationBeforeAccountAuthorization(t *testing.T) {
	session, err := NewOpenAPISession(OpenAPIConfig{Endpoint: "demo", Serialization: SerializationJSON, OAuthClientID: "c", OAuthCredentialReference: "r", AccessTokenReference: "a", AccountID: "acct", HeartbeatInterval: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.OnConnected(time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	client, err := NewProtocolClient(session, NewRequestTracker(nil), &recordingWriter{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Send(context.Background(), 1, []byte{1}, true); err == nil {
		t.Fatal("expected mutation gate")
	}
}

func TestProtocolClientDisconnectResetsRequests(t *testing.T) {
	client, err := NewProtocolClient(testSession(t), NewRequestTracker(nil), &recordingWriter{})
	if err != nil {
		t.Fatal(err)
	}
	request, err := client.Send(context.Background(), 1, []byte{1}, false)
	if err != nil {
		t.Fatal(err)
	}
	reset := client.OnDisconnected()
	if len(reset) != 1 || reset[0].ClientMsgID != request.ClientMsgID {
		t.Fatalf("reset = %#v", reset)
	}
	if _, err := client.HandleResponse(MessageEnvelope{PayloadType: 2, Payload: []byte{2}, ClientMsgID: request.ClientMsgID}); err == nil {
		t.Fatal("expected unknown request after disconnect")
	}
}

func TestProtocolClientSendOperationAppliesTypedGate(t *testing.T) {
	client, err := NewProtocolClient(testSession(t), NewRequestTracker(nil), &recordingWriter{})
	if err != nil {
		t.Fatal(err)
	}
	request, err := client.SendOperation(context.Background(), OperationRequest{Kind: OperationSubmitOrder, PayloadType: 10, Payload: []byte{1}, AccountID: "acct", ApprovedOrder: ptrApproved(validApprovedOrder())})
	if err != nil || request.ClientMsgID == "" {
		t.Fatalf("request = %#v, err %v", request, err)
	}
}

func TestProtocolClientHandlesTypedResponseCorrelation(t *testing.T) {
	client, err := NewProtocolClient(testSession(t), NewRequestTracker(nil), &recordingWriter{})
	if err != nil {
		t.Fatal(err)
	}
	request, err := client.SendOperation(context.Background(), OperationRequest{Kind: OperationSnapshot, PayloadType: 30, Payload: []byte{1}, AccountID: "acct"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.HandleOperationResponse(OperationResponse{Kind: OperationSnapshot, PayloadType: 30, ClientMsgID: request.ClientMsgID, AccountID: "acct", CapturedAt: time.Unix(2, 0)}, OperationSnapshot, 30)
	if err != nil || got.ClientMsgID != request.ClientMsgID {
		t.Fatalf("request = %#v, err %v", got, err)
	}
}

func TestProtocolClientHeartbeatTimeoutResetsPendingAndReconnects(t *testing.T) {
	client, err := NewProtocolClient(testSession(t), NewRequestTracker(nil), &recordingWriter{})
	if err != nil {
		t.Fatal(err)
	}
	request, err := client.Send(context.Background(), 1, []byte{1}, false)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Unix(1, 0).UTC()
	if client.HeartbeatDue(at.Add(9 * time.Second)) {
		// testSession starts at Unix second 1 and its interval is 10 seconds.
		t.Fatal("heartbeat should not yet be due")
	}
	reset := client.HandleHeartbeatTimeout(at.Add(10 * time.Second))
	if len(reset) != 1 || reset[0].ClientMsgID != request.ClientMsgID {
		t.Fatalf("reset = %#v", reset)
	}
	if client.HeartbeatDue(at.Add(10 * time.Second)) {
		t.Fatal("disconnected session should not be heartbeat due")
	}
	if err := client.Reconnect(at.Add(20 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := client.RecordHeartbeat(at.Add(21 * time.Second)); err != nil {
		t.Fatal(err)
	}
}

func TestProtocolClientSafetyGateBlocksNewRequests(t *testing.T) {
	client, err := NewProtocolClient(testSession(t), NewRequestTracker(nil), &recordingWriter{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1, 0).UTC()
	breaker, err := NewCircuitBreaker(1, time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	gate, err := NewSafetyGate(breaker, &KillSwitch{}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	client.AttachSafetyGate(gate)
	gate.RecordFailure()
	if _, err := client.Send(context.Background(), 1, []byte{1}, false); err == nil {
		t.Fatal("expected safety gate block")
	}
}

func TestProtocolClientRecordsHeartbeatAndReconnectEvents(t *testing.T) {
	client, err := NewProtocolClient(testSession(t), NewRequestTracker(nil), &recordingWriter{})
	if err != nil {
		t.Fatal(err)
	}
	recorder := &MemoryAuditRecorder{}
	client.AttachAuditRecorder(recorder)
	client.HandleHeartbeatTimeout(time.Unix(11, 0).UTC())
	if err := client.Reconnect(time.Unix(21, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	events := recorder.Events()
	if len(events) != 3 || events[0].Kind != AuditHeartbeatTimeout || events[1].Kind != AuditDisconnected || events[2].Kind != AuditReconnected {
		t.Fatalf("events = %#v", events)
	}
}
