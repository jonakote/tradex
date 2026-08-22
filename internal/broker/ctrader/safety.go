package ctrader

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

type CircuitState string

const (
	CircuitClosed   CircuitState = "CLOSED"
	CircuitOpen     CircuitState = "OPEN"
	CircuitHalfOpen CircuitState = "HALF_OPEN"
)

type CircuitBreaker struct {
	mu        sync.Mutex
	state     CircuitState
	failures  int
	threshold int
	cooldown  time.Duration
	openedAt  time.Time
	clock     func() time.Time
}

func NewCircuitBreaker(threshold int, cooldown time.Duration, clock func() time.Time) (*CircuitBreaker, error) {
	if threshold <= 0 || cooldown <= 0 {
		return nil, errors.New("cTrader circuit breaker threshold and cooldown must be positive")
	}
	if clock == nil {
		clock = time.Now
	}
	return &CircuitBreaker{state: CircuitClosed, threshold: threshold, cooldown: cooldown, clock: clock}, nil
}

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

func (b *CircuitBreaker) RecordSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.state = CircuitClosed
	b.failures = 0
	b.openedAt = time.Time{}
}

func (b *CircuitBreaker) State() CircuitState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

type KillSwitch struct {
	mu     sync.RWMutex
	active bool
	reason string
}

func (k *KillSwitch) Engage(reason string) error {
	if reason == "" {
		return errors.New("cTrader kill switch reason is required")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.active, k.reason = true, reason
	return nil
}

func (k *KillSwitch) Clear() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.active, k.reason = false, ""
}

func (k *KillSwitch) Check() error {
	k.mu.RLock()
	defer k.mu.RUnlock()
	if k.active {
		return fmt.Errorf("cTrader kill switch engaged: %s", k.reason)
	}
	return nil
}

func (k *KillSwitch) Active() bool { return k.Check() != nil }

type SafetyGate struct {
	breaker  *CircuitBreaker
	kill     *KillSwitch
	clock    func() time.Time
	recorder AuditRecorder
}

func (g *SafetyGate) AttachAuditRecorder(recorder AuditRecorder) { g.recorder = recorder }

func NewSafetyGate(breaker *CircuitBreaker, kill *KillSwitch, clock func() time.Time) (*SafetyGate, error) {
	if breaker == nil || kill == nil {
		return nil, errors.New("cTrader safety gate requires circuit breaker and kill switch")
	}
	if clock == nil {
		clock = time.Now
	}
	return &SafetyGate{breaker: breaker, kill: kill, clock: clock}, nil
}

func (g *SafetyGate) Allow() error {
	if err := g.kill.Check(); err != nil {
		if g.recorder != nil {
			g.recorder.Record(AuditEvent{At: g.clock().UTC(), Kind: AuditSafetyBlocked, Detail: err.Error()})
		}
		return err
	}
	if !g.breaker.Allow(g.clock()) {
		if g.recorder != nil {
			g.recorder.Record(AuditEvent{At: g.clock().UTC(), Kind: AuditSafetyBlocked, Detail: "cTrader circuit breaker is open"})
		}
		return errors.New("cTrader circuit breaker is open")
	}
	return nil
}

func (g *SafetyGate) RecordFailure() {
	at := g.clock().UTC()
	g.breaker.RecordFailure(at)
	if g.recorder != nil {
		g.recorder.Record(AuditEvent{At: at, Kind: AuditSafetyFailure, Detail: "protocol failure recorded"})
	}
}
func (g *SafetyGate) RecordSuccess()             { g.breaker.RecordSuccess() }
func (g *SafetyGate) CircuitState() CircuitState { return g.breaker.State() }
func (g *SafetyGate) KillSwitchActive() bool     { return g.kill.Active() }
