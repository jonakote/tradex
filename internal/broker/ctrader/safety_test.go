package ctrader

import (
	"testing"
	"time"
)

func TestCircuitBreakerTripsAndRecoversHalfOpen(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	b, err := NewCircuitBreaker(2, time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	b.RecordFailure(now)
	if !b.Allow(now) {
		t.Fatal("breaker opened too early")
	}
	b.RecordFailure(now)
	if b.Allow(now) {
		t.Fatal("breaker did not open")
	}
	if !b.Allow(now.Add(time.Minute)) || b.State() != CircuitHalfOpen {
		t.Fatal("breaker did not enter half-open state")
	}
	b.RecordSuccess()
	if b.State() != CircuitClosed || !b.Allow(now) {
		t.Fatal("breaker did not close after success")
	}
}

func TestSafetyGateKillSwitchAndFailureBlocking(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	b, _ := NewCircuitBreaker(1, time.Minute, func() time.Time { return now })
	kill := &KillSwitch{}
	gate, err := NewSafetyGate(b, kill, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.Allow(); err != nil {
		t.Fatal(err)
	}
	gate.RecordFailure()
	if err := gate.Allow(); err == nil {
		t.Fatal("expected circuit block")
	}
	kill.Clear()
	if err := kill.Engage("operator stop"); err != nil {
		t.Fatal(err)
	}
	if err := gate.Allow(); err == nil {
		t.Fatal("expected kill switch block")
	}
}

func TestSafetyGateRecordsBlockingEvents(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	b, _ := NewCircuitBreaker(1, time.Minute, func() time.Time { return now })
	recorder := &MemoryAuditRecorder{}
	gate, err := NewSafetyGate(b, &KillSwitch{}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	gate.AttachAuditRecorder(recorder)
	gate.RecordFailure()
	_ = gate.Allow()
	events := recorder.Events()
	if len(events) != 2 || events[0].Kind != AuditSafetyFailure || events[1].Kind != AuditSafetyBlocked {
		t.Fatalf("events = %#v", events)
	}
}
