package notify

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sampleSignal() Signal {
	return Signal{
		Validated: false, Strategy: "Break-and-retest continuation",
		SpecVersion: "continuation-0.1.0", Symbol: "EURUSD", Side: "sell",
		Timeframe: "1h", EntryType: "LIMIT",
		Entry: "1.11900", Stop: "1.12012", Target: "1.11500",
		RiskPips: "11.2", RewardPips: "40.0", RewardRisk: "3.57", SpreadPips: "0.7",
		Reason:     "broke the 24-bar low",
		DetectedAt: time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC),
		EmittedAt:  time.Date(2026, 11, 6, 9, 0, 5, 0, time.UTC),
	}
}

// An unvalidated signal must say so in the message body, not only in a doc.
func TestSignalTextCarriesUnvalidatedWarning(t *testing.T) {
	text := sampleSignal().Text()
	if !strings.Contains(text, "UNVALIDATED") {
		t.Errorf("unvalidated signal text lacks the warning:\n%s", text)
	}
	for _, want := range []string{"SELL EURUSD", "1.11900", "1.12012", "1.11500", "3.57"} {
		if !strings.Contains(text, want) {
			t.Errorf("text is missing %q:\n%s", want, text)
		}
	}

	validated := sampleSignal()
	validated.Validated = true
	if strings.Contains(validated.Text(), "UNVALIDATED") {
		t.Error("a validated signal must not carry the warning")
	}
	if !strings.Contains(sampleSignal().Subject(), "unvalidated") {
		t.Error("the subject should mark an unvalidated signal")
	}
}

func TestJournalAppendsOneJSONObjectPerLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "signals.jsonl")
	journal, err := NewJournal(path)
	if err != nil {
		t.Fatalf("NewJournal: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := journal.Send(context.Background(), sampleSignal()); err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	defer file.Close()
	lines := 0
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var decoded Signal
		if err := json.Unmarshal(scanner.Bytes(), &decoded); err != nil {
			t.Fatalf("line %d is not valid JSON: %v", lines+1, err)
		}
		if decoded.Symbol != "EURUSD" || decoded.Validated {
			t.Errorf("line %d round-tripped wrong: %+v", lines+1, decoded)
		}
		lines++
	}
	if lines != 3 {
		t.Errorf("journal has %d lines, want 3 (append-only)", lines)
	}
	if _, err := NewJournal("  "); err == nil {
		t.Error("an empty journal path must be rejected")
	}
}

type failingSink struct{ name string }

func (f failingSink) Name() string                       { return f.name }
func (f failingSink) Send(context.Context, Signal) error { return errors.New("boom") }

type countingSink struct {
	name  string
	count int
}

func (c *countingSink) Name() string { return c.name }
func (c *countingSink) Send(context.Context, Signal) error {
	c.count++
	return nil
}

// One broken sink must not cost the others their delivery: a Telegram outage
// cannot be allowed to lose the journal entry.
func TestMultiDeliversDespiteOneFailure(t *testing.T) {
	good := &countingSink{name: "good"}
	var reported []string
	multi := Multi{
		Sinks:   []Sink{failingSink{name: "bad"}, good, nil},
		OnError: func(sink string, err error) { reported = append(reported, sink) },
	}
	if err := multi.Send(context.Background(), sampleSignal()); err != nil {
		t.Errorf("partial failure must not be fatal, got %v", err)
	}
	if good.count != 1 {
		t.Errorf("healthy sink received %d signals, want 1", good.count)
	}
	if len(reported) != 1 || reported[0] != "bad" {
		t.Errorf("failures reported = %v, want [bad]", reported)
	}

	all := Multi{Sinks: []Sink{failingSink{name: "a"}, failingSink{name: "b"}}}
	if err := all.Send(context.Background(), sampleSignal()); err == nil {
		t.Error("a total delivery failure must be reported")
	}
}

func TestTelegramSendsPlainTextAndRedactsToken(t *testing.T) {
	const token = "123456:SUPERSECRET"
	var body map[string]any
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	telegram := Telegram{Token: token, ChatID: "42", BaseURL: server.URL}
	if !telegram.Configured() {
		t.Fatal("telegram should be configured")
	}
	if err := telegram.Send(context.Background(), sampleSignal()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if path != "/bot"+token+"/sendMessage" {
		t.Errorf("unexpected path %q", path)
	}
	if body["chat_id"] != "42" {
		t.Errorf("chat_id = %v", body["chat_id"])
	}
	// No parse_mode: a price string must not be subject to Markdown rules.
	if _, set := body["parse_mode"]; set {
		t.Error("parse_mode must not be set")
	}
	text, _ := body["text"].(string)
	if !strings.Contains(text, "UNVALIDATED") || !strings.Contains(text, "1.11900") {
		t.Errorf("unexpected message text: %q", text)
	}

	missing := Telegram{Token: "", ChatID: "42"}
	if missing.Configured() {
		t.Error("a telegram sink without a token is not configured")
	}
	if err := missing.Send(context.Background(), sampleSignal()); err == nil {
		t.Error("sending without credentials must fail")
	}
}

// A venue error body can echo the request URL. The token must never survive
// into an error that gets logged.
func TestTelegramErrorNeverContainsToken(t *testing.T) {
	const token = "123456:SUPERSECRET"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"ok":false,"description":"Unauthorized for bot` + token + `"}`))
	}))
	defer server.Close()

	telegram := Telegram{Token: token, ChatID: "42", BaseURL: server.URL}
	err := telegram.Send(context.Background(), sampleSignal())
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("error leaked the bot token: %q", err)
	}
	if !strings.Contains(err.Error(), "<redacted>") {
		t.Errorf("error should mark the redaction: %q", err)
	}
}

func TestWriterRendersSignal(t *testing.T) {
	var buffer bytes.Buffer
	if err := (Writer{Out: &buffer}).Send(context.Background(), sampleSignal()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !strings.Contains(buffer.String(), "SELL EURUSD") {
		t.Errorf("unexpected output:\n%s", buffer.String())
	}
}

// The same closed bar is re-read on every poll; it must notify once.
func TestGateDeduplicatesOneBar(t *testing.T) {
	gate := &Gate{}
	detected := time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)
	now := detected.Add(time.Minute)

	if reason := gate.Allow("S", "EURUSD", detected, now); reason.Blocked {
		t.Fatalf("first delivery blocked: %s", reason.Why)
	}
	reason := gate.Allow("S", "EURUSD", detected, now.Add(5*time.Minute))
	if !reason.Blocked || !strings.Contains(reason.Why, "already delivered") {
		t.Errorf("a repeat of the same bar must be suppressed, got %+v", reason)
	}
}

func TestGateBlocksInsideBlackout(t *testing.T) {
	release := time.Date(2026, 11, 6, 13, 30, 0, 0, time.UTC)
	gate := &Gate{Blackouts: []Blackout{{
		Name: "US non-farm payrolls", At: release,
		Before: 30 * time.Minute, After: time.Hour,
	}}}

	for _, at := range []time.Time{
		release.Add(-30 * time.Minute), release, release.Add(time.Hour),
	} {
		reason := gate.Allow("S", "EURUSD", release.Add(-2*time.Hour), at)
		if !reason.Blocked || !strings.Contains(reason.Why, "non-farm") {
			t.Errorf("at %s delivery should be blacked out, got %+v", at.Format("15:04"), reason)
		}
	}
	// Just outside the window on both sides.
	if reason := gate.Allow("S", "EURUSD", release, release.Add(-31*time.Minute)); reason.Blocked {
		t.Errorf("31 minutes before should be allowed: %s", reason.Why)
	}
	if reason := gate.Allow("S", "GBPUSD", release, release.Add(61*time.Minute)); reason.Blocked {
		t.Errorf("61 minutes after should be allowed: %s", reason.Why)
	}
}

// EURUSD and AUDUSD short in the same hour is one dollar bet sized twice.
func TestGateCapsCorrelatedGroup(t *testing.T) {
	gate := &Gate{
		Groups:        DefaultCorrelationGroups(),
		GroupCooldown: 4 * time.Hour,
	}
	base := time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)

	if reason := gate.Allow("S", "EURUSD", base, base); reason.Blocked {
		t.Fatalf("first signal blocked: %s", reason.Why)
	}
	reason := gate.Allow("S", "AUDUSD", base, base.Add(time.Minute))
	if !reason.Blocked || !strings.Contains(reason.Why, "correlated group") {
		t.Errorf("a correlated instrument must be capped, got %+v", reason)
	}
	// A different group is unaffected.
	if reason := gate.Allow("S", "USDJPY", base, base.Add(time.Minute)); reason.Blocked {
		t.Errorf("an unrelated group must not be capped: %s", reason.Why)
	}
	// And the cap expires.
	if reason := gate.Allow("S", "AUDUSD", base.Add(5*time.Hour), base.Add(5*time.Hour)); reason.Blocked {
		t.Errorf("the group cap must expire: %s", reason.Why)
	}
}

func TestGateSymbolCooldown(t *testing.T) {
	gate := &Gate{SymbolCooldown: 2 * time.Hour}
	base := time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)
	if reason := gate.Allow("S", "EURUSD", base, base); reason.Blocked {
		t.Fatalf("first signal blocked: %s", reason.Why)
	}
	if reason := gate.Allow("S", "EURUSD", base.Add(time.Hour), base.Add(time.Hour)); !reason.Blocked {
		t.Error("a repeat inside the symbol cooldown must be suppressed")
	}
	if reason := gate.Allow("S", "EURUSD", base.Add(3*time.Hour), base.Add(3*time.Hour)); reason.Blocked {
		t.Errorf("the symbol cooldown must expire: %s", reason.Why)
	}
}

func TestBlackoutValidateAndNext(t *testing.T) {
	if err := (Blackout{At: time.Now()}).Validate(); err == nil {
		t.Error("a nameless blackout must be rejected")
	}
	if err := (Blackout{Name: "x"}).Validate(); err == nil {
		t.Error("a blackout with no instant must be rejected")
	}
	if err := (Blackout{Name: "x", At: time.Now(), Before: -1}).Validate(); err == nil {
		t.Error("a negative window must be rejected")
	}

	base := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	gate := &Gate{Blackouts: []Blackout{
		{Name: "later", At: base.Add(48 * time.Hour)},
		{Name: "soon", At: base.Add(2 * time.Hour)},
		{Name: "past", At: base.Add(-48 * time.Hour)},
	}}
	next, ok := gate.NextBlackout(base)
	if !ok || next.Name != "soon" {
		t.Errorf("NextBlackout = %+v, %v; want soon", next, ok)
	}
	if _, ok := (&Gate{}).NextBlackout(base); ok {
		t.Error("no blackouts means none pending")
	}
}
