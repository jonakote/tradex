package execution

import (
	"errors"
	"fmt"
	"math"
	"time"

	"tradex/internal/domain"
	"tradex/internal/economics"
	"tradex/internal/risk"
	"tradex/internal/strategy/scan"
)

// RiskContext is the shared-account snapshot handed to the risk engine. It
// is populated by Account.PrepareSetup from the live portfolio view, never
// supplied directly by a caller.
type RiskContext struct {
	EquityMinor            int64
	AggregateOpenRiskMinor int64
	DailyPnLMinor          int64
	WeeklyPnLMinor         int64
	DrawdownBPS            int64
	ConsecutiveLosses      int
	OpenPositions          int
	SymbolOpenPositions    int
	BucketOpenRiskMinor    map[string]int64
	AdapterConnected       bool
	ReconciliationMismatch bool
}

func absPrice(value domain.Price) domain.Price {
	if value < 0 {
		return -value
	}
	return value
}

func multiply(a, b int64) (int64, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	if a == math.MinInt64 || b == math.MinInt64 {
		return 0, false
	}
	result := a * b
	return result, result/b == a
}

func safeAdd(values ...int64) (int64, bool) {
	var total int64
	for _, value := range values {
		if value > 0 && total > math.MaxInt64-value || value < 0 && total < math.MinInt64-value {
			return 0, false
		}
		total += value
	}
	return total, true
}

// SetupPlan prices and sizes a generic scan.Detector Setup into an approved
// order request. It replaces ICCPlan's ICC-specific geometry construction:
// a scan.Setup already carries its own entry and, for a stop/target
// detector, its own concrete stop/target geometry, so SetupPlan's job is
// narrowed to the execution concerns that remain detector-agnostic --
// position sizing, cost economics, and account-currency valuation -- plus
// one explicit, documented policy choice for stopless (hold-bars) setups.
type SetupPlan struct {
	AccountID                       string
	StrategyID                      string
	StrategyVersion                 string
	IntentTTL                       time.Duration
	RiskBPS                         int64
	SlippageTicks                   int64
	RoundTripCommissionPerStepMinor int64
	PriceValue                      economics.PriceValue
	MaxConversionAge                time.Duration

	// StoplessMaxAdverseExcursionTicks is an explicit, conservative EXECUTION
	// POLICY parameter -- it is NOT a measurement and must never be derived
	// from a detector's Setup.RiskTicks. A hold-bars (stopless) setup such as
	// IBS carries no real stop: its RiskTicks is only the R-unit convention
	// scan.Backtest uses to express expectancy, and presenting it to the risk
	// engine as a maximum-loss bound would be exactly the kind of fabricated
	// geometry the migration explicitly forbids.
	//
	// Instead, every stopless setup gets a real, symmetric protective
	// bracket at this many ticks on both sides of the expected fill. It is
	// symmetric specifically so no expected reward is invented to clear the
	// reward:risk gate -- the bracket exists only because paper-broker
	// realism has no primitive for a bar-count time exit (the broker closes
	// positions only by a quote/candle crossing a real price level), so a
	// real worst-case price bound substitutes for the detector's time-based
	// exit. This is a conservative, explicitly-labelled policy choice, flagged
	// as such wherever it is used.
	StoplessMaxAdverseExcursionTicks int64
}

func (p SetupPlan) Validate() error {
	if p.AccountID == "" || p.StrategyID == "" || p.StrategyVersion == "" || p.IntentTTL <= 0 {
		return errors.New("setup execution identity and TTL are required")
	}
	if p.RiskBPS <= 0 || p.RiskBPS > 10000 || p.SlippageTicks < 0 || p.RoundTripCommissionPerStepMinor < 0 {
		return errors.New("invalid setup execution economics")
	}
	if p.StoplessMaxAdverseExcursionTicks <= 0 {
		return errors.New("stopless max adverse excursion policy ticks must be positive")
	}
	return p.PriceValue.Validate()
}

// SetupRequest is one detector Setup proposed for sizing and risk approval.
type SetupRequest struct {
	IntentID   string
	ApprovalID string
	Now        time.Time
	Setup      scan.Setup
	Symbol     string
	Market     domain.MarketSnapshot
	Context    RiskContext
}

// PrepareSetup converts a scan.Setup into a sized intent and submits it to
// the independent risk engine. It returns an ApprovedOrder only when every
// veto passes.
func PrepareSetup(plan SetupPlan, engine risk.Engine, request SetupRequest) (domain.ApprovedOrder, risk.Decision, error) {
	if err := plan.Validate(); err != nil {
		return domain.ApprovedOrder{}, risk.Decision{}, err
	}
	if request.IntentID == "" || request.ApprovalID == "" || request.Now.IsZero() {
		return domain.ApprovedOrder{}, risk.Decision{}, errors.New("setup request identity and time are required")
	}
	if err := request.Market.Validate(); err != nil {
		return domain.ApprovedOrder{}, risk.Decision{}, err
	}
	if err := plan.PriceValue.ValidateAt(request.Now, plan.MaxConversionAge); err != nil {
		return domain.ApprovedOrder{}, risk.Decision{}, fmt.Errorf("validate account-currency value: %w", err)
	}
	if request.Symbol != request.Market.Instrument.Symbol {
		return domain.ApprovedOrder{}, risk.Decision{}, errors.New("setup symbol and market are inconsistent")
	}
	if request.Setup.DetectedAt.IsZero() || request.Now.Before(request.Setup.DetectedAt) {
		return domain.ApprovedOrder{}, risk.Decision{}, errors.New("setup detection time is invalid relative to now")
	}

	instrument := request.Market.Instrument
	slip := domain.Price(plan.SlippageTicks) * instrument.TickSize

	var side domain.Side
	switch request.Setup.Side {
	case "buy":
		side = domain.SideBuy
	case "sell":
		side = domain.SideSell
	default:
		return domain.ApprovedOrder{}, risk.Decision{}, errors.New("unknown setup side")
	}

	var orderType domain.OrderType
	var entryPrice, expectedFill domain.Price
	switch request.Setup.EntryType {
	case "MARKET":
		orderType = domain.OrderTypeMarket
		if side == domain.SideBuy {
			expectedFill = request.Market.Ask + slip
		} else {
			expectedFill = request.Market.Bid - slip
		}
	case "LIMIT":
		orderType = domain.OrderTypeLimit
		entryPrice = request.Setup.Entry
		if side == domain.SideBuy {
			expectedFill = request.Setup.Entry + slip
		} else {
			expectedFill = request.Setup.Entry - slip
		}
	default:
		return domain.ApprovedOrder{}, risk.Decision{}, errors.New("unknown setup entry type")
	}
	if expectedFill <= 0 {
		return domain.ApprovedOrder{}, risk.Decision{}, errors.New("setup expected fill price is not positive")
	}

	var stop, target domain.Price
	if request.Setup.StopTarget() {
		stop, target = request.Setup.Stop, request.Setup.Target
	} else {
		// Stopless setup: install the conservative, symmetric protective
		// bracket documented on StoplessMaxAdverseExcursionTicks above.
		excursion := domain.Price(plan.StoplessMaxAdverseExcursionTicks) * instrument.TickSize
		if side == domain.SideBuy {
			stop, target = expectedFill-excursion, expectedFill+excursion
		} else {
			stop, target = expectedFill+excursion, expectedFill-excursion
		}
	}
	if stop <= 0 || target <= 0 {
		return domain.ApprovedOrder{}, risk.Decision{}, errors.New("setup stop/target geometry is invalid")
	}
	if side == domain.SideBuy && (stop >= expectedFill || target <= expectedFill) {
		return domain.ApprovedOrder{}, risk.Decision{}, errors.New("invalid bullish setup geometry")
	}
	if side == domain.SideSell && (stop <= expectedFill || target >= expectedFill) {
		return domain.ApprovedOrder{}, risk.Decision{}, errors.New("invalid bearish setup geometry")
	}

	riskDistance := absPrice(expectedFill - stop)
	lossValue, err := plan.PriceValue.LossMinor(riskDistance, instrument.QuantityStep, instrument.QuantityStep)
	if err != nil {
		return domain.ApprovedOrder{}, risk.Decision{}, fmt.Errorf("value setup loss: %w", err)
	}
	lossPerStep, ok := safeAdd(lossValue, plan.RoundTripCommissionPerStepMinor)
	if !ok || lossPerStep <= 0 {
		return domain.ApprovedOrder{}, risk.Decision{}, errors.New("setup per-step loss overflow or invalid")
	}

	quantity, err := risk.SizeByRisk(risk.SizeRequest{
		EquityMinor: request.Context.EquityMinor, RiskBPS: plan.RiskBPS,
		LossPerQuantityStepMinor: lossPerStep, QuantityStep: instrument.QuantityStep,
		MinQuantity: instrument.MinQuantity, MaxQuantity: instrument.MaxQuantity,
	})
	if err != nil {
		return domain.ApprovedOrder{}, risk.Decision{}, fmt.Errorf("size setup order: %w", err)
	}
	steps := quantity / instrument.QuantityStep
	estimatedLoss, ok := multiply(lossPerStep, steps)
	if !ok {
		return domain.ApprovedOrder{}, risk.Decision{}, errors.New("setup estimated loss overflow")
	}

	rewardDistance := absPrice(target - expectedFill)
	grossRewardPerStep, err := plan.PriceValue.RewardMinor(rewardDistance, instrument.QuantityStep, instrument.QuantityStep)
	if err != nil || grossRewardPerStep < plan.RoundTripCommissionPerStepMinor {
		return domain.ApprovedOrder{}, risk.Decision{}, errors.New("setup net reward overflow or invalid")
	}
	netRewardPerStep := grossRewardPerStep - plan.RoundTripCommissionPerStepMinor
	estimatedReward, ok := multiply(netRewardPerStep, steps)
	if !ok {
		return domain.ApprovedOrder{}, risk.Decision{}, errors.New("setup estimated reward overflow")
	}

	intent := domain.OrderIntent{
		ID: request.IntentID, StrategyID: plan.StrategyID, StrategyVersion: plan.StrategyVersion,
		AccountID: plan.AccountID, Symbol: request.Symbol, Side: side,
		Type: orderType, Quantity: quantity, EntryPrice: entryPrice, StopLoss: stop, TakeProfit: target,
		SignalTime: request.Setup.DetectedAt, ExpiresAt: request.Setup.DetectedAt.Add(plan.IntentTTL),
	}
	decision := engine.Evaluate(risk.Request{
		Now: request.Now, Intent: intent, Market: request.Market,
		EquityMinor: request.Context.EquityMinor, EstimatedLossMinor: estimatedLoss,
		EstimatedRewardMinor: estimatedReward, AggregateOpenRiskMinor: request.Context.AggregateOpenRiskMinor,
		DailyPnLMinor: request.Context.DailyPnLMinor, WeeklyPnLMinor: request.Context.WeeklyPnLMinor,
		DrawdownBPS: request.Context.DrawdownBPS, ConsecutiveLosses: request.Context.ConsecutiveLosses,
		OpenPositions: request.Context.OpenPositions, SymbolOpenPositions: request.Context.SymbolOpenPositions,
		BucketOpenRiskMinor:    request.Context.BucketOpenRiskMinor,
		ExposureBuckets:        []string{instrument.BaseCurrency, instrument.QuoteCurrency},
		AdapterConnected:       request.Context.AdapterConnected,
		ReconciliationMismatch: request.Context.ReconciliationMismatch,
	})
	if !decision.Approved {
		return domain.ApprovedOrder{}, decision, nil
	}
	approved := domain.ApprovedOrder{Intent: intent, ApprovalID: request.ApprovalID, ApprovedAt: request.Now}
	if err := approved.Validate(); err != nil {
		return domain.ApprovedOrder{}, risk.Decision{}, err
	}
	return approved, decision, nil
}
