package ctrader

import (
	"context"
	"testing"
	"time"
)

func TestDemoTransportTypedSubmitCancelSnapshot(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	transport, err := NewDemoTransport("acct", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	submit, err := transport.SubmitOperation(context.Background(), OperationRequest{Kind: OperationSubmitOrder, PayloadType: 101, Payload: []byte{1}, AccountID: "acct", ApprovedOrder: ptrApproved(validApprovedOrder())})
	if err != nil {
		t.Fatal(err)
	}
	if err := submit.Validate(OperationSubmitOrder, "acct", 101); err != nil {
		t.Fatal(err)
	}
	cancel, err := transport.CancelOperation(context.Background(), OperationRequest{Kind: OperationCancelOrder, PayloadType: 102, Payload: []byte{1}, AccountID: "acct", BrokerOrderID: submit.Order.BrokerOrderID})
	if err != nil {
		t.Fatal(err)
	}
	if err := cancel.Validate(OperationCancelOrder, "acct", 102); err != nil {
		t.Fatal(err)
	}
	snapshot, err := transport.SnapshotOperation(context.Background(), OperationRequest{Kind: OperationSnapshot, PayloadType: 103, Payload: []byte{1}, AccountID: "acct"})
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Validate(OperationSnapshot, "acct", 103); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Orders) != 0 {
		t.Fatalf("cancelled order remained open: %#v", snapshot.Orders)
	}
}

func TestDemoTransportSubmitIsIdempotentByIntent(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	transport, err := NewDemoTransport("acct", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	request := OperationRequest{Kind: OperationSubmitOrder, PayloadType: 101, Payload: []byte{1}, AccountID: "acct", ApprovedOrder: ptrApproved(validApprovedOrder())}
	first, err := transport.SubmitOperation(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := transport.SubmitOperation(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Order.BrokerOrderID != second.Order.BrokerOrderID {
		t.Fatalf("duplicate intent created different orders: %s vs %s", first.Order.BrokerOrderID, second.Order.BrokerOrderID)
	}
	open, err := transport.OpenOrders(context.Background())
	if err != nil || len(open) != 1 {
		t.Fatalf("open orders = %#v, err %v", open, err)
	}
}
