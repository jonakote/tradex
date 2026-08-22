package ctrader

import (
	"context"
	"strings"
	"testing"
	"time"

	"tradex/internal/config"
	"tradex/internal/domain"
)

type fakeTransport struct {
	snapshot  Snapshot
	submitted int
}

func (f *fakeTransport) Submit(context.Context, domain.ApprovedOrder) (domain.BrokerOrder, error) {
	f.submitted++
	return domain.BrokerOrder{BrokerOrderID: "ct-order-1", Status: domain.OrderAcknowledged}, nil
}
func (f *fakeTransport) Cancel(context.Context, string) error { return nil }
func (f *fakeTransport) OpenOrders(context.Context) ([]domain.BrokerOrder, error) {
	return f.snapshot.Orders, nil
}
func (f *fakeTransport) Positions(context.Context) ([]domain.BrokerPosition, error) {
	return f.snapshot.Positions, nil
}
func (f *fakeTransport) Snapshot(context.Context) (Snapshot, error) { return f.snapshot, nil }

func testConfig(mode config.BrokerMode, environment string) Config {
	return Config{Name: "pepperstone", AccountID: "p1", Environment: environment, Mode: mode, Endpoint: "https://demo.ctrader.example", ClientID: "client", CredentialReference: "keychain://pepperstone", ManifestSHA256: strings.Repeat("a", 64)}
}

func TestLiveAdapterRequiresMatchingPromotion(t *testing.T) {
	transport := &fakeTransport{snapshot: Snapshot{CapturedAt: time.Now().UTC()}}
	if _, err := New(testConfig(config.BrokerActiveLive, "live"), transport, nil); err == nil {
		t.Fatal("live adapter accepted missing promotion")
	}
	approval := &PromotionApproval{ManifestSHA256: strings.Repeat("a", 64), Operator: "operator", ApprovedAt: time.Now().UTC(), Reason: "demo-forward gate passed"}
	adapter, err := New(testConfig(config.BrokerActiveLive, "live"), transport, approval)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Submit(context.Background(), domain.ApprovedOrder{}); err == nil {
		t.Fatal("submitted before reconciliation")
	}
}

func TestReconciliationBlocksMismatchedOrders(t *testing.T) {
	transport := &fakeTransport{snapshot: Snapshot{Orders: []domain.BrokerOrder{{BrokerOrderID: "remote", Remaining: 2, Status: domain.OrderAcknowledged}}, CapturedAt: time.Now().UTC()}}
	adapter, err := New(testConfig(config.BrokerActiveDemo, "paper"), transport, nil)
	if err != nil {
		t.Fatal(err)
	}
	reconciliation, err := adapter.Reconcile(context.Background(), []domain.BrokerOrder{{BrokerOrderID: "local", Remaining: 2, Status: domain.OrderAcknowledged}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reconciliation.Match {
		t.Fatal("mismatched snapshot reconciled")
	}
	if _, err := adapter.Submit(context.Background(), domain.ApprovedOrder{}); err == nil {
		t.Fatal("submission bypassed mismatch gate")
	}
}

func TestDemoTransportRunsAdapterLifecycleAfterReconciliation(t *testing.T) {
	now := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	transport, err := NewDemoTransport("p1", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := New(testConfig(config.BrokerActiveDemo, "paper"), transport, nil)
	if err != nil {
		t.Fatal(err)
	}
	reconciliation, err := adapter.Reconcile(context.Background(), nil, nil)
	if err != nil || !reconciliation.Match {
		t.Fatalf("initial demo reconciliation failed: %+v %v", reconciliation, err)
	}
	approved := domain.ApprovedOrder{ApprovalID: "approval", ApprovedAt: now, Intent: domain.OrderIntent{ID: "intent", StrategyID: "icc", StrategyVersion: "v1", AccountID: "p1", Symbol: "EURUSD", Side: domain.SideBuy, Type: domain.OrderTypeMarket, Quantity: 1000, StopLoss: 100, TakeProfit: 200, SignalTime: now.Add(-time.Second), ExpiresAt: now.Add(time.Minute)}}
	order, err := adapter.Submit(context.Background(), approved)
	if err != nil {
		t.Fatal(err)
	}
	if order.Status != domain.OrderAcknowledged || order.BrokerOrderID == "" {
		t.Fatalf("unexpected demo order: %+v", order)
	}
	if err := adapter.Cancel(context.Background(), order.BrokerOrderID); err != nil {
		t.Fatal(err)
	}
}
