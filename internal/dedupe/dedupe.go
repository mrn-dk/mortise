// Package dedupe implements idempotency-key deduplication. A resumed agent
// retrying a request with the same Idempotency-Key must receive the original
// response (replayed) rather than triggering a second upstream call — so it is
// never billed twice.
//
// The store is sharded to avoid a single global lock on the request hot path,
// and each shard is bounded with LRU eviction so an attacker-controlled
// Idempotency-Key cannot grow memory without limit.
package dedupe

import (
	"container/list"
	"context"
	"hash/maphash"
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
	key       string
	done      chan struct{} // closed when the leader completes or fails
	result    *Result       // non-nil once completed successfully
	created   time.Time
	finalized bool // guards against a double Complete/Abort
	elem      *list.Element
}

// shard is an independently-locked slice of the keyspace with its own LRU list.
type shard struct {
	mu      sync.Mutex
	entries map[string]*entry
	lru     *list.List // front = most-recently used; values are *entry
}

// Store holds idempotency entries with a TTL and a bounded, sharded LRU.
type Store struct {
	shards      []*shard
	seed        maphash.Seed
	ttl         time.Duration
	maxPerShard int
	now         func() time.Time
}

const numShards = 16

// NewStore builds a dedup store holding at most maxEntries responses (spread
// across shards) for ttl. A background sweeper evicts expired entries.
func NewStore(ttl time.Duration, maxEntries int) *Store {
	if maxEntries < numShards {
		maxEntries = numShards
	}
	s := &Store{
		shards:      make([]*shard, numShards),
		seed:        maphash.MakeSeed(),
		ttl:         ttl,
		maxPerShard: maxEntries / numShards,
		now:         time.Now,
	}
	for i := range s.shards {
		s.shards[i] = &shard{entries: make(map[string]*entry), lru: list.New()}
	}
	return s
}

func (s *Store) shardFor(key string) *shard {
	var h maphash.Hash
	h.SetSeed(s.seed)
	_, _ = h.WriteString(key)
	return s.shards[h.Sum64()%uint64(len(s.shards))]
}

// Begin registers a request under key. If leader is true, the caller owns
// execution and must eventually call Complete or Abort. If false, the caller
// is a duplicate and should Wait for the leader's result.
func (s *Store) Begin(key string) (e *EntryHandle, leader bool) {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	now := s.now()
	if ex, ok := sh.entries[key]; ok && now.Sub(ex.created) < s.ttl {
		sh.lru.MoveToFront(ex.elem)
		return &EntryHandle{s: s, sh: sh, e: ex}, false
	} else if ok {
		// Stale entry: drop it and start fresh.
		sh.remove(ex)
	}
	ne := &entry{key: key, done: make(chan struct{}), created: now}
	ne.elem = sh.lru.PushFront(ne)
	sh.entries[key] = ne
	sh.evict(s.maxPerShard)
	return &EntryHandle{s: s, sh: sh, e: ne}, true
}

// remove deletes an entry from the shard's map and LRU list. Caller holds mu.
func (sh *shard) remove(e *entry) {
	delete(sh.entries, e.key)
	if e.elem != nil {
		sh.lru.Remove(e.elem)
		e.elem = nil
	}
}

// evict trims the shard to at most max entries, discarding the least-recently
// used *completed* entries first. In-flight leaders are never evicted (their
// waiters depend on them). Caller holds mu.
func (sh *shard) evict(max int) {
	for sh.lru.Len() > max {
		el := sh.lru.Back()
		if el == nil {
			return
		}
		e := el.Value.(*entry)
		if !e.finalized {
			// Oldest entry is still in flight; nothing safe to evict.
			return
		}
		sh.remove(e)
	}
}

// EntryHandle is a caller's handle to a dedup entry.
type EntryHandle struct {
	s  *Store
	sh *shard
	e  *entry
}

// Complete records the leader's successful result and wakes duplicates.
func (h *EntryHandle) Complete(r *Result) {
	h.sh.mu.Lock()
	if h.e.finalized {
		h.sh.mu.Unlock()
		return
	}
	h.e.finalized = true
	h.e.result = r
	h.sh.mu.Unlock()
	close(h.e.done)
}

// Abort discards the entry (e.g. leader failed), allowing a future retry to
// become a fresh leader. Waiting duplicates are woken with no result.
func (h *EntryHandle) Abort() {
	h.sh.mu.Lock()
	if h.e.finalized {
		h.sh.mu.Unlock()
		return
	}
	h.e.finalized = true
	h.sh.remove(h.e)
	h.sh.mu.Unlock()
	close(h.e.done)
}

// Wait blocks until the leader completes, returning the replayable result, or
// nil if the leader aborted or the context is cancelled.
func (h *EntryHandle) Wait(ctx context.Context) *Result {
	select {
	case <-h.e.done:
		h.sh.mu.Lock()
		r := h.e.result
		h.sh.mu.Unlock()
		return r
	case <-ctx.Done():
		return nil
	}
}

// Sweep removes expired entries. Intended to be called periodically.
func (s *Store) Sweep() {
	now := s.now()
	for _, sh := range s.shards {
		sh.mu.Lock()
		for _, e := range sh.entries {
			if e.finalized && now.Sub(e.created) >= s.ttl {
				sh.remove(e)
			}
		}
		sh.mu.Unlock()
	}
}

// Run starts a background sweeper until ctx is cancelled.
func (s *Store) Run(ctx context.Context) {
	interval := s.ttl / 4
	if interval <= 0 {
		interval = time.Minute
	}
	t := time.NewTicker(interval)
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
