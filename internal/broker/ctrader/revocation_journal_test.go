package ctrader

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tradex/internal/journal"
)

func TestRevocationsPersistAndReloadFromJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	stream, err := journal.Create(path, true)
	if err != nil {
		t.Fatal(err)
	}
	list := NewRevocationList()
	hash := strings.Repeat("a", 64)
	at := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	if err := list.RevokeToJournal(context.Background(), stream, hash, "superseded", at); err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	events, err := journal.ReadAll(path)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadRevocations(events)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := reloaded.Lookup(hash)
	if !ok || entry.Reason != "superseded" {
		t.Fatalf("entry=%#v ok=%v", entry, ok)
	}
}

func TestRevocationPrecedenceAndConflictRules(t *testing.T) {
	list := NewRevocationList()
	hash := strings.Repeat("b", 64)
	at := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	if err := list.Revoke(hash, "first", at); err != nil {
		t.Fatal(err)
	}
	if err := list.Revoke(hash, "first", at); err != nil {
		t.Fatal(err)
	}
	if err := list.Revoke(hash, "stale", at.Add(-time.Second)); err == nil {
		t.Fatal("expected stale rejection")
	}
	if err := list.Revoke(hash, "conflict", at); err == nil {
		t.Fatal("expected same-time conflict")
	}
	if err := list.Revoke(hash, "newer", at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	entry, _ := list.Lookup(hash)
	if entry.Reason != "newer" {
		t.Fatalf("entry=%#v", entry)
	}
}
