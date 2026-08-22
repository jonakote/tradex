package backtest

import (
	"errors"
	"time"
)

var ErrEventsOutOfOrder = errors.New("backtest events are out of order")

type TimedEvent interface {
	EventTime() time.Time
}

// Replay processes events exactly in caller-provided order. It rejects an
// unsorted stream instead of silently reordering equal-time market events.
func Replay[T TimedEvent](events []T, handle func(T) error) error {
	var previous time.Time
	for i, event := range events {
		current := event.EventTime()
		if current.IsZero() {
			return errors.New("backtest event has zero timestamp")
		}
		if i > 0 && current.Before(previous) {
			return ErrEventsOutOfOrder
		}
		if err := handle(event); err != nil {
			return err
		}
		previous = current
	}
	return nil
}
