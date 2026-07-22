// Package ratelimit provides per-key request rate limiting (token bucket, via
// golang.org/x/time/rate) and sliding-window token accounting for mortise.
//
// All per-key state is created once at construction from the configured keys,
// so the request hot path only performs read-only map lookups plus a per-key
// lock — there is no single global mutex to contend on.
package ratelimit

import (
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/mrn-dk/mortise/internal/config"
)

// Limiter enforces per-key RPS and per-minute token budgets.
type Limiter struct {
	keys map[string]*keyState
	now  func() time.Time
}

// keyState holds the immutable limits plus mutable counters for one key. Each
// key has its own mutex so distinct keys never contend.
type keyState struct {
	rps          float64
	tokensPerMin int

	limiter *rate.Limiter // nil when RPS unlimited

	mu     sync.Mutex
	window slidingWindow
}

// New builds a Limiter from config keys.
func New(cfg *config.Config) *Limiter {
	keys := make(map[string]*keyState, len(cfg.Keys))
	for i := range cfg.Keys {
		k := &cfg.Keys[i]
		ks := &keyState{rps: k.RPS, tokensPerMin: k.TokensPerMin}
		if k.RPS > 0 {
			burst := k.Burst
			if burst < 1 {
				burst = 1
			}
			ks.limiter = rate.NewLimiter(rate.Limit(k.RPS), burst)
		}
		keys[k.Identity()] = ks
	}
	return &Limiter{keys: keys, now: time.Now}
}

// AllowRequest reports whether a new request for key may proceed under the RPS
// limit. It consumes one unit on success.
func (l *Limiter) AllowRequest(key string) bool {
	ks, ok := l.keys[key]
	if !ok || ks.limiter == nil {
		return true // unknown or unlimited
	}
	return ks.limiter.AllowN(l.now(), 1)
}

// AllowTokens reports whether key still has token budget remaining in the
// current sliding minute. It does not consume; call RecordTokens after a
// response.
func (l *Limiter) AllowTokens(key string) bool {
	ks, ok := l.keys[key]
	if !ok || ks.tokensPerMin <= 0 {
		return true
	}
	ks.mu.Lock()
	defer ks.mu.Unlock()
	return ks.window.count(l.now()) < float64(ks.tokensPerMin)
}

// RecordTokens adds n consumed tokens to key's current sliding window.
func (l *Limiter) RecordTokens(key string, n int) {
	if n <= 0 {
		return
	}
	ks, ok := l.keys[key]
	if !ok {
		return
	}
	ks.mu.Lock()
	ks.window.add(l.now(), n)
	ks.mu.Unlock()
}

// slidingWindow approximates a rolling 1-minute counter using two adjacent
// fixed windows, weighting the previous window by how much of it still overlaps
// the trailing minute. This avoids the burst-at-boundary problem of a plain
// fixed window without tracking every event.
type slidingWindow struct {
	cur      int
	prev     int
	curStart time.Time
}

const windowSize = time.Minute

func (s *slidingWindow) roll(now time.Time) {
	if s.curStart.IsZero() {
		s.curStart = now
		return
	}
	switch elapsed := now.Sub(s.curStart); {
	case elapsed >= 2*windowSize:
		s.prev, s.cur, s.curStart = 0, 0, now
	case elapsed >= windowSize:
		s.prev, s.cur = s.cur, 0
		s.curStart = s.curStart.Add(windowSize)
	}
}

func (s *slidingWindow) count(now time.Time) float64 {
	s.roll(now)
	elapsed := now.Sub(s.curStart)
	if elapsed < 0 {
		elapsed = 0
	}
	weight := float64(windowSize-elapsed) / float64(windowSize)
	if weight < 0 {
		weight = 0
	}
	return float64(s.prev)*weight + float64(s.cur)
}

func (s *slidingWindow) add(now time.Time, n int) {
	s.roll(now)
	s.cur += n
}
