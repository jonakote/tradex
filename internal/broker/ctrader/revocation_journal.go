package ctrader

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"tradex/internal/journal"
)

const artifactRevocationEventKind = "CTRADER_ARTIFACT_REVOKED"

func (r *RevocationList) RevokeToJournal(ctx context.Context, stream *journal.Journal, artifactHash, reason string, at time.Time) error {
	if stream == nil {
		return errors.New("revocation journal is required")
	}
	if err := r.Revoke(artifactHash, reason, at); err != nil {
		return err
	}
	entry, _ := r.Lookup(artifactHash)
	_, err := stream.Append(ctx, at, artifactRevocationEventKind, artifactHash, entry)
	return err
}

func LoadRevocations(events []journal.Event) (*RevocationList, error) {
	list := NewRevocationList()
	for _, event := range events {
		if event.Kind != artifactRevocationEventKind {
			continue
		}
		var entry ArtifactRevocation
		if err := json.Unmarshal(event.Payload, &entry); err != nil {
			return nil, err
		}
		if err := list.Revoke(entry.ArtifactSHA256, entry.Reason, entry.RevokedAt); err != nil {
			return nil, err
		}
	}
	return list, nil
}
