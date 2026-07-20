package dedupe

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestLeaderAndDuplicateReplay(t *testing.T) {
	s := NewStore(time.Minute)

	h1, leader1 := s.Begin("key")
	if !leader1 {
		t.Fatal("first Begin must be leader")
	}
	h2, leader2 := s.Begin("key")
	if leader2 {
		t.Fatal("second Begin must be a duplicate")
	}

	want := &Result{Status: 200, Header: http.Header{"X": {"y"}}, Body: []byte("hello")}

	var got *Result
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		got = h2.Wait(context.Background())
	}()

	h1.Complete(want)
	wg.Wait()

	if got == nil || string(got.Body) != "hello" || got.Status != 200 {
		t.Fatalf("duplicate did not replay leader result: %+v", got)
	}
}

func TestAbortAllowsRetry(t *testing.T) {
	s := NewStore(time.Minute)
	h1, _ := s.Begin("key")
	h1.Abort()

	// After abort, a new Begin should become leader again.
	_, leader := s.Begin("key")
	if !leader {
		t.Fatal("after abort, next Begin should be a fresh leader")
	}
}

func TestExpiry(t *testing.T) {
	s := NewStore(time.Minute)
	base := time.Now()
	s.now = func() time.Time { return base }
	h, _ := s.Begin("key")
	h.Complete(&Result{Status: 200})

	// Duplicate within TTL.
	if _, leader := s.Begin("key"); leader {
		t.Fatal("within TTL should be duplicate")
	}
	// Advance past TTL: a new leader is allowed.
	s.now = func() time.Time { return base.Add(2 * time.Minute) }
	if _, leader := s.Begin("key"); !leader {
		t.Fatal("after TTL should be a fresh leader")
	}
}
