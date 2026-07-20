// Package proxy performs the egress call to OpenAI-compatible backends,
// implementing retries and failover across a pool's backends with per-attempt
// timeouts. The returned response body is left open for the caller to stream
// or buffer.
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mrn-dk/mortise/internal/config"
)

// Proxy issues upstream requests.
type Proxy struct {
	client *http.Client
}

// New builds a Proxy. The transport disables the client-side response timeout
// so streaming works; per-attempt deadlines come from context instead.
func New() *Proxy {
	return &Proxy{
		client: &http.Client{
			// No Timeout: streaming responses must stay open. Per-attempt
			// bounds are enforced via context deadlines.
			Transport: http.DefaultTransport,
		},
	}
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
// attempts. It returns the first non-retryable response, or the last error.
func (p *Proxy) Do(ctx context.Context, pool *config.Pool, body []byte, clientHeader http.Header) (*Response, error) {
	maxAttempts := pool.Retries + 1
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	var lastErr error
	var lastResp *Response
	attempts := 0

	for attempts < maxAttempts {
		backend := &pool.Backends[attempts%len(pool.Backends)]
		attempts++

		resp, err := p.attempt(ctx, pool, backend, body, clientHeader)
		if err != nil {
			lastErr = err
			lastResp = nil
			if ctx.Err() != nil {
				break // client gone or deadline hit; stop trying
			}
			continue
		}
		if retryableStatus(resp.StatusCode) && attempts < maxAttempts {
			// Drain and discard so the connection can be reused, then failover.
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("upstream %s returned %d", backendLabel(backend), resp.StatusCode)
			continue
		}
		return &Response{
			Status:   resp.StatusCode,
			Header:   resp.Header,
			Body:     resp.Body,
			Backend:  backendLabel(backend),
			Attempts: attempts,
		}, nil
	}

	if lastResp != nil {
		return lastResp, nil
	}
	return nil, fmt.Errorf("all %d attempt(s) failed: %w", attempts, lastErr)
}

// attempt performs a single upstream call with its own timeout.
func (p *Proxy) attempt(ctx context.Context, pool *config.Pool, b *config.Backend, body []byte, clientHeader http.Header) (*http.Response, error) {
	actx := ctx
	var cancel context.CancelFunc
	if pool.Timeout > 0 {
		actx, cancel = context.WithTimeout(ctx, pool.Timeout)
	}

	payload := body
	if b.Model != "" {
		if rewritten, err := rewriteModel(body, b.Model); err == nil {
			payload = rewritten
		}
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
	req.Header.Set("Accept", clientAccept(clientHeader))
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

// ForwardTimeout is a small helper for callers building request contexts.
func ForwardTimeout(d time.Duration) time.Duration {
	if d <= 0 {
		return 60 * time.Second
	}
	return d
}
