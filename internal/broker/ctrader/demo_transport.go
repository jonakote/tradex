package ctrader

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"tradex/internal/domain"
)

// DemoTransport is a deterministic remote-state simulator for adapter
// contract tests and demo-forward runs. It is not a cTrader protocol client.
type DemoTransport struct {
	mu        sync.Mutex
	accountID string
	orders    map[string]domain.BrokerOrder
	positions map[string]domain.BrokerPosition
	sequence  uint64
	clock     func() time.Time
}

func NewDemoTransport(accountID string, now func() time.Time) (*DemoTransport, error) {
	if accountID == "" || now == nil {
		return nil, errors.New("demo transport account and clock are required")
	}
	return &DemoTransport{accountID: accountID, orders: make(map[string]domain.BrokerOrder), positions: make(map[string]domain.BrokerPosition), clock: now}, nil
}

func (t *DemoTransport) Submit(ctx context.Context, approved domain.ApprovedOrder) (domain.BrokerOrder, error) {
	if err := ctx.Err(); err != nil {
		return domain.BrokerOrder{}, err
	}
	if err := approved.Validate(); err != nil {
		return domain.BrokerOrder{}, err
	}
	if approved.Intent.AccountID != t.accountID {
		return domain.BrokerOrder{}, errors.New("demo transport account mismatch")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, order := range t.orders {
		if order.IntentID == approved.Intent.ID {
			return order, nil
		}
	}
	t.sequence++
	now := t.clock().UTC()
	order := domain.BrokerOrder{BrokerOrderID: fmt.Sprintf("demo-ct-order-%06d", t.sequence), IntentID: approved.Intent.ID, AccountID: approved.Intent.AccountID, Symbol: approved.Intent.Symbol, Side: approved.Intent.Side, Type: approved.Intent.Type, Quantity: approved.Intent.Quantity, Remaining: approved.Intent.Quantity, EntryPrice: approved.Intent.EntryPrice, StopLoss: approved.Intent.StopLoss, TakeProfit: approved.Intent.TakeProfit, Status: domain.OrderAcknowledged, SubmittedAt: now, UpdatedAt: now}
	t.orders[order.BrokerOrderID] = order
	return order, nil
}

func (t *DemoTransport) Cancel(ctx context.Context, brokerOrderID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	order, exists := t.orders[brokerOrderID]
	if !exists {
		return errors.New("demo order not found")
	}
	if order.Status != domain.OrderAcknowledged && order.Status != domain.OrderPartiallyFilled {
		return errors.New("demo order is not cancellable")
	}
	order.Status = domain.OrderCancelled
	order.UpdatedAt = t.clock().UTC()
	t.orders[brokerOrderID] = order
	return nil
}

func (t *DemoTransport) OpenOrders(ctx context.Context) ([]domain.BrokerOrder, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	result := make([]domain.BrokerOrder, 0)
	for _, order := range t.orders {
		if order.Status == domain.OrderAcknowledged || order.Status == domain.OrderPartiallyFilled {
			result = append(result, order)
		}
	}
	return result, nil
}

func (t *DemoTransport) Positions(ctx context.Context) ([]domain.BrokerPosition, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	result := make([]domain.BrokerPosition, 0, len(t.positions))
	for _, position := range t.positions {
		result = append(result, position)
	}
	return result, nil
}

func (t *DemoTransport) Snapshot(ctx context.Context) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	orders, err := t.OpenOrders(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	positions, err := t.Positions(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Orders: orders, Positions: positions, CapturedAt: t.clock().UTC()}, nil
}
