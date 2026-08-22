package ctrader

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type fakeRefresher struct {
	called int
	next   OAuthToken
	err    error
}

func (f *fakeRefresher) Refresh(context.Context, OAuthToken) (OAuthToken, error) {
	f.called++
	return f.next, f.err
}

func testToken(expires time.Time) OAuthToken {
	return OAuthToken{AccessToken: "access", RefreshToken: "refresh", TokenType: "Bearer", ExpiresAt: expires}
}

func TestTokenManagerRefreshesInsideSkewAndPublishesAtomically(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	refresher := &fakeRefresher{next: testToken(now.Add(30 * time.Minute))}
	m, err := NewTokenManager(testToken(now.Add(30*time.Second)), refresher, func() time.Time { return now }, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if refresher.called != 1 || !reflect.DeepEqual(got, refresher.next) {
		t.Fatalf("refresh result = %#v, calls = %d", got, refresher.called)
	}
}

func TestTokenManagerLeavesTokenUnchangedOnRefreshFailure(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	initial := testToken(now.Add(-time.Minute))
	refresher := &fakeRefresher{err: errors.New("temporary outage")}
	m, err := NewTokenManager(initial, refresher, func() time.Time { return now }, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Current(context.Background()); err == nil {
		t.Fatal("expected refresh error")
	}
	if refresher.called != 1 {
		t.Fatalf("refresh calls = %d, want 1", refresher.called)
	}
}

func TestTCPFrameRoundTripAndPartialInput(t *testing.T) {
	payload := []byte{1, 2, 3, 4}
	frame, err := EncodeTCPFrame(payload)
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte{0, 0, 0, 4, 1, 2, 3, 4}; !reflect.DeepEqual(frame, want) {
		t.Fatalf("frame = %v, want %v", frame, want)
	}
	for _, input := range [][]byte{frame[:2], frame[:6]} {
		_, _, complete, err := DecodeTCPFrame(input, 1024)
		if err != nil || complete {
			t.Fatalf("partial decode = complete %v, err %v", complete, err)
		}
	}
	got, consumed, complete, err := DecodeTCPFrame(append(frame, 9, 9), 1024)
	if err != nil || !complete || consumed != len(frame) || !reflect.DeepEqual(got, payload) {
		t.Fatalf("decode = %v, %d, %v, %v", got, consumed, complete, err)
	}
}

func TestTCPFrameRejectsInvalidLengths(t *testing.T) {
	if _, err := EncodeTCPFrame(nil); err == nil {
		t.Fatal("expected empty payload error")
	}
	if _, _, _, err := DecodeTCPFrame([]byte{0, 0, 0, 9}, 8); err == nil {
		t.Fatal("expected max payload error")
	}
}

func TestWebSocketPayloadIsDetached(t *testing.T) {
	source := []byte{7, 8}
	got, err := EncodeWebSocketMessage(source)
	if err != nil {
		t.Fatal(err)
	}
	source[0] = 1
	if !reflect.DeepEqual(got, []byte{7, 8}) {
		t.Fatalf("payload aliasing: %v", got)
	}
}
