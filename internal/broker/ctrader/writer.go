package ctrader

import (
	"context"
	"errors"
	"io"
	"sync"
)

// ByteSink is implemented by a connected, deadline-managed transport. The
// package deliberately does not create sockets or decide TLS behavior.
type ByteSink interface {
	Write([]byte) (int, error)
}

// TCPMessageWriter applies cTrader's length-prefixed TCP envelope to messages.
type TCPMessageWriter struct {
	mu   sync.Mutex
	sink ByteSink
}

func NewTCPMessageWriter(sink ByteSink) (*TCPMessageWriter, error) {
	if sink == nil {
		return nil, errors.New("cTrader TCP writer requires a byte sink")
	}
	return &TCPMessageWriter{sink: sink}, nil
}

func (w *TCPMessageWriter) WriteMessage(ctx context.Context, message MessageEnvelope) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := message.Validate(); err != nil {
		return err
	}
	frame, err := EncodeTCPFrame(message.Payload)
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := contextError(ctx); err != nil {
		return err
	}
	return writeAll(w.sink, frame)
}

// WebSocketSink is expected to perform WebSocket message framing, masking, and
// fragmentation. The writer only supplies the detached serialized payload.
type WebSocketSink interface {
	WriteMessage([]byte) error
}

type WebSocketMessageWriter struct {
	mu   sync.Mutex
	sink WebSocketSink
}

func NewWebSocketMessageWriter(sink WebSocketSink) (*WebSocketMessageWriter, error) {
	if sink == nil {
		return nil, errors.New("cTrader WebSocket writer requires a message sink")
	}
	return &WebSocketMessageWriter{sink: sink}, nil
}

func (w *WebSocketMessageWriter) WriteMessage(ctx context.Context, message MessageEnvelope) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := message.Validate(); err != nil {
		return err
	}
	payload, err := EncodeWebSocketMessage(message.Payload)
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := contextError(ctx); err != nil {
		return err
	}
	return w.sink.WriteMessage(payload)
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return errors.New("cTrader writer context is required")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func writeAll(sink ByteSink, payload []byte) error {
	for len(payload) > 0 {
		n, err := sink.Write(payload)
		if n < 0 || n > len(payload) {
			return errors.New("cTrader byte sink returned invalid write count")
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		payload = payload[n:]
	}
	return nil
}
