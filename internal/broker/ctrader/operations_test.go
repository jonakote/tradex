package ctrader

import (
	"testing"
	"time"

	"tradex/internal/domain"
)

func validApprovedOrder() domain.ApprovedOrder {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	return domain.ApprovedOrder{Intent: domain.OrderIntent{ID: "intent-1", StrategyID: "s", StrategyVersion: "v1", AccountID: "acct", Symbol: "EURUSD", Side: domain.SideBuy, Type: domain.OrderTypeMarket, Quantity: 1, StopLoss: 1, TakeProfit: 2, SignalTime: now, ExpiresAt: now.Add(time.Hour)}, ApprovalID: "approval-1", ApprovedAt: now.Add(time.Minute)}
}

func TestOperationRequestValidation(t *testing.T) {
	request := OperationRequest{Kind: OperationSubmitOrder, PayloadType: 1, Payload: []byte{1}, AccountID: "acct", ApprovedOrder: ptrApproved(validApprovedOrder())}
	if err := request.Validate("acct"); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []OperationRequest{{Kind: OperationSubmitOrder, PayloadType: 1, Payload: []byte{1}, AccountID: "acct"}, {Kind: OperationCancelOrder, PayloadType: 1, Payload: []byte{1}, AccountID: "acct"}, {Kind: OperationSnapshot, PayloadType: 1, Payload: []byte{1}, AccountID: "other"}} {
		if err := invalid.Validate("acct"); err == nil {
			t.Fatal("expected invalid operation")
		}
	}
}

func ptrApproved(order domain.ApprovedOrder) *domain.ApprovedOrder { return &order }
