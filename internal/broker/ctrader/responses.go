package ctrader

import (
	"errors"
	"fmt"
	"time"

	"tradex/internal/domain"
)

// OperationResponse is the typed result produced after a transport decodes a
// cTrader response payload. It contains no protobuf-specific assumptions.
type OperationResponse struct {
	Kind          OperationKind
	PayloadType   uint32
	ClientMsgID   string
	AccountID     string
	Order         *domain.BrokerOrder
	Orders        []domain.BrokerOrder
	Positions     []domain.BrokerPosition
	CapturedAt    time.Time
	ProtocolError *ProtocolError
}

func (r OperationResponse) Validate(expected OperationKind, accountID string, expectedPayloadType uint32) error {
	if r.Kind != expected || r.AccountID != accountID || r.PayloadType == 0 || (expectedPayloadType != 0 && r.PayloadType != expectedPayloadType) {
		return errors.New("cTrader response identity or payload type mismatch")
	}
	if r.ClientMsgID == "" {
		return errors.New("cTrader response client message ID is required")
	}
	if r.ProtocolError != nil {
		return r.ProtocolError
	}
	switch r.Kind {
	case OperationSubmitOrder:
		if r.Order == nil {
			return errors.New("cTrader order response is missing broker order")
		}
		if r.Order.AccountID != accountID || r.Order.BrokerOrderID == "" {
			return errors.New("cTrader order response account or broker ID mismatch")
		}
	case OperationSnapshot:
		if r.CapturedAt.IsZero() {
			return errors.New("cTrader snapshot response capture time is required")
		}
		for _, order := range r.Orders {
			if order.AccountID != accountID || order.BrokerOrderID == "" {
				return errors.New("cTrader snapshot order account or broker ID mismatch")
			}
		}
		for _, position := range r.Positions {
			if position.AccountID != accountID || position.BrokerPositionID == "" {
				return errors.New("cTrader snapshot position account or broker ID mismatch")
			}
		}
	case OperationApplicationAuth, OperationAccountAuth, OperationCancelOrder:
		if r.Order != nil || len(r.Orders) != 0 || len(r.Positions) != 0 {
			return fmt.Errorf("cTrader %s response contains unexpected state", r.Kind)
		}
	default:
		return fmt.Errorf("unsupported cTrader response kind %q", r.Kind)
	}
	return nil
}

func (r OperationResponse) Snapshot() (Snapshot, error) {
	if r.Kind != OperationSnapshot {
		return Snapshot{}, errors.New("cTrader response is not a snapshot")
	}
	if err := r.Validate(OperationSnapshot, r.AccountID, r.PayloadType); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Orders: append([]domain.BrokerOrder(nil), r.Orders...), Positions: append([]domain.BrokerPosition(nil), r.Positions...), CapturedAt: r.CapturedAt.UTC()}, nil
}
