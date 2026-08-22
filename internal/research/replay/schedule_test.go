package replay

import (
	"path/filepath"
	"testing"
	"time"

	"tradex/internal/marketdata"
)

func TestCompileBrokerScheduleInjectsStatusesAndRollover(t *testing.T) {
	base, err := Load(filepath.Join("testdata", "eurusd_icc_win.json"))
	if err != nil {
		t.Fatal(err)
	}
	schedule := scheduleFixture()
	compiled, err := CompileBrokerSchedule(base, schedule)
	if err != nil {
		t.Fatal(err)
	}
	if compiled.BrokerSchedule == nil || compiled.Paper.Rollover != schedule.Rollover {
		t.Fatalf("schedule provenance or rollover missing: %+v", compiled)
	}
	statusCount := 0
	for _, event := range compiled.Events {
		if event.Kind == EventMarketStatus {
			statusCount++
		}
	}
	if statusCount == 0 {
		t.Fatal("schedule compiler emitted no market-status events")
	}
}

func scheduleFixture() marketdata.BrokerSchedule {
	weekly := make([]marketdata.WeeklyWindow, 0, 7)
	for weekday := time.Sunday; weekday <= time.Saturday; weekday++ {
		weekly = append(weekly, marketdata.WeeklyWindow{Weekday: weekday, OpenMinute: 9 * 60, CloseMinute: 17 * 60, Closed: weekday == time.Saturday || weekday == time.Sunday})
	}
	return marketdata.BrokerSchedule{SchemaVersion: marketdata.BrokerScheduleSchema, Provenance: marketdata.ScheduleProvenance{DatasetID: "fixture-schedule", Vendor: "fixture", Feed: "hours", Broker: "Pepperstone", RetrievedAt: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), SourceTimezone: "UTC", RawArtifactSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", LicenseReference: "fixture"}, Broker: "Pepperstone", AccountType: "demo", Symbol: "EURUSD", EffectiveFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), EffectiveTo: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), Calendar: marketdata.BrokerCalendar{Timezone: "UTC", Weekly: weekly}}
}
