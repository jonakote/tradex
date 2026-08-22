package replay

import (
	"errors"
	"fmt"
	"sort"

	"tradex/internal/marketdata"
)

// CompileBrokerSchedule replaces hand-authored market-status events, applies
// the exact rollover block, and records the effective-dated schedule in the
// scenario for manifest provenance.
func CompileBrokerSchedule(base Scenario, schedule marketdata.BrokerSchedule) (Scenario, error) {
	if err := base.Validate(); err != nil {
		return Scenario{}, fmt.Errorf("validate base scenario: %w", err)
	}
	if err := schedule.Validate(); err != nil {
		return Scenario{}, err
	}
	if schedule.Symbol != base.Instrument.Symbol {
		return Scenario{}, errors.New("broker schedule symbol does not match base scenario")
	}
	start, end := base.Events[0].At, base.Events[len(base.Events)-1].At
	boundaries, err := schedule.Boundaries(start, end)
	if err != nil {
		return Scenario{}, err
	}
	result := base
	result.Name = base.Name + "-schedule-" + schedule.Provenance.DatasetID
	result.BrokerSchedule = &schedule
	result.Paper.Rollover = schedule.Rollover
	filtered := make([]Event, 0, len(base.Events)+len(boundaries))
	for _, event := range base.Events {
		if event.Kind != EventMarketStatus {
			filtered = append(filtered, event)
		}
	}
	for _, boundary := range boundaries {
		open := boundary.Open
		filtered = append(filtered, Event{At: boundary.At, Kind: EventMarketStatus, MarketOpen: &open})
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].At.Equal(filtered[j].At) {
			return filtered[i].Kind == EventMarketStatus && filtered[j].Kind != EventMarketStatus
		}
		return filtered[i].At.Before(filtered[j].At)
	})
	result.Events = filtered
	if err := result.Validate(); err != nil {
		return Scenario{}, fmt.Errorf("validate schedule-compiled scenario: %w", err)
	}
	return result, nil
}
