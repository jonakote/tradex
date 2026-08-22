// Package mt4 implements the Trade Nation MT4 bridge adapter boundary.
//
// The MT4 bridge uses a thin MQL4 Expert Advisor running inside the MetaTrader 4
// terminal. The EA receives signed, expiring OrderIntents from the Go control
// service and independently validates account ID, symbol, maximum size, price age,
// stop distance, intent expiry and duplicate ID before submission. Fills, positions,
// margin and heartbeat events stream back over the same channel.
//
// Trade Nation documents only MT4 Web access as the native Mac path, while desktop
// EAs require a terminal environment. Continuous automation uses a supervised
// Windows VPS node for MetaTrader rather than depending on the development Mac.
// The bridge connects outbound to Go using mutual authentication; MetaTrader ports
// are not exposed publicly.
//
// This package implements the Go-side adapter: configuration, transport interface,
// snapshot reconciliation, and fail-closed submission gates. It does not contain
// the MQL4 EA code, socket dialing, or terminal-specific behavior.
package mt4

import (
	"context"
	"errors"
	"fmt"
	"time"

	"tradex/internal/config"
	"tradex/internal/domain"
)

// Config defines the Trade Nation MT4 bridge adapter identity and deployment parameters.
type Config struct {
	Name                string            `json:"name"`
	AccountID           string            `json:"account_id"`
	Environment         string            `json:"environment"`
	Mode                config.BrokerMode `json:"mode"`
	TerminalHost        string            `json:"terminal_host"`
	TerminalPort        int               `json:"terminal_port"`
	CredentialReference string            `json:"credential_reference"`
	ManifestSHA256      string            `json:"manifest_sha256"`
}

// Validate ensures all required fields are present and semantically valid.
func (c Config) Validate() error {
	if c.Name == "" || c.AccountID == "" || c.TerminalHost == "" || c.CredentialReference == "" || !validHash(c.ManifestSHA256) {
		return errors.New("invalid MT4 adapter identity or credential reference")
	}
	if c.TerminalPort <= 0 || c.TerminalPort > 65535 {
		return errors.New("MT4 terminal port must be between 1 and 65535")
	}
	if c.Environment != "paper" && c.Environment != "live" {
		return errors.New("MT4 environment must be paper or live")
	}
	if c.Mode != config.BrokerActiveDemo && c.Mode != config.BrokerActiveLive && c.Mode != config.BrokerShadow {
		return errors.New("MT4 mode must be shadow, active demo, or active live")
	}
	if c.Environment != "live" && c.Mode == config.BrokerActiveLive {
		return errors.New("MT4 live mode requires live environment")
	}
	return nil
}

// PromotionApproval is required for live-mode activation. It proves that an
// operator explicitly promoted this adapter after demo-forward testing.
type PromotionApproval struct {
	ManifestSHA256 string    `json:"manifest_sha256"`
	Operator       string    `json:"operator"`
	ApprovedAt     time.Time `json:"approved_at"`
	Reason         string    `json:"reason"`
}

// Validate checks that the promotion matches the adapter configuration.
func (p PromotionApproval) Validate(cfg Config) error {
	if cfg.Environment != "live" || cfg.Mode != config.BrokerActiveLive {
		return errors.New("MT4 live promotion requires live active mode")
	}
	if !validHash(p.ManifestSHA256) || p.ManifestSHA256 != cfg.ManifestSHA256 || p.Operator == "" || p.ApprovedAt.IsZero() || p.Reason == "" {
		return errors.New("invalid or mismatched MT4 promotion approval")
	}
	return nil
}

// Snapshot represents the EA's current view of the terminal state.
type Snapshot struct {
	Orders      []domain.BrokerOrder
	Positions   []domain.BrokerPosition
	EquityMinor int64
	CapturedAt  time.Time
}

// Transport is the bridge communication interface. Implementations may use TCP,
// named pipes, or any other channel between the Go service and the MQL4 EA.
// The interface mirrors the broker contract so the adapter can delegate directly.
type Transport interface {
	Submit(context.Context, domain.ApprovedOrder) (domain.BrokerOrder, error)
	Cancel(context.Context, string) error
	OpenOrders(context.Context) ([]domain.BrokerOrder, error)
	Positions(context.Context) ([]domain.BrokerPosition, error)
	Snapshot(context.Context) (Snapshot, error)
}

// Reconciliation records the result of comparing local expectations with the
// terminal snapshot obtained through the EA bridge.
type Reconciliation struct {
	Match     bool      `json:"match"`
	Reasons   []string  `json:"reasons,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
}

// CompareSnapshots compares expected local state against what the EA reported.
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
		if index >= len(observed.Orders) ||
			expectedOrders[index].BrokerOrderID != observed.Orders[index].BrokerOrderID ||
			expectedOrders[index].Remaining != observed.Orders[index].Remaining ||
			expectedOrders[index].Status != observed.Orders[index].Status {
			result.Match = false
			result.Reasons = append(result.Reasons, "open-order state differs")
			break
		}
	}
	for index := range expectedPositions {
		if index >= len(observed.Positions) ||
			expectedPositions[index].BrokerPositionID != observed.Positions[index].BrokerPositionID ||
			expectedPositions[index].Quantity != observed.Positions[index].Quantity {
			result.Match = false
			result.Reasons = append(result.Reasons, "position state differs")
			break
		}
	}
	return result
}

// Adapter is the Trade Nation MT4 bridge adapter implementing domain.Broker.
// It enforces reconciliation before any mutation and account-identity checks
// on every submission.
type Adapter struct {
	config             Config
	transport          Transport
	reconciled         bool
	lastReconciliation Reconciliation
}

// New creates an MT4 adapter. Live mode requires a valid PromotionApproval.
func New(cfg Config, transport Transport, approval *PromotionApproval) (*Adapter, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if transport == nil {
		return nil, errors.New("MT4 transport is required")
	}
	if cfg.Mode == config.BrokerActiveLive {
		if approval == nil {
			return nil, errors.New("MT4 live mode requires promotion approval")
		}
		if err := approval.Validate(cfg); err != nil {
			return nil, err
		}
	}
	return &Adapter{config: cfg, transport: transport}, nil
}

// Reconcile compares local expected state against the terminal snapshot.
// Submission is blocked until reconciliation succeeds.
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

// Submit sends a risk-approved order to the MT4 terminal through the EA bridge.
// It fails closed if reconciliation has not succeeded or the account does not match.
func (a *Adapter) Submit(ctx context.Context, approved domain.ApprovedOrder) (domain.BrokerOrder, error) {
	if err := ctx.Err(); err != nil {
		return domain.BrokerOrder{}, err
	}
	if !a.reconciled {
		return domain.BrokerOrder{}, errors.New("MT4 submission blocked by reconciliation mismatch")
	}
	if approved.Intent.AccountID != a.config.AccountID {
		return domain.BrokerOrder{}, errors.New("MT4 order belongs to another account")
	}
	return a.transport.Submit(ctx, approved)
}

// Cancel requests cancellation of an open order through the EA bridge.
func (a *Adapter) Cancel(ctx context.Context, id string) error {
	if !a.reconciled {
		return errors.New("MT4 cancellation blocked by reconciliation mismatch")
	}
	return a.transport.Cancel(ctx, id)
}

// OpenOrders returns the current open orders from the terminal.
func (a *Adapter) OpenOrders(ctx context.Context) ([]domain.BrokerOrder, error) {
	return a.transport.OpenOrders(ctx)
}

// Positions returns the current open positions from the terminal.
func (a *Adapter) Positions(ctx context.Context) ([]domain.BrokerPosition, error) {
	return a.transport.Positions(ctx)
}

// LastReconciliation returns the most recent reconciliation result.
func (a *Adapter) LastReconciliation() Reconciliation { return a.lastReconciliation }

// String returns a human-readable adapter identifier.
func (a *Adapter) String() string {
	return fmt.Sprintf("mt4:%s:%s", a.config.Name, a.config.AccountID)
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
