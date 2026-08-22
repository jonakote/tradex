package marketdata

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"tradex/internal/broker/paper"
	"tradex/internal/experiment"
)

const BrokerScheduleSchema = "tradex.broker-schedule/v1"

type ScheduleProvenance struct {
	DatasetID         string    `json:"dataset_id"`
	Vendor            string    `json:"vendor"`
	Feed              string    `json:"feed"`
	Broker            string    `json:"broker"`
	RetrievedAt       time.Time `json:"retrieved_at"`
	SourceTimezone    string    `json:"source_timezone"`
	RawArtifactSHA256 string    `json:"raw_artifact_sha256"`
	LicenseReference  string    `json:"license_reference"`
}

type WeeklyWindow struct {
	Weekday     time.Weekday `json:"weekday"`
	OpenMinute  int          `json:"open_minute"`
	CloseMinute int          `json:"close_minute"`
	Closed      bool         `json:"closed"`
}

type CalendarException struct {
	Date    string         `json:"date"`
	Windows []WeeklyWindow `json:"windows,omitempty"`
	Closed  bool           `json:"closed"`
}

type BrokerCalendar struct {
	Timezone   string              `json:"timezone"`
	Weekly     []WeeklyWindow      `json:"weekly"`
	Exceptions []CalendarException `json:"exceptions,omitempty"`
}

type BrokerSchedule struct {
	SchemaVersion string               `json:"schema_version"`
	Provenance    ScheduleProvenance   `json:"provenance"`
	Broker        string               `json:"broker"`
	AccountType   string               `json:"account_type"`
	Symbol        string               `json:"symbol"`
	EffectiveFrom time.Time            `json:"effective_from"`
	EffectiveTo   time.Time            `json:"effective_to"`
	Calendar      BrokerCalendar       `json:"calendar"`
	Rollover      paper.RolloverConfig `json:"rollover"`
}

func LoadBrokerSchedule(path string) (BrokerSchedule, error) {
	file, err := os.Open(path)
	if err != nil {
		return BrokerSchedule{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var schedule BrokerSchedule
	if err := decoder.Decode(&schedule); err != nil {
		return BrokerSchedule{}, fmt.Errorf("decode broker schedule: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return BrokerSchedule{}, errors.New("broker schedule contains trailing JSON")
	}
	if err := schedule.Validate(); err != nil {
		return BrokerSchedule{}, err
	}
	return schedule, nil
}

func (s BrokerSchedule) Validate() error {
	if s.SchemaVersion != BrokerScheduleSchema || s.Broker == "" || s.AccountType == "" || s.Symbol == "" || s.EffectiveFrom.IsZero() || s.EffectiveTo.IsZero() || !s.EffectiveFrom.Before(s.EffectiveTo) || !isUTC(s.EffectiveFrom) || !isUTC(s.EffectiveTo) {
		return errors.New("invalid broker schedule identity or effective range")
	}
	if s.Provenance.DatasetID == "" || s.Provenance.Vendor == "" || s.Provenance.Feed == "" || s.Provenance.Broker == "" || s.Provenance.RetrievedAt.IsZero() || !isUTC(s.Provenance.RetrievedAt) || s.Provenance.SourceTimezone == "" || !validSHA256(s.Provenance.RawArtifactSHA256) || s.Provenance.LicenseReference == "" {
		return errors.New("invalid broker schedule provenance")
	}
	if s.Provenance.Broker != s.Broker {
		return errors.New("schedule broker contradicts provenance")
	}
	if _, err := time.LoadLocation(s.Provenance.SourceTimezone); err != nil {
		return fmt.Errorf("load schedule provenance timezone: %w", err)
	}
	if err := s.Calendar.Validate(); err != nil {
		return err
	}
	return s.Rollover.Validate()
}

func (c BrokerCalendar) Validate() error {
	if c.Timezone == "" {
		return errors.New("broker calendar timezone is required")
	}
	if _, err := time.LoadLocation(c.Timezone); err != nil {
		return fmt.Errorf("load broker calendar timezone: %w", err)
	}
	if len(c.Weekly) != 7 {
		return errors.New("broker calendar requires exactly seven weekly entries")
	}
	seen := make(map[time.Weekday]bool, 7)
	for _, window := range c.Weekly {
		if err := validateWindow(window); err != nil {
			return err
		}
		if seen[window.Weekday] {
			return errors.New("broker calendar contains duplicate weekday")
		}
		seen[window.Weekday] = true
	}
	for _, weekday := range []time.Weekday{time.Sunday, time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday, time.Saturday} {
		if !seen[weekday] {
			return errors.New("broker calendar is missing a weekday")
		}
	}
	seenDates := make(map[string]bool, len(c.Exceptions))
	for _, exception := range c.Exceptions {
		date, err := time.ParseInLocation("2006-01-02", exception.Date, time.UTC)
		if err != nil || date.Format("2006-01-02") != exception.Date || seenDates[exception.Date] {
			return errors.New("invalid or duplicate broker calendar exception date")
		}
		seenDates[exception.Date] = true
		if !exception.Closed && len(exception.Windows) == 0 {
			return errors.New("open calendar exception requires windows")
		}
		if !exception.Closed && len(exception.Windows) > 0 {
			if exceptionDate, parseErr := time.ParseInLocation("2006-01-02", exception.Date, time.UTC); parseErr == nil {
				for _, window := range exception.Windows {
					if window.Weekday != exceptionDate.Weekday() {
						return errors.New("calendar exception window weekday does not match its date")
					}
				}
			}
		}
		for _, window := range exception.Windows {
			if err := validateWindow(window); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateWindow(window WeeklyWindow) error {
	if window.Weekday < time.Sunday || window.Weekday > time.Saturday || window.OpenMinute < 0 || window.OpenMinute >= 1440 || window.CloseMinute < 0 || window.CloseMinute >= 1440 {
		return errors.New("invalid broker calendar window")
	}
	if !window.Closed && window.OpenMinute == window.CloseMinute {
		return errors.New("open broker calendar window cannot span exactly 24 hours")
	}
	return nil
}

func (s BrokerSchedule) Fingerprint() (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	return experiment.Digest(s)
}

func (s BrokerSchedule) openAt(at time.Time) bool {
	local := at.In(mustLocation(s.Calendar.Timezone))
	date := local.Format("2006-01-02")
	windows := s.Calendar.windowsFor(date, local.Weekday())
	minute := local.Hour()*60 + local.Minute()
	for _, window := range windows {
		if window.Closed {
			continue
		}
		if window.OpenMinute < window.CloseMinute && minute >= window.OpenMinute && minute < window.CloseMinute {
			return true
		}
		if window.OpenMinute > window.CloseMinute && (minute >= window.OpenMinute || minute < window.CloseMinute) {
			return true
		}
	}
	previousDate := local.AddDate(0, 0, -1).Format("2006-01-02")
	for _, window := range s.Calendar.windowsFor(previousDate, local.AddDate(0, 0, -1).Weekday()) {
		if !window.Closed && window.OpenMinute > window.CloseMinute && minute < window.CloseMinute {
			return true
		}
	}
	return false
}

func (c BrokerCalendar) windowsFor(date string, weekday time.Weekday) []WeeklyWindow {
	for _, exception := range c.Exceptions {
		if exception.Date == date {
			if exception.Closed {
				return nil
			}
			return exception.Windows
		}
	}
	for _, window := range c.Weekly {
		if window.Weekday == weekday {
			return []WeeklyWindow{window}
		}
	}
	return nil
}

func mustLocation(name string) *time.Location {
	location, _ := time.LoadLocation(name)
	return location
}

type scheduleBoundary struct {
	At   time.Time
	Open bool
}

func (s BrokerSchedule) Boundaries(start, end time.Time) ([]scheduleBoundary, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	if start.Before(s.EffectiveFrom) || end.After(s.EffectiveTo) {
		return nil, errors.New("scenario range lies outside broker schedule effective range")
	}
	location := mustLocation(s.Calendar.Timezone)
	localStart := start.In(location).AddDate(0, 0, -1)
	localEnd := end.In(location).AddDate(0, 0, 1)
	boundaries := []scheduleBoundary{{At: start, Open: s.openAt(start)}}
	for date := localStart; !date.After(localEnd); date = date.AddDate(0, 0, 1) {
		dateText := date.Format("2006-01-02")
		for _, window := range s.Calendar.windowsFor(dateText, date.Weekday()) {
			if window.Closed {
				continue
			}
			open := time.Date(date.Year(), date.Month(), date.Day(), window.OpenMinute/60, window.OpenMinute%60, 0, 0, location).UTC()
			closeDate := date
			if window.OpenMinute > window.CloseMinute {
				closeDate = closeDate.AddDate(0, 0, 1)
			}
			close := time.Date(closeDate.Year(), closeDate.Month(), closeDate.Day(), window.CloseMinute/60, window.CloseMinute%60, 0, 0, location).UTC()
			if open.After(start) && open.Before(end) {
				boundaries = append(boundaries, scheduleBoundary{open, true})
			}
			if close.After(start) && close.Before(end) {
				boundaries = append(boundaries, scheduleBoundary{close, false})
			}
		}
	}
	sort.SliceStable(boundaries, func(i, j int) bool { return boundaries[i].At.Before(boundaries[j].At) })
	result := make([]scheduleBoundary, 0, len(boundaries))
	for _, boundary := range boundaries {
		if len(result) == 0 || !result[len(result)-1].At.Equal(boundary.At) || result[len(result)-1].Open != boundary.Open {
			result = append(result, boundary)
		}
	}
	return result, nil
}
