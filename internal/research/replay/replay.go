package replay

import (
	"bytes"
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
	"tradex/internal/instrument"
	"tradex/internal/journal"
	"tradex/internal/marketdata"
	"tradex/internal/portfolio"
	"tradex/internal/risk"
	"tradex/internal/strategy/scan"
)

type EventKind string

const (
	EventCandle       EventKind = "EXECUTION_CANDLE"
	EventQuote        EventKind = "QUOTE"
	EventMarketStatus EventKind = "MARKET_STATUS"
)

// legacyICCEventKinds are the ICC-only event kinds this schema retired. They
// must fail with an explicit migration error, never be silently reinterpreted.
var legacyICCEventKinds = map[EventKind]bool{
	"CONTEXT": true, "INDICATION": true, "SETUP_CANDLE": true, "CONFIRMATION": true,
}

type Event struct {
	At                time.Time      `json:"at"`
	Kind              EventKind      `json:"kind"`
	Candle            *domain.Candle `json:"candle,omitempty"`
	Quote             *domain.Quote  `json:"quote,omitempty"`
	AvailableQuantity *int64         `json:"available_quantity,omitempty"`
	HalfSpreadTicks   *int64         `json:"half_spread_ticks,omitempty"`
	MarketOpen        *bool          `json:"market_open,omitempty"`
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

// DetectorParams fixes the scan.Stream lifecycle parameters a replay
// scenario runs the chosen detector under: the bar timeframe, how long an
// unfilled limit remains armed, the time-stop for a stalled stop/target
// position, and the spread floor handed to the detector's own cost gate.
type DetectorParams struct {
	Timeframe   time.Duration `json:"timeframe"`
	ExpiryBars  int           `json:"expiry_bars"`
	MaxHoldBars int           `json:"max_hold_bars"`
	SpreadTicks int64         `json:"spread_ticks"`
}

func (p DetectorParams) Validate() error {
	if p.Timeframe <= 0 {
		return errors.New("detector timeframe must be positive")
	}
	if p.ExpiryBars < 1 || p.MaxHoldBars < 1 {
		return errors.New("detector expiry and max-hold bars must be positive")
	}
	if p.SpreadTicks < 0 {
		return errors.New("detector spread ticks cannot be negative")
	}
	return nil
}

// btParams adapts DetectorParams into the scan.BTParams shape scan.Stream
// requires. Costs/RiskUnitTicks/RiskMinorPerTrade/InitialEquityMinor are
// scan.Backtest-only measurement concepts the Stream lifecycle itself never
// reads; replay supplies the minimum positive placeholders Validate requires
// and relies on paper.Broker + execution.Account for every real cost, size
// and risk figure.
func (p DetectorParams) btParams() scan.BTParams {
	return scan.BTParams{
		SpreadTicks: p.SpreadTicks, ExpiryBars: p.ExpiryBars, MaxHoldBars: p.MaxHoldBars,
		RiskUnitTicks: 1, RiskMinorPerTrade: 1, InitialEquityMinor: 1,
	}
}

type Scenario struct {
	Name               string                        `json:"name"`
	InitialEquityMinor int64                         `json:"initial_equity_minor"`
	Instrument         domain.Instrument             `json:"instrument"`
	PriceValue         economics.PriceValue          `json:"price_value"`
	Paper              PaperSettings                 `json:"paper"`
	Risk               config.Risk                   `json:"risk"`
	Detector           scan.Config                   `json:"detector"`
	DetectorParams     DetectorParams                `json:"detector_params"`
	Execution          execution.SetupPlan           `json:"execution"`
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
	data, err := io.ReadAll(reader)
	if err != nil {
		return Scenario{}, err
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err == nil {
		if _, has := probe["icc"]; has {
			return Scenario{}, errors.New("ICC configuration/field is no longer supported, migrate to a detector config (Scenario.Detector)")
		}
	}
	var scenario Scenario
	decoder := json.NewDecoder(bytes.NewReader(data))
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
	if _, err := s.Detector.Build(); err != nil {
		return fmt.Errorf("validate detector config: %w", err)
	}
	if err := s.DetectorParams.Validate(); err != nil {
		return err
	}
	if _, ok := instrument.Lookup(s.Instrument.Symbol); !ok {
		return errors.New("scenario instrument is not a canonical, registry-verified instrument")
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
		case EventCandle:
			if event.Candle == nil || !event.At.Equal(event.Candle.CloseTime) {
				return fmt.Errorf("scenario candle event %d is missing or time-mismatched", index)
			}
			if event.Candle.Symbol != s.Instrument.Symbol {
				return fmt.Errorf("scenario candle event %d belongs to another instrument", index)
			}
			if err := event.Candle.Validate(); err != nil {
				return fmt.Errorf("scenario candle event %d: %w", index, err)
			}
			if event.Candle.Timeframe != s.DetectorParams.Timeframe {
				return fmt.Errorf("scenario candle event %d does not match the detector timeframe", index)
			}
			if event.HalfSpreadTicks != nil && *event.HalfSpreadTicks < 0 {
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
			if legacyICCEventKinds[event.Kind] {
				return fmt.Errorf("scenario event %d uses ICC event kind %q; ICC configuration/field is no longer supported, migrate to a detector config", index, event.Kind)
			}
			return fmt.Errorf("scenario event %d has unknown kind %q", index, event.Kind)
		}
	}
	return nil
}

func normalize(scenario *Scenario) {
	if scenario.Risk.RiskPerTradeBPS == 0 {
		scenario.Risk = config.DefaultRisk()
	}
	scenario.Execution.AccountID = scenario.Paper.AccountID
	scenario.Execution.PriceValue = scenario.PriceValue
}

type Result struct {
	Scenario      string                `json:"scenario"`
	Manifest      experiment.Manifest   `json:"manifest"`
	Lifecycle     []scan.LifecycleEvent `json:"lifecycle"`
	Decisions     []risk.Decision       `json:"decisions"`
	Opportunities []Opportunity         `json:"opportunities"`
	Orders        []domain.BrokerOrder  `json:"orders"`
	Fills         []domain.Fill         `json:"fills"`
	Report        analytics.Report      `json:"report"`
	Portfolio     portfolio.View        `json:"portfolio"`
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
		Detector       scan.Config
		DetectorParams DetectorParams
	}{scenario.Detector, scenario.DetectorParams}
	riskDefinition := struct {
		Limits  config.Risk
		RiskBPS int64
	}{scenario.Risk, scenario.Execution.RiskBPS}
	costDefinition := struct {
		Paper                            PaperSettings
		ExecutionSlippageTicks           int64
		RoundTripCommissionPerStepMinor  int64
		PriceValue                       economics.PriceValue
		StoplessMaxAdverseExcursionTicks int64
	}{scenario.Paper, scenario.Execution.SlippageTicks, scenario.Execution.RoundTripCommissionPerStepMinor,
		scenario.PriceValue, scenario.Execution.StoplessMaxAdverseExcursionTicks}
	datasetID := scenario.Name + "/events"
	var dataset any = scenario.Events
	if scenario.RawDataset != nil {
		datasetID = scenario.RawDataset.Provenance.DatasetID
		dataset = *scenario.RawDataset
	}
	return experiment.Build(experiment.Input{
		ExperimentID: scenario.Name,
		DatasetID:    datasetID, Dataset: dataset,
		EngineID: "tradex-replay", EngineVersion: "v4-scan",
		Engine: struct {
			EventSchema      string
			FillModel        string
			AuditModel       string
			OpportunityModel string
		}{"v3-scan-lifecycle", "paper-realism-v2", "sha256-chain-v1", "opportunity-funnel-v2"},
		StrategyID: scenario.Execution.StrategyID, StrategyVersion: scenario.Execution.StrategyVersion,
		Strategy:    strategyDefinition,
		RiskVersion: "v1", Risk: riskDefinition,
		CostModelID: "paper", CostVersion: "v3", CostModel: costDefinition,
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
	detector, err := scenario.Detector.Build()
	if err != nil {
		return Result{}, err
	}
	spec, _ := instrument.Lookup(scenario.Instrument.Symbol)
	stream, err := scan.NewStream(scenario.Instrument.Symbol, spec, scenario.DetectorParams.Timeframe, detector, scenario.DetectorParams.btParams())
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
				if currentOpportunity != nil && currentOpportunity.opportunity.IntentID == fill.IntentID {
					currentOpportunity = nil
				}
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
				if currentOpportunity != nil && currentOpportunity.opportunity.IntentID == order.IntentID {
					currentOpportunity = nil
				}
			}
		}
		return nil
	}
	if err := appendAudit(scenario.Events[0].At, "EXPERIMENT_MANIFEST", scenario.Name, manifest); err != nil {
		return Result{}, err
	}

	// admit is scan.Stream's admission gate: every DETECTED setup is priced,
	// sized and risk-approved exactly like the old ICC confirmation handler,
	// then (on approval) submitted to the real paper.Broker. REAL economics
	// (fills, P&L, reservation release) always come from the paper broker's
	// own quote/candle processing below, never from the Stream's own
	// idealized lifecycle timing -- Stream is the detection/opportunity
	// narrative, paper.Broker is the execution of record, exactly paralleling
	// how the ICC machine's transitions were bookkeeping separate from
	// paperBroker.Fills(). Settlement of the PREVIOUS position's exit and
	// release of its reservation happens via paperBroker+recordFills before
	// this callback runs for the SAME candle (see the EventCandle case
	// below), satisfying "release reservations before admitting the next
	// setup" within the structural limits of execution.Account, whose
	// portfolio tracker only releases risk against an explicit Release call
	// or a real qualifying Fill -- never against an idealized lifecycle event.
	admit := func(setup scan.Setup) (bool, error) {
		if lastQuote == nil {
			return false, errors.New("detector setup has no prior executable quote")
		}
		intentSequence++
		intentID := fmt.Sprintf("%s-intent-%03d", scenario.Name, intentSequence)
		approved, decision, err := sharedAccount.PrepareSetup(scenario.Execution, execution.SetupRequest{
			IntentID: intentID, ApprovalID: "risk-" + intentID, Now: setup.DetectedAt,
			Setup: setup, Symbol: scenario.Instrument.Symbol,
			Market: domain.MarketSnapshot{
				Instrument: scenario.Instrument, Bid: lastQuote.Bid, Ask: lastQuote.Ask, ObservedAt: lastQuote.At,
			},
			Context: execution.RiskContext{AdapterConnected: true},
		})
		if err != nil {
			return false, err
		}
		result.Decisions = append(result.Decisions, decision)
		if currentOpportunity != nil {
			currentOpportunity.opportunity.IntentID = intentID
		}
		if err := appendAudit(setup.DetectedAt, "RISK_DECISION", intentID, decision); err != nil {
			return false, err
		}
		if !decision.Approved {
			if currentOpportunity != nil {
				currentOpportunity.opportunity.Status = OpportunityRiskRejected
				currentOpportunity.opportunity.DecisionCode = decision.Code
				currentOpportunity.opportunity.Reason = decision.Reason
				currentOpportunity.opportunity.ResolvedAt = setup.DetectedAt
				currentOpportunity.explicit = true
			}
			return false, nil
		}
		order, err := paperBroker.Submit(ctx, approved)
		if err != nil {
			_ = sharedAccount.CancelReservation(intentID)
			releasedReservations[intentID] = true
			return false, err
		}
		result.Orders = append(result.Orders, order)
		if err := appendAudit(setup.DetectedAt, "ORDER", intentID, order); err != nil {
			return false, err
		}
		if order.Status == domain.OrderRejected {
			if currentOpportunity != nil {
				currentOpportunity.opportunity.Status = OpportunityBrokerRejected
				currentOpportunity.opportunity.BrokerOrderID = order.BrokerOrderID
				currentOpportunity.opportunity.Reason = order.RejectReason
				currentOpportunity.opportunity.ResolvedAt = setup.DetectedAt
				currentOpportunity.explicit = true
			}
			if err := sharedAccount.CancelReservation(intentID); err != nil {
				return false, err
			}
			releasedReservations[intentID] = true
			return false, nil
		}
		return true, nil
	}
	// observe is pure bookkeeping/audit for the detection narrative: it never
	// mutates the account. DETECTED opens one Opportunity (at most one is
	// active at a time, matching Stream's single pending-setup design);
	// ENTRY/EXIT/EXPIRED are recorded informationally for the funnel, with
	// the final Opportunity status still resolved from the REAL orders/fills
	// in finalizeOpportunities, exactly as the old ICC-era builder did.
	observe := func(event scan.LifecycleEvent) error {
		result.Lifecycle = append(result.Lifecycle, event)
		switch event.Kind {
		case scan.LifecycleDetected:
			currentOpportunity = &opportunityBuilder{opportunity: Opportunity{
				ID:     fmt.Sprintf("%s-opportunity-%03d", scenario.Name, len(opportunityBuilders)+1),
				Symbol: scenario.Instrument.Symbol, Strategy: event.Strategy, Side: event.Setup.Side,
				StartedAt: event.At,
			}}
			opportunityBuilders = append(opportunityBuilders, currentOpportunity)
			return appendAudit(event.At, "SETUP_DETECTED", scenario.Name, event)
		case scan.LifecycleRejected:
			return appendAudit(event.At, "SETUP_REJECTED", scenario.Name, event)
		case scan.LifecycleEntry:
			return appendAudit(event.At, "SETUP_ENTRY", scenario.Name, event)
		case scan.LifecycleExpired:
			if currentOpportunity != nil && !currentOpportunity.explicit {
				currentOpportunity.opportunity.Status = OpportunityExpired
				currentOpportunity.opportunity.ResolvedAt = event.At
				currentOpportunity.explicit = true
			}
			return appendAudit(event.At, "SETUP_EXPIRED", scenario.Name, event)
		case scan.LifecycleExit:
			return appendAudit(event.At, "SETUP_EXIT", scenario.Name, event)
		default:
			return fmt.Errorf("unsupported lifecycle event %q", event.Kind)
		}
	}

	err = backtest.Replay(scenario.Events, func(event Event) error {
		if err := manualClock.Set(event.At); err != nil {
			return err
		}
		switch event.Kind {
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
		case EventCandle:
			// Settle the PREVIOUS setup's real exit/release before letting
			// the Stream consider admitting a new one on this same bar.
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
			if err := releaseTerminalOrders(); err != nil {
				return err
			}
			_, err = stream.AdvanceWithObserver(*event.Candle, admit, observe)
			return err
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
