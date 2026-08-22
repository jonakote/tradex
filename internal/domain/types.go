package domain

import (
	"context"
	"errors"
	"time"
)

type Side string

const (
	SideBuy  Side = "BUY"
	SideSell Side = "SELL"
)

func (s Side) Valid() bool { return s == SideBuy || s == SideSell }

type OrderType string

const (
	OrderTypeMarket OrderType = "MARKET"
	OrderTypeLimit  OrderType = "LIMIT"
	OrderTypeStop   OrderType = "STOP"
)

func (o OrderType) Valid() bool {
	return o == OrderTypeMarket || o == OrderTypeLimit || o == OrderTypeStop
}

// Price is stored in instrument price units. A EUR/USD price of 1.08432 with
// PriceScale 100000 is represented as 108432.
type Price int64

type Instrument struct {
	Symbol        string
	BaseCurrency  string
	QuoteCurrency string
	PriceScale    int64
	TickSize      Price
	QuantityStep  int64
	MinQuantity   int64
	MaxQuantity   int64
}

func (i Instrument) Validate() error {
	if i.Symbol == "" || i.BaseCurrency == "" || i.QuoteCurrency == "" {
		return errors.New("instrument symbol and currencies are required")
	}
	if i.BaseCurrency == i.QuoteCurrency {
		return errors.New("instrument base and quote currencies must differ")
	}
	if i.PriceScale <= 0 || i.TickSize <= 0 {
		return errors.New("instrument price scale and tick size must be positive")
	}
	if i.QuantityStep <= 0 || i.MinQuantity <= 0 || i.MaxQuantity < i.MinQuantity {
		return errors.New("invalid instrument quantity limits")
	}
	return nil
}

type MarketSnapshot struct {
	Instrument Instrument
	Bid        Price
	Ask        Price
	ObservedAt time.Time
}

func (m MarketSnapshot) Validate() error {
	if err := m.Instrument.Validate(); err != nil {
		return err
	}
	if m.Bid <= 0 || m.Ask <= 0 || m.Ask < m.Bid {
		return errors.New("invalid bid/ask")
	}
	if m.ObservedAt.IsZero() {
		return errors.New("market observation time is required")
	}
	return nil
}

// OrderIntent is a strategy proposal. It is never sent directly to a broker;
// the risk engine must approve it first.
type OrderIntent struct {
	ID              string
	StrategyID      string
	StrategyVersion string
	AccountID       string
	Symbol          string
	Side            Side
	Type            OrderType
	Quantity        int64
	EntryPrice      Price
	StopLoss        Price
	TakeProfit      Price
	SignalTime      time.Time
	ExpiresAt       time.Time
}

func (o OrderIntent) Validate() error {
	if o.ID == "" || o.StrategyID == "" || o.StrategyVersion == "" {
		return errors.New("intent identity and strategy version are required")
	}
	if o.AccountID == "" || o.Symbol == "" {
		return errors.New("intent account and symbol are required")
	}
	if !o.Side.Valid() || !o.Type.Valid() {
		return errors.New("invalid side or order type")
	}
	if o.Quantity <= 0 || o.StopLoss <= 0 || o.TakeProfit <= 0 {
		return errors.New("quantity, stop loss, and take profit must be positive")
	}
	if o.Type != OrderTypeMarket && o.EntryPrice <= 0 {
		return errors.New("pending orders require a positive limit/stop price")
	}
	if o.SignalTime.IsZero() || o.ExpiresAt.IsZero() || !o.ExpiresAt.After(o.SignalTime) {
		return errors.New("invalid intent time window")
	}
	return nil
}

type BrokerOrder struct {
	BrokerOrderID string
	IntentID      string
	AccountID     string
	Symbol        string
	Side          Side
	Type          OrderType
	Quantity      int64
	Remaining     int64
	EntryPrice    Price
	StopLoss      Price
	TakeProfit    Price
	Status        OrderStatus
	RejectReason  string
	SubmittedAt   time.Time
	UpdatedAt     time.Time
}

type BrokerPosition struct {
	BrokerPositionID     string
	BrokerOrderID        string
	IntentID             string
	AccountID            string
	Symbol               string
	Side                 Side
	Quantity             int64
	EntryPrice           Price
	StopLoss             Price
	TakeProfit           Price
	OpenedAt             time.Time
	EntryCommissionMinor int64
	AccruedRolloverMinor int64
	LastRolloverAt       time.Time
}

type OrderStatus string

const (
	OrderAcknowledged    OrderStatus = "ACKNOWLEDGED"
	OrderPartiallyFilled OrderStatus = "PARTIALLY_FILLED"
	OrderFilled          OrderStatus = "FILLED"
	OrderCancelled       OrderStatus = "CANCELLED"
	OrderRejected        OrderStatus = "REJECTED"
)

type FillRole string

const (
	FillEntry      FillRole = "ENTRY"
	FillStopLoss   FillRole = "STOP_LOSS"
	FillTakeProfit FillRole = "TAKE_PROFIT"
)

type Fill struct {
	FillID              string
	BrokerOrderID       string
	BrokerPositionID    string
	IntentID            string
	AccountID           string
	Symbol              string
	Side                Side
	Role                FillRole
	Quantity            int64
	Price               Price
	CommissionMinor     int64
	RolloverMinor       int64
	RealizedGrossMinor  int64
	RealizedNetMinor    int64
	ClosedTradeNetMinor int64
	FilledAt            time.Time
}

// ApprovedOrder can only be constructed after the independent risk engine has
// approved its embedded intent. Broker adapters accept this type, not a raw
// strategy intent.
type ApprovedOrder struct {
	Intent     OrderIntent
	ApprovalID string
	ApprovedAt time.Time
}

func (o ApprovedOrder) Validate() error {
	if err := o.Intent.Validate(); err != nil {
		return err
	}
	if o.ApprovalID == "" || o.ApprovedAt.IsZero() {
		return errors.New("risk approval identity and time are required")
	}
	if o.ApprovedAt.Before(o.Intent.SignalTime) || o.ApprovedAt.After(o.Intent.ExpiresAt) {
		return errors.New("risk approval lies outside intent time window")
	}
	return nil
}

type Broker interface {
	Submit(context.Context, ApprovedOrder) (BrokerOrder, error)
	Cancel(context.Context, string) error
	OpenOrders(context.Context) ([]BrokerOrder, error)
	Positions(context.Context) ([]BrokerPosition, error)
}
