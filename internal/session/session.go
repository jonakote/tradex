package session

import (
	"errors"
	"fmt"
	"time"
)

type Definition struct {
	Name        string
	Location    *time.Location
	StartMinute int
	EndMinute   int
}

func New(name, timezone string, startHour, startMinute, endHour, endMinute int) (Definition, error) {
	if name == "" {
		return Definition{}, errors.New("session name is required")
	}
	if startHour < 0 || startHour > 23 || endHour < 0 || endHour > 23 ||
		startMinute < 0 || startMinute > 59 || endMinute < 0 || endMinute > 59 {
		return Definition{}, errors.New("invalid session time")
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return Definition{}, fmt.Errorf("load session timezone: %w", err)
	}
	start := startHour*60 + startMinute
	end := endHour*60 + endMinute
	if start == end {
		return Definition{}, errors.New("session cannot span exactly 24 hours")
	}
	return Definition{Name: name, Location: location, StartMinute: start, EndMinute: end}, nil
}

func (d Definition) Contains(at time.Time) bool {
	local := at.In(d.Location)
	minute := local.Hour()*60 + local.Minute()
	if d.StartMinute < d.EndMinute {
		return minute >= d.StartMinute && minute < d.EndMinute
	}
	return minute >= d.StartMinute || minute < d.EndMinute
}

// Window returns the UTC interval containing at, or the next interval if at is
// outside the session. DST is resolved by the IANA timezone database.
func (d Definition) Window(at time.Time) (time.Time, time.Time) {
	local := at.In(d.Location)
	year, month, day := local.Date()
	startHour, startMinute := d.StartMinute/60, d.StartMinute%60
	endHour, endMinute := d.EndMinute/60, d.EndMinute%60
	start := time.Date(year, month, day, startHour, startMinute, 0, 0, d.Location)
	if !d.Contains(at) && local.Before(start) {
		// Today is the next window.
	} else if !d.Contains(at) {
		start = start.AddDate(0, 0, 1)
	} else if d.StartMinute > d.EndMinute && local.Hour()*60+local.Minute() < d.EndMinute {
		start = start.AddDate(0, 0, -1)
	}
	end := time.Date(start.Year(), start.Month(), start.Day(), endHour, endMinute, 0, 0, d.Location)
	if d.StartMinute > d.EndMinute {
		end = end.AddDate(0, 0, 1)
	}
	return start.UTC(), end.UTC()
}

type MarketSessions struct {
	Sydney  Definition
	Tokyo   Definition
	London  Definition
	NewYork Definition
}

func Defaults() (MarketSessions, error) {
	sydney, err := New("sydney", "Australia/Sydney", 8, 0, 17, 0)
	if err != nil {
		return MarketSessions{}, err
	}
	tokyo, err := New("tokyo", "Asia/Tokyo", 9, 0, 17, 0)
	if err != nil {
		return MarketSessions{}, err
	}
	london, err := New("london", "Europe/London", 8, 0, 17, 0)
	if err != nil {
		return MarketSessions{}, err
	}
	newYork, err := New("new_york", "America/New_York", 8, 0, 17, 0)
	if err != nil {
		return MarketSessions{}, err
	}
	return MarketSessions{Sydney: sydney, Tokyo: tokyo, London: london, NewYork: newYork}, nil
}

func (s MarketSessions) Active(at time.Time) []string {
	definitions := []Definition{s.Sydney, s.Tokyo, s.London, s.NewYork}
	active := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		if definition.Contains(at) {
			active = append(active, definition.Name)
		}
	}
	return active
}

func (s MarketSessions) LondonNewYorkOverlap(at time.Time) bool {
	return s.London.Contains(at) && s.NewYork.Contains(at)
}
