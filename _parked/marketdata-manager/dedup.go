package manager

import (
	"sync"
)

// inflightEntry tracks a single in-flight request.
// Multiple callers for the same cache key await the same result.
type inflightEntry struct {
	done   chan struct{}
	result []byte
	err    error
}

// Deduplicator ensures that identical concurrent requests produce only one
// actual provider call. All callers for the same key await a single result.
type Deduplicator struct {
	mu       sync.Mutex
	inflight map[string]*inflightEntry
}

// NewDeduplicator creates a request deduplicator.
func NewDeduplicator() *Deduplicator {
	return &Deduplicator{
		inflight: make(map[string]*inflightEntry),
	}
}

// DoResult represents the outcome of a deduplicated call.
type DoResult struct {
	Data   []byte
	Err    error
	Shared bool // true if this result came from a shared in-flight request
}

// Do executes fn only if no in-flight request exists for the given key.
// If another goroutine is already fetching this key, the caller blocks
// until the result is available and receives the same data.
//
// Returns the result and whether it was shared (came from another goroutine's call).
func (d *Deduplicator) Do(key string, fn func() ([]byte, error)) DoResult {
	d.mu.Lock()
	if entry, exists := d.inflight[key]; exists {
		// Another goroutine is already fetching this — wait for it
		d.mu.Unlock()
		<-entry.done
		return DoResult{Data: entry.result, Err: entry.err, Shared: true}
	}

	// We are the first — register our in-flight entry
	entry := &inflightEntry{done: make(chan struct{})}
	d.inflight[key] = entry
	d.mu.Unlock()

	// Execute the actual call
	result, err := fn()
	entry.result = result
	entry.err = err
	close(entry.done) // Wake all waiters

	// Remove from in-flight map
	d.mu.Lock()
	delete(d.inflight, key)
	d.mu.Unlock()

	return DoResult{Data: result, Err: err, Shared: false}
}

// InFlight returns the number of currently in-flight requests.
func (d *Deduplicator) InFlight() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.inflight)
}
