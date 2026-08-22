package ctrader

import (
	"context"
	"errors"
	"fmt"
	"time"

	"tradex/internal/config"
	"tradex/internal/domain"
)

type Config struct {
	Name                string            `json:"name"`
	AccountID           string            `json:"account_id"`
	Environment         string            `json:"environment"`
	Mode                config.BrokerMode `json:"mode"`
	Endpoint            string            `json:"endpoint"`
	ClientID            string            `json:"client_id"`
	CredentialReference string            `json:"credential_reference"`
	ManifestSHA256      string            `json:"manifest_sha256"`
}

func (c Config) Validate() error {
	if c.Name == "" || c.AccountID == "" || c.Endpoint == "" || c.ClientID == "" || c.CredentialReference == "" || !validHash(c.ManifestSHA256) {
		return errors.New("invalid cTrader adapter identity or credential reference")
	}
	if c.Environment != "paper" && c.Environment != "live" {
		return errors.New("cTrader environment must be paper or live")
	}
	if c.Mode != config.BrokerActiveDemo && c.Mode != config.BrokerActiveLive && c.Mode != config.BrokerShadow {
		return errors.New("cTrader mode must be shadow, active demo, or active live")
	}
	if c.Environment != "live" && c.Mode == config.BrokerActiveLive {
		return errors.New("cTrader live mode requires live environment")
	}
	return nil
}

type PromotionApproval struct {
	ManifestSHA256 string    `json:"manifest_sha256"`
	Operator       string    `json:"operator"`
	ApprovedAt     time.Time `json:"approved_at"`
	Reason         string    `json:"reason"`
}

func (p PromotionApproval) Validate(cfg Config) error {
	if cfg.Environment != "live" || cfg.Mode != config.BrokerActiveLive {
		return errors.New("cTrader live promotion requires live active mode")
	}
	if !validHash(p.ManifestSHA256) || p.ManifestSHA256 != cfg.ManifestSHA256 || p.Operator == "" || p.ApprovedAt.IsZero() || p.Reason == "" {
		return errors.New("invalid or mismatched cTrader promotion approval")
	}
	return nil
}

type Snapshot struct {
	Orders      []domain.BrokerOrder
	Positions   []domain.BrokerPosition
	EquityMinor int64
	CapturedAt  time.Time
}

type Transport interface {
	Submit(context.Context, domain.ApprovedOrder) (domain.BrokerOrder, error)
	Cancel(context.Context, string) error
	OpenOrders(context.Context) ([]domain.BrokerOrder, error)
	Positions(context.Context) ([]domain.BrokerPosition, error)
	Snapshot(context.Context) (Snapshot, error)
}

type Reconciliation struct {
	Match     bool      `json:"match"`
	Reasons   []string  `json:"reasons,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
}

func CompareSnapshots(expectedOrders []domain.BrokerOrder, expectedPositions []domain.BrokerPosition, observed Snapshot) Reconciliation {
	result := Reconciliation{Match: true, CheckedAt: observed.CapturedAt}
	if observed.CapturedAt.IsZero() {
		result.Match = false
		result.Reasons = append(result.Reasons, "snapshot has no capture time")
	}
	if len(expectedOrders) != len(observed.Orders) {
		result.Match = false
		result.Reasons = append(result.Reasons, "open-order count differs")
	}
	if len(expectedPositions) != len(observed.Positions) {
		result.Match = false
		result.Reasons = append(result.Reasons, "position count differs")
	}
	for index := range expectedOrders {
		if index >= len(observed.Orders) || expectedOrders[index].BrokerOrderID != observed.Orders[index].BrokerOrderID || expectedOrders[index].Remaining != observed.Orders[index].Remaining || expectedOrders[index].Status != observed.Orders[index].Status {
			result.Match = false
			result.Reasons = append(result.Reasons, "open-order state differs")
			break
		}
	}
	for index := range expectedPositions {
		if index >= len(observed.Positions) || expectedPositions[index].BrokerPositionID != observed.Positions[index].BrokerPositionID || expectedPositions[index].Quantity != observed.Positions[index].Quantity {
			result.Match = false
			result.Reasons = append(result.Reasons, "position state differs")
			break
		}
	}
	return result
}

type Adapter struct {
	config             Config
	transport          Transport
	reconciled         bool
	lastReconciliation Reconciliation
}

func New(cfg Config, transport Transport, approval *PromotionApproval) (*Adapter, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if transport == nil {
		return nil, errors.New("cTrader transport is required")
	}
	if cfg.Mode == config.BrokerActiveLive {
		if approval == nil {
			return nil, errors.New("cTrader live mode requires promotion approval")
		}
		if err := approval.Validate(cfg); err != nil {
			return nil, err
		}
	}
	return &Adapter{config: cfg, transport: transport}, nil
}

func (a *Adapter) Reconcile(ctx context.Context, expectedOrders []domain.BrokerOrder, expectedPositions []domain.BrokerPosition) (Reconciliation, error) {
	if err := ctx.Err(); err != nil {
		return Reconciliation{}, err
	}
	snapshot, err := a.transport.Snapshot(ctx)
	if err != nil {
		a.reconciled = false
		return Reconciliation{}, err
	}
	reconciliation := CompareSnapshots(expectedOrders, expectedPositions, snapshot)
	a.lastReconciliation = reconciliation
	a.reconciled = reconciliation.Match
	return reconciliation, nil
}

func (a *Adapter) Submit(ctx context.Context, approved domain.ApprovedOrder) (domain.BrokerOrder, error) {
	if err := ctx.Err(); err != nil {
		return domain.BrokerOrder{}, err
	}
	if !a.reconciled {
		return domain.BrokerOrder{}, errors.New("cTrader submission blocked by reconciliation mismatch")
	}
	if approved.Intent.AccountID != a.config.AccountID {
		return domain.BrokerOrder{}, errors.New("cTrader order belongs to another account")
	}
	return a.transport.Submit(ctx, approved)
}
func (a *Adapter) Cancel(ctx context.Context, id string) error {
	if !a.reconciled {
		return errors.New("cTrader cancellation blocked by reconciliation mismatch")
	}
	return a.transport.Cancel(ctx, id)
}
func (a *Adapter) OpenOrders(ctx context.Context) ([]domain.BrokerOrder, error) {
	return a.transport.OpenOrders(ctx)
}
func (a *Adapter) Positions(ctx context.Context) ([]domain.BrokerPosition, error) {
	return a.transport.Positions(ctx)
}
func (a *Adapter) LastReconciliation() Reconciliation { return a.lastReconciliation }
func (a *Adapter) String() string {
	return fmt.Sprintf("ctrader:%s:%s", a.config.Name, a.config.AccountID)
}

func validHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

var _ domain.Broker = (*Adapter)(nil)
