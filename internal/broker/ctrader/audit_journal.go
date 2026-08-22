package ctrader

import (
	"context"
	"errors"
	"sync"

	"tradex/internal/journal"
)

// JournalAuditRecorder adapts operational events to the hash-chained journal.
// Append failures are retained and can be checked by the owning process.
type JournalAuditRecorder struct {
	journal *journal.Journal
	mu      sync.Mutex
	err     error
}

func NewJournalAuditRecorder(stream *journal.Journal) (*JournalAuditRecorder, error) {
	if stream == nil {
		return nil, errors.New("cTrader journal audit recorder requires a journal")
	}
	return &JournalAuditRecorder{journal: stream}, nil
}

func (r *JournalAuditRecorder) Record(event AuditEvent) {
	correlation := event.ClientMsgID
	if correlation == "" {
		correlation = event.AccountID
	}
	if correlation == "" {
		correlation = "ctrader"
	}
	_, err := r.journal.Append(context.Background(), event.At, "CTRADER_"+string(event.Kind), correlation, event)
	if err != nil {
		r.mu.Lock()
		r.err = err
		r.mu.Unlock()
	}
}

func (r *JournalAuditRecorder) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}
