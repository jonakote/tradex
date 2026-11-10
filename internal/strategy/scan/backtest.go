package scan

import (
	"errors"
	"fmt"
	"time"

	"tradex/internal/analytics"
	"tradex/internal/domain"
	"tradex/internal/instrument"
)

// Backtest measures any Detector over a candle series with the same
// determinism discipline the whole project uses: a setup is knowable only at
// its confirming bar's close, an ambiguous bar resolves to the stop, costs
// are charged per trade, and one position is held at a time. Results are in R
// for stop/target strategies; hold-exit strategies (IBS) have no stop, so R
// is defined against a notional risk equal to the realized adverse excursion
// is avoided — instead their expectancy is reported in the same R units using
// the entry-to-exit move over a fixed risk unit, which keeps every strategy
// on one comparable axis.

// BTParams fixes a measurement run.
type BTParams struct {
	Costs       Costs
	SpreadTicks int64 // handed to the detector's cost floor
	ExpiryBars  int   // unfilled limit lifetime
	MaxHoldBars int   // time-stop for a stop/target position that stalls
	// RiskUnitTicks defines R for hold-exit strategies that carry no stop:
	// the move is divided by this to express expectancy in R. For a
	// stop/target strategy it is ignored (R comes from the stop distance).
	RiskUnitTicks      int64
	RiskMinorPerTrade  int64
	InitialEquityMinor int64
}

type Costs struct {
	SpreadTicks   int64
	SlippageTicks int64
}

func (c Costs) totalTicks() int64 { return c.SpreadTicks + 2*c.SlippageTicks }

func DefaultBTParams() BTParams {
	return BTParams{
		Costs:              Costs{SpreadTicks: 10, SlippageTicks: 2},
		SpreadTicks:        10,
		ExpiryBars:         8,
		MaxHoldBars:        48,
		RiskUnitTicks:      100,
		RiskMinorPerTrade:  10000,
		InitialEquityMinor: 1000000,
	}
}

func (p BTParams) validate() error {
	if p.ExpiryBars < 1 || p.MaxHoldBars < 1 {
		return errors.New("expiry and max-hold must be positive")
	}
	if p.RiskUnitTicks < 1 {
		return errors.New("risk unit must be positive")
	}
	if p.RiskMinorPerTrade <= 0 || p.InitialEquityMinor <= 0 {
		return errors.New("risk and equity must be positive")
	}
	if p.Costs.SpreadTicks < 0 || p.Costs.SlippageTicks < 0 || p.SpreadTicks < 0 {
		return errors.New("costs cannot be negative")
	}
	return nil
}

// BTResult is one measured run.
type BTResult struct {
	Strategy        string           `json:"strategy"`
	Spec            string           `json:"spec"`
	Symbol          string           `json:"symbol"`
	From            time.Time        `json:"from"`
	To              time.Time        `json:"to"`
	Armed           int              `json:"armed"`
	Filled          int              `json:"filled"`
	Expired         int              `json:"expired"`
	Wins            int              `json:"wins"`
	Report          analytics.Report `json:"report"`
	NetRBps         int64            `json:"net_r_bps"`
	ExpectancyRBps  int64            `json:"expectancy_r_bps"`
	MaxDrawdownRBps int64            `json:"max_drawdown_r_bps"`
	AmbiguousStops  int              `json:"ambiguous_bars_resolved_to_stop"`
	StopExits       int              `json:"stop_exits"`
	TargetExits     int              `json:"target_exits"`
	TimeExits       int              `json:"time_exits"`
}

type pending struct {
	setup      Setup
	armedIndex int
	filled     bool
	fillIndex  int
	filledAt   time.Time
}

func (p pending) short() bool { return p.setup.Side == "sell" }

// Backtest runs det over series and returns the measured result plus the
// synthetic fills (for analytics reuse).
func Backtest(symbol string, spec instrument.Spec, timeframe time.Duration,
	candles []domain.Candle, det Detector, params BTParams) (BTResult, []domain.Fill, error) {
	return backtest(symbol, spec, timeframe, candles, det, params, nil)
}

// BacktestFiltered is Backtest with an admission gate applied to every
// detected setup before it is armed — e.g. scan.HTFTrendAdmit, to measure
// whether a higher-timeframe trend filter changes a detector's expectancy.
// A nil admit behaves exactly like Backtest.
func BacktestFiltered(symbol string, spec instrument.Spec, timeframe time.Duration,
	candles []domain.Candle, det Detector, params BTParams,
	admit func(Setup) (bool, error)) (BTResult, []domain.Fill, error) {
	return backtest(symbol, spec, timeframe, candles, det, params, admit)
}

func backtest(symbol string, spec instrument.Spec, timeframe time.Duration,
	candles []domain.Candle, det Detector, params BTParams,
	admit func(Setup) (bool, error)) (BTResult, []domain.Fill, error) {

	if err := params.validate(); err != nil {
		return BTResult{}, nil, err
	}
	if det == nil {
		return BTResult{}, nil, errors.New("detector is required")
	}
	required := det.RequiredBars()
	if len(candles) <= required {
		return BTResult{}, nil, fmt.Errorf("series has %d bars, need more than %d", len(candles), required)
	}

	result := BTResult{Strategy: det.Name(), Spec: det.Spec(), Symbol: symbol}
	if len(candles) > 0 {
		result.From = candles[0].OpenTime
		result.To = candles[len(candles)-1].CloseTime
	}

	stream, err := NewStream(symbol, spec, timeframe, det, params)
	if err != nil {
		return BTResult{}, nil, err
	}
	var (
		fills      []domain.Fill
		equityRBps int64
		peakRBps   int64
		sequence   int
	)

	book := func(p pending, exit domain.Price, resolvedAt time.Time, reason string) {
		switch reason {
		case "STOP_LOSS":
			result.StopExits++
		case "TAKE_PROFIT":
			result.TargetExits++
		case "TIME_EXIT":
			result.TimeExits++
		}
		var riskTicks int64
		if p.setup.StopTarget() {
			riskTicks = p.setup.RiskTicks
		} else {
			riskTicks = params.RiskUnitTicks
		}
		move := int64(exit) - int64(p.setup.Entry)
		if p.short() {
			move = -move
		}
		grossRBps := int64(0)
		if riskTicks > 0 {
			grossRBps = move * 10000 / riskTicks
		}
		costRBps := int64(0)
		if riskTicks > 0 {
			costRBps = params.Costs.totalTicks() * 10000 / riskTicks
		}
		netRBps := grossRBps - costRBps
		pnlMinor := netRBps * params.RiskMinorPerTrade / 10000

		sequence++
		base := domain.Fill{FillID: fmt.Sprintf("%s-%s-%d", det.Name(), symbol, sequence),
			Symbol: symbol, AccountID: "scan", Quantity: 1}
		entry := base
		entry.Role = domain.FillEntry
		entry.Price = p.setup.Entry
		entry.FilledAt = p.filledAt
		closing := base
		closing.Role = domain.FillTakeProfit
		if netRBps < 0 {
			closing.Role = domain.FillStopLoss
		}
		closing.Price = exit
		closing.FilledAt = resolvedAt
		closing.ClosedTradeNetMinor = pnlMinor
		closing.RealizedNetMinor = pnlMinor
		fills = append(fills, entry, closing)

		if netRBps > 0 {
			result.Wins++
		}
		result.NetRBps += netRBps
		equityRBps += netRBps
		if equityRBps > peakRBps {
			peakRBps = equityRBps
		}
		if dd := peakRBps - equityRBps; dd > result.MaxDrawdownRBps {
			result.MaxDrawdownRBps = dd
		}
	}

	for _, candle := range candles {
		events, err := stream.Advance(candle, admit)
		if err != nil {
			return BTResult{}, nil, err
		}
		for _, event := range events {
			switch event.Kind {
			case LifecycleDetected:
				result.Armed++
			case LifecycleEntry:
				result.Filled++
			case LifecycleExpired:
				result.Expired++
			case LifecycleExit:
				if event.Ambiguous {
					result.AmbiguousStops++
				}
				book(pending{setup: event.Setup, filledAt: event.EntryAt}, event.Price, event.At, event.ExitReason)
			}
		}
	}

	report, err := analytics.BuildReport(fills, params.InitialEquityMinor)
	if err != nil {
		return BTResult{}, nil, err
	}
	result.Report = report
	if report.Trades > 0 {
		result.ExpectancyRBps = result.NetRBps / int64(report.Trades)
	}
	return result, fills, nil
}
