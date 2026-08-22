package ctrader

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

// OAuthToken is the minimum credential set needed for an Open API session.
// Token values must never be written to logs or included in order metadata.
type OAuthToken struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	TokenType    string    `json:"token_type"`
	ExpiresAt    time.Time `json:"expires_at"`
}

func (t OAuthToken) Validate() error {
	if t.AccessToken == "" || t.RefreshToken == "" || t.ExpiresAt.IsZero() {
		return errors.New("cTrader OAuth token requires access token, refresh token, and expiry")
	}
	if t.TokenType != "" && t.TokenType != "Bearer" {
		return fmt.Errorf("unsupported cTrader OAuth token type %q", t.TokenType)
	}
	return nil
}

// NeedsRefresh returns true when expiry is at or inside the safety window.
func (t OAuthToken) NeedsRefresh(now time.Time, skew time.Duration) (bool, error) {
	if err := t.Validate(); err != nil {
		return false, err
	}
	if now.IsZero() || skew < 0 {
		return false, errors.New("token refresh check requires a non-zero time and non-negative skew")
	}
	return !now.Before(t.ExpiresAt.UTC().Add(-skew)), nil
}

// TokenRefresher abstracts the OAuth refresh HTTP exchange. It deliberately
// contains no network implementation so production credentials stay injectable
// and the policy remains deterministic in tests.
type TokenRefresher interface {
	Refresh(context.Context, OAuthToken) (OAuthToken, error)
}

type TokenManager struct {
	mu          sync.Mutex
	token       OAuthToken
	refresher   TokenRefresher
	clock       func() time.Time
	refreshSkew time.Duration
}

func NewTokenManager(token OAuthToken, refresher TokenRefresher, clock func() time.Time, refreshSkew time.Duration) (*TokenManager, error) {
	if err := token.Validate(); err != nil {
		return nil, err
	}
	if refresher == nil {
		return nil, errors.New("cTrader OAuth token refresher is required")
	}
	if clock == nil {
		clock = time.Now
	}
	if refreshSkew < 0 {
		return nil, errors.New("cTrader OAuth refresh skew cannot be negative")
	}
	return &TokenManager{token: token, refresher: refresher, clock: clock, refreshSkew: refreshSkew}, nil
}

// Current returns a usable token, refreshing it atomically when required.
func (m *TokenManager) Current(ctx context.Context) (OAuthToken, error) {
	if ctx == nil {
		return OAuthToken{}, errors.New("cTrader OAuth token context is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	needs, err := m.token.NeedsRefresh(m.clock(), m.refreshSkew)
	if err != nil {
		return OAuthToken{}, err
	}
	if !needs {
		return m.token, nil
	}
	refreshed, err := m.refresher.Refresh(ctx, m.token)
	if err != nil {
		return OAuthToken{}, fmt.Errorf("refresh cTrader OAuth token: %w", err)
	}
	if err := refreshed.Validate(); err != nil {
		return OAuthToken{}, fmt.Errorf("validate refreshed cTrader OAuth token: %w", err)
	}
	m.token = refreshed
	return m.token, nil
}

const tcpFrameHeaderSize = 4

// EncodeTCPFrame creates the cTrader Open API TCP frame: a four-byte
// big-endian payload length followed by the serialized ProtoMessage bytes.
func EncodeTCPFrame(payload []byte) ([]byte, error) {
	if len(payload) == 0 {
		return nil, errors.New("cTrader TCP payload cannot be empty")
	}
	if uint64(len(payload)) > math.MaxUint32 {
		return nil, errors.New("cTrader TCP payload exceeds uint32 frame limit")
	}
	frame := make([]byte, tcpFrameHeaderSize+len(payload))
	binary.BigEndian.PutUint32(frame[:tcpFrameHeaderSize], uint32(len(payload)))
	copy(frame[tcpFrameHeaderSize:], payload)
	return frame, nil
}

// DecodeTCPFrame decodes one frame from input. complete is false when more
// bytes are needed; consumed is the number of bytes to remove on completion.
func DecodeTCPFrame(input []byte, maxPayload int) (payload []byte, consumed int, complete bool, err error) {
	if maxPayload <= 0 {
		return nil, 0, false, errors.New("cTrader TCP max payload must be positive")
	}
	if len(input) < tcpFrameHeaderSize {
		return nil, 0, false, nil
	}
	length := binary.BigEndian.Uint32(input[:tcpFrameHeaderSize])
	if length == 0 || uint64(length) > uint64(maxPayload) {
		return nil, 0, false, fmt.Errorf("invalid cTrader TCP payload length %d", length)
	}
	total := tcpFrameHeaderSize + int(length)
	if len(input) < total {
		return nil, 0, false, nil
	}
	payload = append([]byte(nil), input[tcpFrameHeaderSize:total]...)
	return payload, total, true, nil
}

// EncodeWebSocketMessage returns a detached protobuf payload. WebSocket
// framing (including masking and fragmentation) belongs to the WebSocket SDK.
func EncodeWebSocketMessage(payload []byte) ([]byte, error) {
	if len(payload) == 0 {
		return nil, errors.New("cTrader WebSocket payload cannot be empty")
	}
	return append([]byte(nil), payload...), nil
}
