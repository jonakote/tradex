// Package icc implements a mechanical research interpretation of Trades by
// Sci's Indication-Correction-Continuation model. It is deliberately versioned
// and evidence-producing; it does not claim to reproduce discretionary intent.
package icc

import (
	"errors"
	"fmt"
	"time"

	"tradex/internal/domain"
	"tradex/internal/features"
)

type State string

const (
	SeekingIndication    State = "SEEKING_INDICATION"
	AwaitingCorrection   State = "AWAITING_CORRECTION"
	AwaitingContinuation State = "AWAITING_CONTINUATION"
	Completed            State = "COMPLETED"
	Invalidated          State = "INVALIDATED"
)

type Config struct {
	SetupTimeframe        time.Duration
	ConfirmationTimeframe time.Duration
	MaxCorrectionBars     int
}

func (c Config) Validate() error {
	if c.SetupTimeframe <= 0 || c.ConfirmationTimeframe <= 0 {
		return errors.New("ICC timeframes must be positive")
	}
	if c.ConfirmationTimeframe >= c.SetupTimeframe {
		return errors.New("ICC confirmation timeframe must be below setup timeframe")
	}
	if c.MaxCorrectionBars <= 0 {
		return errors.New("ICC maximum correction bars must be positive")
	}
	return nil
}

func DefaultConfig() Config {
	return Config{SetupTimeframe: time.Hour, ConfirmationTimeframe: 15 * time.Minute, MaxCorrectionBars: 8}
}

type Indication struct {
	Break             features.StructureBreak
	InvalidationPrice domain.Price
	AllowedSession    bool
}

type Transition struct {
	From         State
	To           State
	At           time.Time
	Reason       string
	Level        domain.Price
	Invalidation domain.Price
}

type Signal struct {
	Direction       features.BreakDirection
	Symbol          string
	KnownAt         time.Time
	EntryReference  domain.Price
	StopReference   domain.Price
	IndicationLevel domain.Price
}

type Machine struct {
	config            Config
	state             State
	h4Trend           features.Trend
	h1Trend           features.Trend
	direction         features.BreakDirection
	symbol            string
	indicationLevel   domain.Price
	invalidationPrice domain.Price
	correctionExtreme domain.Price
	indicationAt      time.Time
	correctionAt      time.Time
	setupBars         int
	transitions       []Transition
}

func New(config Config) (*Machine, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &Machine{config: config, state: SeekingIndication, h4Trend: features.TrendNeutral, h1Trend: features.TrendNeutral}, nil
}

func (m *Machine) State() State { return m.state }

func (m *Machine) SetContext(h4, h1 features.Trend) error {
	if m.state != SeekingIndication {
		return errors.New("ICC context can change only while seeking an indication")
	}
	m.h4Trend, m.h1Trend = h4, h1
	return nil
}

func (m *Machine) Begin(input Indication) error {
	if m.state != SeekingIndication {
		return fmt.Errorf("cannot begin indication from %s", m.state)
	}
	if !input.AllowedSession {
		return errors.New("indication is outside allowed session")
	}
	if err := input.Break.Candle.Validate(); err != nil {
		return err
	}
	if input.Break.Candle.Timeframe != m.config.SetupTimeframe {
		return errors.New("indication is not on setup timeframe")
	}
	if input.Break.KnownAt.IsZero() || !input.Break.KnownAt.Equal(input.Break.Candle.CloseTime) {
		return errors.New("indication known time must be its completed candle close")
	}
	if !m.contextAligned(input.Break.Direction) {
		return errors.New("H4 and H1 context do not align with indication")
	}
	level := input.Break.BrokenSwing.Price
	if level <= 0 || input.InvalidationPrice <= 0 {
		return errors.New("indication and invalidation levels must be positive")
	}
	if input.Break.Direction == features.BreakBullish &&
		(input.InvalidationPrice >= level || input.Break.Candle.Close <= level) {
		return errors.New("invalid bullish indication geometry")
	}
	if input.Break.Direction == features.BreakBearish &&
		(input.InvalidationPrice <= level || input.Break.Candle.Close >= level) {
		return errors.New("invalid bearish indication geometry")
	}

	m.direction = input.Break.Direction
	m.symbol = input.Break.Candle.Symbol
	m.indicationLevel = level
	m.invalidationPrice = input.InvalidationPrice
	m.indicationAt = input.Break.KnownAt
	m.setupBars = 0
	m.move(AwaitingCorrection, input.Break.KnownAt, "aligned structure break established indication", level)
	return nil
}

func (m *Machine) OnSetupCandle(candle domain.Candle) error {
	if m.state != AwaitingCorrection && m.state != AwaitingContinuation {
		return fmt.Errorf("setup candle is not accepted in %s", m.state)
	}
	if err := m.validateCandle(candle, m.config.SetupTimeframe); err != nil {
		return err
	}
	if !candle.CloseTime.After(m.indicationAt) {
		return errors.New("setup candle does not occur after indication")
	}
	m.setupBars++
	if m.setupBars > m.config.MaxCorrectionBars {
		m.move(Invalidated, candle.CloseTime, "correction expired", m.indicationLevel)
		return nil
	}

	if m.direction == features.BreakBullish {
		if candle.Close <= m.invalidationPrice {
			m.move(Invalidated, candle.CloseTime, "bullish structural invalidation closed through", m.invalidationPrice)
			return nil
		}
		if m.state == AwaitingCorrection && candle.Low <= m.indicationLevel {
			m.correctionExtreme = candle.Low
			m.correctionAt = candle.CloseTime
			m.move(AwaitingContinuation, candle.CloseTime, "price corrected to bullish indication level", candle.Low)
		} else if m.state == AwaitingContinuation && candle.Low < m.correctionExtreme {
			m.correctionExtreme = candle.Low
			m.correctionAt = candle.CloseTime
		}
		return nil
	}

	if candle.Close >= m.invalidationPrice {
		m.move(Invalidated, candle.CloseTime, "bearish structural invalidation closed through", m.invalidationPrice)
		return nil
	}
	if m.state == AwaitingCorrection && candle.High >= m.indicationLevel {
		m.correctionExtreme = candle.High
		m.correctionAt = candle.CloseTime
		m.move(AwaitingContinuation, candle.CloseTime, "price corrected to bearish indication level", candle.High)
	} else if m.state == AwaitingContinuation && candle.High > m.correctionExtreme {
		m.correctionExtreme = candle.High
		m.correctionAt = candle.CloseTime
	}
	return nil
}

func (m *Machine) OnConfirmation(event features.StructureBreak, allowedSession bool) (*Signal, error) {
	if m.state != AwaitingContinuation {
		return nil, fmt.Errorf("continuation is not accepted in %s", m.state)
	}
	if !allowedSession {
		return nil, errors.New("continuation is outside allowed session")
	}
	if err := m.validateCandle(event.Candle, m.config.ConfirmationTimeframe); err != nil {
		return nil, err
	}
	if event.KnownAt.IsZero() || !event.KnownAt.Equal(event.Candle.CloseTime) || !event.KnownAt.After(m.correctionAt) {
		return nil, errors.New("continuation is not known after correction")
	}
	if event.Direction != m.direction {
		return nil, errors.New("continuation direction opposes indication")
	}
	if m.direction == features.BreakBullish && event.Candle.Close <= m.indicationLevel {
		return nil, errors.New("bullish continuation did not reclaim indication level")
	}
	if m.direction == features.BreakBearish && event.Candle.Close >= m.indicationLevel {
		return nil, errors.New("bearish continuation did not reclaim indication level")
	}

	signal := &Signal{
		Direction: m.direction, Symbol: m.symbol, KnownAt: event.KnownAt,
		EntryReference: event.Candle.Close, StopReference: m.correctionExtreme,
		IndicationLevel: m.indicationLevel,
	}
	m.move(Completed, event.KnownAt, "lower-timeframe structure confirmed continuation", event.Candle.Close)
	return signal, nil
}

func (m *Machine) Transitions() []Transition {
	result := make([]Transition, len(m.transitions))
	copy(result, m.transitions)
	return result
}

func (m *Machine) contextAligned(direction features.BreakDirection) bool {
	if direction == features.BreakBullish {
		return m.h4Trend == features.TrendBullish && m.h1Trend == features.TrendBullish
	}
	return m.h4Trend == features.TrendBearish && m.h1Trend == features.TrendBearish
}

func (m *Machine) validateCandle(candle domain.Candle, timeframe time.Duration) error {
	if err := candle.Validate(); err != nil {
		return err
	}
	if candle.Symbol != m.symbol || candle.Timeframe != timeframe {
		return errors.New("candle symbol or timeframe does not match ICC setup")
	}
	return nil
}

func (m *Machine) move(to State, at time.Time, reason string, level domain.Price) {
	from := m.state
	m.state = to
	m.transitions = append(m.transitions, Transition{
		From: from, To: to, At: at, Reason: reason, Level: level, Invalidation: m.invalidationPrice,
	})
}
