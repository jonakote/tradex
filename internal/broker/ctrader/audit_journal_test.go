package ctrader

import (
	"path/filepath"
	"testing"
	"time"

	"tradex/internal/journal"
)

func TestJournalAuditRecorderPersistsOperationalEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	stream, err := journal.Create(path, true)
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := NewJournalAuditRecorder(stream)
	if err != nil {
		t.Fatal(err)
	}
	recorder.Record(AuditEvent{At: time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC), Kind: AuditSafetyBlocked, AccountID: "acct", Detail: "breaker"})
	if err := recorder.Err(); err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	events, err := journal.ReadAll(path)
	if err != nil || len(events) != 1 || events[0].Kind != "CTRADER_SAFETY_BLOCKED" {
		t.Fatalf("events = %#v, err %v", events, err)
	}
}
