package proxy

import (
	"net/http"
	"testing"
	"time"
)

func TestParseRetryAfterSeconds(t *testing.T) {
	if got := parseRetryAfter("2"); got != 2*time.Second {
		t.Fatalf("want 2s, got %v", got)
	}
	if got := parseRetryAfter(""); got != 0 {
		t.Fatalf("empty should be 0, got %v", got)
	}
	if got := parseRetryAfter("-5"); got != 0 {
		t.Fatalf("negative should be 0, got %v", got)
	}
}

func TestParseRetryAfterDate(t *testing.T) {
	future := time.Now().Add(3 * time.Second).UTC().Format(http.TimeFormat)
	got := parseRetryAfter(future)
	if got <= 0 || got > 4*time.Second {
		t.Fatalf("want ~3s, got %v", got)
	}
	past := time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)
	if got := parseRetryAfter(past); got != 0 {
		t.Fatalf("past date should be 0, got %v", got)
	}
}

func TestBackoffDelayJitterAndCap(t *testing.T) {
	// Full-jitter backoff is bounded by the (capped) exponential ceiling.
	for attempt := 0; attempt < 20; attempt++ {
		d := backoffDelay(attempt)
		if d < 0 || d > 5*time.Second {
			t.Fatalf("attempt %d: delay %v out of range", attempt, d)
		}
	}
}
