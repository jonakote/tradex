package replay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"tradex/internal/analytics"
	"tradex/internal/backtest"
	"tradex/internal/broker/paper"
	"tradex/internal/clock"
	"tradex/internal/config"
	"tradex/internal/domain"
	"tradex/internal/economics"
	"tradex/internal/execution"
	"tradex/internal/experiment"
	"tradex/internal/features"
	"tradex/internal/journal"
	"tradex/internal/marketdata"
	"tradex/internal/portfolio"
	"tradex/internal/risk"
	"tradex/internal/strategy/icc"
)

type EventKind string

const (
	EventContext      EventKind = "CONTEXT"
	EventIndication   EventKind = "INDICATION"
	EventSetupCandle  EventKind = "SETUP_CANDLE"
	EventQuote        EventKind = "QUOTE"
	EventConfirmation EventKind = "CONFIRMATION"
	EventCandle       EventKind = "EXECUTION_CANDLE"
	EventMarketStatus EventKind = "MARKET_STATUS"
)

type Event struct {
	At                time.Time               `json:"at"`
	Kind              EventKind               `json:"kind"`
	H4Trend           features.Trend          `json:"h4_trend,omitempty"`
	H1Trend           features.Trend          `json:"h1_trend,omitempty"`
	Direction         features.BreakDirection `json:"direction,omitempty"`
	BrokenLevel       domain.Price            `json:"broken_level,omitempty"`
	InvalidationPrice domain.Price            `json:"invalidation_price,omitempty"`
	AllowedSession    bool                    `json:"allowed_session,omitempty"`
	Candle            *domain.Candle          `json:"candle,omitempty"`
	Quote             *domain.Quote           `json:"quote,omitempty"`
	AvailableQuantity *int64                  `json:"available_quantity,omitempty"`
	HalfSpreadTicks   *int64                  `json:"half_spread_ticks,omitempty"`
	MarketOpen        *bool                   `json:"market_open,omitempty"`
}

func (e Event) EventTime() time.Time { return e.At }

type PaperSettings struct {
	AccountID                      string               `json:"account_id"`
	MaxQuoteAge                    time.Duration        `json:"max_quote_age"`
	SlippageTicks                  int64                `json:"slippage_ticks"`
	HalfSpreadTicksForCandle       int64                `json:"half_spread_ticks_for_candle"`
	CommissionPerQuantityStepMinor int64                `json:"commission_per_quantity_step_minor"`
	SubmissionLatency              time.Duration        `json:"submission_latency"`
	Rollover                       paper.RolloverConfig `json:"rollover"`
}

type Scenario struct {
	Name               string                        `json:"name"`
	InitialEquityMinor int64                         `json:"initial_equity_minor"`
	Instrument         domain.Instrument             `json:"instrument"`
	PriceValue         economics.PriceValue          `json:"price_value"`
	Paper              PaperSettings                 `json:"paper"`
	Risk               config.Risk                   `json:"risk"`
	ICC                icc.Config                    `json:"icc"`
	Execution          execution.ICCPlan             `json:"execution"`
	Events             []Event                       `json:"events"`
	RawDataset         *marketdata.CandleDataset     `json:"raw_dataset,omitempty"`
	QuoteDataset       *marketdata.QuoteDataset      `json:"quote_dataset,omitempty"`
	BrokerSchedule     *marketdata.BrokerSchedule    `json:"broker_schedule,omitempty"`
	ConversionDataset  *marketdata.ConversionDataset `json:"conversion_dataset,omitempty"`
	ResampleReports    []marketdata.ResampleReport   `json:"resample_reports,omitempty"`
}

func Load(path string) (Scenario, error) {
	file, err := os.Open(path)
	if err != nil {
		return Scenario{}, err
	}
	defer file.Close()
	return Decode(file)
}

func Decode(reader io.Reader) (Scenario, error) {
	var scenario Scenario
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&scenario); err != nil {
		return Scenario{}, fmt.Errorf("decode replay scenario: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return Scenario{}, errors.New("replay scenario contains trailing JSON")
	}
	normalize(&scenario)
	if err := scenario.Validate(); err != nil {
		return Scenario{}, err
	}
	return scenario, nil
}

func (s Scenario) Validate() error {
	if s.Name == "" || s.InitialEquityMinor <= 0 || len(s.Events) == 0 {
		return errors.New("scenario name, equity, and events are required")
	}
	if err := s.Instrument.Validate(); err != nil {
		return err
	}
	if err := s.PriceValue.Validate(); err != nil {
		return err
	}
	if s.Paper.AccountID == "" || s.Paper.MaxQuoteAge <= 0 || s.Paper.SlippageTicks < 0 ||
		s.Paper.SubmissionLatency < 0 ||
		s.Paper.CommissionPerQuantityStepMinor < 0 {
		return errors.New("invalid scenario paper settings")
	}
	if err := s.Paper.Rollover.Validate(); err != nil {
		return err
	}
	if err := s.Risk.Validate(); err != nil {
		return err
	}
	if err := s.ICC.Validate(); err != nil {
		return err
	}
	if err := s.Execution.Validate(); err != nil {
		return err
	}
	if s.Execution.AccountID != s.Paper.AccountID || s.Execution.PriceValue != s.PriceValue {
		return errors.New("scenario execution plan does not match its paper account or price value")
	}
	if s.RawDataset != nil {
		if err := s.RawDataset.Validate(); err != nil {
			return fmt.Errorf("validate raw dataset: %w", err)
		}
	}
	if s.QuoteDataset != nil {
		if err := s.QuoteDataset.Validate(); err != nil {
			return fmt.Errorf("validate quote dataset: %w", err)
		}
		if len(s.QuoteDataset.Streams) != 1 || s.QuoteDataset.Streams[0].Symbol != s.Instrument.Symbol {
			return errors.New("quote dataset must contain exactly the scenario instrument")
		}
	}
	if s.BrokerSchedule != nil {
		if err := s.BrokerSchedule.Validate(); err != nil {
			return fmt.Errorf("validate broker schedule: %w", err)
		}
		if s.BrokerSchedule.Symbol != s.Instrument.Symbol {
			return errors.New("broker schedule symbol does not match scenario instrument")
		}
	}
	if s.ConversionDataset != nil {
		if err := s.ConversionDataset.Validate(); err != nil {
			return fmt.Errorf("validate conversion dataset: %w", err)
		}
		if s.ConversionDataset.AccountCurrency != s.PriceValue.AccountCurrency {
			return errors.New("conversion dataset account currency does not match scenario")
		}
	}
	if len(s.ResampleReports) > 0 && (s.RawDataset == nil || len(s.ResampleReports) != len(s.RawDataset.Transformations)) {
		return errors.New("resample reports require matching raw dataset transformations")
	}
	for index, report := range s.ResampleReports {
		if err := report.Validate(); err != nil {
			return fmt.Errorf("validate resample report %d: %w", index, err)
		}
	}
	for index, event := range s.Events {
		if event.At.IsZero() {
			return fmt.Errorf("scenario event %d has no time", index)
		}
		if index > 0 && event.At.Before(s.Events[index-1].At) {
			return fmt.Errorf("scenario event %d is out of chronological order", index)
		}
		if (event.AvailableQuantity != nil && event.Kind != EventQuote) ||
			(event.HalfSpreadTicks != nil && event.Kind != EventCandle) ||
			(event.MarketOpen != nil && event.Kind != EventMarketStatus) {
			return fmt.Errorf("scenario event %d has execution conditions for another event kind", index)
		}
		switch event.Kind {
		case EventContext:
			if event.H4Trend == features.TrendNeutral || event.H1Trend == features.TrendNeutral {
				return fmt.Errorf("scenario context event %d is neutral", index)
			}
		case EventIndication, EventSetupCandle, EventConfirmation, EventCandle:
			if event.Candle == nil || !event.At.Equal(event.Candle.CloseTime) {
				return fmt.Errorf("scenario candle event %d is missing or time-mismatched", index)
			}
			if event.Candle.Symbol != s.Instrument.Symbol {
				return fmt.Errorf("scenario candle event %d belongs to another instrument", index)
			}
			if err := event.Candle.Validate(); err != nil {
				return fmt.Errorf("scenario candle event %d: %w", index, err)
			}
			if event.Kind == EventCandle && event.HalfSpreadTicks != nil && *event.HalfSpreadTicks < 0 {
				return fmt.Errorf("scenario candle event %d has negative spread", index)
			}
		case EventQuote:
			if event.Quote == nil || !event.At.Equal(event.Quote.At) {
				return fmt.Errorf("scenario quote event %d is missing or time-mismatched", index)
			}
			if err := event.Quote.Validate(); err != nil {
				return fmt.Errorf("scenario quote event %d: %w", index, err)
			}
			if event.Quote.Symbol != s.Instrument.Symbol {
				return fmt.Errorf("scenario quote event %d belongs to another instrument", index)
			}
			if event.AvailableQuantity != nil && (*event.AvailableQuantity < 0 ||
				*event.AvailableQuantity%s.Instrument.QuantityStep != 0) {
				return fmt.Errorf("scenario quote event %d has invalid available quantity", index)
			}
		case EventMarketStatus:
			if event.MarketOpen == nil {
				return fmt.Errorf("scenario market-status event %d has no state", index)
			}
		default:
			return fmt.Errorf("scenario event %d has unknown kind %q", index, event.Kind)
		}
	}
	return nil
}

func normalize(scenario *Scenario) {
	if scenario.Risk.RiskPerTradeBPS == 0 {
		scenario.Risk = config.DefaultRisk()
	}
	if scenario.ICC.SetupTimeframe == 0 {
		scenario.ICC = icc.DefaultConfig()
	}
	scenario.Execution.AccountID = scenario.Paper.AccountID
	scenario.Execution.PriceValue = scenario.PriceValue
}

type Result struct {
	Scenario      string               `json:"scenario"`
	Manifest      experiment.Manifest  `json:"manifest"`
	Transitions   []icc.Transition     `json:"transitions"`
	Decisions     []risk.Decision      `json:"decisions"`
	Opportunities []Opportunity        `json:"opportunities"`
	Orders        []domain.BrokerOrder `json:"orders"`
	Fills         []domain.Fill        `json:"fills"`
	Report        analytics.Report     `json:"report"`
	Portfolio     portfolio.View       `json:"portfolio"`
}

func Run(ctx context.Context, scenario Scenario, auditPath string) (result Result, runErr error) {
	manifest, err := ManifestFor(scenario)
	if err != nil {
		return Result{}, err
	}
	return run(ctx, scenario, manifest, auditPath)
}

func RunVerified(ctx context.Context, scenario Scenario, expected experiment.Manifest, auditPath string) (Result, error) {
	if err := expected.Verify(); err != nil {
		return Result{}, fmt.Errorf("verify expected experiment manifest: %w", err)
	}
	actual, err := ManifestFor(scenario)
	if err != nil {
		return Result{}, err
	}
	if actual.ManifestSHA256 != expected.ManifestSHA256 {
		return Result{}, errors.New("scenario does not match the frozen experiment manifest")
	}
	return run(ctx, scenario, actual, auditPath)
}

func ManifestFor(scenario Scenario) (experiment.Manifest, error) {
	if err := scenario.Validate(); err != nil {
		return experiment.Manifest{}, err
	}
	strategyDefinition := struct {
		ICC             icc.Config
		IntentTTL       time.Duration
		RewardRiskBPS   int64
		StopBufferTicks int64
	}{scenario.ICC, scenario.Execution.IntentTTL, scenario.Execution.RewardRiskBPS, scenario.Execution.StopBufferTicks}
	riskDefinition := struct {
		Limits  config.Risk
		RiskBPS int64
	}{scenario.Risk, scenario.Execution.RiskBPS}
	costDefinition := struct {
		Paper                           PaperSettings
		ExecutionSlippageTicks          int64
		RoundTripCommissionPerStepMinor int64
		PriceValue                      economics.PriceValue
	}{scenario.Paper, scenario.Execution.SlippageTicks, scenario.Execution.RoundTripCommissionPerStepMinor, scenario.PriceValue}
	datasetID := scenario.Name + "/events"
	var dataset any = scenario.Events
	if scenario.RawDataset != nil {
		datasetID = scenario.RawDataset.Provenance.DatasetID
		dataset = *scenario.RawDataset
	}
	return experiment.Build(experiment.Input{
		ExperimentID: scenario.Name,
		DatasetID:    datasetID, Dataset: dataset,
		EngineID: "tradex-replay", EngineVersion: "v3",
		Engine: struct {
			EventSchema      string
			FillModel        string
			AuditModel       string
			OpportunityModel string
		}{"v2", "paper-realism-v2", "sha256-chain-v1", "opportunity-funnel-v1"},
		StrategyID: scenario.Execution.StrategyID, StrategyVersion: scenario.Execution.StrategyVersion,
		Strategy:    strategyDefinition,
		RiskVersion: "v1", Risk: riskDefinition,
		CostModelID: "paper", CostVersion: "v2", CostModel: costDefinition,
		InstrumentID: scenario.Instrument.Symbol, InstrumentVersion: "v1", Instrument: scenario.Instrument,
		Scenario: scenario,
	})
}

func run(ctx context.Context, scenario Scenario, manifest experiment.Manifest, auditPath string) (result Result, runErr error) {
	manualClock := clock.NewManual(scenario.Events[0].At)
	sharedAccount, err := execution.NewAccount(scenario.Risk, scenario.InitialEquityMinor)
	if err != nil {
		return Result{}, err
	}
	machine, err := icc.New(scenario.ICC)
	if err != nil {
		return Result{}, err
	}
	paperBroker, err := paper.New(paper.Config{
		AccountID: scenario.Paper.AccountID, MaxQuoteAge: scenario.Paper.MaxQuoteAge,
		Instruments: map[string]paper.InstrumentConfig{
			scenario.Instrument.Symbol: {
				Instrument: scenario.Instrument, SlippageTicks: scenario.Paper.SlippageTicks,
				HalfSpreadTicksForCandle:       scenario.Paper.HalfSpreadTicksForCandle,
				CommissionPerQuantityStepMinor: scenario.Paper.CommissionPerQuantityStepMinor,
				PriceValue:                     scenario.PriceValue,
				SubmissionLatency:              scenario.Paper.SubmissionLatency,
				Rollover:                       scenario.Paper.Rollover,
			},
		},
	}, manualClock)
	if err != nil {
		return Result{}, err
	}
	audit, err := journal.Create(auditPath, true)
	if err != nil {
		return Result{}, err
	}
	defer func() {
		if err := audit.Close(); runErr == nil && err != nil {
			runErr = err
		}
	}()

	result.Scenario = scenario.Name
	result.Manifest = manifest
	var lastQuote *domain.Quote
	intentSequence := 0
	allTransitions := make([]icc.Transition, 0)
	opportunityBuilders := make([]*opportunityBuilder, 0)
	var currentOpportunity *opportunityBuilder
	releasedReservations := make(map[string]bool)
	appendAudit := func(at time.Time, kind, correlation string, payload any) error {
		_, err := audit.Append(ctx, at, kind, correlation, payload)
		return err
	}
	recordFills := func(fills []domain.Fill) error {
		for _, fill := range fills {
			if err := appendAudit(fill.FilledAt, "FILL", fill.IntentID, fill); err != nil {
				return err
			}
			if fill.Role != domain.FillEntry {
				if err := sharedAccount.ApplyClosingFill(fill); err != nil {
					return err
				}
				releasedReservations[fill.IntentID] = true
			}
		}
		return nil
	}
	releaseTerminalOrders := func() error {
		positions, err := paperBroker.Positions(ctx)
		if err != nil {
			return err
		}
		active := make(map[string]bool, len(positions))
		for _, position := range positions {
			active[position.IntentID] = true
		}
		for _, order := range paperBroker.Orders() {
			terminal := order.Status == domain.OrderRejected || order.Status == domain.OrderCancelled
			if terminal && !active[order.IntentID] && !releasedReservations[order.IntentID] {
				if err := sharedAccount.CancelReservation(order.IntentID); err != nil {
					return err
				}
				releasedReservations[order.IntentID] = true
			}
		}
		return nil
	}
	if err := appendAudit(scenario.Events[0].At, "EXPERIMENT_MANIFEST", scenario.Name, manifest); err != nil {
		return Result{}, err
	}

	err = backtest.Replay(scenario.Events, func(event Event) error {
		if err := manualClock.Set(event.At); err != nil {
			return err
		}
		switch event.Kind {
		case EventContext:
			if machine.State() == icc.Completed || machine.State() == icc.Invalidated {
				allTransitions = append(allTransitions, machine.Transitions()...)
				machine, err = icc.New(scenario.ICC)
				if err != nil {
					return err
				}
			}
			if err := machine.SetContext(event.H4Trend, event.H1Trend); err != nil {
				return err
			}
			return appendAudit(event.At, "ICC_CONTEXT", scenario.Name, event)
		case EventIndication:
			currentOpportunity = &opportunityBuilder{opportunity: Opportunity{
				ID:     fmt.Sprintf("%s-opportunity-%03d", scenario.Name, len(opportunityBuilders)+1),
				Symbol: scenario.Instrument.Symbol, Direction: event.Direction, StartedAt: event.At,
			}}
			opportunityBuilders = append(opportunityBuilders, currentOpportunity)
			swingKind := features.SwingHigh
			if event.Direction == features.BreakBearish {
				swingKind = features.SwingLow
			}
			input := icc.Indication{
				Break: features.StructureBreak{
					Direction: event.Direction,
					BrokenSwing: features.Swing{
						Kind: swingKind, Symbol: event.Candle.Symbol, Timeframe: event.Candle.Timeframe,
						Price: event.BrokenLevel, PivotTime: event.Candle.OpenTime.Add(-event.Candle.Timeframe),
						ConfirmedAt: event.Candle.OpenTime,
					},
					Candle: *event.Candle, KnownAt: event.At,
				},
				InvalidationPrice: event.InvalidationPrice, AllowedSession: event.AllowedSession,
			}
			if err := machine.Begin(input); err != nil {
				return err
			}
			return appendAudit(event.At, "ICC_INDICATION", scenario.Name, input)
		case EventSetupCandle:
			if err := machine.OnSetupCandle(*event.Candle); err != nil {
				return err
			}
			if currentOpportunity != nil && machine.State() == icc.Invalidated {
				currentOpportunity.opportunity.Status = OpportunityInvalidated
				currentOpportunity.opportunity.Reason = machine.Transitions()[len(machine.Transitions())-1].Reason
				currentOpportunity.opportunity.ResolvedAt = event.At
				currentOpportunity.explicit = true
			}
			return appendAudit(event.At, "ICC_SETUP", scenario.Name, struct {
				Candle domain.Candle
				State  icc.State
			}{*event.Candle, machine.State()})
		case EventQuote:
			var fills []domain.Fill
			var err error
			if event.AvailableQuantity == nil {
				fills, err = paperBroker.OnQuote(*event.Quote)
			} else {
				fills, err = paperBroker.OnQuoteWithLiquidity(*event.Quote, *event.AvailableQuantity)
			}
			if err != nil {
				return err
			}
			quoteCopy := *event.Quote
			lastQuote = &quoteCopy
			if err := appendAudit(event.At, "QUOTE", scenario.Name, event); err != nil {
				return err
			}
			if err := recordFills(fills); err != nil {
				return err
			}
			return releaseTerminalOrders()
		case EventConfirmation:
			signal, err := machine.OnConfirmation(features.StructureBreak{
				Direction: event.Direction, Candle: *event.Candle, KnownAt: event.At,
			}, event.AllowedSession)
			if err != nil {
				return err
			}
			if lastQuote == nil {
				return errors.New("ICC confirmation has no prior executable quote")
			}
			intentSequence++
			intentID := fmt.Sprintf("%s-intent-%03d", scenario.Name, intentSequence)
			beforeFills := len(paperBroker.Fills())
			approved, decision, err := sharedAccount.PrepareICC(scenario.Execution, execution.ICCRequest{
				IntentID: intentID, ApprovalID: "risk-" + intentID, Now: event.At,
				Signal: *signal,
				Market: domain.MarketSnapshot{
					Instrument: scenario.Instrument, Bid: lastQuote.Bid, Ask: lastQuote.Ask, ObservedAt: lastQuote.At,
				},
				Context: execution.RiskContext{AdapterConnected: true},
			})
			if err != nil {
				return err
			}
			result.Decisions = append(result.Decisions, decision)
			if currentOpportunity == nil {
				return errors.New("ICC confirmation has no active opportunity")
			}
			currentOpportunity.opportunity.IntentID = intentID
			if !decision.Approved {
				currentOpportunity.opportunity.Status = OpportunityRiskRejected
				currentOpportunity.opportunity.DecisionCode = decision.Code
				currentOpportunity.opportunity.Reason = decision.Reason
				currentOpportunity.opportunity.ResolvedAt = event.At
				currentOpportunity.explicit = true
			}
			if err := appendAudit(event.At, "RISK_DECISION", intentID, decision); err != nil {
				return err
			}
			if !decision.Approved {
				return nil
			}
			order, err := paperBroker.Submit(ctx, approved)
			if err != nil {
				_ = sharedAccount.CancelReservation(intentID)
				return err
			}
			result.Orders = append(result.Orders, order)
			if err := appendAudit(event.At, "ORDER", intentID, order); err != nil {
				return err
			}
			if order.Status == domain.OrderRejected {
				currentOpportunity.opportunity.Status = OpportunityBrokerRejected
				currentOpportunity.opportunity.BrokerOrderID = order.BrokerOrderID
				currentOpportunity.opportunity.Reason = order.RejectReason
				currentOpportunity.opportunity.ResolvedAt = event.At
				currentOpportunity.explicit = true
				if err := sharedAccount.CancelReservation(intentID); err != nil {
					return err
				}
				releasedReservations[intentID] = true
				return nil
			}
			allFills := paperBroker.Fills()
			return recordFills(allFills[beforeFills:])
		case EventCandle:
			var fills []domain.Fill
			var err error
			if event.HalfSpreadTicks == nil {
				fills, err = paperBroker.OnCandle(*event.Candle)
			} else {
				fills, err = paperBroker.OnCandleWithSpread(*event.Candle, *event.HalfSpreadTicks)
			}
			if err != nil {
				return err
			}
			if err := appendAudit(event.At, "EXECUTION_CANDLE", scenario.Name, event); err != nil {
				return err
			}
			if err := recordFills(fills); err != nil {
				return err
			}
			return releaseTerminalOrders()
		case EventMarketStatus:
			if err := paperBroker.SetMarketOpen(scenario.Instrument.Symbol, *event.MarketOpen, event.At); err != nil {
				return err
			}
			return appendAudit(event.At, "MARKET_STATUS", scenario.Name, event)
		default:
			return fmt.Errorf("unsupported replay event %q", event.Kind)
		}
	})
	if err != nil {
		return Result{}, err
	}
	result.Transitions = append(allTransitions, machine.Transitions()...)
	result.Orders = paperBroker.Orders()
	result.Fills = paperBroker.Fills()
	result.Opportunities = finalizeOpportunities(opportunityBuilders, result.Orders, result.Fills, scenario.Events[len(scenario.Events)-1].At)
	result.Report, err = analytics.BuildReport(result.Fills, scenario.InitialEquityMinor)
	if err != nil {
		return Result{}, err
	}
	result.Portfolio, err = sharedAccount.View(scenario.Events[len(scenario.Events)-1].At, scenario.Instrument.Symbol)
	if err != nil {
		return Result{}, err
	}
	if err := appendAudit(scenario.Events[len(scenario.Events)-1].At, "REPORT", scenario.Name, result.Report); err != nil {
		return Result{}, err
	}
	return result, nil
}
