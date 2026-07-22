// Package proxy performs the egress call to OpenAI-compatible backends,
// implementing retries and failover across a pool's backends with per-attempt
// timeouts and backoff. The returned response body is left open for the caller
// to stream or buffer.
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mrn-dk/mortise/internal/config"
)

// Proxy issues upstream requests.
type Proxy struct {
	client *http.Client
}

// New builds a Proxy with a transport tuned for a fan-out reverse proxy.
//
// The stdlib http.DefaultTransport caps idle connections per host at 2, which
// serializes a gateway talking to a small set of backends under load; we raise
// those limits substantially. There is deliberately no client Timeout —
// streaming responses must stay open, and per-attempt bounds come from context.
func New() *Proxy {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &Proxy{client: &http.Client{Transport: transport}}
}

// Response is a live upstream response. The caller must close Body.
type Response struct {
	Status   int
	Header   http.Header
	Body     io.ReadCloser
	Backend  string // name (or base_url) of the backend that served it
	Attempts int    // total attempts made, including the successful one
}

// retryableStatus reports whether an HTTP status warrants failover.
func retryableStatus(code int) bool {
	switch code {
	case http.StatusRequestTimeout, // 408
		http.StatusTooManyRequests,     // 429
		http.StatusInternalServerError, // 500
		http.StatusBadGateway,          // 502
		http.StatusServiceUnavailable,  // 503
		http.StatusGatewayTimeout:      // 504
		return true
	}
	return false
}

// Do sends the request to the pool, trying each backend in order and retrying
// on transport errors or retryable statuses, up to pool.Retries additional
// attempts. Between attempts it waits with exponential backoff and jitter,
// honoring a Retry-After header when present. It returns the first
// non-retryable response, or the last error.
func (p *Proxy) Do(ctx context.Context, pool *config.Pool, body []byte, clientHeader http.Header) (*Response, error) {
	maxAttempts := pool.Retries + 1
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	// Per-backend request payloads are computed once (model rewriting is
	// backend-specific but constant across retries against that backend).
	payloads := make([][]byte, len(pool.Backends))
	for i := range pool.Backends {
		payloads[i] = body
		if m := pool.Backends[i].Model; m != "" {
			if rewritten, err := rewriteModel(body, m); err == nil {
				payloads[i] = rewritten
			}
		}
	}

	accept := clientAccept(clientHeader)
	var lastErr error

	for attempt := 0; attempt < maxAttempts; attempt++ {
		idx := attempt % len(pool.Backends)
		backend := &pool.Backends[idx]

		resp, err := p.attempt(ctx, pool, backend, payloads[idx], accept)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				break // client gone or deadline hit; stop trying
			}
			if !p.wait(ctx, attempt, 0) {
				break
			}
			continue
		}
		if retryableStatus(resp.StatusCode) && attempt < maxAttempts-1 {
			retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
			// Drain and discard so the connection can be reused, then failover.
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("upstream %s returned %d", backendLabel(backend), resp.StatusCode)
			if !p.wait(ctx, attempt, retryAfter) {
				break
			}
			continue
		}
		return &Response{
			Status:   resp.StatusCode,
			Header:   resp.Header,
			Body:     resp.Body,
			Backend:  backendLabel(backend),
			Attempts: attempt + 1,
		}, nil
	}

	return nil, fmt.Errorf("all %d attempt(s) failed: %w", maxAttempts, lastErr)
}

// wait sleeps before the next attempt, returning false if ctx is cancelled.
// The delay is max(exponential-backoff-with-jitter, retryAfter).
func (p *Proxy) wait(ctx context.Context, attempt int, retryAfter time.Duration) bool {
	delay := backoffDelay(attempt)
	if retryAfter > delay {
		delay = retryAfter
	}
	if delay <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// backoffDelay returns an exponential backoff with full jitter for the given
// zero-based attempt number, capped at a few seconds.
func backoffDelay(attempt int) time.Duration {
	const base = 50 * time.Millisecond
	const max = 5 * time.Second
	backoff := base << attempt
	if backoff > max || backoff <= 0 {
		backoff = max
	}
	// Full jitter: uniform in [0, backoff].
	return time.Duration(rand.Int63n(int64(backoff) + 1))
}

// parseRetryAfter interprets a Retry-After header (delta-seconds or HTTP date).
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// attempt performs a single upstream call with its own timeout.
func (p *Proxy) attempt(ctx context.Context, pool *config.Pool, b *config.Backend, payload []byte, accept string) (*http.Response, error) {
	actx := ctx
	var cancel context.CancelFunc
	if pool.Timeout > 0 {
		actx, cancel = context.WithTimeout(ctx, pool.Timeout)
	}

	url := strings.TrimRight(b.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(actx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		if cancel != nil {
			cancel()
		}
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", accept)
	if b.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+b.APIKey)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		if cancel != nil {
			cancel()
		}
		return nil, err
	}
	// Ensure the per-attempt context is cancelled once the body is closed.
	if cancel != nil {
		resp.Body = &cancelBody{ReadCloser: resp.Body, cancel: cancel}
	}
	return resp, nil
}

// rewriteModel replaces the "model" field in a JSON request body.
func rewriteModel(body []byte, model string) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	mv, _ := json.Marshal(model)
	m["model"] = mv
	return json.Marshal(m)
}

func backendLabel(b *config.Backend) string {
	if b.Name != "" {
		return b.Name
	}
	return b.BaseURL
}

func clientAccept(h http.Header) string {
	if a := h.Get("Accept"); a != "" {
		return a
	}
	return "application/json"
}

// cancelBody cancels the attempt context when the response body is closed.
type cancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelBody) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}
