// Package server wires ingress auth, rate limiting, routing, dedup, egress
// proxying, and telemetry into an OpenAI-compatible HTTP handler.
package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
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

	maxCacheBody int

	// attrCache memoizes metric attribute sets keyed by (key, pool, result) so
	// the hot path does not allocate an attribute slice per metric per request.
	attrCache sync.Map // attrKey -> metric.MeasurementOption
}

// New constructs a Server.
func New(cfg *config.Config, tel *telemetry.Telemetry) *Server {
	maxCacheBody := cfg.Limits.IdempotencyMaxBodyBytes
	if maxCacheBody <= 0 {
		maxCacheBody = 4 << 20 // 4 MiB fallback when limits weren't defaulted
	}
	return &Server{
		cfg:          cfg,
		auth:         auth.New(cfg),
		limit:        ratelimit.New(cfg),
		router:       router.New(cfg),
		proxy:        proxy.New(),
		dedupe:       dedupe.NewStore(cfg.Limits.IdempotencyTTL, cfg.Limits.IdempotencyMaxEntries),
		tel:          tel,
		maxCacheBody: maxCacheBody,
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

// reqState bundles the per-request context passed through the pipeline, so the
// stages don't need long positional parameter lists.
type reqState struct {
	span   trace.Span
	key    *config.Key
	pool   *config.Pool
	body   []byte
	stream bool
	header http.Header
	start  time.Time
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	ctx, span := s.tel.Tracer.Start(r.Context(), "chat.completions")
	defer span.End()

	// 1. Ingress auth.
	key, ok := s.auth.Authenticate(r)
	if !ok {
		s.fail(ctx, span, w, http.StatusUnauthorized, "invalid api key", "invalid_request_error", "invalid_api_key", nil)
		return
	}
	span.SetAttributes(attribute.String("mortise.key", key.Name))

	// 2. Per-key RPS limit.
	if !s.limit.AllowRequest(key.Key) {
		s.fail(ctx, span, w, http.StatusTooManyRequests, "rate limit exceeded", "rate_limit_error", "rate_limit_exceeded",
			[]attribute.KeyValue{attribute.String("mortise.reject", "rps")})
		return
	}
	// Per-key token budget (checked against the current window's usage).
	if !s.limit.AllowTokens(key.Key) {
		s.fail(ctx, span, w, http.StatusTooManyRequests, "token quota exceeded", "rate_limit_error", "token_quota_exceeded",
			[]attribute.KeyValue{attribute.String("mortise.reject", "tokens")})
		return
	}

	// 3. Read + inspect body.
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes))
	if err != nil {
		s.fail(ctx, span, w, http.StatusBadRequest, "failed to read request body", "invalid_request_error", "", nil)
		return
	}
	model, stream, err := openai.PeekRequest(body)
	if err != nil {
		s.fail(ctx, span, w, http.StatusBadRequest, "invalid JSON body", "invalid_request_error", "", nil)
		return
	}
	if model == "" {
		s.fail(ctx, span, w, http.StatusBadRequest, "missing model", "invalid_request_error", "", nil)
		return
	}
	span.SetAttributes(
		attribute.String("mortise.model", model),
		attribute.Bool("mortise.stream", stream),
	)

	// 4. Route.
	pool, err := s.router.Resolve(model)
	if err != nil {
		s.fail(ctx, span, w, http.StatusNotFound, "no route for model "+model, "invalid_request_error", "model_not_found", nil)
		return
	}
	span.SetAttributes(attribute.String("mortise.pool", pool.Name))

	rs := &reqState{span: span, key: key, pool: pool, body: body, stream: stream, header: r.Header, start: start}

	// 5. Idempotency dedup.
	idemKey := r.Header.Get("Idempotency-Key")
	if idemKey != "" {
		s.serveWithDedup(ctx, w, rs, idemKey)
		return
	}
	s.execute(ctx, w, rs, nil)
}

// serveWithDedup coordinates leader/duplicate handling for an idempotency key.
func (s *Server) serveWithDedup(ctx context.Context, w http.ResponseWriter, rs *reqState, idemKey string) {
	dkey := rs.key.Key + "\x00" + idemKey
	handle, leader := s.dedupe.Begin(dkey)
	rs.span.SetAttributes(attribute.String("mortise.idempotency_key", idemKey), attribute.Bool("mortise.dedup_leader", leader))

	if !leader {
		// Duplicate: wait for the leader's captured response and replay it.
		res := handle.Wait(ctx)
		if res == nil {
			s.fail(ctx, rs.span, w, http.StatusBadGateway, "original request failed or was cancelled", "api_error", "dedup_no_result", nil)
			return
		}
		rs.span.SetAttributes(attribute.Bool("mortise.replayed", true))
		replay(w, res)
		s.tel.Requests.Add(ctx, 1, s.attrs(rs.key, rs.pool, "replay"))
		s.tel.Duration.Record(ctx, time.Since(rs.start).Seconds(), s.attrs(rs.key, rs.pool, "replay"))
		return
	}
	// Leader: run and capture. execute reports completion via the handle.
	s.execute(ctx, w, rs, handle)
}

// execute performs the upstream call, streams/buffers the response to the
// client, does token accounting, and (if handle != nil) captures the response
// for idempotent replay.
func (s *Server) execute(ctx context.Context, w http.ResponseWriter, rs *reqState, handle *dedupe.EntryHandle) {
	resp, err := s.proxy.Do(ctx, rs.pool, rs.body, rs.header)
	if err != nil {
		if handle != nil {
			handle.Abort()
		}
		s.fail(ctx, rs.span, w, http.StatusBadGateway, "upstream request failed: "+err.Error(), "api_error", "upstream_error",
			[]attribute.KeyValue{attribute.String("mortise.pool", rs.pool.Name)})
		return
	}
	defer resp.Body.Close()
	rs.span.SetAttributes(
		attribute.String("mortise.backend", resp.Backend),
		attribute.Int("mortise.attempts", resp.Attempts),
	)
	if resp.Attempts > 1 {
		s.tel.Retries.Add(ctx, int64(resp.Attempts-1), s.attrs(rs.key, rs.pool, ""))
	}

	// Relay status + headers, then body (streaming with flush), capturing for
	// dedup only when this is a leader.
	copyHeader(w.Header(), resp.Header)
	capturedHeader := http.Header(nil)
	if handle != nil {
		capturedHeader = cloneHeader(w.Header())
	}
	w.WriteHeader(resp.Status)

	rr := relay(w, resp.Body, rs.stream, handle != nil, s.maxCacheBody)

	// Token accounting.
	if rr.usage != nil {
		s.limit.RecordTokens(rs.key.Key, rr.usage.TotalTokens)
		s.tel.PromptTokens.Add(ctx, int64(rr.usage.PromptTokens), s.attrs(rs.key, rs.pool, ""))
		s.tel.CompTokens.Add(ctx, int64(rr.usage.CompletionTokens), s.attrs(rs.key, rs.pool, ""))
		rs.span.SetAttributes(
			attribute.Int("mortise.tokens.prompt", rr.usage.PromptTokens),
			attribute.Int("mortise.tokens.completion", rr.usage.CompletionTokens),
		)
	}

	// Finalize dedup: only cache a complete, client-delivered response that fit
	// within the cache size limit (rr.cacheBody is nil otherwise).
	if handle != nil {
		if rr.clientErr != nil || rr.cacheBody == nil {
			handle.Abort()
		} else {
			handle.Complete(&dedupe.Result{Status: resp.Status, Header: capturedHeader, Body: rr.cacheBody})
		}
	}

	status := "ok"
	if resp.Status >= 400 {
		status = "upstream_error"
		s.tel.Errors.Add(ctx, 1, s.attrs(rs.key, rs.pool, ""))
		rs.span.SetStatus(codes.Error, "upstream status")
	}
	s.tel.Requests.Add(ctx, 1, s.attrs(rs.key, rs.pool, status))
	s.tel.Duration.Record(ctx, time.Since(rs.start).Seconds(), s.attrs(rs.key, rs.pool, status))
}

func (s *Server) fail(ctx context.Context, span trace.Span, w http.ResponseWriter, status int, msg, typ, code string, attrs []attribute.KeyValue) {
	if len(attrs) > 0 {
		span.SetAttributes(attrs...)
	}
	span.SetStatus(codes.Error, msg)
	s.tel.Errors.Add(ctx, 1)
	writeError(w, status, msg, typ, code)
}

func writeError(w http.ResponseWriter, status int, msg, typ, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(openai.NewError(msg, typ, code))
}

// attrKey identifies a memoized metric attribute set.
type attrKey struct {
	key, pool, result string
}

// attrs returns a cached MeasurementOption for the given labels, building it
// once per distinct combination.
func (s *Server) attrs(key *config.Key, pool *config.Pool, result string) metric.MeasurementOption {
	ak := attrKey{key: key.Name, pool: pool.Name, result: result}
	if v, ok := s.attrCache.Load(ak); ok {
		return v.(metric.MeasurementOption)
	}
	kv := []attribute.KeyValue{
		attribute.String("key", key.Name),
		attribute.String("pool", pool.Name),
	}
	if result != "" {
		kv = append(kv, attribute.String("result", result))
	}
	opt := metric.WithAttributes(kv...)
	actual, _ := s.attrCache.LoadOrStore(ak, opt)
	return actual.(metric.MeasurementOption)
}
