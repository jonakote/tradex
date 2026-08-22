package execution

import (
	"errors"
	"fmt"
	"math"
	"time"

	"tradex/internal/domain"
	"tradex/internal/economics"
	"tradex/internal/features"
	"tradex/internal/risk"
	"tradex/internal/strategy/icc"
)

type ICCPlan struct {
	AccountID                       string
	StrategyID                      string
	StrategyVersion                 string
	IntentTTL                       time.Duration
	RiskBPS                         int64
	RewardRiskBPS                   int64
	StopBufferTicks                 int64
	SlippageTicks                   int64
	RoundTripCommissionPerStepMinor int64
	PriceValue                      economics.PriceValue
	MaxConversionAge                time.Duration
}

func (p ICCPlan) Validate() error {
	if p.AccountID == "" || p.StrategyID == "" || p.StrategyVersion == "" || p.IntentTTL <= 0 {
		return errors.New("ICC execution identity and TTL are required")
	}
	if p.RiskBPS <= 0 || p.RiskBPS > 10000 || p.RewardRiskBPS <= 0 ||
		p.StopBufferTicks < 0 || p.SlippageTicks < 0 ||
		p.RoundTripCommissionPerStepMinor < 0 {
		return errors.New("invalid ICC execution economics")
	}
	return p.PriceValue.Validate()
}

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

type ICCRequest struct {
	IntentID   string
	ApprovalID string
	Now        time.Time
	Signal     icc.Signal
	Market     domain.MarketSnapshot
	Context    RiskContext
}

// PrepareICC converts an evidence-backed ICC signal into a sized intent, then
// submits that intent to the independent risk engine. It returns an
// ApprovedOrder only when every veto passes.
func PrepareICC(plan ICCPlan, engine risk.Engine, request ICCRequest) (domain.ApprovedOrder, risk.Decision, error) {
	if err := plan.Validate(); err != nil {
		return domain.ApprovedOrder{}, risk.Decision{}, err
	}
	if request.IntentID == "" || request.ApprovalID == "" || request.Now.IsZero() {
		return domain.ApprovedOrder{}, risk.Decision{}, errors.New("ICC request identity and time are required")
	}
	if err := request.Market.Validate(); err != nil {
		return domain.ApprovedOrder{}, risk.Decision{}, err
	}
	if err := plan.PriceValue.ValidateAt(request.Now, plan.MaxConversionAge); err != nil {
		return domain.ApprovedOrder{}, risk.Decision{}, fmt.Errorf("validate account-currency value: %w", err)
	}
	if request.Signal.Symbol != request.Market.Instrument.Symbol || request.Signal.KnownAt.IsZero() || request.Now.Before(request.Signal.KnownAt) {
		return domain.ApprovedOrder{}, risk.Decision{}, errors.New("ICC signal and market are inconsistent")
	}

	instrument := request.Market.Instrument
	slip := domain.Price(plan.SlippageTicks) * instrument.TickSize
	buffer := domain.Price(plan.StopBufferTicks) * instrument.TickSize
	var side domain.Side
	var expectedEntry, stop domain.Price
	if request.Signal.Direction == features.BreakBullish {
		side = domain.SideBuy
		expectedEntry = request.Market.Ask + slip
		stop = request.Signal.StopReference - buffer
		if stop <= 0 || stop >= expectedEntry {
			return domain.ApprovedOrder{}, risk.Decision{}, errors.New("invalid bullish ICC stop geometry")
		}
	} else if request.Signal.Direction == features.BreakBearish {
		side = domain.SideSell
		expectedEntry = request.Market.Bid - slip
		stop = request.Signal.StopReference + buffer
		if expectedEntry <= 0 || stop <= expectedEntry {
			return domain.ApprovedOrder{}, risk.Decision{}, errors.New("invalid bearish ICC stop geometry")
		}
	} else {
		return domain.ApprovedOrder{}, risk.Decision{}, errors.New("unknown ICC signal direction")
	}

	distance := absPrice(expectedEntry - stop)
	lossValue, err := plan.PriceValue.LossMinor(distance+slip, instrument.QuantityStep, instrument.QuantityStep)
	if err != nil {
		return domain.ApprovedOrder{}, risk.Decision{}, fmt.Errorf("value ICC loss: %w", err)
	}
	lossPerStep, ok := safeAdd(lossValue, plan.RoundTripCommissionPerStepMinor)
	if !ok || lossPerStep <= 0 {
		return domain.ApprovedOrder{}, risk.Decision{}, errors.New("ICC per-step loss overflow or invalid")
	}

	desiredNetReward, ok := multiplyRatioCeil(lossPerStep, plan.RewardRiskBPS)
	if !ok {
		return domain.ApprovedOrder{}, risk.Decision{}, errors.New("ICC reward value overflow")
	}
	requiredGrossReward, ok := safeAdd(desiredNetReward, plan.RoundTripCommissionPerStepMinor)
	if !ok {
		return domain.ApprovedOrder{}, risk.Decision{}, errors.New("ICC gross reward overflow")
	}
	targetDistancePrice, err := plan.PriceValue.PriceDistanceForMinorCeil(requiredGrossReward)
	if err != nil {
		return domain.ApprovedOrder{}, risk.Decision{}, fmt.Errorf("value ICC target: %w", err)
	}
	targetDistance := int64(targetDistancePrice)
	var target domain.Price
	if side == domain.SideBuy {
		target = expectedEntry + domain.Price(targetDistance)
	} else {
		target = expectedEntry - domain.Price(targetDistance)
	}
	if target <= 0 {
		return domain.ApprovedOrder{}, risk.Decision{}, errors.New("ICC target is invalid")
	}

	quantity, err := risk.SizeByRisk(risk.SizeRequest{
		EquityMinor: request.Context.EquityMinor, RiskBPS: plan.RiskBPS,
		LossPerQuantityStepMinor: lossPerStep, QuantityStep: instrument.QuantityStep,
		MinQuantity: instrument.MinQuantity, MaxQuantity: instrument.MaxQuantity,
	})
	if err != nil {
		return domain.ApprovedOrder{}, risk.Decision{}, fmt.Errorf("size ICC order: %w", err)
	}
	steps := quantity / instrument.QuantityStep
	estimatedLoss, ok := multiply(lossPerStep, steps)
	if !ok {
		return domain.ApprovedOrder{}, risk.Decision{}, errors.New("ICC estimated loss overflow")
	}
	grossRewardPerStep, err := plan.PriceValue.RewardMinor(targetDistancePrice, instrument.QuantityStep, instrument.QuantityStep)
	if err != nil || grossRewardPerStep < plan.RoundTripCommissionPerStepMinor {
		return domain.ApprovedOrder{}, risk.Decision{}, errors.New("ICC net reward overflow or invalid")
	}
	netRewardPerStep := grossRewardPerStep - plan.RoundTripCommissionPerStepMinor
	estimatedReward, ok := multiply(netRewardPerStep, steps)
	if !ok {
		return domain.ApprovedOrder{}, risk.Decision{}, errors.New("ICC estimated reward overflow")
	}

	intent := domain.OrderIntent{
		ID: request.IntentID, StrategyID: plan.StrategyID, StrategyVersion: plan.StrategyVersion,
		AccountID: plan.AccountID, Symbol: request.Signal.Symbol, Side: side,
		Type: domain.OrderTypeMarket, Quantity: quantity, StopLoss: stop, TakeProfit: target,
		SignalTime: request.Signal.KnownAt, ExpiresAt: request.Signal.KnownAt.Add(plan.IntentTTL),
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

func multiplyRatioCeil(value, ratioBPS int64) (int64, bool) {
	product, ok := multiply(value, ratioBPS)
	if !ok {
		return 0, false
	}
	return ceilDiv(product, 10000), true
}

func ceilDiv(value, divisor int64) int64 {
	return value/divisor + boolInt(value%divisor != 0)
}

func boolInt(value bool) int64 {
	if value {
		return 1
	}
	return 0
}
