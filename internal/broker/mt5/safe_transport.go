package mt5

import (
	"context"
	"fmt"
	"time"

	"tradex/internal/domain"
)

// SafeTransport wraps a Transport with a SafetyGate that blocks requests when
// the circuit breaker is open or the kill switch is engaged. It also records
// audit events for operational visibility.
type SafeTransport struct {
	inner    Transport
	gate     *SafetyGate
	recorder AuditRecorder
	clock    func() time.Time
}

// NewSafeTransport wraps an existing transport with safety controls.
func NewSafeTransport(inner Transport, gate *SafetyGate, recorder AuditRecorder, clock func() time.Time) (*SafeTransport, error) {
	if inner == nil || gate == nil {
		return nil, fmt.Errorf("MT5 safe transport requires inner transport and safety gate")
	}
	if clock == nil {
		clock = time.Now
	}
	return &SafeTransport{inner: inner, gate: gate, recorder: recorder, clock: clock}, nil
}

// Submit delegates to the inner transport after safety checks pass.
func (s *SafeTransport) Submit(ctx context.Context, approved domain.ApprovedOrder) (domain.BrokerOrder, error) {
	if err := s.gate.Allow(); err != nil {
		return domain.BrokerOrder{}, err
	}

	order, err := s.inner.Submit(ctx, approved)
	if err != nil {
		s.gate.RecordFailure(fmt.Sprintf("submit failed: %v", err))
		if s.recorder != nil {
			s.recorder.Record(AuditEvent{
				At:        s.clock().UTC(),
				Kind:      AuditOrderRejected,
				IntentID:  approved.Intent.ID,
				AccountID: approved.Intent.AccountID,
				Detail:    err.Error(),
			})
		}
		return domain.BrokerOrder{}, err
	}

	s.gate.RecordSuccess()
	if s.recorder != nil {
		s.recorder.Record(AuditEvent{
			At:        s.clock().UTC(),
			Kind:      AuditOrderSubmitted,
			IntentID:  approved.Intent.ID,
			AccountID: approved.Intent.AccountID,
			OrderID:   order.BrokerOrderID,
		})
	}
	return order, nil
}

// Cancel delegates to the inner transport after safety checks pass.
func (s *SafeTransport) Cancel(ctx context.Context, brokerOrderID string) error {
	if err := s.gate.Allow(); err != nil {
		return err
	}

	err := s.inner.Cancel(ctx, brokerOrderID)
	if err != nil {
		s.gate.RecordFailure(fmt.Sprintf("cancel failed: %v", err))
		return err
	}

	s.gate.RecordSuccess()
	if s.recorder != nil {
		s.recorder.Record(AuditEvent{
			At:      s.clock().UTC(),
			Kind:    AuditOrderCancelled,
			OrderID: brokerOrderID,
		})
	}
	return nil
}

// OpenOrders delegates directly (read-only, no safety gate).
func (s *SafeTransport) OpenOrders(ctx context.Context) ([]domain.BrokerOrder, error) {
	return s.inner.OpenOrders(ctx)
}

// Positions delegates directly (read-only, no safety gate).
func (s *SafeTransport) Positions(ctx context.Context) ([]domain.BrokerPosition, error) {
	return s.inner.Positions(ctx)
}

// Snapshot delegates directly (read-only, no safety gate).
func (s *SafeTransport) Snapshot(ctx context.Context) (Snapshot, error) {
	return s.inner.Snapshot(ctx)
}

// Gate returns the underlying safety gate for external inspection.
func (s *SafeTransport) Gate() *SafetyGate { return s.gate }
