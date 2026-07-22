package ratelimit

import (
	"testing"
	"time"

	"github.com/mrn-dk/mortise/internal/config"
)

func newLimiter() *Limiter {
	cfg := &config.Config{Keys: []config.Key{
		{Key: "k1", Name: "k1", RPS: 5, Burst: 5, TokensPerMin: 100},
		{Key: "unlimited", Name: "u"},
	}}
	return New(cfg)
}

func TestAllowRequestBurstThenDeny(t *testing.T) {
	l := newLimiter()
	now := time.Now()
	l.now = func() time.Time { return now }

	for i := 0; i < 5; i++ {
		if !l.AllowRequest("k1") {
			t.Fatalf("request %d should be allowed within burst", i)
		}
	}
	if l.AllowRequest("k1") {
		t.Fatal("6th request should be denied (burst exhausted)")
	}
	// Advance one second: rate=5/s refills ~5 tokens.
	now = now.Add(time.Second)
	if !l.AllowRequest("k1") {
		t.Fatal("request after refill should be allowed")
	}
}

func TestUnlimitedKey(t *testing.T) {
	l := newLimiter()
	for i := 0; i < 1000; i++ {
		if !l.AllowRequest("unlimited") {
			t.Fatal("unlimited key must never be denied")
		}
	}
}

func TestTokenBudget(t *testing.T) {
	l := newLimiter()
	now := time.Now()
	l.now = func() time.Time { return now }

	if !l.AllowTokens("k1") {
		t.Fatal("fresh key should have token budget")
	}
	l.RecordTokens("k1", 100)
	if l.AllowTokens("k1") {
		t.Fatal("budget should be exhausted at limit")
	}
	// Half a window later the previous window is weighted ~0.5, so ~50 tokens
	// remain counted -> budget available again under the sliding window.
	now = now.Add(90 * time.Second)
	if !l.AllowTokens("k1") {
		t.Fatal("budget should partially recover mid-window")
	}
	// Two full windows later the usage has fully aged out.
	now = now.Add(2 * time.Minute)
	if !l.AllowTokens("k1") {
		t.Fatal("budget should reset after the window fully rolls over")
	}
}
