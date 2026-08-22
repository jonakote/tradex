package ctrader

import (
	"testing"
	"time"

	"tradex/internal/journal"
)

func TestBuildOperationalEvidence(t *testing.T) {
	at := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	events := []journal.Event{{Kind: "CTRADER_SAFETY_BLOCKED", At: at}, {Kind: "CTRADER_SAFETY_FAILURE", At: at.Add(time.Second)}, {Kind: "CTRADER_HEARTBEAT_TIMEOUT", At: at.Add(2 * time.Second)}, {Kind: "CTRADER_DISCONNECTED", At: at.Add(3 * time.Second)}, {Kind: "CTRADER_RECONNECTED", At: at.Add(4 * time.Second)}, {Kind: "ORDER_FILLED", At: at.Add(5 * time.Second)}}
	report, err := BuildOperationalEvidence(events, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.TotalEvents != 5 || report.SafetyBlocked != 1 || report.SafetyFailures != 1 || report.HeartbeatTimeouts != 1 || report.Disconnects != 1 || report.Reconnects != 1 || !report.Healthy || !report.LastEventAt.Equal(at.Add(4*time.Second)) {
		t.Fatalf("report = %#v", report)
	}
}

func TestBuildOperationalEvidenceRequiresEventsOrReportsRecorderError(t *testing.T) {
	if _, err := BuildOperationalEvidence(nil, nil); err == nil {
		t.Fatal("expected empty evidence error")
	}
	report, err := BuildOperationalEvidence(nil, assertError("append failed"))
	if err != nil || report.Healthy || report.RecorderError != "append failed" {
		t.Fatalf("report = %#v, err %v", report, err)
	}
}

type assertError string

func (e assertError) Error() string { return string(e) }
