package marketdata

import (
	"strings"
	"testing"
	"time"

	"tradex/internal/broker/paper"
)

func testBrokerSchedule() BrokerSchedule {
	weekly := make([]WeeklyWindow, 0, 7)
	for weekday := time.Sunday; weekday <= time.Saturday; weekday++ {
		weekly = append(weekly, WeeklyWindow{Weekday: weekday, OpenMinute: 9 * 60, CloseMinute: 17 * 60, Closed: weekday == time.Saturday || weekday == time.Sunday})
	}
	return BrokerSchedule{
		SchemaVersion: BrokerScheduleSchema,
		Provenance:    ScheduleProvenance{DatasetID: "pepperstone-eurusd-2026", Vendor: "fixture", Feed: "pepperstone-hours", Broker: "Pepperstone", RetrievedAt: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), SourceTimezone: "UTC", RawArtifactSHA256: strings.Repeat("b", 64), LicenseReference: "fixture-license"},
		Broker:        "Pepperstone", AccountType: "demo-standard", Symbol: "EURUSD", EffectiveFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), EffectiveTo: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
		Calendar: BrokerCalendar{Timezone: "UTC", Weekly: weekly, Exceptions: []CalendarException{{Date: "2026-08-18", Closed: true}}},
		Rollover: paper.RolloverConfig{Enabled: true, MinuteUTC: 22 * 60, ChargeWeekdays: [7]bool{false, true, true, true, true, true, false}, TripleWeekday: time.Wednesday, TripleMultiplier: 3, LongPerQuantityStepMinor: -1, ShortPerQuantityStepMinor: 1},
	}
}

func TestBrokerScheduleValidatesEffectiveDatesAndExceptions(t *testing.T) {
	schedule := testBrokerSchedule()
	if err := schedule.Validate(); err != nil {
		t.Fatal(err)
	}
	first, err := schedule.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	schedule.ExceptionsMutationForTest()
	second, err := schedule.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("schedule fingerprint did not change")
	}
}

func (s *BrokerSchedule) ExceptionsMutationForTest() {
	s.Calendar.Exceptions[0].Closed = false
	s.Calendar.Exceptions[0].Windows = []WeeklyWindow{{Weekday: time.Tuesday, OpenMinute: 10 * 60, CloseMinute: 16 * 60}}
}

func TestBrokerScheduleBoundariesRespectClosure(t *testing.T) {
	schedule := testBrokerSchedule()
	boundaries, err := schedule.Boundaries(time.Date(2026, 8, 18, 8, 0, 0, 0, time.UTC), time.Date(2026, 8, 18, 18, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(boundaries) != 1 || boundaries[0].Open {
		t.Fatalf("closed exception generated unexpected boundaries: %+v", boundaries)
	}
}
