// Package server wires ingress auth, rate limiting, routing, dedup, egress
// proxying, and telemetry into an OpenAI-compatible HTTP handler.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/mrn-dk/mortise/internal/auth"
	"github.com/mrn-dk/mortise/internal/config"
	"github.com/mrn-dk/mortise/internal/dedupe"
	"github.com/mrn-dk/mortise/internal/openai"
	"github.com/mrn-dk/mortise/internal/proxy"
	"github.com/mrn-dk/mortise/internal/ratelimit"
	"github.com/mrn-dk/mortise/internal/router"
	"github.com/mrn-dk/mortise/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// maxRequestBytes bounds an ingress request body.
const maxRequestBytes = 8 << 20 // 8 MiB

// Server holds the wired dependencies.
type Server struct {
	cfg    *config.Config
	auth   *auth.Authenticator
	limit  *ratelimit.Limiter
	router *router.Router
	proxy  *proxy.Proxy
	dedupe *dedupe.Store
	tel    *telemetry.Telemetry
}

// New constructs a Server.
func New(cfg *config.Config, tel *telemetry.Telemetry) *Server {
	return &Server{
		cfg:    cfg,
		auth:   auth.New(cfg),
		limit:  ratelimit.New(cfg),
		router: router.New(cfg),
		proxy:  proxy.New(),
		dedupe: dedupe.NewStore(cfg.Limits.IdempotencyTTL),
		tel:    tel,
	}
}

// Dedupe exposes the dedup store so callers can run its background sweeper.
func (s *Server) Dedupe() *dedupe.Store { return s.dedupe }

// Handler returns the top-level HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", s.handleChat)
	mux.HandleFunc("GET /v1/models", s.handleModels)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.auth.Authenticate(r); !ok {
		writeError(w, http.StatusUnauthorized, "invalid api key", "invalid_request_error", "invalid_api_key")
		return
	}
	type model struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		OwnedBy string `json:"owned_by"`
	}
	out := struct {
		Object string  `json:"object"`
		Data   []model `json:"data"`
	}{Object: "list"}
	for _, m := range s.router.Models() {
		out.Data = append(out.Data, model{ID: m, Object: "model", OwnedBy: "mortise"})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	ctx, span := s.tel.Tracer.Start(r.Context(), "chat.completions")
	defer span.End()

	// 1. Ingress auth.
	key, ok := s.auth.Authenticate(r)
	if !ok {
		s.fail(span, w, http.StatusUnauthorized, "invalid api key", "invalid_request_error", "invalid_api_key", nil)
		return
	}
	span.SetAttributes(attribute.String("mortise.key", key.Name))

	// 2. Per-key RPS limit.
	if !s.limit.AllowRequest(key.Key) {
		s.fail(span, w, http.StatusTooManyRequests, "rate limit exceeded", "rate_limit_error", "rate_limit_exceeded",
			[]attribute.KeyValue{attribute.String("mortise.reject", "rps")})
		return
	}
	// Per-key token budget (checked against the current minute's usage).
	if !s.limit.AllowTokens(key.Key) {
		s.fail(span, w, http.StatusTooManyRequests, "token quota exceeded", "rate_limit_error", "token_quota_exceeded",
			[]attribute.KeyValue{attribute.String("mortise.reject", "tokens")})
		return
	}

	// 3. Read + inspect body.
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes))
	if err != nil {
		s.fail(span, w, http.StatusBadRequest, "failed to read request body", "invalid_request_error", "", nil)
		return
	}
	model, stream, err := openai.PeekRequest(body)
	if err != nil {
		s.fail(span, w, http.StatusBadRequest, "invalid JSON body", "invalid_request_error", "", nil)
		return
	}
	if model == "" {
		s.fail(span, w, http.StatusBadRequest, "missing model", "invalid_request_error", "", nil)
		return
	}
	span.SetAttributes(
		attribute.String("mortise.model", model),
		attribute.Bool("mortise.stream", stream),
	)

	// 4. Route.
	pool, err := s.router.Resolve(model)
	if err != nil {
		s.fail(span, w, http.StatusNotFound, "no route for model "+model, "invalid_request_error", "model_not_found", nil)
		return
	}
	span.SetAttributes(attribute.String("mortise.pool", pool.Name))

	// 5. Idempotency dedup.
	idemKey := r.Header.Get("Idempotency-Key")
	if idemKey != "" {
		s.serveWithDedup(ctx, span, w, key, pool, body, stream, idemKey, r.Header, start)
		return
	}
	s.execute(ctx, span, w, key, pool, body, stream, nil, r.Header, start)
}

// serveWithDedup coordinates leader/duplicate handling for an idempotency key.
func (s *Server) serveWithDedup(ctx context.Context, span trace.Span, w http.ResponseWriter, key *config.Key, pool *config.Pool, body []byte, stream bool, idemKey string, clientHeader http.Header, start time.Time) {
	dkey := key.Key + "\x00" + idemKey
	handle, leader := s.dedupe.Begin(dkey)
	span.SetAttributes(attribute.String("mortise.idempotency_key", idemKey), attribute.Bool("mortise.dedup_leader", leader))

	if !leader {
		// Duplicate: wait for the leader's captured response and replay it.
		res := handle.Wait(ctx)
		if res == nil {
			s.fail(span, w, http.StatusBadGateway, "original request failed or was cancelled", "api_error", "dedup_no_result", nil)
			return
		}
		span.SetAttributes(attribute.Bool("mortise.replayed", true))
		replay(w, res)
		s.tel.Requests.Add(ctx, 1, metricAttrs(key, pool, "replay"))
		s.tel.Duration.Record(ctx, time.Since(start).Seconds(), metricAttrs(key, pool, "replay"))
		return
	}
	// Leader: run and capture. execute reports completion via the handle.
	s.execute(ctx, span, w, key, pool, body, stream, handle, clientHeader, start)
}

// execute performs the upstream call, streams/buffers the response to the
// client, does token accounting, and (if handle != nil) captures the response
// for idempotent replay.
func (s *Server) execute(ctx context.Context, span trace.Span, w http.ResponseWriter, key *config.Key, pool *config.Pool, body []byte, stream bool, handle *dedupe.EntryHandle, clientHeader http.Header, start time.Time) {
	resp, err := s.proxy.Do(ctx, pool, body, clientHeader)
	if err != nil {
		if handle != nil {
			handle.Abort()
		}
		s.fail(span, w, http.StatusBadGateway, "upstream request failed: "+err.Error(), "api_error", "upstream_error",
			[]attribute.KeyValue{attribute.String("mortise.pool", pool.Name)})
		return
	}
	defer resp.Body.Close()
	span.SetAttributes(
		attribute.String("mortise.backend", resp.Backend),
		attribute.Int("mortise.attempts", resp.Attempts),
	)
	if resp.Attempts > 1 {
		s.tel.Retries.Add(ctx, int64(resp.Attempts-1), metricAttrs(key, pool, ""))
	}

	// Relay status + headers, then body (streaming with flush), capturing for dedup.
	copyHeader(w.Header(), resp.Header)
	w.WriteHeader(resp.Status)

	var captured *capture
	if handle != nil {
		captured = &capture{header: cloneHeader(w.Header()), status: resp.Status}
	}
	written, clientErr := relayBody(w, resp.Body)

	// Token accounting from the (fully captured or freshly parsed) body.
	usage := extractUsage(written, stream)
	if usage != nil {
		s.limit.RecordTokens(key.Key, usage.TotalTokens)
		s.tel.PromptTokens.Add(ctx, int64(usage.PromptTokens), metricAttrs(key, pool, ""))
		s.tel.CompTokens.Add(ctx, int64(usage.CompletionTokens), metricAttrs(key, pool, ""))
		span.SetAttributes(
			attribute.Int("mortise.tokens.prompt", usage.PromptTokens),
			attribute.Int("mortise.tokens.completion", usage.CompletionTokens),
		)
	}

	// Finalize dedup: only cache a complete, client-delivered response.
	if handle != nil {
		if clientErr != nil {
			handle.Abort()
		} else {
			captured.body = written
			handle.Complete(&dedupe.Result{Status: captured.status, Header: captured.header, Body: captured.body})
		}
	}

	status := "ok"
	if resp.Status >= 400 {
		status = "upstream_error"
		s.tel.Errors.Add(ctx, 1, metricAttrs(key, pool, ""))
		span.SetStatus(codes.Error, "upstream status")
	}
	s.tel.Requests.Add(ctx, 1, metricAttrs(key, pool, status))
	s.tel.Duration.Record(ctx, time.Since(start).Seconds(), metricAttrs(key, pool, status))
}

// capture accumulates a response for idempotent replay.
type capture struct {
	status int
	header http.Header
	body   []byte
}

func (s *Server) fail(span trace.Span, w http.ResponseWriter, status int, msg, typ, code string, attrs []attribute.KeyValue) {
	if len(attrs) > 0 {
		span.SetAttributes(attrs...)
	}
	span.SetStatus(codes.Error, msg)
	s.tel.Errors.Add(context.Background(), 1)
	writeError(w, status, msg, typ, code)
}

func writeError(w http.ResponseWriter, status int, msg, typ, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(openai.NewError(msg, typ, code))
}

func metricAttrs(key *config.Key, pool *config.Pool, result string) metric.MeasurementOption {
	kv := []attribute.KeyValue{
		attribute.String("key", key.Name),
		attribute.String("pool", pool.Name),
	}
	if result != "" {
		kv = append(kv, attribute.String("result", result))
	}
	return metric.WithAttributes(kv...)
}

var _ = errors.Is // reserved for future typed error handling
