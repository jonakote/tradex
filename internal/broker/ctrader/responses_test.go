package ctrader

import (
	"testing"
	"time"

	"tradex/internal/domain"
)

func TestOperationResponseValidatesTypedOrder(t *testing.T) {
	response := OperationResponse{Kind: OperationSubmitOrder, PayloadType: 20, ClientMsgID: "tradex-1", AccountID: "acct", Order: &domain.BrokerOrder{BrokerOrderID: "broker-1", AccountID: "acct"}}
	if err := response.Validate(OperationSubmitOrder, "acct", 20); err != nil {
		t.Fatal(err)
	}
	response.Order.AccountID = "other"
	if err := response.Validate(OperationSubmitOrder, "acct", 20); err == nil {
		t.Fatal("expected account mismatch")
	}
}

func TestOperationResponseMapsSnapshot(t *testing.T) {
	at := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	response := OperationResponse{Kind: OperationSnapshot, PayloadType: 30, ClientMsgID: "tradex-2", AccountID: "acct", CapturedAt: at, Orders: []domain.BrokerOrder{{BrokerOrderID: "o", AccountID: "acct"}}, Positions: []domain.BrokerPosition{{BrokerPositionID: "p", AccountID: "acct"}}}
	snapshot, err := response.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.CapturedAt.Equal(at) || len(snapshot.Orders) != 1 || len(snapshot.Positions) != 1 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestOperationResponsePreservesProtocolErrors(t *testing.T) {
	response := OperationResponse{Kind: OperationCancelOrder, PayloadType: 40, ClientMsgID: "tradex-3", AccountID: "acct", ProtocolError: &ProtocolError{Code: 9, Description: "denied"}}
	if err := response.Validate(OperationCancelOrder, "acct", 40); err == nil || err.Error() != "cTrader protocol error code 9: denied" {
		t.Fatalf("error = %v", err)
	}
}
