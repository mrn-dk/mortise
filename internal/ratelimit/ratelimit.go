// Package ratelimit provides per-key request rate limiting (token bucket) and
// rolling-window token accounting for mortise.
package ratelimit

import (
	"sync"
	"time"

	"github.com/mrn-dk/mortise/internal/config"
)

// Limiter enforces per-key RPS and per-minute token budgets.
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	tokens  map[string]*window
	limits  map[string]*config.Key
	now     func() time.Time
}

// bucket is a simple token-bucket rate limiter.
type bucket struct {
	rate     float64 // tokens per second
	capacity float64
	tokens   float64
	last     time.Time
}

// window is a fixed 1-minute counter for token accounting.
type window struct {
	count int
	start time.Time
}

// New builds a Limiter from config keys.
func New(cfg *config.Config) *Limiter {
	limits := make(map[string]*config.Key, len(cfg.Keys))
	for i := range cfg.Keys {
		limits[cfg.Keys[i].Key] = &cfg.Keys[i]
	}
	return &Limiter{
		buckets: make(map[string]*bucket),
		tokens:  make(map[string]*window),
		limits:  limits,
		now:     time.Now,
	}
}

// AllowRequest reports whether a new request for key may proceed under the RPS
// limit. It consumes one unit on success.
func (l *Limiter) AllowRequest(key string) bool {
	lim, ok := l.limits[key]
	if !ok || lim.RPS <= 0 {
		return true // unlimited
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.buckets[key]
	now := l.now()
	if b == nil {
		b = &bucket{rate: lim.RPS, capacity: float64(lim.Burst), tokens: float64(lim.Burst), last: now}
		l.buckets[key] = b
	}
	// Refill.
	elapsed := now.Sub(b.last).Seconds()
	b.last = now
	b.tokens += elapsed * b.rate
	if b.tokens > b.capacity {
		b.tokens = b.capacity
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// AllowTokens reports whether key still has token budget remaining in the
// current minute. It does not consume; call RecordTokens after a response.
func (l *Limiter) AllowTokens(key string) bool {
	lim, ok := l.limits[key]
	if !ok || lim.TokensPerMin <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.currentWindow(key)
	return w.count < lim.TokensPerMin
}

// RecordTokens adds n consumed tokens to key's current-minute window.
func (l *Limiter) RecordTokens(key string, n int) {
	if n <= 0 {
		return
	}
	if _, ok := l.limits[key]; !ok {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.currentWindow(key)
	w.count += n
}

// currentWindow returns key's window, resetting it if the minute rolled over.
// Caller must hold l.mu.
func (l *Limiter) currentWindow(key string) *window {
	now := l.now()
	w := l.tokens[key]
	if w == nil || now.Sub(w.start) >= time.Minute {
		w = &window{start: now}
		l.tokens[key] = w
	}
	return w
}
