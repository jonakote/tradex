package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

type BrokerMode string

const (
	BrokerDisabled   BrokerMode = "DISABLED"
	BrokerShadow     BrokerMode = "SHADOW"
	BrokerActiveDemo BrokerMode = "ACTIVE_DEMO"
	BrokerActiveLive BrokerMode = "ACTIVE_LIVE"
)

type Broker struct {
	Name         string     `json:"name"`
	Adapter      string     `json:"adapter"`
	Mode         BrokerMode `json:"mode"`
	AccountID    string     `json:"account_id"`
	BaseCurrency string     `json:"base_currency"`
}

type Risk struct {
	RiskPerTradeBPS          int64         `json:"risk_per_trade_bps"`
	MaxAggregateRiskBPS      int64         `json:"max_aggregate_risk_bps"`
	DailyLossStopBPS         int64         `json:"daily_loss_stop_bps"`
	WeeklyLossStopBPS        int64         `json:"weekly_loss_stop_bps"`
	DrawdownKillBPS          int64         `json:"drawdown_kill_bps"`
	MinRewardRiskBPS         int64         `json:"min_reward_risk_bps"`
	MaxConsecutiveLosses     int           `json:"max_consecutive_losses"`
	MaxQuoteAge              time.Duration `json:"max_quote_age"`
	MaxSpreadBPS             int64         `json:"max_spread_bps"`
	MaxOpenPositions         int           `json:"max_open_positions"`
	MaxSymbolPositions       int           `json:"max_symbol_positions"`
	MaxExposureBucketRiskBPS int64         `json:"max_exposure_bucket_risk_bps"`
}

type Runtime struct {
	Environment string   `json:"environment"`
	Brokers     []Broker `json:"brokers"`
	Risk        Risk     `json:"risk"`
}

func DefaultRisk() Risk {
	return Risk{
		RiskPerTradeBPS: 25, MaxAggregateRiskBPS: 75,
		DailyLossStopBPS: 100, WeeklyLossStopBPS: 250,
		DrawdownKillBPS: 500, MinRewardRiskBPS: 20000,
		MaxConsecutiveLosses: 4, MaxQuoteAge: 5 * time.Second,
		MaxSpreadBPS:     20,
		MaxOpenPositions: 3, MaxSymbolPositions: 1, MaxExposureBucketRiskBPS: 50,
	}
}

func LoadJSON(r io.Reader) (Runtime, error) {
	var cfg Runtime
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return Runtime{}, fmt.Errorf("decode config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Runtime{}, err
	}
	return cfg, nil
}

func (r Runtime) Validate() error {
	if r.Environment != "research" && r.Environment != "paper" && r.Environment != "live" {
		return errors.New("environment must be research, paper, or live")
	}
	if err := r.Risk.Validate(); err != nil {
		return err
	}

	active := 0
	for _, broker := range r.Brokers {
		if broker.Name == "" || broker.Adapter == "" || broker.AccountID == "" || broker.BaseCurrency == "" {
			return errors.New("broker name, adapter, account ID, and base currency are required")
		}
		switch broker.Mode {
		case BrokerDisabled, BrokerShadow:
		case BrokerActiveDemo, BrokerActiveLive:
			active++
		default:
			return fmt.Errorf("broker %s has invalid mode %q", broker.Name, broker.Mode)
		}
		if r.Environment != "live" && broker.Mode == BrokerActiveLive {
			return fmt.Errorf("broker %s cannot be live in %s environment", broker.Name, r.Environment)
		}
	}
	if active > 1 {
		return errors.New("at most one broker may be active")
	}
	return nil
}

func (r Risk) Validate() error {
	if r.RiskPerTradeBPS <= 0 || r.RiskPerTradeBPS > 10000 {
		return errors.New("risk per trade must be between 1 and 10000 bps")
	}
	if r.MaxAggregateRiskBPS < r.RiskPerTradeBPS || r.MaxAggregateRiskBPS > 10000 {
		return errors.New("aggregate risk must cover trade risk and be at most 10000 bps")
	}
	if r.DailyLossStopBPS <= 0 || r.WeeklyLossStopBPS < r.DailyLossStopBPS || r.DrawdownKillBPS <= 0 {
		return errors.New("invalid loss or drawdown limits")
	}
	if r.MinRewardRiskBPS <= 0 || r.MaxConsecutiveLosses <= 0 || r.MaxQuoteAge <= 0 || r.MaxSpreadBPS <= 0 ||
		r.MaxOpenPositions <= 0 || r.MaxSymbolPositions <= 0 || r.MaxSymbolPositions > r.MaxOpenPositions ||
		r.MaxExposureBucketRiskBPS < r.RiskPerTradeBPS || r.MaxExposureBucketRiskBPS > r.MaxAggregateRiskBPS {
		return errors.New("reward/risk, loss streak, quote age, spread, and position limits are invalid")
	}
	return nil
}
