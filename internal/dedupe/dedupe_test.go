package dedupe

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestLeaderAndDuplicateReplay(t *testing.T) {
	s := NewStore(time.Minute, 1024)

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
	s := NewStore(time.Minute, 1024)
	h1, _ := s.Begin("key")
	h1.Abort()

	// After abort, a new Begin should become leader again.
	_, leader := s.Begin("key")
	if !leader {
		t.Fatal("after abort, next Begin should be a fresh leader")
	}
}

func TestExpiry(t *testing.T) {
	s := NewStore(time.Minute, 1024)
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

func TestLRUEvictionOfCompleted(t *testing.T) {
	// maxEntries rounds down to maxPerShard = maxEntries/numShards. Use a value
	// giving 1 completed entry per shard, then prove a second completed entry
	// in the same shard evicts the first.
	s := NewStore(time.Hour, numShards) // maxPerShard = 1
	sh := s.shardFor("a")

	// Find two keys that land on the same shard as "a".
	var k1, k2 string
	for i := 0; i < 100000 && (k1 == "" || k2 == ""); i++ {
		key := "k" + string(rune('A'+i%26)) + string(rune('a'+(i/26)%26)) + string(rune('0'+i%10))
		if s.shardFor(key) != sh {
			continue
		}
		if k1 == "" {
			k1 = key
		} else if key != k1 {
			k2 = key
		}
	}
	if k1 == "" || k2 == "" {
		t.Skip("could not find two colliding keys")
	}

	h1, _ := s.Begin(k1)
	h1.Complete(&Result{Status: 200})
	h2, _ := s.Begin(k2)
	h2.Complete(&Result{Status: 200})

	// k1 (LRU, completed) should have been evicted -> a fresh Begin is leader.
	if _, leader := s.Begin(k1); !leader {
		t.Fatal("evicted completed entry should allow a fresh leader")
	}
}

func TestInFlightNotEvicted(t *testing.T) {
	s := NewStore(time.Hour, numShards) // maxPerShard = 1
	sh := s.shardFor("x")
	var k1, k2 string
	for i := 0; i < 100000 && (k1 == "" || k2 == ""); i++ {
		key := "z" + string(rune('A'+i%26)) + string(rune('a'+(i/26)%26)) + string(rune('0'+i%10))
		if s.shardFor(key) != sh {
			continue
		}
		if k1 == "" {
			k1 = key
		} else if key != k1 {
			k2 = key
		}
	}
	if k1 == "" || k2 == "" {
		t.Skip("could not find two colliding keys")
	}

	s.Begin(k1) // leader, in-flight (never completed)
	s.Begin(k2) // second leader; must not evict the in-flight k1

	// k1 is still tracked: a duplicate Begin is not a leader.
	if _, leader := s.Begin(k1); leader {
		t.Fatal("in-flight leader must not be evicted")
	}
}
