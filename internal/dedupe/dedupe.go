// Package dedupe implements idempotency-key deduplication. A resumed agent
// retrying a request with the same Idempotency-Key must receive the original
// response (replayed) rather than triggering a second upstream call — so it is
// never billed twice.
package dedupe

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// Result is a captured upstream response, replayable to duplicate requests.
type Result struct {
	Status int
	Header http.Header
	Body   []byte
}

// entry tracks an in-flight or completed request for one idempotency key.
type entry struct {
	done    chan struct{} // closed when the leader completes or fails
	result  *Result       // non-nil once completed successfully
	created time.Time
}

// Store holds idempotency entries with a TTL.
type Store struct {
	mu      sync.Mutex
	entries map[string]*entry
	ttl     time.Duration
	now     func() time.Time
}

// NewStore builds a dedup store. A background sweeper evicts expired entries.
func NewStore(ttl time.Duration) *Store {
	s := &Store{
		entries: make(map[string]*entry),
		ttl:     ttl,
		now:     time.Now,
	}
	return s
}

// Begin registers a request under key. If leader is true, the caller owns
// execution and must eventually call Complete or Abort. If false, the caller
// is a duplicate and should Wait for the leader's result.
func (s *Store) Begin(key string) (e *EntryHandle, leader bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if ex, ok := s.entries[key]; ok && now.Sub(ex.created) < s.ttl {
		return &EntryHandle{s: s, key: key, e: ex}, false
	}
	ne := &entry{done: make(chan struct{}), created: now}
	s.entries[key] = ne
	return &EntryHandle{s: s, key: key, e: ne}, true
}

// EntryHandle is a caller's handle to a dedup entry.
type EntryHandle struct {
	s   *Store
	key string
	e   *entry
}

// Complete records the leader's successful result and wakes duplicates.
func (h *EntryHandle) Complete(r *Result) {
	h.s.mu.Lock()
	h.e.result = r
	h.s.mu.Unlock()
	close(h.e.done)
}

// Abort discards the entry (e.g. leader failed), allowing a future retry to
// become a fresh leader. Waiting duplicates are woken with no result.
func (h *EntryHandle) Abort() {
	h.s.mu.Lock()
	delete(h.s.entries, h.key)
	h.s.mu.Unlock()
	close(h.e.done)
}

// Wait blocks until the leader completes, returning the replayable result, or
// nil if the leader aborted or the context is cancelled.
func (h *EntryHandle) Wait(ctx context.Context) *Result {
	select {
	case <-h.e.done:
		h.s.mu.Lock()
		r := h.e.result
		h.s.mu.Unlock()
		return r
	case <-ctx.Done():
		return nil
	}
}

// Sweep removes expired entries. Intended to be called periodically.
func (s *Store) Sweep() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for k, e := range s.entries {
		if now.Sub(e.created) >= s.ttl {
			delete(s.entries, k)
		}
	}
}

// Run starts a background sweeper until ctx is cancelled.
func (s *Store) Run(ctx context.Context) {
	t := time.NewTicker(s.ttl / 4)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Sweep()
		}
	}
}
