package ctrader

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// MessageEnvelope is the transport-independent shape of a cTrader ProtoMessage.
// Payload remains opaque until generated protobuf types are introduced. Keeping
// it opaque here prevents the adapter from coupling risk decisions to a wire SDK.
type MessageEnvelope struct {
	PayloadType uint32
	Payload     []byte
	ClientMsgID string
}

func (m MessageEnvelope) Validate() error {
	if m.PayloadType == 0 {
		return errors.New("cTrader message payload type is required")
	}
	if len(m.Payload) == 0 {
		return errors.New("cTrader message payload is required")
	}
	if len(m.ClientMsgID) > 128 {
		return errors.New("cTrader client message ID exceeds 128 characters")
	}
	return nil
}

// ProtocolError represents a broker/API error without assuming a particular
// generated protobuf enum. Retriability is assigned by the transport policy.
type ProtocolError struct {
	Code        int32
	Description string
	PayloadType uint32
	Retriable   bool
}

func (e ProtocolError) Error() string {
	if e.Description == "" {
		return fmt.Sprintf("cTrader protocol error code %d", e.Code)
	}
	return fmt.Sprintf("cTrader protocol error code %d: %s", e.Code, e.Description)
}

type PendingRequest struct {
	ClientMsgID string
	PayloadType uint32
	CreatedAt   time.Time
}

// RequestTracker creates deterministic client message IDs and tracks requests
// until a response, cancellation, or connection reset completes them.
type RequestTracker struct {
	mu      sync.Mutex
	next    uint64
	clock   func() time.Time
	pending map[string]PendingRequest
}

func NewRequestTracker(clock func() time.Time) *RequestTracker {
	if clock == nil {
		clock = time.Now
	}
	return &RequestTracker{clock: clock, pending: make(map[string]PendingRequest)}
}

func (t *RequestTracker) Register(payloadType uint32) (PendingRequest, error) {
	if payloadType == 0 {
		return PendingRequest{}, errors.New("cTrader request payload type is required")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.next++
	id := fmt.Sprintf("tradex-%08d", t.next)
	request := PendingRequest{ClientMsgID: id, PayloadType: payloadType, CreatedAt: t.clock().UTC()}
	t.pending[id] = request
	return request, nil
}

func (t *RequestTracker) Complete(clientMsgID string) (PendingRequest, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	request, ok := t.pending[clientMsgID]
	if ok {
		delete(t.pending, clientMsgID)
	}
	return request, ok
}

// Reset abandons all outstanding requests after a disconnect. The returned
// slice is sorted by registration ID because IDs are monotonically generated.
func (t *RequestTracker) Reset() []PendingRequest {
	t.mu.Lock()
	defer t.mu.Unlock()
	requests := make([]PendingRequest, 0, len(t.pending))
	for _, request := range t.pending {
		requests = append(requests, request)
	}
	t.pending = make(map[string]PendingRequest)
	for i := 1; i < len(requests); i++ {
		for j := i; j > 0 && strings.Compare(requests[j].ClientMsgID, requests[j-1].ClientMsgID) < 0; j-- {
			requests[j], requests[j-1] = requests[j-1], requests[j]
		}
	}
	return requests
}

func (t *RequestTracker) PendingCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.pending)
}
