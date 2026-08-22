package clock

import (
	"sync"
	"time"
)

type Clock interface {
	Now() time.Time
}

type System struct{}

func (System) Now() time.Time { return time.Now().UTC() }

type Fixed struct{ Time time.Time }

func (f Fixed) Now() time.Time { return f.Time }

type Manual struct {
	mu   sync.RWMutex
	time time.Time
}

func NewManual(initial time.Time) *Manual { return &Manual{time: initial.UTC()} }

func (m *Manual) Now() time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.time
}

func (m *Manual) Set(at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if at.IsZero() || (!m.time.IsZero() && at.Before(m.time)) {
		return &TimeError{Current: m.time, Requested: at}
	}
	m.time = at.UTC()
	return nil
}

type TimeError struct {
	Current   time.Time
	Requested time.Time
}

func (e *TimeError) Error() string { return "manual clock cannot move backward or to zero" }
