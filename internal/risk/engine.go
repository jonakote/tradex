package risk

import (
	"errors"
	"fmt"
	"time"

	"tradex/internal/config"
	"tradex/internal/domain"
)

type RejectionCode string

const (
	RejectInvalidIntent          RejectionCode = "INVALID_INTENT"
	RejectIntentExpired          RejectionCode = "INTENT_EXPIRED"
	RejectAdapterDisconnected    RejectionCode = "ADAPTER_DISCONNECTED"
	RejectReconciliationMismatch RejectionCode = "RECONCILIATION_MISMATCH"
	RejectStaleMarket            RejectionCode = "STALE_MARKET"
	RejectInvalidAccount         RejectionCode = "INVALID_ACCOUNT"
	RejectTradeRisk              RejectionCode = "TRADE_RISK_LIMIT"
	RejectAggregateRisk          RejectionCode = "AGGREGATE_RISK_LIMIT"
	RejectDailyLoss              RejectionCode = "DAILY_LOSS_LIMIT"
	RejectWeeklyLoss             RejectionCode = "WEEKLY_LOSS_LIMIT"
	RejectDrawdown               RejectionCode = "DRAWDOWN_LIMIT"
	RejectConsecutiveLosses      RejectionCode = "CONSECUTIVE_LOSS_LIMIT"
	RejectRewardRisk             RejectionCode = "REWARD_RISK_LIMIT"
	RejectSpread                 RejectionCode = "SPREAD_LIMIT"
	RejectOpenPositions          RejectionCode = "OPEN_POSITION_LIMIT"
	RejectSymbolPositions        RejectionCode = "SYMBOL_POSITION_LIMIT"
	RejectExposureBucket         RejectionCode = "EXPOSURE_BUCKET_LIMIT"
)

type Request struct {
	Now                    time.Time
	Intent                 domain.OrderIntent
	Market                 domain.MarketSnapshot
	EquityMinor            int64
	EstimatedLossMinor     int64
	EstimatedRewardMinor   int64
	AggregateOpenRiskMinor int64
	DailyPnLMinor          int64
	WeeklyPnLMinor         int64
	DrawdownBPS            int64
	ConsecutiveLosses      int
	OpenPositions          int
	SymbolOpenPositions    int
	BucketOpenRiskMinor    map[string]int64
	ExposureBuckets        []string
	AdapterConnected       bool
	ReconciliationMismatch bool
}

type Decision struct {
	Approved             bool
	Code                 RejectionCode
	Reason               string
	RiskBudget           int64
	AggregateLimit       int64
	EstimatedLossMinor   int64
	EstimatedRewardMinor int64
	ExposureLimitMinor   int64
	BreachedBucket       string
}

type Engine struct{ limits config.Risk }

func New(limits config.Risk) (Engine, error) {
	if err := limits.Validate(); err != nil {
		return Engine{}, err
	}
	return Engine{limits: limits}, nil
}

func (e Engine) Evaluate(req Request) Decision {
	reject := func(code RejectionCode, reason string) Decision {
		return Decision{Code: code, Reason: reason, EstimatedLossMinor: req.EstimatedLossMinor, EstimatedRewardMinor: req.EstimatedRewardMinor}
	}
	if err := req.Intent.Validate(); err != nil {
		return reject(RejectInvalidIntent, err.Error())
	}
	if req.Now.IsZero() || req.Now.After(req.Intent.ExpiresAt) {
		return reject(RejectIntentExpired, "intent is expired or evaluation time is invalid")
	}
	if !req.AdapterConnected {
		return reject(RejectAdapterDisconnected, "broker adapter is disconnected")
	}
	if req.ReconciliationMismatch {
		return reject(RejectReconciliationMismatch, "broker and internal state differ")
	}
	if err := req.Market.Validate(); err != nil {
		return reject(RejectStaleMarket, err.Error())
	}
	if req.Market.Instrument.Symbol != req.Intent.Symbol {
		return reject(RejectInvalidIntent, "intent and market symbols differ")
	}
	instrument := req.Market.Instrument
	if req.Intent.Quantity < instrument.MinQuantity || req.Intent.Quantity > instrument.MaxQuantity ||
		req.Intent.Quantity%instrument.QuantityStep != 0 {
		return reject(RejectInvalidIntent, "intent quantity violates instrument limits")
	}
	entry := req.Intent.EntryPrice
	if req.Intent.Type == domain.OrderTypeMarket {
		if req.Intent.Side == domain.SideBuy {
			entry = req.Market.Ask
		} else {
			entry = req.Market.Bid
		}
	}
	if (req.Intent.Side == domain.SideBuy && (req.Intent.StopLoss >= entry || req.Intent.TakeProfit <= entry)) ||
		(req.Intent.Side == domain.SideSell && (req.Intent.StopLoss <= entry || req.Intent.TakeProfit >= entry)) {
		return reject(RejectInvalidIntent, "stop loss and take profit do not match order direction")
	}
	if age := req.Now.Sub(req.Market.ObservedAt); age < 0 || age > e.limits.MaxQuoteAge {
		return reject(RejectStaleMarket, fmt.Sprintf("quote age %s exceeds %s", age, e.limits.MaxQuoteAge))
	}
	if req.EquityMinor <= 0 || req.EstimatedLossMinor <= 0 || req.EstimatedRewardMinor <= 0 || req.AggregateOpenRiskMinor < 0 ||
		req.OpenPositions < 0 || req.SymbolOpenPositions < 0 || req.SymbolOpenPositions > req.OpenPositions {
		return reject(RejectInvalidAccount, "equity and loss/reward estimates must be positive")
	}

	riskBudget := bpsOf(req.EquityMinor, e.limits.RiskPerTradeBPS)
	aggregateLimit := bpsOf(req.EquityMinor, e.limits.MaxAggregateRiskBPS)
	exposureLimit := bpsOf(req.EquityMinor, e.limits.MaxExposureBucketRiskBPS)
	if req.OpenPositions >= e.limits.MaxOpenPositions {
		return reject(RejectOpenPositions, "maximum open positions reached")
	}
	if req.SymbolOpenPositions >= e.limits.MaxSymbolPositions {
		return reject(RejectSymbolPositions, "maximum same-symbol positions reached")
	}
	if len(req.ExposureBuckets) == 0 {
		return reject(RejectInvalidAccount, "proposed exposure buckets are required")
	}
	seenBuckets := make(map[string]bool, len(req.ExposureBuckets))
	for _, bucket := range req.ExposureBuckets {
		if bucket == "" || seenBuckets[bucket] || req.BucketOpenRiskMinor[bucket] < 0 {
			return reject(RejectInvalidAccount, "exposure buckets or bucket risks are invalid")
		}
		seenBuckets[bucket] = true
		current := req.BucketOpenRiskMinor[bucket]
		if req.EstimatedLossMinor > exposureLimit || current > exposureLimit-req.EstimatedLossMinor {
			decision := reject(RejectExposureBucket, "exposure bucket risk would exceed limit")
			decision.ExposureLimitMinor = exposureLimit
			decision.BreachedBucket = bucket
			return decision
		}
	}
	if req.EstimatedLossMinor > riskBudget {
		return reject(RejectTradeRisk, "estimated loss exceeds per-trade risk budget")
	}
	if req.AggregateOpenRiskMinor+req.EstimatedLossMinor > aggregateLimit {
		return reject(RejectAggregateRisk, "aggregate open risk would exceed limit")
	}
	if req.DailyPnLMinor <= -bpsOf(req.EquityMinor, e.limits.DailyLossStopBPS) {
		return reject(RejectDailyLoss, "daily loss stop reached")
	}
	if req.WeeklyPnLMinor <= -bpsOf(req.EquityMinor, e.limits.WeeklyLossStopBPS) {
		return reject(RejectWeeklyLoss, "weekly loss stop reached")
	}
	if req.DrawdownBPS >= e.limits.DrawdownKillBPS {
		return reject(RejectDrawdown, "drawdown kill switch reached")
	}
	if req.ConsecutiveLosses >= e.limits.MaxConsecutiveLosses {
		return reject(RejectConsecutiveLosses, "consecutive loss limit reached")
	}
	if ratioBPS(req.EstimatedRewardMinor, req.EstimatedLossMinor) < e.limits.MinRewardRiskBPS {
		return reject(RejectRewardRisk, "estimated reward/risk is below minimum")
	}
	if spreadBPS(req.Market.Bid, req.Market.Ask) > e.limits.MaxSpreadBPS {
		return reject(RejectSpread, "market spread exceeds limit")
	}

	return Decision{Approved: true, RiskBudget: riskBudget, AggregateLimit: aggregateLimit,
		EstimatedLossMinor: req.EstimatedLossMinor, EstimatedRewardMinor: req.EstimatedRewardMinor,
		ExposureLimitMinor: exposureLimit}
}

func bpsOf(value, bps int64) int64 {
	return (value/10000)*bps + (value%10000)*bps/10000
}

func ratioBPS(reward, loss int64) int64 {
	if loss <= 0 {
		return 0
	}
	return (reward/loss)*10000 + (reward%loss)*10000/loss
}

func spreadBPS(bid, ask domain.Price) int64 {
	if bid <= 0 || ask < bid {
		return 1<<63 - 1
	}
	return int64((ask - bid) * 10000 / bid)
}

type SizeRequest struct {
	EquityMinor              int64
	RiskBPS                  int64
	LossPerQuantityStepMinor int64
	QuantityStep             int64
	MinQuantity              int64
	MaxQuantity              int64
}

func SizeByRisk(req SizeRequest) (int64, error) {
	if req.EquityMinor <= 0 || req.RiskBPS <= 0 || req.RiskBPS > 10000 ||
		req.LossPerQuantityStepMinor <= 0 || req.QuantityStep <= 0 || req.MinQuantity <= 0 ||
		req.MaxQuantity < req.MinQuantity {
		return 0, errors.New("invalid sizing request")
	}
	budget := bpsOf(req.EquityMinor, req.RiskBPS)
	quantity := budget / req.LossPerQuantityStepMinor * req.QuantityStep
	if quantity > req.MaxQuantity {
		quantity = req.MaxQuantity / req.QuantityStep * req.QuantityStep
	}
	if quantity < req.MinQuantity {
		return 0, errors.New("risk budget cannot fund minimum quantity")
	}
	return quantity, nil
}
