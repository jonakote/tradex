package ctrader

import (
	"errors"
	"fmt"

	"tradex/internal/domain"
)

type OperationKind string

const (
	OperationApplicationAuth OperationKind = "APPLICATION_AUTH"
	OperationAccountAuth     OperationKind = "ACCOUNT_AUTH"
	OperationSubmitOrder     OperationKind = "SUBMIT_ORDER"
	OperationCancelOrder     OperationKind = "CANCEL_ORDER"
	OperationSnapshot        OperationKind = "SNAPSHOT"
)

// OperationRequest validates the business meaning of a protocol request while
// leaving protobuf serialization to the eventual broker transport.
type OperationRequest struct {
	Kind          OperationKind
	PayloadType   uint32
	Payload       []byte
	AccountID     string
	ApprovedOrder *domain.ApprovedOrder
	BrokerOrderID string
}

func (r OperationRequest) Validate(accountID string) error {
	if r.PayloadType == 0 || len(r.Payload) == 0 || r.AccountID == "" || r.AccountID != accountID {
		return errors.New("invalid cTrader operation identity or payload")
	}
	switch r.Kind {
	case OperationApplicationAuth, OperationAccountAuth, OperationSnapshot:
		if r.ApprovedOrder != nil || r.BrokerOrderID != "" {
			return errors.New("cTrader operation contains fields for another operation kind")
		}
	case OperationSubmitOrder:
		if r.ApprovedOrder == nil {
			return errors.New("cTrader order operation requires risk-approved order")
		}
		if err := r.ApprovedOrder.Validate(); err != nil {
			return fmt.Errorf("validate cTrader approved order: %w", err)
		}
		if r.ApprovedOrder.Intent.AccountID != accountID {
			return errors.New("cTrader approved order account mismatch")
		}
	case OperationCancelOrder:
		if r.BrokerOrderID == "" {
			return errors.New("cTrader cancel operation requires broker order ID")
		}
	default:
		return fmt.Errorf("unsupported cTrader operation kind %q", r.Kind)
	}
	return nil
}
