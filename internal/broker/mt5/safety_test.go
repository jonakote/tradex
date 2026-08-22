package mt5

import (
	"context"
	"strings"
	"testing"
	"time"

	"tradex/internal/domain"
)

func TestCircuitBreakerClosedAllows(t *testing.T) {
	cb, err := NewCircuitBreaker(3, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !cb.Allow(time.Now()) {
		t.Fatal("closed breaker rejected request")
	}
	if cb.State() != CircuitClosed {
		t.Fatalf("expected CLOSED, got %s", cb.State())
	}
}

func TestCircuitBreakerOpensAfterThreshold(t *testing.T) {
	cb, _ := NewCircuitBreaker(3, 5*time.Second)
	now := time.Now()
	cb.RecordFailure(now)
	cb.RecordFailure(now)
	if cb.State() != CircuitClosed {
		t.Fatal("opened too early")
	}
	cb.RecordFailure(now)
	if cb.State() != CircuitOpen {
		t.Fatalf("expected OPEN, got %s", cb.State())
	}
	if cb.Allow(now.Add(time.Second)) {
		t.Fatal("open breaker allowed before cooldown")
	}
}

func TestCircuitBreakerHalfOpenAfterCooldown(t *testing.T) {
	cb, _ := NewCircuitBreaker(1, 2*time.Second)
	now := time.Now()
	cb.RecordFailure(now)
	if cb.State() != CircuitOpen {
		t.Fatal("not open")
	}
	// After cooldown
	if !cb.Allow(now.Add(3 * time.Second)) {
		t.Fatal("did not allow half-open probe")
	}
	if cb.State() != CircuitHalfOpen {
		t.Fatalf("expected HALF_OPEN, got %s", cb.State())
	}
}

func TestCircuitBreakerReopensOnHalfOpenFailure(t *testing.T) {
	cb, _ := NewCircuitBreaker(1, 2*time.Second)
	now := time.Now()
	cb.RecordFailure(now)
	cb.Allow(now.Add(3 * time.Second)) // half-open
	cb.RecordFailure(now.Add(3 * time.Second))
	if cb.State() != CircuitOpen {
		t.Fatalf("expected re-OPEN, got %s", cb.State())
	}
}

func TestCircuitBreakerResetsOnSuccess(t *testing.T) {
	cb, _ := NewCircuitBreaker(2, 2*time.Second)
	now := time.Now()
	cb.RecordFailure(now)
	cb.RecordSuccess()
	if cb.State() != CircuitClosed {
		t.Fatal("did not reset")
	}
	// Should need threshold failures again
	cb.RecordFailure(now)
	if cb.State() != CircuitClosed {
		t.Fatal("opened after reset with one failure")
	}
}

func TestCircuitBreakerRejectsInvalidConfig(t *testing.T) {
	if _, err := NewCircuitBreaker(0, time.Second); err == nil {
		t.Fatal("accepted zero threshold")
	}
	if _, err := NewCircuitBreaker(3, 0); err == nil {
		t.Fatal("accepted zero cooldown")
	}
}

func TestKillSwitch(t *testing.T) {
	k := &KillSwitch{}
	if k.Active() {
		t.Fatal("fresh kill switch is active")
	}
	if err := k.Check(); err != nil {
		t.Fatal("fresh kill switch check failed")
	}
	if err := k.Engage(""); err == nil {
		t.Fatal("accepted empty reason")
	}
	if err := k.Engage("drawdown breach"); err != nil {
		t.Fatal(err)
	}
	if !k.Active() {
		t.Fatal("engaged kill switch not active")
	}
	if err := k.Check(); err == nil {
		t.Fatal("engaged kill switch passed check")
	}
	if k.Reason() != "drawdown breach" {
		t.Fatalf("wrong reason: %s", k.Reason())
	}
	k.Clear()
	if k.Active() {
		t.Fatal("cleared kill switch still active")
	}
}

func TestSafetyGateBlocksOnKillSwitch(t *testing.T) {
	cb, _ := NewCircuitBreaker(3, 5*time.Second)
	kill := &KillSwitch{}
	recorder := &MemoryAuditRecorder{}
	gate, _ := NewSafetyGate(cb, kill, time.Now)
	gate.SetRecorder(recorder)

	if err := gate.Allow(); err != nil {
		t.Fatalf("blocked when not engaged: %v", err)
	}

	kill.Engage("test stop")
	if err := gate.Allow(); err == nil {
		t.Fatal("allowed through engaged kill switch")
	}

	events := recorder.Events()
	if len(events) != 1 || events[0].Kind != AuditSafetyBlocked {
		t.Fatalf("expected one SAFETY_BLOCKED event, got %+v", events)
	}
}

func TestSafetyGateBlocksOnOpenCircuit(t *testing.T) {
	cb, _ := NewCircuitBreaker(1, 10*time.Second)
	kill := &KillSwitch{}
	now := time.Now()
	gate, _ := NewSafetyGate(cb, kill, func() time.Time { return now })

	cb.RecordFailure(now)
	if err := gate.Allow(); err == nil {
		t.Fatal("allowed through open circuit")
	}
}

func TestSafetyGateRecordFailureOpensCircuit(t *testing.T) {
	cb, _ := NewCircuitBreaker(2, 5*time.Second)
	kill := &KillSwitch{}
	recorder := &MemoryAuditRecorder{}
	gate, _ := NewSafetyGate(cb, kill, time.Now)
	gate.SetRecorder(recorder)

	gate.RecordFailure("timeout")
	gate.RecordFailure("disconnect")
	if cb.State() != CircuitOpen {
		t.Fatal("circuit not open after two failures")
	}
	events := recorder.Events()
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	for _, e := range events {
		if e.Kind != AuditSafetyFailure {
			t.Fatalf("unexpected event kind: %s", e.Kind)
		}
	}
}

func TestSafetyGateRequiresComponents(t *testing.T) {
	cb, _ := NewCircuitBreaker(3, time.Second)
	if _, err := NewSafetyGate(nil, &KillSwitch{}, nil); err == nil {
		t.Fatal("accepted nil breaker")
	}
	if _, err := NewSafetyGate(cb, nil, nil); err == nil {
		t.Fatal("accepted nil kill switch")
	}
}

// ---------- SafeTransport tests ----------

func TestSafeTransportBlocksWhenKillSwitchEngaged(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	inner, _ := NewDemoTransport("acct1", func() time.Time { return now })
	cb, _ := NewCircuitBreaker(3, 5*time.Second)
	kill := &KillSwitch{}
	gate, _ := NewSafetyGate(cb, kill, func() time.Time { return now })
	recorder := &MemoryAuditRecorder{}

	safe, err := NewSafeTransport(inner, gate, recorder, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}

	kill.Engage("emergency stop")

	approved := domain.ApprovedOrder{
		ApprovalID: "a1", ApprovedAt: now,
		Intent: domain.OrderIntent{
			ID: "i1", StrategyID: "icc", StrategyVersion: "v1",
			AccountID: "acct1", Symbol: "EURUSD",
			Side: domain.SideBuy, Type: domain.OrderTypeMarket,
			Quantity: 1000, StopLoss: 100, TakeProfit: 200,
			SignalTime: now.Add(-time.Second), ExpiresAt: now.Add(time.Minute),
		},
	}
	_, err = safe.Submit(context.Background(), approved)
	if err == nil {
		t.Fatal("submitted through engaged kill switch")
	}
	if !strings.Contains(err.Error(), "kill switch") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSafeTransportBlocksWhenCircuitOpen(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	inner, _ := NewDemoTransport("acct1", func() time.Time { return now })
	cb, _ := NewCircuitBreaker(1, 10*time.Second)
	kill := &KillSwitch{}
	gate, _ := NewSafetyGate(cb, kill, func() time.Time { return now })
	recorder := &MemoryAuditRecorder{}

	safe, _ := NewSafeTransport(inner, gate, recorder, func() time.Time { return now })

	// Trip the breaker
	gate.RecordFailure("test failure")

	err := safe.Cancel(context.Background(), "order-1")
	if err == nil {
		t.Fatal("cancel succeeded through open circuit")
	}
	if !strings.Contains(err.Error(), "circuit breaker") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSafeTransportAuditsSuccessfulSubmission(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	inner, _ := NewDemoTransport("acct1", func() time.Time { return now })
	cb, _ := NewCircuitBreaker(3, 5*time.Second)
	kill := &KillSwitch{}
	gate, _ := NewSafetyGate(cb, kill, func() time.Time { return now })
	recorder := &MemoryAuditRecorder{}

	safe, _ := NewSafeTransport(inner, gate, recorder, func() time.Time { return now })

	approved := domain.ApprovedOrder{
		ApprovalID: "a1", ApprovedAt: now,
		Intent: domain.OrderIntent{
			ID: "i1", StrategyID: "icc", StrategyVersion: "v1",
			AccountID: "acct1", Symbol: "EURUSD",
			Side: domain.SideBuy, Type: domain.OrderTypeMarket,
			Quantity: 1000, StopLoss: 100, TakeProfit: 200,
			SignalTime: now.Add(-time.Second), ExpiresAt: now.Add(time.Minute),
		},
	}
	order, err := safe.Submit(context.Background(), approved)
	if err != nil {
		t.Fatal(err)
	}
	if order.BrokerOrderID == "" {
		t.Fatal("no order ID")
	}

	events := recorder.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 audit event, got %d", len(events))
	}
	if events[0].Kind != AuditOrderSubmitted {
		t.Fatalf("expected MT5_ORDER_SUBMITTED, got %s", events[0].Kind)
	}
	if events[0].IntentID != "i1" || events[0].OrderID == "" {
		t.Fatalf("audit event missing IDs: %+v", events[0])
	}
}

func TestSafeTransportReadOnlyBypassesSafety(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	inner, _ := NewDemoTransport("acct1", func() time.Time { return now })
	cb, _ := NewCircuitBreaker(1, 10*time.Second)
	kill := &KillSwitch{}
	gate, _ := NewSafetyGate(cb, kill, func() time.Time { return now })

	safe, _ := NewSafeTransport(inner, gate, nil, func() time.Time { return now })

	// Kill switch and circuit open
	kill.Engage("test")
	gate.RecordFailure("test")

	// Read-only operations still work
	snap, err := safe.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("snapshot blocked: %v", err)
	}
	if snap.CapturedAt.IsZero() {
		t.Fatal("no capture time")
	}

	orders, err := safe.OpenOrders(context.Background())
	if err != nil {
		t.Fatalf("open orders blocked: %v", err)
	}
	_ = orders

	positions, err := safe.Positions(context.Background())
	if err != nil {
		t.Fatalf("positions blocked: %v", err)
	}
	_ = positions
}

func TestSafeTransportRequiresComponents(t *testing.T) {
	cb, _ := NewCircuitBreaker(3, time.Second)
	kill := &KillSwitch{}
	gate, _ := NewSafetyGate(cb, kill, nil)
	inner, _ := NewDemoTransport("a", time.Now)

	if _, err := NewSafeTransport(nil, gate, nil, nil); err == nil {
		t.Fatal("accepted nil inner")
	}
	if _, err := NewSafeTransport(inner, nil, nil, nil); err == nil {
		t.Fatal("accepted nil gate")
	}
}
