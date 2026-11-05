// Package crt implements a mechanical research interpretation of Candle Range
// Theory: a range candle, a purge of exactly one of its extremes, and an
// expansion back across the range. It is deliberately versioned and
// evidence-producing; it does not claim to reproduce discretionary intent.
//
// Determinism rules that this package fixes once, so a replay can never
// depend on evaluation order:
//
//  1. A pattern is knowable only at the purge candle's close.
//  2. Entry is a limit at the purge candle's equilibrium, never a market fill.
//  3. If one confirmation bar spans both stop and target, the stop is taken.
//     Intrabar path is unknown, so the pessimistic branch is mandatory.
//  4. An unfilled setup expires on a fixed setup-bar budget, not on judgment.
package crt

import (
	"errors"
	"fmt"
	"time"

	"tradex/internal/domain"
	"tradex/internal/features"
)

type State string

const (
	SeekingRange State = "SEEKING_RANGE"
	Armed        State = "ARMED"
	Filled       State = "FILLED"
	Completed    State = "COMPLETED"
	Invalidated  State = "INVALIDATED"
	Expired      State = "EXPIRED"
)

// Outcome is the resolved reason a trade left the book.
type Outcome string

const (
	OutcomeTakeProfit  Outcome = "TAKE_PROFIT"
	OutcomeStopLoss    Outcome = "STOP_LOSS"
	OutcomeTimeStop    Outcome = "TIME_STOP"
	OutcomeExpired     Outcome = "ENTRY_EXPIRED"
	OutcomeInvalidated Outcome = "INVALIDATED"
)

type Config struct {
	SetupTimeframe        time.Duration `json:"setup_timeframe"`
	ConfirmationTimeframe time.Duration `json:"confirmation_timeframe"`
	// EntryExpirySetupBars is how many completed setup candles the limit may
	// remain unfilled. 1 means "fill inside the expansion candle or stand down".
	EntryExpirySetupBars int `json:"entry_expiry_setup_bars"`
	// MaxHoldSetupBars closes an open position at market after this many
	// completed setup candles.
	MaxHoldSetupBars int `json:"max_hold_setup_bars"`
	// RequireTrendAlignment gates a setup on higher-timeframe context.
	RequireTrendAlignment bool                       `json:"require_trend_alignment"`
	Detector              features.CandleRangeConfig `json:"detector"`
}

func DefaultConfig() Config {
	return Config{
		SetupTimeframe:        4 * time.Hour,
		ConfirmationTimeframe: 15 * time.Minute,
		EntryExpirySetupBars:  1,
		MaxHoldSetupBars:      2,
		RequireTrendAlignment: false,
		Detector:              features.DefaultCandleRangeConfig(),
	}
}

func (c Config) Validate() error {
	if c.SetupTimeframe <= 0 || c.ConfirmationTimeframe <= 0 {
		return errors.New("CRT timeframes must be positive")
	}
	if c.ConfirmationTimeframe >= c.SetupTimeframe {
		return errors.New("CRT confirmation timeframe must be below setup timeframe")
	}
	if c.SetupTimeframe%c.ConfirmationTimeframe != 0 {
		return errors.New("CRT setup timeframe must be a whole multiple of the confirmation timeframe")
	}
	if c.EntryExpirySetupBars <= 0 {
		return errors.New("CRT entry expiry must be at least one setup bar")
	}
	if c.MaxHoldSetupBars <= 0 {
		return errors.New("CRT maximum hold must be at least one setup bar")
	}
	return c.Detector.Validate()
}

// Setup is emitted when a range is detected and all gates pass. Entry, stop,
// and target are already fixed; the risk engine sizes it independently.
type Setup struct {
	Pattern features.CandleRange `json:"pattern"`
	ArmedAt time.Time            `json:"armed_at"`
	Trend   features.Trend       `json:"trend"`
}

// Resolution reports a terminal event. RMultipleBps is realized reward over
// the objective entry-to-stop distance (10000 bps = 1R) and is zero for a
// setup that never filled.
type Resolution struct {
	Outcome      Outcome              `json:"outcome"`
	Pattern      features.CandleRange `json:"pattern"`
	EntryPrice   domain.Price         `json:"entry_price"`
	ExitPrice    domain.Price         `json:"exit_price"`
	FilledAt     time.Time            `json:"filled_at"`
	ResolvedAt   time.Time            `json:"resolved_at"`
	RMultipleBps int64                `json:"r_multiple_bps"`
}

type Transition struct {
	From   State        `json:"from"`
	To     State        `json:"to"`
	At     time.Time    `json:"at"`
	Reason string       `json:"reason"`
	Level  domain.Price `json:"level"`
}

type Machine struct {
	config      Config
	state       State
	trend       features.Trend
	previous    *domain.Candle
	pattern     features.CandleRange
	armedAt     time.Time
	filledAt    time.Time
	entryPrice  domain.Price
	setupBars   int
	transitions []Transition
}

func New(config Config) (*Machine, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &Machine{state: SeekingRange, config: config, trend: features.TrendNeutral}, nil
}

func (m *Machine) State() State                  { return m.state }
func (m *Machine) Pattern() features.CandleRange { return m.pattern }

// SetContext records higher-timeframe bias. It is accepted only while seeking
// a range so an armed setup cannot be re-justified after the fact.
func (m *Machine) SetContext(trend features.Trend) error {
	if m.state != SeekingRange {
		return errors.New("CRT context can change only while seeking a range")
	}
	m.trend = trend
	return nil
}

// StandDown cancels an armed setup without resolving it as a trade. It exists
// for research harnesses that discover the setup cannot be adjudicated — for
// example when the confirmation stream has no data in its fill window. It is
// recorded as an invalidation in the audit trail and is never a P&L event.
func (m *Machine) StandDown(reason string) error {
	if m.state != Armed {
		return fmt.Errorf("cannot stand down from %s", m.state)
	}
	if reason == "" {
		return errors.New("stand-down requires a reason")
	}
	m.move(Invalidated, m.armedAt, reason, m.pattern.EntryReference)
	m.Reset()
	return nil
}

// Reset returns a terminal machine to SEEKING_RANGE, preserving the candle
// stream so the next pair is evaluated without re-feeding history.
func (m *Machine) Reset() {
	switch m.state {
	case Completed, Invalidated, Expired:
		m.state = SeekingRange
		m.pattern = features.CandleRange{}
		m.setupBars = 0
		m.entryPrice, m.filledAt, m.armedAt = 0, time.Time{}, time.Time{}
	}
}

// OnSetupCandle consumes a completed setup-timeframe candle. It arms a new
// setup, ages an existing one, and applies the time stop. At most one of the
// returned pointers is non-nil.
func (m *Machine) OnSetupCandle(candle domain.Candle) (*Setup, *Resolution, error) {
	if err := m.validateCandle(candle, m.config.SetupTimeframe); err != nil {
		return nil, nil, err
	}
	if m.previous != nil && !candle.OpenTime.After(m.previous.OpenTime) {
		return nil, nil, errors.New("setup candle is duplicate or out of order")
	}
	previous := m.previous
	stored := candle
	m.previous = &stored

	switch m.state {
	case Armed:
		m.setupBars++
		if m.setupBars >= m.config.EntryExpirySetupBars {
			resolution := m.terminal(OutcomeExpired, Expired, candle.CloseTime,
				"entry limit expired unfilled", 0, 0)
			return nil, &resolution, nil
		}
		return nil, nil, nil
	case Filled:
		m.setupBars++
		if m.setupBars >= m.config.EntryExpirySetupBars+m.config.MaxHoldSetupBars {
			resolution := m.terminal(OutcomeTimeStop, Completed, candle.CloseTime,
				"time stop closed position at setup close", candle.Close,
				rMultipleBps(m.pattern, m.entryPrice, candle.Close))
			return nil, &resolution, nil
		}
		return nil, nil, nil
	case SeekingRange:
		if previous == nil {
			return nil, nil, nil
		}
		pattern, ok := features.DetectCandleRange(*previous, candle, m.config.Detector)
		if !ok {
			return nil, nil, nil
		}
		if m.config.RequireTrendAlignment && !m.trendAligned(pattern.Direction()) {
			return nil, nil, nil
		}
		m.pattern = pattern
		m.armedAt = pattern.DetectedAt
		m.setupBars = 0
		m.move(Armed, pattern.DetectedAt, "purge closed back inside range", pattern.EntryReference)
		setup := Setup{Pattern: pattern, ArmedAt: pattern.DetectedAt, Trend: m.trend}
		return &setup, nil, nil
	default:
		return nil, nil, fmt.Errorf("setup candle is not accepted in %s", m.state)
	}
}

// OnConfirmationCandle consumes a completed confirmation-timeframe candle and
// resolves fills and exits. A bar that spans both stop and target resolves to
// the stop: the intrabar path is unknown and must not be assumed favourable.
func (m *Machine) OnConfirmationCandle(candle domain.Candle) (*Resolution, error) {
	if m.state != Armed && m.state != Filled {
		return nil, fmt.Errorf("confirmation candle is not accepted in %s", m.state)
	}
	if err := m.validateCandle(candle, m.config.ConfirmationTimeframe); err != nil {
		return nil, err
	}
	if !candle.CloseTime.After(m.armedAt) {
		return nil, errors.New("confirmation candle does not occur after the armed pattern")
	}
	bullish := m.pattern.Kind == features.CandleRangeBullish

	if m.state == Armed {
		touched := (bullish && candle.Low <= m.pattern.EntryReference) ||
			(!bullish && candle.High >= m.pattern.EntryReference)
		if !touched {
			return nil, nil
		}
		m.entryPrice = m.pattern.EntryReference
		m.filledAt = candle.CloseTime
		m.move(Filled, candle.CloseTime, "limit filled at purge equilibrium", m.entryPrice)
	}

	stopHit := (bullish && candle.Low <= m.pattern.StopReference) ||
		(!bullish && candle.High >= m.pattern.StopReference)
	targetHit := (bullish && candle.High >= m.pattern.TargetReference) ||
		(!bullish && candle.Low <= m.pattern.TargetReference)

	switch {
	case stopHit:
		resolution := m.terminal(OutcomeStopLoss, Completed, candle.CloseTime,
			"stop reached inside confirmation bar", m.pattern.StopReference,
			rMultipleBps(m.pattern, m.entryPrice, m.pattern.StopReference))
		return &resolution, nil
	case targetHit:
		resolution := m.terminal(OutcomeTakeProfit, Completed, candle.CloseTime,
			"target reached inside confirmation bar", m.pattern.TargetReference,
			rMultipleBps(m.pattern, m.entryPrice, m.pattern.TargetReference))
		return &resolution, nil
	}
	return nil, nil
}

func (m *Machine) Transitions() []Transition {
	result := make([]Transition, len(m.transitions))
	copy(result, m.transitions)
	return result
}

func (m *Machine) terminal(outcome Outcome, state State, at time.Time, reason string,
	exit domain.Price, rBps int64) Resolution {
	resolution := Resolution{
		Outcome: outcome, Pattern: m.pattern, EntryPrice: m.entryPrice, ExitPrice: exit,
		FilledAt: m.filledAt, ResolvedAt: at, RMultipleBps: rBps,
	}
	m.move(state, at, reason, exit)
	return resolution
}

func (m *Machine) trendAligned(direction features.BreakDirection) bool {
	if direction == features.BreakBullish {
		return m.trend == features.TrendBullish
	}
	return m.trend == features.TrendBearish
}

func (m *Machine) validateCandle(candle domain.Candle, timeframe time.Duration) error {
	if err := candle.Validate(); err != nil {
		return err
	}
	if candle.Timeframe != timeframe {
		return errors.New("candle timeframe does not match CRT configuration")
	}
	if m.pattern.Symbol != "" && candle.Symbol != m.pattern.Symbol {
		return errors.New("candle symbol does not match the armed CRT pattern")
	}
	return nil
}

func (m *Machine) move(to State, at time.Time, reason string, level domain.Price) {
	from := m.state
	m.state = to
	m.transitions = append(m.transitions, Transition{
		From: from, To: to, At: at, Reason: reason, Level: level,
	})
}

// rMultipleBps expresses realized price movement as a ratio of the objective
// risk distance recorded on the pattern.
func rMultipleBps(pattern features.CandleRange, entry, exit domain.Price) int64 {
	if pattern.RiskTicks <= 0 || entry <= 0 {
		return 0
	}
	moved := int64(exit - entry)
	if pattern.Kind == features.CandleRangeBearish {
		moved = int64(entry - exit)
	}
	return moved * 10000 / pattern.RiskTicks
}
