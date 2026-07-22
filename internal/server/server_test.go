package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mrn-dk/mortise/internal/config"
	"github.com/mrn-dk/mortise/internal/telemetry"
)

func testTel(t *testing.T) *telemetry.Telemetry {
	t.Helper()
	tel, err := telemetry.Init(context.Background(), config.Telemetry{ServiceName: "test"})
	if err != nil {
		t.Fatalf("telemetry init: %v", err)
	}
	return tel
}

func baseConfig(pools []config.Pool, routes []config.Route) *config.Config {
	cfg := &config.Config{
		Pools:  pools,
		Routes: routes,
		Keys:   []config.Key{{Key: "sk-test", Name: "test", RPS: 1000, Burst: 1000}},
	}
	cfg2 := *cfg
	// apply defaults via Load path is skipped; set required timeouts manually.
	for i := range cfg2.Pools {
		if cfg2.Pools[i].Timeout == 0 {
			cfg2.Pools[i].Timeout = 5 * time.Second
		}
	}
	cfg2.Limits.IdempotencyTTL = time.Minute
	return &cfg2
}

func chatReq(t *testing.T, url, key, idem, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}
	return req
}

const okBody = `{"id":"c","object":"chat.completion","usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`

func TestRoutingAndAuth(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, okBody)
	}))
	defer upstream.Close()

	cfg := baseConfig(
		[]config.Pool{{Name: "p", Backends: []config.Backend{{BaseURL: upstream.URL + "/v1"}}}},
		[]config.Route{{Model: "m", Pool: "p"}},
	)
	srv := httptest.NewServer(New(cfg, testTel(t)).Handler())
	defer srv.Close()

	// No auth -> 401.
	resp, err := http.DefaultClient.Do(chatReq(t, srv.URL, "", "", `{"model":"m"}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Unknown model -> 404.
	resp, _ = http.DefaultClient.Do(chatReq(t, srv.URL, "sk-test", "", `{"model":"nope"}`))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Happy path -> 200 + body.
	resp, _ = http.DefaultClient.Do(chatReq(t, srv.URL, "sk-test", "", `{"model":"m"}`))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "chat.completion") {
		t.Fatalf("unexpected body: %s", b)
	}
}

func TestFailover(t *testing.T) {
	var bad, good int32
	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&bad, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer badSrv.Close()
	goodSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&good, 1)
		_, _ = io.WriteString(w, okBody)
	}))
	defer goodSrv.Close()

	cfg := baseConfig(
		[]config.Pool{{Name: "p", Retries: 1, Backends: []config.Backend{
			{BaseURL: badSrv.URL + "/v1"},
			{BaseURL: goodSrv.URL + "/v1"},
		}}},
		[]config.Route{{Model: "m", Pool: "p"}},
	)
	srv := httptest.NewServer(New(cfg, testTel(t)).Handler())
	defer srv.Close()

	resp, err := http.DefaultClient.Do(chatReq(t, srv.URL, "sk-test", "", `{"model":"m"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200 after failover, got %d", resp.StatusCode)
	}
	if atomic.LoadInt32(&bad) != 1 || atomic.LoadInt32(&good) != 1 {
		t.Fatalf("expected bad=1 good=1, got bad=%d good=%d", bad, good)
	}
}

func TestIdempotencyNoDoubleCall(t *testing.T) {
	var calls int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		time.Sleep(80 * time.Millisecond) // hold the leader in-flight
		_, _ = io.WriteString(w, okBody)
	}))
	defer upstream.Close()

	cfg := baseConfig(
		[]config.Pool{{Name: "p", Backends: []config.Backend{{BaseURL: upstream.URL + "/v1"}}}},
		[]config.Route{{Model: "m", Pool: "p"}},
	)
	srv := httptest.NewServer(New(cfg, testTel(t)).Handler())
	defer srv.Close()

	var wg sync.WaitGroup
	bodies := make([]string, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			if idx == 1 {
				time.Sleep(20 * time.Millisecond) // ensure leader begins first
			}
			resp, err := http.DefaultClient.Do(chatReq(t, srv.URL, "sk-test", "same-key", `{"model":"m"}`))
			if err != nil {
				t.Errorf("req %d: %v", idx, err)
				return
			}
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			bodies[idx] = string(b)
		}(i)
	}
	wg.Wait()

	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("idempotency: upstream should be called once, got %d", calls)
	}
	if bodies[0] != bodies[1] || !strings.Contains(bodies[0], "chat.completion") {
		t.Fatalf("both clients should get identical replayed body:\n0=%s\n1=%s", bodies[0], bodies[1])
	}
}

func TestStreamingPassthrough(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for _, c := range []string{
			`data: {"choices":[{"delta":{"content":"hi"}}]}`,
			`data: {"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`,
			`data: [DONE]`,
		} {
			_, _ = io.WriteString(w, c+"\n\n")
			if fl != nil {
				fl.Flush()
			}
		}
	}))
	defer upstream.Close()

	cfg := baseConfig(
		[]config.Pool{{Name: "p", Backends: []config.Backend{{BaseURL: upstream.URL + "/v1"}}}},
		[]config.Route{{Model: "m", Pool: "p"}},
	)
	srv := httptest.NewServer(New(cfg, testTel(t)).Handler())
	defer srv.Close()

	resp, err := http.DefaultClient.Do(chatReq(t, srv.URL, "sk-test", "", `{"model":"m","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("want SSE content-type, got %q", ct)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "[DONE]") || !strings.Contains(string(b), `"content":"hi"`) {
		t.Fatalf("stream not passed through: %s", b)
	}
}

func TestStreamingTokenAccounting(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		// Split a data frame across writes to exercise chunk-boundary handling.
		parts := []string{
			`data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n",
			`data: {"choices":[],"usage":{"prompt_to`,
			`kens":7,"completion_tokens":3,"total_tokens":10}}` + "\n\n",
			"data: [DONE]\n\n",
		}
		for _, p := range parts {
			_, _ = io.WriteString(w, p)
			if fl != nil {
				fl.Flush()
			}
		}
	}))
	defer upstream.Close()

	cfg := baseConfig(
		[]config.Pool{{Name: "p", Backends: []config.Backend{{BaseURL: upstream.URL + "/v1"}}}},
		[]config.Route{{Model: "m", Pool: "p"}},
	)
	// Give the key a token budget so RecordTokens has an effect we can observe.
	cfg.Keys = []config.Key{{Key: "sk-test", Name: "test", RPS: 1000, Burst: 1000, TokensPerMin: 5}}
	s := New(cfg, testTel(t))
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, err := http.DefaultClient.Do(chatReq(t, srv.URL, "sk-test", "", `{"model":"m","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "[DONE]") {
		t.Fatalf("stream not passed through: %s", b)
	}
	// 10 tokens recorded > budget of 5 -> next request denied by token gate.
	if s.limit.AllowTokens("sk-test") {
		t.Fatal("token budget should be exhausted after streaming usage was recorded")
	}
}

func TestRetryAfterHonored(t *testing.T) {
	var hits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = io.WriteString(w, okBody)
	}))
	defer upstream.Close()

	cfg := baseConfig(
		[]config.Pool{{Name: "p", Retries: 1, Backends: []config.Backend{{BaseURL: upstream.URL + "/v1"}}}},
		[]config.Route{{Model: "m", Pool: "p"}},
	)
	srv := httptest.NewServer(New(cfg, testTel(t)).Handler())
	defer srv.Close()

	resp, err := http.DefaultClient.Do(chatReq(t, srv.URL, "sk-test", "", `{"model":"m"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200 after retry, got %d", resp.StatusCode)
	}
	if atomic.LoadInt32(&hits) != 2 {
		t.Fatalf("want 2 upstream hits (retry), got %d", hits)
	}
}
