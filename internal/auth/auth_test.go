package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"testing"

	"github.com/mrn-dk/mortise/internal/config"
)

func TestAuthenticate(t *testing.T) {
	a := New(&config.Config{Keys: []config.Key{{Key: "sk-secret", Name: "team"}}})

	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer sk-secret")
	k, ok := a.Authenticate(req)
	if !ok || k.Name != "team" {
		t.Fatalf("valid key should authenticate, got ok=%v k=%v", ok, k)
	}

	req.Header.Set("Authorization", "Bearer wrong")
	if _, ok := a.Authenticate(req); ok {
		t.Fatal("wrong key must not authenticate")
	}

	req.Header.Del("Authorization")
	if _, ok := a.Authenticate(req); ok {
		t.Fatal("missing key must not authenticate")
	}
}

func TestAuthenticateHashedKey(t *testing.T) {
	// SHA-256 of "sk-secret" (printf %s "sk-secret" | sha256sum).
	digest := func() string {
		h := sha256.Sum256([]byte("sk-secret"))
		return hex.EncodeToString(h[:])
	}()
	a := New(&config.Config{Keys: []config.Key{{KeySHA256: digest, Name: "hashed"}}})

	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer sk-secret")
	k, ok := a.Authenticate(req)
	if !ok || k.Name != "hashed" {
		t.Fatalf("hashed key should authenticate, got ok=%v k=%v", ok, k)
	}
	req.Header.Set("Authorization", "Bearer nope")
	if _, ok := a.Authenticate(req); ok {
		t.Fatal("wrong token must not authenticate against a hashed key")
	}
}
