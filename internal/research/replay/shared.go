package replay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"tradex/internal/analytics"
	"tradex/internal/broker/paper"
	"tradex/internal/clock"
	"tradex/internal/config"
	"tradex/internal/domain"
	"tradex/internal/execution"
	"tradex/internal/experiment"
	"tradex/internal/instrument"
	"tradex/internal/journal"
	"tradex/internal/portfolio"
	"tradex/internal/risk"
	"tradex/internal/strategy/scan"
)

type SharedScenario struct {
	Name               string      `json:"name"`
	InitialEquityMinor int64       `json:"initial_equity_minor"`
	Risk               config.Risk `json:"risk"`
	Legs               []Scenario  `json:"legs"`
}

type SharedDecision struct {
	Symbol   string        `json:"symbol"`
	IntentID string        `json:"intent_id"`
	Decision risk.Decision `json:"decision"`
}

type SharedResult struct {
	Scenario      string                           `json:"scenario"`
	Manifest      experiment.Manifest              `json:"manifest"`
	Lifecycle     map[string][]scan.LifecycleEvent `json:"lifecycle"`
	Decisions     []SharedDecision                 `json:"decisions"`
	Opportunities []Opportunity                    `json:"opportunities"`
	Orders        []domain.BrokerOrder             `json:"orders"`
	Fills         []domain.Fill                    `json:"fills"`
	Report        analytics.Report                 `json:"report"`
	LegReports    map[string]analytics.Report      `json:"leg_reports"`
	Portfolio     portfolio.View                   `json:"portfolio"`
}

type sharedEvent struct {
	symbol string
	event  Event
}

func LoadShared(path string) (SharedScenario, error) {
	file, err := os.Open(path)
	if err != nil {
		return SharedScenario{}, err
	}
	defer file.Close()
	return DecodeShared(file)
}

func DecodeShared(reader io.Reader) (SharedScenario, error) {
	data, err := io.ReadAll(reader)
	if err != nil {
		return SharedScenario{}, err
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err == nil {
		if _, has := probe["icc"]; has {
			return SharedScenario{}, errors.New("ICC configuration/field is no longer supported, migrate to a detector config (Scenario.Detector)")
		}
	}
	var scenario SharedScenario
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&scenario); err != nil {
		return SharedScenario{}, fmt.Errorf("decode shared replay scenario: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return SharedScenario{}, errors.New("shared replay scenario contains trailing JSON")
	}
	for index := range scenario.Legs {
		normalize(&scenario.Legs[index])
	}
	if err := scenario.Validate(); err != nil {
		return SharedScenario{}, err
	}
	return scenario, nil
}

func (s SharedScenario) Validate() error {
	if s.Name == "" || s.InitialEquityMinor <= 0 || len(s.Legs) < 2 {
		return errors.New("shared scenario name, equity, and at least two legs are required")
	}
	if err := s.Risk.Validate(); err != nil {
		return err
	}
	symbols := make(map[string]bool, len(s.Legs))
	accountID := s.Legs[0].Paper.AccountID
	maxQuoteAge := s.Legs[0].Paper.MaxQuoteAge
	for index, leg := range s.Legs {
		if err := leg.Validate(); err != nil {
			return fmt.Errorf("validate shared leg %d: %w", index, err)
		}
		if leg.InitialEquityMinor != s.InitialEquityMinor || leg.Risk != s.Risk {
			return fmt.Errorf("shared leg %s does not use shared equity and risk", leg.Instrument.Symbol)
		}
		if leg.Paper.AccountID != accountID || leg.Paper.MaxQuoteAge != maxQuoteAge {
			return fmt.Errorf("shared leg %s does not use the shared paper account", leg.Instrument.Symbol)
		}
		if leg.Execution.AccountID != accountID {
			return fmt.Errorf("shared leg %s execution plan belongs to another account", leg.Instrument.Symbol)
		}
		if symbols[leg.Instrument.Symbol] {
			return fmt.Errorf("duplicate shared symbol %s", leg.Instrument.Symbol)
		}
		symbols[leg.Instrument.Symbol] = true
	}
	_, err := mergeSharedEvents(s.Legs)
	return err
}

func SharedManifestFor(scenario SharedScenario) (experiment.Manifest, error) {
	if err := scenario.Validate(); err != nil {
		return experiment.Manifest{}, err
	}
	type legDefinition struct {
		Name           string
		Instrument     domain.Instrument
		Detector       scan.Config
		DetectorParams DetectorParams
		Execution      execution.SetupPlan
		Paper          PaperSettings
		Events         []Event
		RawDataset     any
	}
	definitions := make([]legDefinition, 0, len(scenario.Legs))
	instruments := make([]domain.Instrument, 0, len(scenario.Legs))
	for _, leg := range scenario.Legs {
		definitions = append(definitions, legDefinition{
			Name: leg.Name, Instrument: leg.Instrument, Detector: leg.Detector, DetectorParams: leg.DetectorParams,
			Execution: leg.Execution, Paper: leg.Paper, Events: leg.Events, RawDataset: leg.RawDataset,
		})
		instruments = append(instruments, leg.Instrument)
	}
	return experiment.Build(experiment.Input{
		ExperimentID: scenario.Name, DatasetID: scenario.Name + "/shared-events", Dataset: definitions,
		EngineID: "tradex-shared-replay", EngineVersion: "v4-scan",
		Engine:     struct{ Ordering string }{Ordering: "UTC_THEN_SYMBOL_LEXICAL_V1"},
		StrategyID: "multi-detector", StrategyVersion: "v1", Strategy: definitions,
		RiskVersion: "v1", Risk: scenario.Risk,
		CostModelID: "multi-paper", CostVersion: "v3", CostModel: definitions,
		InstrumentID: "shared", InstrumentVersion: "v1", Instrument: instruments,
		Scenario: scenario,
	})
}

func RunShared(ctx context.Context, scenario SharedScenario, auditPath string) (SharedResult, error) {
	manifest, err := SharedManifestFor(scenario)
	if err != nil {
		return SharedResult{}, err
	}
	return runShared(ctx, scenario, manifest, auditPath)
}

func RunSharedVerified(ctx context.Context, scenario SharedScenario, expected experiment.Manifest, auditPath string) (SharedResult, error) {
	if err := expected.Verify(); err != nil {
		return SharedResult{}, err
	}
	actual, err := SharedManifestFor(scenario)
	if err != nil {
		return SharedResult{}, err
	}
	if actual.ManifestSHA256 != expected.ManifestSHA256 {
		return SharedResult{}, errors.New("shared scenario does not match the frozen experiment manifest")
	}
	return runShared(ctx, scenario, actual, auditPath)
}

func runShared(ctx context.Context, scenario SharedScenario, manifest experiment.Manifest, auditPath string) (result SharedResult, runErr error) {
	events, err := mergeSharedEvents(scenario.Legs)
	if err != nil {
		return SharedResult{}, err
	}
	manualClock := clock.NewManual(events[0].event.At)
	account, err := execution.NewAccount(scenario.Risk, scenario.InitialEquityMinor)
	if err != nil {
		return SharedResult{}, err
	}
	brokerInstruments := make(map[string]paper.InstrumentConfig, len(scenario.Legs))
	streams := make(map[string]*scan.Stream, len(scenario.Legs))
	legBySymbol := make(map[string]Scenario, len(scenario.Legs))
	lastQuotes := make(map[string]domain.Quote, len(scenario.Legs))
	intentSequences := make(map[string]int, len(scenario.Legs))
	opportunityBuilders := make([]*opportunityBuilder, 0)
	currentOpportunities := make(map[string]*opportunityBuilder, len(scenario.Legs))
	lifecycle := make(map[string][]scan.LifecycleEvent, len(scenario.Legs))
	for _, leg := range scenario.Legs {
		brokerInstruments[leg.Instrument.Symbol] = paper.InstrumentConfig{
			Instrument: leg.Instrument, SlippageTicks: leg.Paper.SlippageTicks,
			HalfSpreadTicksForCandle:       leg.Paper.HalfSpreadTicksForCandle,
			CommissionPerQuantityStepMinor: leg.Paper.CommissionPerQuantityStepMinor,
			PriceValue:                     leg.PriceValue,
			SubmissionLatency:              leg.Paper.SubmissionLatency,
			Rollover:                       leg.Paper.Rollover,
		}
		detector, err := leg.Detector.Build()
		if err != nil {
			return SharedResult{}, err
		}
		spec, _ := instrument.Lookup(leg.Instrument.Symbol)
		stream, err := scan.NewStream(leg.Instrument.Symbol, spec, leg.DetectorParams.Timeframe, detector, leg.DetectorParams.btParams())
		if err != nil {
			return SharedResult{}, err
		}
		streams[leg.Instrument.Symbol] = stream
		legBySymbol[leg.Instrument.Symbol] = leg
	}
	broker, err := paper.New(paper.Config{
		AccountID: scenario.Legs[0].Paper.AccountID, MaxQuoteAge: scenario.Legs[0].Paper.MaxQuoteAge,
		Instruments: brokerInstruments,
	}, manualClock)
	if err != nil {
		return SharedResult{}, err
	}
	audit, err := journal.Create(auditPath, true)
	if err != nil {
		return SharedResult{}, err
	}
	defer func() {
		if err := audit.Close(); runErr == nil && err != nil {
			runErr = err
		}
	}()
	appendAudit := func(at time.Time, kind, correlation string, payload any) error {
		_, err := audit.Append(ctx, at, kind, correlation, payload)
		return err
	}
	releasedReservations := make(map[string]bool)
	recordFills := func(fills []domain.Fill) error {
		for _, fill := range fills {
			if err := appendAudit(fill.FilledAt, "FILL", fill.IntentID, fill); err != nil {
				return err
			}
			if fill.Role != domain.FillEntry {
				if err := account.ApplyClosingFill(fill); err != nil {
					return err
				}
				releasedReservations[fill.IntentID] = true
			}
		}
		return nil
	}
	releaseTerminalOrders := func() error {
		positions, err := broker.Positions(ctx)
		if err != nil {
			return err
		}
		active := make(map[string]bool, len(positions))
		for _, position := range positions {
			active[position.IntentID] = true
		}
		for _, order := range broker.Orders() {
			terminal := order.Status == domain.OrderRejected || order.Status == domain.OrderCancelled
			if terminal && !active[order.IntentID] && !releasedReservations[order.IntentID] {
				if err := account.CancelReservation(order.IntentID); err != nil {
					return err
				}
				releasedReservations[order.IntentID] = true
			}
		}
		return nil
	}
	if err := appendAudit(events[0].event.At, "EXPERIMENT_MANIFEST", scenario.Name, manifest); err != nil {
		return SharedResult{}, err
	}
	result = SharedResult{Scenario: scenario.Name, Manifest: manifest, Lifecycle: lifecycle, LegReports: make(map[string]analytics.Report)}

	for _, tagged := range events {
		event, symbol := tagged.event, tagged.symbol
		if err := manualClock.Set(event.At); err != nil {
			return SharedResult{}, err
		}
		leg := legBySymbol[symbol]
		switch event.Kind {
		case EventQuote:
			var fills []domain.Fill
			var err error
			if event.AvailableQuantity == nil {
				fills, err = broker.OnQuote(*event.Quote)
			} else {
				fills, err = broker.OnQuoteWithLiquidity(*event.Quote, *event.AvailableQuantity)
			}
			if err != nil {
				return SharedResult{}, err
			}
			lastQuotes[symbol] = *event.Quote
			if err := appendAudit(event.At, "QUOTE", symbol, event); err != nil {
				return SharedResult{}, err
			}
			if err := recordFills(fills); err != nil {
				return SharedResult{}, err
			}
			if err := releaseTerminalOrders(); err != nil {
				return SharedResult{}, err
			}
		case EventCandle:
			var fills []domain.Fill
			var err error
			if event.HalfSpreadTicks == nil {
				fills, err = broker.OnCandle(*event.Candle)
			} else {
				fills, err = broker.OnCandleWithSpread(*event.Candle, *event.HalfSpreadTicks)
			}
			if err != nil {
				return SharedResult{}, err
			}
			if err := appendAudit(event.At, "EXECUTION_CANDLE", symbol, event); err != nil {
				return SharedResult{}, err
			}
			if err := recordFills(fills); err != nil {
				return SharedResult{}, err
			}
			if err := releaseTerminalOrders(); err != nil {
				return SharedResult{}, err
			}
			admit := func(setup scan.Setup) (bool, error) {
				quote, exists := lastQuotes[symbol]
				if !exists {
					return false, fmt.Errorf("shared detection for %s has no quote", symbol)
				}
				intentSequences[symbol]++
				intentID := fmt.Sprintf("%s-%s-intent-%03d", scenario.Name, symbol, intentSequences[symbol])
				approved, decision, err := account.PrepareSetup(leg.Execution, execution.SetupRequest{
					IntentID: intentID, ApprovalID: "risk-" + intentID, Now: setup.DetectedAt,
					Setup: setup, Symbol: symbol,
					Market:  domain.MarketSnapshot{Instrument: leg.Instrument, Bid: quote.Bid, Ask: quote.Ask, ObservedAt: quote.At},
					Context: execution.RiskContext{AdapterConnected: true},
				})
				if err != nil {
					return false, err
				}
				result.Decisions = append(result.Decisions, SharedDecision{Symbol: symbol, IntentID: intentID, Decision: decision})
				if opp := currentOpportunities[symbol]; opp != nil {
					opp.opportunity.IntentID = intentID
				}
				if err := appendAudit(setup.DetectedAt, "RISK_DECISION", intentID, decision); err != nil {
					return false, err
				}
				if !decision.Approved {
					if opp := currentOpportunities[symbol]; opp != nil {
						opp.opportunity.Status = OpportunityRiskRejected
						opp.opportunity.DecisionCode = decision.Code
						opp.opportunity.Reason = decision.Reason
						opp.opportunity.ResolvedAt = setup.DetectedAt
						opp.explicit = true
					}
					return false, nil
				}
				order, err := broker.Submit(ctx, approved)
				if err != nil {
					_ = account.CancelReservation(intentID)
					releasedReservations[intentID] = true
					return false, err
				}
				result.Orders = append(result.Orders, order)
				if err := appendAudit(setup.DetectedAt, "ORDER", intentID, order); err != nil {
					return false, err
				}
				if order.Status == domain.OrderRejected {
					if opp := currentOpportunities[symbol]; opp != nil {
						opp.opportunity.Status = OpportunityBrokerRejected
						opp.opportunity.BrokerOrderID = order.BrokerOrderID
						opp.opportunity.Reason = order.RejectReason
						opp.opportunity.ResolvedAt = setup.DetectedAt
						opp.explicit = true
					}
					if err := account.CancelReservation(intentID); err != nil {
						return false, err
					}
					releasedReservations[intentID] = true
					return false, nil
				}
				return true, nil
			}
			observe := func(evt scan.LifecycleEvent) error {
				lifecycle[symbol] = append(lifecycle[symbol], evt)
				switch evt.Kind {
				case scan.LifecycleDetected:
					opp := &opportunityBuilder{opportunity: Opportunity{
						ID:     fmt.Sprintf("%s-%s-opportunity-%03d", scenario.Name, symbol, len(opportunityBuilders)+1),
						Symbol: symbol, Strategy: evt.Strategy, Side: evt.Setup.Side, StartedAt: evt.At,
					}}
					opportunityBuilders = append(opportunityBuilders, opp)
					currentOpportunities[symbol] = opp
					return appendAudit(evt.At, "SETUP_DETECTED", symbol, evt)
				case scan.LifecycleRejected:
					return appendAudit(evt.At, "SETUP_REJECTED", symbol, evt)
				case scan.LifecycleEntry:
					return appendAudit(evt.At, "SETUP_ENTRY", symbol, evt)
				case scan.LifecycleExpired:
					if opp := currentOpportunities[symbol]; opp != nil && !opp.explicit {
						opp.opportunity.Status = OpportunityExpired
						opp.opportunity.ResolvedAt = evt.At
						opp.explicit = true
					}
					return appendAudit(evt.At, "SETUP_EXPIRED", symbol, evt)
				case scan.LifecycleExit:
					return appendAudit(evt.At, "SETUP_EXIT", symbol, evt)
				default:
					return fmt.Errorf("unsupported lifecycle event %q", evt.Kind)
				}
			}
			if _, err := streams[symbol].AdvanceWithObserver(*event.Candle, admit, observe); err != nil {
				return SharedResult{}, err
			}
		case EventMarketStatus:
			if err := broker.SetMarketOpen(symbol, *event.MarketOpen, event.At); err != nil {
				return SharedResult{}, err
			}
			if err := appendAudit(event.At, "MARKET_STATUS", symbol, event); err != nil {
				return SharedResult{}, err
			}
		default:
			return SharedResult{}, fmt.Errorf("unsupported shared replay event %q", event.Kind)
		}
	}
	result.Fills = broker.Fills()
	result.Orders = broker.Orders()
	result.Opportunities = finalizeOpportunities(opportunityBuilders, result.Orders, result.Fills, events[len(events)-1].event.At)
	result.Report, err = analytics.BuildReport(result.Fills, scenario.InitialEquityMinor)
	if err != nil {
		return SharedResult{}, err
	}
	for _, leg := range scenario.Legs {
		fills := make([]domain.Fill, 0)
		for _, fill := range result.Fills {
			if fill.Symbol == leg.Instrument.Symbol {
				fills = append(fills, fill)
			}
		}
		result.LegReports[leg.Instrument.Symbol], err = analytics.BuildReport(fills, scenario.InitialEquityMinor)
		if err != nil {
			return SharedResult{}, err
		}
	}
	last := events[len(events)-1].event.At
	result.Portfolio, err = account.View(last, "")
	if err != nil {
		return SharedResult{}, err
	}
	if err := appendAudit(last, "REPORT", scenario.Name, result.Report); err != nil {
		return SharedResult{}, err
	}
	return result, nil
}

func mergeSharedEvents(legs []Scenario) ([]sharedEvent, error) {
	events := make([]sharedEvent, 0)
	for _, leg := range legs {
		var previous time.Time
		for index, event := range leg.Events {
			if index > 0 && event.At.Before(previous) {
				return nil, fmt.Errorf("shared leg %s events are out of order", leg.Instrument.Symbol)
			}
			events = append(events, sharedEvent{symbol: leg.Instrument.Symbol, event: event})
			previous = event.At
		}
	}
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].event.At.Equal(events[j].event.At) {
			return events[i].symbol < events[j].symbol
		}
		return events[i].event.At.Before(events[j].event.At)
	})
	if len(events) == 0 {
		return nil, errors.New("shared scenario has no events")
	}
	return events, nil
}
