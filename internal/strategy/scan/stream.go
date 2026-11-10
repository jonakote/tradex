package scan

import (
	"errors"
	"fmt"
	"time"

	"tradex/internal/domain"
	"tradex/internal/features"
	"tradex/internal/instrument"
)

// LifecycleKind describes detector-independent execution events. Prices are
// idealized MID prices; the caller applies disclosed costs exactly once.
type LifecycleKind string

const (
	LifecycleDetected LifecycleKind = "DETECTED"
	LifecycleRejected LifecycleKind = "REJECTED"
	LifecycleEntry    LifecycleKind = "ENTRY"
	LifecycleExit     LifecycleKind = "EXIT"
	LifecycleExpired  LifecycleKind = "EXPIRED"
)

type LifecycleEvent struct {
	Kind       LifecycleKind `json:"kind"`
	Strategy   string        `json:"strategy"`
	Spec       string        `json:"spec"`
	Validation string        `json:"validation"`
	At         time.Time     `json:"at"`
	Setup      Setup         `json:"setup"`
	Price      domain.Price  `json:"price,omitempty"`
	EntryAt    time.Time     `json:"entry_at,omitempty"`
	ExitReason string        `json:"exit_reason,omitempty"`
	Ambiguous  bool          `json:"ambiguous,omitempty"`
}

// Stream owns one detector's trailing closed-bar window and pending position.
// It is intentionally independent of account sizing, costs and authorization.
// The admission callback is called before a proposal can become a position.
// Callers must serialize Advance; shared replay orders streams by UTC and ID.
type Stream struct {
	symbol    string
	spec      instrument.Spec
	timeframe time.Duration
	detector  Detector
	params    BTParams
	bars      []domain.Candle
	index     int
	last      time.Time
	open      *pending
	failed    error
}

func NewStream(symbol string, spec instrument.Spec, timeframe time.Duration, detector Detector, params BTParams) (*Stream, error) {
	verified, ok := instrument.Lookup(symbol)
	if !ok || verified != spec || symbol != spec.Symbol {
		return nil, errors.New("stream requires canonical instrument and registry-verified precision")
	}
	if detector == nil || detector.RequiredBars() < 1 || symbol == "" || timeframe <= 0 {
		return nil, errors.New("stream requires a detector, instrument and positive timeframe")
	}
	if err := params.validate(); err != nil {
		return nil, err
	}
	return &Stream{symbol: symbol, spec: spec, timeframe: timeframe, detector: detector, params: params, index: -1}, nil
}
func (s *Stream) event(kind LifecycleKind, setup Setup, at time.Time) LifecycleEvent {
	return LifecycleEvent{Kind: kind, Strategy: s.detector.Name(), Spec: s.detector.Spec(), Validation: "UNVALIDATED", At: at, Setup: setup}
}

// Advance accepts only ordered, non-overlapping, closed candles. Future data
// cannot enter the detector: Detect receives only a copy of the trailing window.
// A bar is known at CloseTime; the host replay clock must be that timestamp.
func (s *Stream) Advance(c domain.Candle, admit func(Setup) (bool, error)) ([]LifecycleEvent, error) {
	return s.AdvanceWithObserver(c, admit, nil)
}

// AdvanceWithObserver delivers each lifecycle event synchronously, before the
// next event or admission. Shared accounts must settle exits in observe so a
// new setup sees the updated exposure and equity. Any callback failure aborts
// the run; the stream must not be reused after an error.
func (s *Stream) AdvanceWithObserver(c domain.Candle, admit func(Setup) (bool, error), observe func(LifecycleEvent) error) (out []LifecycleEvent, runErr error) {
	if s.failed != nil {
		return nil, fmt.Errorf("stream aborted: %w", s.failed)
	}
	defer func() {
		if runErr != nil {
			s.failed = runErr
		}
	}()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if c.Symbol != s.symbol || c.Timeframe != s.timeframe {
		return nil, errors.New("stream candle instrument or timeframe mismatch")
	}
	if !s.last.IsZero() && c.OpenTime.Before(s.last) {
		return nil, errors.New("stream candles overlap or are out of order")
	}
	s.index++
	s.last = c.CloseTime
	s.bars = append(s.bars, c)
	required := s.detector.RequiredBars()
	if len(s.bars) > required {
		s.bars = s.bars[len(s.bars)-required:]
	}
	var events []LifecycleEvent
	emit := func(e LifecycleEvent) error {
		events = append(events, e)
		if observe != nil {
			return observe(e)
		}
		return nil
	}
	if s.open != nil && s.index > s.open.armedIndex {
		p := s.open
		if !p.filled {
			touched := c.High >= p.setup.Entry
			if !p.short() {
				touched = c.Low <= p.setup.Entry
			}
			if touched {
				p.filled = true
				p.fillIndex = s.index
				p.filledAt = c.CloseTime
				e := s.event(LifecycleEntry, p.setup, c.CloseTime)
				e.Price = p.setup.Entry
				e.EntryAt = p.filledAt
				if err := emit(e); err != nil {
					return events, err
				}
			} else if s.index-p.armedIndex >= s.params.ExpiryBars {
				if err := emit(s.event(LifecycleExpired, p.setup, c.CloseTime)); err != nil {
					return events, err
				}
				s.open = nil
				// Match batch semantics: no rearming on an expiry bar.
				return events, nil
			}
		}
		if p.filled {
			var exit domain.Price
			reason := ""
			ambiguous := false
			if !p.setup.StopTarget() {
				if reason == "" && p.setup.HoldExitRule != "" {
					if ibsBps, ok := features.IBSBps(c); ok {
						switch p.setup.HoldExitRule {
						case "ibs_strength_above":
							if ibsBps >= p.setup.HoldExitThresholdBps {
								exit, reason = c.Close, "STRENGTH_EXIT"
							}
						case "ibs_strength_below":
							if ibsBps <= p.setup.HoldExitThresholdBps {
								exit, reason = c.Close, "STRENGTH_EXIT"
							}
						}
					}
				}
				if reason == "" && s.index-p.fillIndex >= p.setup.HoldBars {
					exit, reason = c.Close, "TIME_EXIT"
				}
			} else {
				hitStop, hitTarget := c.High >= p.setup.Stop, c.Low <= p.setup.Target
				if !p.short() {
					hitStop, hitTarget = c.Low <= p.setup.Stop, c.High >= p.setup.Target
				}
				switch {
				case hitStop:
					exit = p.setup.Stop
					reason = "STOP_LOSS"
					ambiguous = hitTarget
				case hitTarget:
					exit = p.setup.Target
					reason = "TAKE_PROFIT"
				case s.index-p.fillIndex >= s.params.MaxHoldBars:
					exit = c.Close
					reason = "TIME_EXIT"
				}
			}
			if reason != "" {
				e := s.event(LifecycleExit, p.setup, c.CloseTime)
				e.Price = exit
				e.EntryAt = p.filledAt
				e.ExitReason = reason
				e.Ambiguous = ambiguous
				if err := emit(e); err != nil {
					return events, err
				}
				s.open = nil
			}
		}
	}
	if s.open == nil && len(s.bars) >= required {
		setup, ok := s.detector.Detect(s.spec, append([]domain.Candle(nil), s.bars...), s.params.SpreadTicks)
		if ok {
			if err := ValidateSetup(setup, c.CloseTime); err != nil {
				return events, fmt.Errorf("detector %s: %w", s.detector.Name(), err)
			}
			if err := emit(s.event(LifecycleDetected, setup, c.CloseTime)); err != nil {
				return events, err
			}
			accepted := true
			if admit != nil {
				var err error
				accepted, err = admit(setup)
				if err != nil {
					return events, err
				}
			}
			if !accepted {
				if err := emit(s.event(LifecycleRejected, setup, c.CloseTime)); err != nil {
					return events, err
				}
				return events, nil
			}
			s.open = &pending{setup: setup, armedIndex: s.index}
			if setup.EntryType == "MARKET" {
				s.open.filled = true
				s.open.fillIndex = s.index
				s.open.filledAt = c.CloseTime
				e := s.event(LifecycleEntry, setup, c.CloseTime)
				e.Price = setup.Entry
				e.EntryAt = c.CloseTime
				if err := emit(e); err != nil {
					return events, err
				}
			}
		}
	}
	return events, nil
}

func ValidateSetup(s Setup, knownAt time.Time) error {
	if s.Side != "buy" && s.Side != "sell" || s.EntryType != "LIMIT" && s.EntryType != "MARKET" || s.Entry <= 0 || s.HoldBars < 0 || !s.DetectedAt.Equal(knownAt) {
		return errors.New("invalid setup identity, geometry or detection time")
	}
	if s.HoldBars > 0 {
		if s.Stop != 0 || s.Target != 0 {
			return errors.New("time exit must not carry stop/target geometry")
		}
		return nil
	}
	if s.Stop <= 0 || s.Target <= 0 || s.RiskTicks <= 0 {
		return errors.New("stop/target setup requires positive geometry and risk")
	}
	if s.Side == "buy" && (s.Stop >= s.Entry || s.Target <= s.Entry) || s.Side == "sell" && (s.Stop <= s.Entry || s.Target >= s.Entry) {
		return errors.New("setup stop/target on wrong side")
	}
	distance := int64(s.Entry - s.Stop)
	if distance < 0 {
		distance = -distance
	}
	if distance != s.RiskTicks {
		return errors.New("setup risk differs from stop distance")
	}
	return nil
}
