package ctrader

import (
	"errors"
	"strings"
	"time"

	"tradex/internal/journal"
)

type OperationalEvidence struct {
	TotalEvents       int       `json:"total_events"`
	SafetyBlocked     int       `json:"safety_blocked"`
	SafetyFailures    int       `json:"safety_failures"`
	HeartbeatTimeouts int       `json:"heartbeat_timeouts"`
	Disconnects       int       `json:"disconnects"`
	Reconnects        int       `json:"reconnects"`
	LastEventAt       time.Time `json:"last_event_at,omitempty"`
	Healthy           bool      `json:"healthy"`
	RecorderError     string    `json:"recorder_error,omitempty"`
}

func BuildOperationalEvidence(events []journal.Event, recorderErr error) (OperationalEvidence, error) {
	report := OperationalEvidence{Healthy: recorderErr == nil}
	if recorderErr != nil {
		report.RecorderError = recorderErr.Error()
	}
	for _, event := range events {
		if !strings.HasPrefix(event.Kind, "CTRADER_") {
			continue
		}
		report.TotalEvents++
		if event.At.After(report.LastEventAt) {
			report.LastEventAt = event.At
		}
		switch event.Kind {
		case "CTRADER_SAFETY_BLOCKED":
			report.SafetyBlocked++
		case "CTRADER_SAFETY_FAILURE":
			report.SafetyFailures++
		case "CTRADER_HEARTBEAT_TIMEOUT":
			report.HeartbeatTimeouts++
		case "CTRADER_DISCONNECTED":
			report.Disconnects++
		case "CTRADER_RECONNECTED":
			report.Reconnects++
		}
	}
	if report.TotalEvents == 0 && recorderErr == nil {
		return report, errors.New("no cTrader operational events found")
	}
	return report, nil
}
