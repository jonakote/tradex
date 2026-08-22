package mt5

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// CircuitState represents the state of the circuit breaker.
type CircuitState string

const (
	CircuitClosed   CircuitState = "CLOSED"
	CircuitOpen     CircuitState = "OPEN"
	CircuitHalfOpen CircuitState = "HALF_OPEN"
)

// CircuitBreaker tracks consecutive failures and opens the circuit after
// reaching a threshold. After a cooldown period, one request is allowed
// through (half-open). If it succeeds the circuit closes; if it fails
// the circuit reopens.
type CircuitBreaker struct {
	mu        sync.Mutex
	state     CircuitState
	failures  int
	threshold int
	cooldown  time.Duration
	openedAt  time.Time
}

// NewCircuitBreaker creates a circuit breaker with the given failure threshold
// and cooldown period before allowing a half-open probe.
func NewCircuitBreaker(threshold int, cooldown time.Duration) (*CircuitBreaker, error) {
	if threshold <= 0 || cooldown <= 0 {
		return nil, errors.New("MT5 circuit breaker threshold and cooldown must be positive")
	}
	return &CircuitBreaker{state: CircuitClosed, threshold: threshold, cooldown: cooldown}, nil
}

// Allow returns true if a request is permitted. When the circuit is open,
// it waits until cooldown expires before allowing one half-open probe.
func (b *CircuitBreaker) Allow(at time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state != CircuitOpen {
		return true
	}
	if at.IsZero() || at.Before(b.openedAt.Add(b.cooldown)) {
		return false
	}
	b.state = CircuitHalfOpen
	return true
}

// RecordFailure records a failed request. If the threshold is reached,
// the circuit opens.
func (b *CircuitBreaker) RecordFailure(at time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == CircuitHalfOpen || b.state == CircuitOpen {
		b.state = CircuitOpen
		b.openedAt = at.UTC()
		return
	}
	b.failures++
	if b.failures >= b.threshold {
		b.state = CircuitOpen
		b.openedAt = at.UTC()
	}
}

// RecordSuccess resets the circuit to closed state.
func (b *CircuitBreaker) RecordSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.state = CircuitClosed
	b.failures = 0
	b.openedAt = time.Time{}
}

// State returns the current circuit state.
func (b *CircuitBreaker) State() CircuitState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

// KillSwitch provides a manual emergency stop requiring an operator reason.
type KillSwitch struct {
	mu     sync.RWMutex
	active bool
	reason string
}

// Engage activates the kill switch with a mandatory reason.
func (k *KillSwitch) Engage(reason string) error {
	if reason == "" {
		return errors.New("MT5 kill switch reason is required")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.active = true
	k.reason = reason
	return nil
}

// Clear deactivates the kill switch.
func (k *KillSwitch) Clear() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.active = false
	k.reason = ""
}

// Check returns an error if the kill switch is engaged, nil otherwise.
func (k *KillSwitch) Check() error {
	k.mu.RLock()
	defer k.mu.RUnlock()
	if k.active {
		return fmt.Errorf("MT5 kill switch engaged: %s", k.reason)
	}
	return nil
}

// Active returns true if the kill switch is currently engaged.
func (k *KillSwitch) Active() bool { return k.Check() != nil }

// Reason returns the kill switch reason (empty if not engaged).
func (k *KillSwitch) Reason() string {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.reason
}

// ---------- Audit ----------

// AuditKind identifies the type of operational event.
type AuditKind string

const (
	AuditSafetyBlocked    AuditKind = "MT5_SAFETY_BLOCKED"
	AuditSafetyFailure    AuditKind = "MT5_SAFETY_FAILURE"
	AuditHeartbeatTimeout AuditKind = "MT5_HEARTBEAT_TIMEOUT"
	AuditDisconnected     AuditKind = "MT5_DISCONNECTED"
	AuditReconnected      AuditKind = "MT5_RECONNECTED"
	AuditOrderSubmitted   AuditKind = "MT5_ORDER_SUBMITTED"
	AuditOrderRejected    AuditKind = "MT5_ORDER_REJECTED"
	AuditOrderCancelled   AuditKind = "MT5_ORDER_CANCELLED"
	AuditFillReceived     AuditKind = "MT5_FILL_RECEIVED"
)

// AuditEvent records an operational event without credentials or payloads.
type AuditEvent struct {
	At        time.Time `json:"at"`
	Kind      AuditKind `json:"kind"`
	AccountID string    `json:"account_id,omitempty"`
	RequestID string    `json:"request_id,omitempty"`
	IntentID  string    `json:"intent_id,omitempty"`
	OrderID   string    `json:"order_id,omitempty"`
	Detail    string    `json:"detail,omitempty"`
}

// AuditRecorder receives operational events for recording.
type AuditRecorder interface {
	Record(AuditEvent)
}

// MemoryAuditRecorder stores audit events in memory for testing.
type MemoryAuditRecorder struct {
	mu     sync.Mutex
	events []AuditEvent
}

// Record appends an event to the in-memory log.
func (r *MemoryAuditRecorder) Record(event AuditEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

// Events returns a copy of all recorded events.
func (r *MemoryAuditRecorder) Events() []AuditEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]AuditEvent(nil), r.events...)
}

// ---------- Safety gate ----------

// SafetyGate combines the circuit breaker and kill switch into a single
// pre-request check. It optionally records audit events.
type SafetyGate struct {
	breaker  *CircuitBreaker
	kill     *KillSwitch
	clock    func() time.Time
	recorder AuditRecorder
}

// NewSafetyGate creates a safety gate from a circuit breaker and kill switch.
func NewSafetyGate(breaker *CircuitBreaker, kill *KillSwitch, clock func() time.Time) (*SafetyGate, error) {
	if breaker == nil || kill == nil {
		return nil, errors.New("MT5 safety gate requires circuit breaker and kill switch")
	}
	if clock == nil {
		clock = time.Now
	}
	return &SafetyGate{breaker: breaker, kill: kill, clock: clock}, nil
}

// SetRecorder attaches an audit recorder to the safety gate.
func (g *SafetyGate) SetRecorder(recorder AuditRecorder) {
	g.recorder = recorder
}

// Allow checks both kill switch and circuit breaker. Returns nil if the
// request may proceed, or an error if it must be blocked.
func (g *SafetyGate) Allow() error {
	if err := g.kill.Check(); err != nil {
		if g.recorder != nil {
			g.recorder.Record(AuditEvent{At: g.clock().UTC(), Kind: AuditSafetyBlocked, Detail: err.Error()})
		}
		return err
	}
	if !g.breaker.Allow(g.clock()) {
		msg := "MT5 circuit breaker is open"
		if g.recorder != nil {
			g.recorder.Record(AuditEvent{At: g.clock().UTC(), Kind: AuditSafetyBlocked, Detail: msg})
		}
		return errors.New(msg)
	}
	return nil
}

// RecordFailure records a transport failure in the circuit breaker and audit log.
func (g *SafetyGate) RecordFailure(detail string) {
	at := g.clock().UTC()
	g.breaker.RecordFailure(at)
	if g.recorder != nil {
		g.recorder.Record(AuditEvent{At: at, Kind: AuditSafetyFailure, Detail: detail})
	}
}

// RecordSuccess resets the circuit breaker on a successful request.
func (g *SafetyGate) RecordSuccess() { g.breaker.RecordSuccess() }

// CircuitState returns the current circuit breaker state.
func (g *SafetyGate) CircuitState() CircuitState { return g.breaker.State() }

// KillSwitchActive returns true if the kill switch is engaged.
func (g *SafetyGate) KillSwitchActive() bool { return g.kill.Active() }
