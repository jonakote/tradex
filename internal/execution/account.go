package execution

import (
	"sync"
	"time"

	"tradex/internal/config"
	"tradex/internal/domain"
	"tradex/internal/portfolio"
	"tradex/internal/risk"
)

// Account serializes view, risk evaluation, and reservation so simultaneous
// strategies cannot approve against the same stale portfolio snapshot.
type Account struct {
	mu      sync.Mutex
	risk    risk.Engine
	tracker *portfolio.Tracker
}

func NewAccount(limits config.Risk, initialEquityMinor int64) (*Account, error) {
	engine, err := risk.New(limits)
	if err != nil {
		return nil, err
	}
	tracker, err := portfolio.New(initialEquityMinor)
	if err != nil {
		return nil, err
	}
	return &Account{risk: engine, tracker: tracker}, nil
}

func (a *Account) PrepareICC(plan ICCPlan, request ICCRequest) (domain.ApprovedOrder, risk.Decision, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	view, err := a.tracker.View(request.Now, request.Market.Instrument.Symbol)
	if err != nil {
		return domain.ApprovedOrder{}, risk.Decision{}, err
	}
	request.Context.EquityMinor = view.EquityMinor
	request.Context.AggregateOpenRiskMinor = view.AggregateOpenRiskMinor
	request.Context.DailyPnLMinor = view.DailyPnLMinor
	request.Context.WeeklyPnLMinor = view.WeeklyPnLMinor
	request.Context.DrawdownBPS = view.DrawdownBPS
	request.Context.ConsecutiveLosses = view.ConsecutiveLosses
	request.Context.OpenPositions = view.OpenPositions
	request.Context.SymbolOpenPositions = view.SymbolOpenPositions
	request.Context.BucketOpenRiskMinor = view.BucketOpenRiskMinor
	approved, decision, err := PrepareICC(plan, a.risk, request)
	if err != nil || !decision.Approved {
		return approved, decision, err
	}
	if err := a.tracker.Reserve(request.IntentID, request.Market.Instrument, approved.Intent.Side, decision.EstimatedLossMinor); err != nil {
		return domain.ApprovedOrder{}, risk.Decision{}, err
	}
	return approved, decision, nil
}

func (a *Account) ApplyClosingFill(fill domain.Fill) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.tracker.ApplyClosingFill(fill)
}

func (a *Account) CancelReservation(intentID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.tracker.Release(intentID)
}

func (a *Account) View(at time.Time, symbol string) (portfolio.View, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.tracker.View(at, symbol)
}
