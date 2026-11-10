// Package notify delivers signals to the operator.
//
// A signal that reaches no one is indistinguishable from no signal, so
// delivery is treated as part of the product rather than as logging. Three
// rules hold for every sink:
//
//   - A failing sink must never stop the others. A Telegram outage cannot be
//     allowed to lose the journal entry.
//   - A credential never appears in a log line, an error, or a journal
//     record. Sinks carry tokens; messages do not.
//   - The journal is written first and flushed, because it is the only sink
//     that is also the research record. Everything else is a notification.
package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Signal is one delivered setup, already formatted for human reading and
// shaped for later scoring. Prices are decimal strings at the instrument's
// verified precision: the integer ticks stay in the domain layer, and a
// formatted string cannot be accidentally re-scaled by a consumer.
type Signal struct {
	// Validated marks whether the emitting strategy has a measured
	// out-of-sample result. False prints a prominent warning, because an
	// unmeasured signal that looks authoritative is the most expensive
	// thing this system can produce.
	Validated bool `json:"validated"`

	Strategy    string    `json:"strategy"`
	SpecVersion string    `json:"spec_version"`
	Symbol      string    `json:"symbol"`
	Side        string    `json:"side"`
	Timeframe   string    `json:"timeframe"`
	EntryType   string    `json:"entry_type"`
	Entry       string    `json:"entry"`
	Stop        string    `json:"stop"`
	Target      string    `json:"target"`
	RiskPips    string    `json:"risk_pips,omitempty"`
	RewardPips  string    `json:"reward_pips,omitempty"`
	RewardRisk  string    `json:"reward_risk"`
	SpreadPips  string    `json:"spread_pips,omitempty"`
	// SuggestedRiskPct is a half-Kelly-derived position size, a percent of
	// equity to risk on this specific signal. Computed from this
	// instrument's OWN measured backtest stats (see
	// internal/execution/sizing.go) — never a flat number applied across
	// strategies, and 0 when the instrument has no measured stats on file
	// (the caller must not invent a size for something unmeasured).
	SuggestedRiskPct string    `json:"suggested_risk_pct,omitempty"`
	Reason      string    `json:"reason"`
	DetectedAt  time.Time `json:"detected_at"`
	EmittedAt   time.Time `json:"emitted_at"`
	// Diagnostics carries the detector's own numbers verbatim so a journal
	// entry can be audited without re-running detection.
	Diagnostics map[string]any `json:"diagnostics,omitempty"`
}

// Text renders the signal as plain text. Used by every sink, so what lands
// in Telegram is what lands in the journal and on the terminal.
func (s Signal) Text() string {
	var b strings.Builder
	arrow := "SELL"
	if strings.EqualFold(s.Side, "buy") {
		arrow = "BUY"
	}
	fmt.Fprintf(&b, "%s %s  %s\n", arrow, s.Symbol, s.Timeframe)
	if !s.Validated {
		b.WriteString("** UNVALIDATED STRATEGY — NOT A TRADE RECOMMENDATION **\n")
	}
	fmt.Fprintf(&b, "entry  %s (%s)\n", s.Entry, s.EntryType)
	fmt.Fprintf(&b, "stop   %s", s.Stop)
	if s.RiskPips != "" {
		fmt.Fprintf(&b, "  (%s pips)", s.RiskPips)
	}
	fmt.Fprintf(&b, "\ntarget %s", s.Target)
	if s.RewardPips != "" {
		fmt.Fprintf(&b, "  (%s pips)", s.RewardPips)
	}
	fmt.Fprintf(&b, "\nR:R    %s", s.RewardRisk)
	if s.SpreadPips != "" {
		fmt.Fprintf(&b, "   spread %s pips", s.SpreadPips)
	}
	if s.SuggestedRiskPct != "" {
		fmt.Fprintf(&b, "\nrisk   %s%% of equity (half-Kelly on measured stats)", s.SuggestedRiskPct)
	}
	fmt.Fprintf(&b, "\n%s %s\n", s.Strategy, s.SpecVersion)
	fmt.Fprintf(&b, "%s\n", s.Reason)
	fmt.Fprintf(&b, "bar close %s UTC", s.DetectedAt.UTC().Format("2006-01-02 15:04"))
	return b.String()
}

// Subject is a one-line summary for notification titles.
func (s Signal) Subject() string {
	prefix := "tradex"
	if !s.Validated {
		prefix = "tradex (unvalidated)"
	}
	return fmt.Sprintf("%s: %s %s @ %s", prefix, strings.ToUpper(s.Side), s.Symbol, s.Entry)
}

// Sink delivers one signal. Name identifies it in failure messages.
type Sink interface {
	Name() string
	Send(ctx context.Context, signal Signal) error
}

// Multi fans a signal out to every sink, collecting errors rather than
// stopping at the first. The journal is expected to be first in the slice.
type Multi struct {
	Sinks []Sink
	// OnError reports a sink failure without halting delivery. Nil
	// discards; the runner supplies a logger.
	OnError func(sink string, err error)
}

func (m Multi) Name() string { return "multi" }

func (m Multi) Send(ctx context.Context, signal Signal) error {
	var failed []string
	for _, sink := range m.Sinks {
		if sink == nil {
			continue
		}
		if err := sink.Send(ctx, signal); err != nil {
			if m.OnError != nil {
				m.OnError(sink.Name(), err)
			}
			failed = append(failed, sink.Name())
		}
	}
	if len(failed) == len(m.Sinks) && len(failed) > 0 {
		return fmt.Errorf("notify: every sink failed (%s)", strings.Join(failed, ", "))
	}
	return nil
}

// Journal appends one JSON object per line. This is the research record:
// append-only, never rewritten, flushed on every signal so a crash cannot
// lose the entry that preceded it.
type Journal struct {
	Path string

	mu sync.Mutex
}

func NewJournal(path string) (*Journal, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("notify: journal path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return &Journal{Path: path}, nil
}

func (j *Journal) Name() string { return "journal" }

func (j *Journal) Send(_ context.Context, signal Signal) error {
	payload, err := json.Marshal(signal)
	if err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	file, err := os.OpenFile(j.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(append(payload, '\n')); err != nil {
		return err
	}
	return file.Sync()
}

// Writer prints the human-readable form to a stream, for the terminal.
type Writer struct {
	Out interface{ Write([]byte) (int, error) }
}

func (w Writer) Name() string { return "stdout" }

func (w Writer) Send(_ context.Context, signal Signal) error {
	line := "\n" + strings.Repeat("─", 52) + "\n" + signal.Text() + "\n" +
		strings.Repeat("─", 52) + "\n"
	_, err := w.Out.Write([]byte(line))
	return err
}
