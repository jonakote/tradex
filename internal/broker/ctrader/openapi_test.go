package ctrader

import (
	"testing"
	"time"
)

func TestOpenAPISessionRequiresApplicationAndAccountAuthorization(t *testing.T) {
	config := OpenAPIConfig{Endpoint: "wss://demo", UseWebSocket: true, Serialization: SerializationJSON, OAuthClientID: "client", OAuthCredentialReference: "keychain://secret", AccessTokenReference: "keychain://token", AccountID: "123", HeartbeatInterval: 10 * time.Second}
	session, err := NewOpenAPISession(config)
	if err != nil {
		t.Fatal(err)
	}
	if session.CanMutate() {
		t.Fatal("disconnected session can mutate")
	}
	at := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	if err := session.OnConnected(at); err != nil {
		t.Fatal(err)
	}
	if err := session.OnApplicationAuthorized(); err != nil {
		t.Fatal(err)
	}
	if err := session.OnAccountAuthorized("999"); err == nil {
		t.Fatal("accepted wrong account authorization")
	}
	if err := session.OnAccountAuthorized("123"); err != nil {
		t.Fatal(err)
	}
	if !session.CanMutate() {
		t.Fatal("authorized session cannot mutate")
	}
}

func TestOpenAPISessionHeartbeatDeadline(t *testing.T) {
	config := OpenAPIConfig{Endpoint: "tcp://demo", Serialization: SerializationProtobuf, OAuthClientID: "client", OAuthCredentialReference: "secret", AccessTokenReference: "token", AccountID: "123", HeartbeatInterval: 10 * time.Second}
	session, err := NewOpenAPISession(config)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	if err := session.OnConnected(at); err != nil {
		t.Fatal(err)
	}
	if session.HeartbeatDue(at.Add(9 * time.Second)) {
		t.Fatal("heartbeat due too early")
	}
	if !session.HeartbeatDue(at.Add(10 * time.Second)) {
		t.Fatal("heartbeat not due")
	}
	if err := session.RecordHeartbeat(at.Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if session.HeartbeatDue(at.Add(11 * time.Second)) {
		t.Fatal("heartbeat remained due after recording")
	}
}
