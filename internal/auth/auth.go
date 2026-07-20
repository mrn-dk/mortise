// Package auth implements API-key ingress authentication for mortise clients.
package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/mrn-dk/mortise/internal/config"
)

type ctxKey struct{}

// Authenticator validates client bearer tokens against configured keys.
type Authenticator struct {
	keys map[string]*config.Key
}

// New builds an Authenticator from config keys.
func New(cfg *config.Config) *Authenticator {
	m := make(map[string]*config.Key, len(cfg.Keys))
	for i := range cfg.Keys {
		m[cfg.Keys[i].Key] = &cfg.Keys[i]
	}
	return &Authenticator{keys: m}
}

// Authenticate extracts and validates the bearer token, returning the key.
func (a *Authenticator) Authenticate(r *http.Request) (*config.Key, bool) {
	tok := bearer(r)
	if tok == "" {
		return nil, false
	}
	k, ok := a.keys[tok]
	return k, ok
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	const p = "Bearer "
	if len(h) > len(p) && strings.EqualFold(h[:len(p)], p) {
		return strings.TrimSpace(h[len(p):])
	}
	return strings.TrimSpace(h)
}

// FromContext returns the authenticated key stored on the request context.
func FromContext(ctx context.Context) (*config.Key, bool) {
	k, ok := ctx.Value(ctxKey{}).(*config.Key)
	return k, ok
}

// WithKey stores the authenticated key on the context.
func WithKey(ctx context.Context, k *config.Key) context.Context {
	return context.WithValue(ctx, ctxKey{}, k)
}
