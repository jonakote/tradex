package ctrader

import (
	"errors"
	"time"
)

type Serialization string

const (
	SerializationJSON     Serialization = "JSON"
	SerializationProtobuf Serialization = "PROTOBUF"
)

type OpenAPIConfig struct {
	Endpoint                 string        `json:"endpoint"`
	UseWebSocket             bool          `json:"use_websocket"`
	Serialization            Serialization `json:"serialization"`
	OAuthClientID            string        `json:"oauth_client_id"`
	OAuthCredentialReference string        `json:"oauth_credential_reference"`
	AccessTokenReference     string        `json:"access_token_reference"`
	AccountID                string        `json:"account_id"`
	HeartbeatInterval        time.Duration `json:"heartbeat_interval"`
}

func (c OpenAPIConfig) Validate() error {
	if c.Endpoint == "" || c.OAuthClientID == "" || c.OAuthCredentialReference == "" || c.AccessTokenReference == "" || c.AccountID == "" {
		return errors.New("cTrader Open API endpoint, OAuth references, and account are required")
	}
	if c.Serialization != SerializationJSON && c.Serialization != SerializationProtobuf {
		return errors.New("cTrader Open API serialization must be JSON or PROTOBUF")
	}
	if c.HeartbeatInterval <= 0 || c.HeartbeatInterval > 10*time.Second {
		return errors.New("cTrader Open API heartbeat interval must be positive and no greater than 10 seconds")
	}
	return nil
}

type SessionState string

const (
	SessionDisconnected          SessionState = "DISCONNECTED"
	SessionConnected             SessionState = "CONNECTED"
	SessionApplicationAuthorized SessionState = "APPLICATION_AUTHORIZED"
	SessionAccountAuthorized     SessionState = "ACCOUNT_AUTHORIZED"
)

type OpenAPISession struct {
	config        OpenAPIConfig
	state         SessionState
	lastHeartbeat time.Time
}

func NewOpenAPISession(config OpenAPIConfig) (*OpenAPISession, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &OpenAPISession{config: config, state: SessionDisconnected}, nil
}
func (s *OpenAPISession) OnConnected(at time.Time) error {
	if at.IsZero() {
		return errors.New("cTrader connection time is required")
	}
	if s.state != SessionDisconnected {
		return errors.New("cTrader session is already connected")
	}
	s.state = SessionConnected
	s.lastHeartbeat = at.UTC()
	return nil
}
func (s *OpenAPISession) OnApplicationAuthorized() error {
	if s.state != SessionConnected {
		return errors.New("cTrader application authorization requires a connected session")
	}
	s.state = SessionApplicationAuthorized
	return nil
}
func (s *OpenAPISession) OnAccountAuthorized(accountID string) error {
	if s.state != SessionApplicationAuthorized || accountID == "" || accountID != s.config.AccountID {
		return errors.New("cTrader account authorization is invalid or mismatched")
	}
	s.state = SessionAccountAuthorized
	return nil
}
func (s *OpenAPISession) OnDisconnected()     { s.state = SessionDisconnected }
func (s *OpenAPISession) State() SessionState { return s.state }
func (s *OpenAPISession) CanMutate() bool     { return s.state == SessionAccountAuthorized }
func (s *OpenAPISession) HeartbeatDue(at time.Time) bool {
	return s.state != SessionDisconnected && (s.lastHeartbeat.IsZero() || !at.Before(s.lastHeartbeat.Add(s.config.HeartbeatInterval)))
}
func (s *OpenAPISession) RecordHeartbeat(at time.Time) error {
	if s.state == SessionDisconnected || at.IsZero() {
		return errors.New("heartbeat requires a connected cTrader session")
	}
	s.lastHeartbeat = at.UTC()
	return nil
}
