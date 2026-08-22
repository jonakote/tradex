package journal

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestJournalPersistsSequenceAndHashChainAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	j, err := Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	first, err := j.Append(context.Background(), t0, "RISK_DECISION", "intent-1", map[string]any{"approved": true})
	if err != nil {
		t.Fatal(err)
	}
	second, err := j.Append(context.Background(), t0.Add(time.Second), "ORDER_FILLED", "intent-1", map[string]any{"price": 108403})
	if err != nil {
		t.Fatal(err)
	}
	if second.PreviousHash != first.Hash {
		t.Fatal("journal hash chain did not link")
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}

	j, err = Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	third, err := j.Append(context.Background(), t0.Add(2*time.Second), "POSITION_CLOSED", "intent-1", map[string]any{"net_minor": 100})
	if err != nil {
		t.Fatal(err)
	}
	_ = j.Close()
	if third.Sequence != 3 || third.PreviousHash != second.Hash {
		t.Fatalf("reopened sequence/hash incorrect: %+v", third)
	}
	events, err := ReadAll(path)
	if err != nil || len(events) != 3 {
		t.Fatalf("read events=%d err=%v", len(events), err)
	}
}

func TestJournalConcurrentAppendsRemainSequential(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	j, err := Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := j.Append(context.Background(), time.Unix(int64(i+1), 0), "TEST", "same-run", map[string]int{"i": i})
			if err != nil {
				t.Errorf("append: %v", err)
			}
		}(i)
	}
	wg.Wait()
	_ = j.Close()
	events, err := ReadAll(path)
	if err != nil || len(events) != 20 {
		t.Fatalf("events=%d err=%v", len(events), err)
	}
}

func TestJournalDetectsTampering(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	j, _ := Open(path, true)
	_, _ = j.Append(context.Background(), time.Unix(1, 0), "TEST", "run", map[string]int{"value": 1})
	_ = j.Close()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := range data {
		if data[i] == '1' {
			data[i] = '2'
			break
		}
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, true); err == nil {
		t.Fatal("tampered journal was accepted")
	}
}

func TestCreateRefusesExistingAuditFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	j, err := Create(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(path, true); err == nil {
		t.Fatal("exclusive journal creation accepted an existing audit file")
	}
}
