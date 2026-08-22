package ctrader

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
)

type fakeWebSocketSink struct{ payload []byte }

func (s *fakeWebSocketSink) WriteMessage(payload []byte) error {
	s.payload = append([]byte(nil), payload...)
	return nil
}

type shortSink struct{ writes int }

func (s *shortSink) Write(payload []byte) (int, error) {
	s.writes++
	if s.writes == 1 {
		return 1, nil
	}
	return len(payload), nil
}

func TestTCPMessageWriterFramesAndHandlesShortWrites(t *testing.T) {
	var sink bytes.Buffer
	writer, err := NewTCPMessageWriter(&sink)
	if err != nil {
		t.Fatal(err)
	}
	err = writer.WriteMessage(context.Background(), MessageEnvelope{PayloadType: 1, Payload: []byte{4, 5, 6}})
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte{0, 0, 0, 3, 4, 5, 6}; !reflect.DeepEqual(sink.Bytes(), want) {
		t.Fatalf("wire bytes = %v, want %v", sink.Bytes(), want)
	}
	short := &shortSink{}
	writer, err = NewTCPMessageWriter(short)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteMessage(context.Background(), MessageEnvelope{PayloadType: 1, Payload: []byte{1, 2, 3}}); err != nil {
		t.Fatal(err)
	}
}

func TestWebSocketMessageWriterCopiesPayload(t *testing.T) {
	sink := &fakeWebSocketSink{}
	writer, err := NewWebSocketMessageWriter(sink)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteMessage(context.Background(), MessageEnvelope{PayloadType: 1, Payload: []byte{8, 9}}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sink.payload, []byte{8, 9}) {
		t.Fatalf("payload = %v", sink.payload)
	}
}

func TestWritersRejectCancelledContextAndInvalidWrites(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	writer, err := NewTCPMessageWriter(&bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteMessage(ctx, MessageEnvelope{PayloadType: 1, Payload: []byte{1}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if err := writeAll(invalidCountSink{}, []byte{1}); err == nil {
		t.Fatal("expected invalid write count")
	}
}

type invalidCountSink struct{}

func (invalidCountSink) Write([]byte) (int, error) { return 2, nil }
