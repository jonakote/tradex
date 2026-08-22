package ctrader

import (
	"sync"
	"time"
)

type AuditKind string

const (
	AuditSafetyBlocked    AuditKind = "SAFETY_BLOCKED"
	AuditSafetyFailure    AuditKind = "SAFETY_FAILURE"
	AuditHeartbeatTimeout AuditKind = "HEARTBEAT_TIMEOUT"
	AuditDisconnected     AuditKind = "DISCONNECTED"
	AuditReconnected      AuditKind = "RECONNECTED"
)

// AuditEvent contains metadata only; credentials and serialized payloads are excluded.
type AuditEvent struct {
	At          time.Time `json:"at"`
	Kind        AuditKind `json:"kind"`
	AccountID   string    `json:"account_id,omitempty"`
	ClientMsgID string    `json:"client_msg_id,omitempty"`
	Detail      string    `json:"detail,omitempty"`
}

type AuditRecorder interface{ Record(AuditEvent) }

type MemoryAuditRecorder struct {
	mu     sync.Mutex
	events []AuditEvent
}

func (r *MemoryAuditRecorder) Record(event AuditEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *MemoryAuditRecorder) Events() []AuditEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]AuditEvent(nil), r.events...)
}
