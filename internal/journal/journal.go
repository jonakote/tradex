package journal

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

type Event struct {
	Sequence      uint64          `json:"sequence"`
	ID            string          `json:"id"`
	At            time.Time       `json:"at"`
	Kind          string          `json:"kind"`
	CorrelationID string          `json:"correlation_id"`
	Payload       json.RawMessage `json:"payload"`
	PreviousHash  string          `json:"previous_hash,omitempty"`
	Hash          string          `json:"hash"`
}

type Journal struct {
	file         *os.File
	syncOnAppend bool
	sequence     uint64
	previousHash string
	closed       bool
	mu           chan struct{}
}

func Open(path string, syncOnAppend bool) (*Journal, error) {
	return open(path, syncOnAppend, os.O_CREATE|os.O_RDWR|os.O_APPEND)
}

// Create starts a new audit stream and refuses to append to or overwrite an
// existing file. A research run can therefore never silently share a journal.
func Create(path string, syncOnAppend bool) (*Journal, error) {
	return open(path, syncOnAppend, os.O_CREATE|os.O_EXCL|os.O_RDWR|os.O_APPEND)
}

func open(path string, syncOnAppend bool, flags int) (*Journal, error) {
	if path == "" {
		return nil, errors.New("journal path is required")
	}
	file, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open journal: %w", err)
	}
	events, err := readAndVerify(file)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	j := &Journal{file: file, syncOnAppend: syncOnAppend, mu: make(chan struct{}, 1)}
	j.mu <- struct{}{}
	if len(events) > 0 {
		last := events[len(events)-1]
		j.sequence, j.previousHash = last.Sequence, last.Hash
	}
	return j, nil
}

func (j *Journal) Append(ctx context.Context, at time.Time, kind, correlationID string, payload any) (Event, error) {
	if at.IsZero() || kind == "" || correlationID == "" {
		return Event{}, errors.New("journal time, kind, and correlation ID are required")
	}
	select {
	case <-ctx.Done():
		return Event{}, ctx.Err()
	case <-j.mu:
	}
	defer func() { j.mu <- struct{}{} }()
	if j.closed {
		return Event{}, errors.New("journal is closed")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return Event{}, fmt.Errorf("marshal journal payload: %w", err)
	}
	event := Event{
		Sequence: j.sequence + 1, ID: fmt.Sprintf("audit-%020d", j.sequence+1),
		At: at.UTC(), Kind: kind, CorrelationID: correlationID,
		Payload: raw, PreviousHash: j.previousHash,
	}
	event.Hash, err = eventHash(event)
	if err != nil {
		return Event{}, err
	}
	line, err := json.Marshal(event)
	if err != nil {
		return Event{}, fmt.Errorf("marshal journal event: %w", err)
	}
	line = append(line, '\n')
	written, err := j.file.Write(line)
	if err != nil {
		return Event{}, fmt.Errorf("append journal: %w", err)
	}
	if written != len(line) {
		return Event{}, errors.New("append journal: short write")
	}
	j.sequence, j.previousHash = event.Sequence, event.Hash
	if j.syncOnAppend {
		if err := j.file.Sync(); err != nil {
			return Event{}, fmt.Errorf("sync journal: %w", err)
		}
	}
	return event, nil
}

func (j *Journal) Close() error {
	<-j.mu
	defer func() { j.mu <- struct{}{} }()
	if j.closed {
		return nil
	}
	j.closed = true
	return j.file.Close()
}

func ReadAll(path string) ([]Event, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readAndVerify(file)
}

func readAndVerify(file *os.File) ([]Event, error) {
	if _, err := file.Seek(0, 0); err != nil {
		return nil, fmt.Errorf("seek journal: %w", err)
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	events := make([]Event, 0)
	var previousHash string
	var sequence uint64
	for scanner.Scan() {
		var event Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, fmt.Errorf("decode journal sequence %d: %w", sequence+1, err)
		}
		if event.Sequence != sequence+1 || event.ID != fmt.Sprintf("audit-%020d", event.Sequence) {
			return nil, fmt.Errorf("journal sequence %d is discontinuous", event.Sequence)
		}
		if event.At.IsZero() || event.Kind == "" || event.CorrelationID == "" || event.PreviousHash != previousHash {
			return nil, fmt.Errorf("journal sequence %d has invalid metadata or hash chain", event.Sequence)
		}
		hash, err := eventHash(event)
		if err != nil || hash != event.Hash {
			return nil, fmt.Errorf("journal sequence %d failed hash verification", event.Sequence)
		}
		events = append(events, event)
		sequence, previousHash = event.Sequence, event.Hash
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan journal: %w", err)
	}
	return events, nil
}

func eventHash(event Event) (string, error) {
	material := struct {
		Sequence      uint64          `json:"sequence"`
		ID            string          `json:"id"`
		At            time.Time       `json:"at"`
		Kind          string          `json:"kind"`
		CorrelationID string          `json:"correlation_id"`
		Payload       json.RawMessage `json:"payload"`
		PreviousHash  string          `json:"previous_hash,omitempty"`
	}{event.Sequence, event.ID, event.At.UTC(), event.Kind, event.CorrelationID, event.Payload, event.PreviousHash}
	encoded, err := json.Marshal(material)
	if err != nil {
		return "", fmt.Errorf("marshal journal hash material: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
