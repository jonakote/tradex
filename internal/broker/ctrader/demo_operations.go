package ctrader

import (
	"context"
	"errors"
	"fmt"

	"tradex/internal/domain"
)

// SubmitOperation applies a typed order request to the deterministic demo
// state machine and returns the decoded response shape used by transports.
func (t *DemoTransport) SubmitOperation(ctx context.Context, request OperationRequest) (OperationResponse, error) {
	if err := request.Validate(t.accountID); err != nil {
		return OperationResponse{}, err
	}
	if request.Kind != OperationSubmitOrder {
		return OperationResponse{}, errors.New("demo submit operation requires submit-order kind")
	}
	order, err := t.Submit(ctx, *request.ApprovedOrder)
	if err != nil {
		return OperationResponse{Kind: request.Kind, PayloadType: request.PayloadType, AccountID: t.accountID, ProtocolError: &ProtocolError{Code: 1, Description: err.Error()}}, err
	}
	return OperationResponse{Kind: request.Kind, PayloadType: request.PayloadType, ClientMsgID: "demo-response-" + order.BrokerOrderID, AccountID: t.accountID, Order: &order}, nil
}

func (t *DemoTransport) CancelOperation(ctx context.Context, request OperationRequest) (OperationResponse, error) {
	if err := request.Validate(t.accountID); err != nil {
		return OperationResponse{}, err
	}
	if request.Kind != OperationCancelOrder {
		return OperationResponse{}, errors.New("demo cancel operation requires cancel-order kind")
	}
	if err := t.Cancel(ctx, request.BrokerOrderID); err != nil {
		return OperationResponse{Kind: request.Kind, PayloadType: request.PayloadType, AccountID: t.accountID, ProtocolError: &ProtocolError{Code: 2, Description: err.Error()}}, err
	}
	return OperationResponse{Kind: request.Kind, PayloadType: request.PayloadType, ClientMsgID: fmt.Sprintf("demo-cancel-%s", request.BrokerOrderID), AccountID: t.accountID}, nil
}

func (t *DemoTransport) SnapshotOperation(ctx context.Context, request OperationRequest) (OperationResponse, error) {
	if err := request.Validate(t.accountID); err != nil {
		return OperationResponse{}, err
	}
	if request.Kind != OperationSnapshot {
		return OperationResponse{}, errors.New("demo snapshot operation requires snapshot kind")
	}
	snapshot, err := t.Snapshot(ctx)
	if err != nil {
		return OperationResponse{}, err
	}
	return OperationResponse{Kind: request.Kind, PayloadType: request.PayloadType, ClientMsgID: "demo-snapshot", AccountID: t.accountID, Orders: snapshot.Orders, Positions: snapshot.Positions, CapturedAt: snapshot.CapturedAt}, nil
}

var _ domain.Broker = (*DemoTransport)(nil)
