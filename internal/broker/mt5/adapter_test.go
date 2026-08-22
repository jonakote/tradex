package mt5

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

func (f *fakeTransport) Submit(_ context.Context, _ domain.ApprovedOrder) (domain.BrokerOrder, error) {
	f.submitted++
	return domain.BrokerOrder{BrokerOrderID: "mt5-order-1", Status: domain.OrderAcknowledged}, nil
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
	return Config{
		Name:                "exness",
		AccountID:           "ex1",
		Environment:         environment,
		Mode:                mode,
		TerminalHost:        "192.168.1.10",
		TerminalPort:        9100,
		CredentialReference: "keychain://exness-mt5",
		ManifestSHA256:      strings.Repeat("b", 64),
	}
}

func TestConfigValidation(t *testing.T) {
	valid := testConfig(config.BrokerActiveDemo, "paper")
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	t.Run("missing name", func(t *testing.T) {
		c := valid
		c.Name = ""
		if err := c.Validate(); err == nil {
			t.Fatal("accepted empty name")
		}
	})
	t.Run("missing account", func(t *testing.T) {
		c := valid
		c.AccountID = ""
		if err := c.Validate(); err == nil {
			t.Fatal("accepted empty account")
		}
	})
	t.Run("invalid port zero", func(t *testing.T) {
		c := valid
		c.TerminalPort = 0
		if err := c.Validate(); err == nil {
			t.Fatal("accepted zero port")
		}
	})
	t.Run("invalid port high", func(t *testing.T) {
		c := valid
		c.TerminalPort = 70000
		if err := c.Validate(); err == nil {
			t.Fatal("accepted port > 65535")
		}
	})
	t.Run("invalid environment", func(t *testing.T) {
		c := valid
		c.Environment = "staging"
		if err := c.Validate(); err == nil {
			t.Fatal("accepted invalid environment")
		}
	})
	t.Run("invalid mode", func(t *testing.T) {
		c := valid
		c.Mode = "INVALID"
		if err := c.Validate(); err == nil {
			t.Fatal("accepted invalid mode")
		}
	})
	t.Run("live mode in paper env", func(t *testing.T) {
		c := valid
		c.Mode = config.BrokerActiveLive
		if err := c.Validate(); err == nil {
			t.Fatal("accepted live mode in paper environment")
		}
	})
	t.Run("invalid manifest hash", func(t *testing.T) {
		c := valid
		c.ManifestSHA256 = "short"
		if err := c.Validate(); err == nil {
			t.Fatal("accepted invalid hash")
		}
	})
}

func TestLiveAdapterRequiresMatchingPromotion(t *testing.T) {
	transport := &fakeTransport{snapshot: Snapshot{CapturedAt: time.Now().UTC()}}
	if _, err := New(testConfig(config.BrokerActiveLive, "live"), transport, nil); err == nil {
		t.Fatal("live adapter accepted missing promotion")
	}
	approval := &PromotionApproval{
		ManifestSHA256: strings.Repeat("b", 64),
		Operator:       "operator",
		ApprovedAt:     time.Now().UTC(),
		Reason:         "demo-forward gate passed",
	}
	adapter, err := New(testConfig(config.BrokerActiveLive, "live"), transport, approval)
	if err != nil {
		t.Fatal(err)
	}
	// Submission still blocked by reconciliation
	if _, err := adapter.Submit(context.Background(), domain.ApprovedOrder{}); err == nil {
		t.Fatal("submitted before reconciliation")
	}
}

func TestAdapterRequiresTransport(t *testing.T) {
	if _, err := New(testConfig(config.BrokerActiveDemo, "paper"), nil, nil); err == nil {
		t.Fatal("accepted nil transport")
	}
}

func TestReconciliationBlocksMismatchedOrders(t *testing.T) {
	transport := &fakeTransport{
		snapshot: Snapshot{
			Orders:     []domain.BrokerOrder{{BrokerOrderID: "remote", Remaining: 2, Status: domain.OrderAcknowledged}},
			CapturedAt: time.Now().UTC(),
		},
	}
	adapter, err := New(testConfig(config.BrokerActiveDemo, "paper"), transport, nil)
	if err != nil {
		t.Fatal(err)
	}
	reconciliation, err := adapter.Reconcile(context.Background(),
		[]domain.BrokerOrder{{BrokerOrderID: "local", Remaining: 2, Status: domain.OrderAcknowledged}}, nil)
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

func TestReconciliationBlocksMismatchedPositions(t *testing.T) {
	transport := &fakeTransport{
		snapshot: Snapshot{
			Positions:  []domain.BrokerPosition{{BrokerPositionID: "remote-pos", Quantity: 1000}},
			CapturedAt: time.Now().UTC(),
		},
	}
	adapter, err := New(testConfig(config.BrokerActiveDemo, "paper"), transport, nil)
	if err != nil {
		t.Fatal(err)
	}
	reconciliation, err := adapter.Reconcile(context.Background(), nil,
		[]domain.BrokerPosition{{BrokerPositionID: "local-pos", Quantity: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	if reconciliation.Match {
		t.Fatal("mismatched positions reconciled")
	}
}

func TestCancellationBlockedWithoutReconciliation(t *testing.T) {
	transport := &fakeTransport{snapshot: Snapshot{CapturedAt: time.Now().UTC()}}
	adapter, err := New(testConfig(config.BrokerActiveDemo, "paper"), transport, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Cancel(context.Background(), "order-1"); err == nil {
		t.Fatal("cancellation bypassed reconciliation gate")
	}
}

func TestAccountMismatchRejectsSubmission(t *testing.T) {
	transport := &fakeTransport{snapshot: Snapshot{CapturedAt: time.Now().UTC()}}
	adapter, err := New(testConfig(config.BrokerActiveDemo, "paper"), transport, nil)
	if err != nil {
		t.Fatal(err)
	}
	reconciliation, err := adapter.Reconcile(context.Background(), nil, nil)
	if err != nil || !reconciliation.Match {
		t.Fatalf("reconciliation failed: %+v %v", reconciliation, err)
	}
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	approved := domain.ApprovedOrder{
		ApprovalID: "approval",
		ApprovedAt: now,
		Intent: domain.OrderIntent{
			ID: "intent", StrategyID: "icc", StrategyVersion: "v1",
			AccountID: "wrong-account", Symbol: "EURUSD",
			Side: domain.SideBuy, Type: domain.OrderTypeMarket,
			Quantity: 1000, StopLoss: 100, TakeProfit: 200,
			SignalTime: now.Add(-time.Second), ExpiresAt: now.Add(time.Minute),
		},
	}
	if _, err := adapter.Submit(context.Background(), approved); err == nil {
		t.Fatal("accepted order for wrong account")
	}
}

func TestDemoTransportRunsAdapterLifecycleAfterReconciliation(t *testing.T) {
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	transport, err := NewDemoTransport("ex1", func() time.Time { return now })
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
	approved := domain.ApprovedOrder{
		ApprovalID: "approval",
		ApprovedAt: now,
		Intent: domain.OrderIntent{
			ID: "intent-1", StrategyID: "icc", StrategyVersion: "v1",
			AccountID: "ex1", Symbol: "EURUSD",
			Side: domain.SideBuy, Type: domain.OrderTypeMarket,
			Quantity: 1000, StopLoss: 100, TakeProfit: 200,
			SignalTime: now.Add(-time.Second), ExpiresAt: now.Add(time.Minute),
		},
	}
	order, err := adapter.Submit(context.Background(), approved)
	if err != nil {
		t.Fatal(err)
	}
	if order.Status != domain.OrderAcknowledged || order.BrokerOrderID == "" {
		t.Fatalf("unexpected demo order: %+v", order)
	}
	if !strings.HasPrefix(order.BrokerOrderID, "demo-mt5-order-") {
		t.Fatalf("unexpected order ID prefix: %s", order.BrokerOrderID)
	}
	// Idempotent resubmission
	duplicate, err := adapter.Submit(context.Background(), approved)
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.BrokerOrderID != order.BrokerOrderID {
		t.Fatal("idempotent resubmission returned different order")
	}
	// Cancel
	if err := adapter.Cancel(context.Background(), order.BrokerOrderID); err != nil {
		t.Fatal(err)
	}
	// Verify cancelled order no longer appears in open orders
	openOrders, err := adapter.OpenOrders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(openOrders) != 0 {
		t.Fatalf("expected zero open orders after cancel, got %d", len(openOrders))
	}
}

func TestAdapterString(t *testing.T) {
	transport := &fakeTransport{snapshot: Snapshot{CapturedAt: time.Now().UTC()}}
	adapter, err := New(testConfig(config.BrokerActiveDemo, "paper"), transport, nil)
	if err != nil {
		t.Fatal(err)
	}
	expected := "mt5:exness:ex1"
	if adapter.String() != expected {
		t.Fatalf("got %q, want %q", adapter.String(), expected)
	}
}

func TestSnapshotWithoutCaptureTimeFailsReconciliation(t *testing.T) {
	transport := &fakeTransport{snapshot: Snapshot{}} // zero CapturedAt
	adapter, err := New(testConfig(config.BrokerActiveDemo, "paper"), transport, nil)
	if err != nil {
		t.Fatal(err)
	}
	reconciliation, err := adapter.Reconcile(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reconciliation.Match {
		t.Fatal("reconciled snapshot with no capture time")
	}
}
