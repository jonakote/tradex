package domain

import (
	"testing"
	"time"
)

func TestOrderIntentValidate(t *testing.T) {
	now := time.Date(2026, time.August, 18, 10, 0, 0, 0, time.UTC)
	intent := OrderIntent{
		ID: "intent-1", StrategyID: "icc", StrategyVersion: "v1",
		AccountID: "demo", Symbol: "EURUSD", Side: SideBuy,
		Type: OrderTypeMarket, Quantity: 1000, StopLoss: 108000,
		TakeProfit: 110000, SignalTime: now, ExpiresAt: now.Add(time.Minute),
	}

	if err := intent.Validate(); err != nil {
		t.Fatalf("valid intent rejected: %v", err)
	}

	intent.Quantity = 0
	if err := intent.Validate(); err == nil {
		t.Fatal("zero quantity accepted")
	}
}

func TestApprovedOrderRequiresApprovalInsideIntentWindow(t *testing.T) {
	now := time.Date(2026, time.August, 18, 10, 0, 0, 0, time.UTC)
	intent := OrderIntent{
		ID: "intent-1", StrategyID: "icc", StrategyVersion: "v1",
		AccountID: "demo", Symbol: "EURUSD", Side: SideBuy,
		Type: OrderTypeMarket, Quantity: 1000, StopLoss: 108000,
		TakeProfit: 110000, SignalTime: now, ExpiresAt: now.Add(time.Minute),
	}
	approved := ApprovedOrder{Intent: intent, ApprovalID: "risk-1", ApprovedAt: now.Add(time.Second)}
	if err := approved.Validate(); err != nil {
		t.Fatalf("valid approval rejected: %v", err)
	}
	approved.ApprovedAt = now.Add(2 * time.Minute)
	if err := approved.Validate(); err == nil {
		t.Fatal("late approval accepted")
	}
}
