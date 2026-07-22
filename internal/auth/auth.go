// Package auth implements API-key ingress authentication for mortise clients.
package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/mrn-dk/mortise/internal/config"
)

// Authenticator validates client bearer tokens against configured keys.
//
// Tokens are looked up by their SHA-256 digest rather than by the raw secret.
// Comparing fixed-size digests (instead of variable-length secrets via a map of
// plaintext keys) avoids leaking, through timing, how much of a candidate key
// matched a configured one.
type Authenticator struct {
	keys map[[32]byte]*config.Key
}

// New builds an Authenticator from config keys. Each key is indexed by the
// SHA-256 digest of its plaintext token — computed here for plaintext keys, or
// taken directly from the configured hash for key_sha256 entries (whose
// plaintext mortise never sees).
func New(cfg *config.Config) *Authenticator {
	m := make(map[[32]byte]*config.Key, len(cfg.Keys))
	for i := range cfg.Keys {
		k := &cfg.Keys[i]
		var digest [32]byte
		if k.KeySHA256 != "" {
			b, err := hex.DecodeString(k.KeySHA256)
			if err != nil || len(b) != 32 {
				continue // rejected by config.validate; skip defensively
			}
			copy(digest[:], b)
		} else {
			digest = sha256.Sum256([]byte(k.Key))
		}
		m[digest] = k
	}
	return &Authenticator{keys: m}
}

// Authenticate extracts and validates the bearer token, returning the key.
func (a *Authenticator) Authenticate(r *http.Request) (*config.Key, bool) {
	tok := bearer(r)
	if tok == "" {
		return nil, false
	}
	k, ok := a.keys[sha256.Sum256([]byte(tok))]
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
