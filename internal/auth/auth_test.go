package auth

import (
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
