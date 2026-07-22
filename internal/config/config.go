// Package config loads and validates the single-file mortise configuration:
// backend pools, model->pool routes, API keys, and limits.
package config

import (
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level mortise configuration.
type Config struct {
	// Listen is the ingress bind address, e.g. ":8080".
	Listen string `yaml:"listen"`

	// Pools are named groups of interchangeable OpenAI-compatible backends.
	Pools []Pool `yaml:"pools"`

	// Routes map a client-visible model name to a pool.
	Routes []Route `yaml:"routes"`

	// Keys are the client API keys accepted at ingress.
	Keys []Key `yaml:"keys"`

	// Limits configures global defaults.
	Limits Limits `yaml:"limits"`

	// Telemetry configures OTel export.
	Telemetry Telemetry `yaml:"telemetry"`
}

// Pool is a set of interchangeable backends. Requests routed to a pool are
// tried against its backends in order, with failover to the next on error.
type Pool struct {
	Name     string    `yaml:"name"`
	Backends []Backend `yaml:"backends"`

	// Retries is the max number of additional attempts (across backends) for a
	// single request. 0 means a single attempt.
	Retries int `yaml:"retries"`

	// Timeout bounds a single upstream attempt. Zero uses Limits.RequestTimeout.
	Timeout time.Duration `yaml:"timeout"`
}

// Backend is a single OpenAI-compatible upstream endpoint.
type Backend struct {
	// Name is an optional human label used in telemetry.
	Name string `yaml:"name"`
	// BaseURL is the root of the OpenAI-compatible API, e.g.
	// "http://vllm-a:8000/v1" or "https://api.openai.com/v1".
	BaseURL string `yaml:"base_url"`
	// APIKey is the credential mortise presents to this upstream (egress auth).
	APIKey string `yaml:"api_key"`
	// Model, if set, overrides the model name sent upstream (e.g. map a public
	// alias to a backend-specific model id).
	Model string `yaml:"model"`
	// Weight is reserved for future load balancing; unused in v0 (order = priority).
	Weight int `yaml:"weight"`
}

// Route maps a public model name to a pool.
type Route struct {
	Model string `yaml:"model"`
	Pool  string `yaml:"pool"`
}

// Key is a client credential with optional per-key limits. Provide exactly one
// of Key (plaintext) or KeySHA256 (the hex-encoded SHA-256 of the token, so the
// raw secret never lives in the config file).
type Key struct {
	// Key is the secret bearer token presented by clients. Prefer KeySHA256 or
	// a ${ENV} reference so plaintext secrets aren't stored at rest.
	Key string `yaml:"key"`
	// KeySHA256 is the lowercase hex SHA-256 digest of the bearer token. When
	// set, mortise verifies clients against the digest and never sees the
	// plaintext. Generate with: printf %s "$TOKEN" | sha256sum.
	KeySHA256 string `yaml:"key_sha256"`
	// Name is a human label for accounting/telemetry.
	Name string `yaml:"name"`
	// RPS is the sustained requests-per-second limit for this key. 0 = unlimited.
	RPS float64 `yaml:"rps"`
	// Burst is the token-bucket burst size. Defaults to ceil(RPS) if 0.
	Burst int `yaml:"burst"`
	// TokensPerMin caps total (prompt+completion) tokens per rolling minute.
	// 0 = unlimited.
	TokensPerMin int `yaml:"tokens_per_min"`
}

// Identity returns a stable, non-reversible-agnostic key used internally for
// rate-limit and dedup namespacing. It never appears in logs or telemetry
// (those use Name). For hashed keys it is the digest; for plaintext keys it is
// the token itself (already in memory), so behaviour is identical either way.
func (k *Key) Identity() string {
	if k.KeySHA256 != "" {
		return k.KeySHA256
	}
	return k.Key
}

// Limits holds global defaults.
type Limits struct {
	// RequestTimeout bounds a single upstream attempt when a pool sets none.
	RequestTimeout time.Duration `yaml:"request_timeout"`
	// IdempotencyTTL is how long a completed response is cached for dedup.
	IdempotencyTTL time.Duration `yaml:"idempotency_ttl"`
	// IdempotencyMaxEntries caps the number of cached idempotent responses
	// (LRU eviction beyond this). 0 uses a built-in default.
	IdempotencyMaxEntries int `yaml:"idempotency_max_entries"`
	// IdempotencyMaxBodyBytes caps the size of a response that may be cached
	// for replay; larger responses stream through but are not cached.
	IdempotencyMaxBodyBytes int `yaml:"idempotency_max_body_bytes"`
}

// Telemetry configures OpenTelemetry export.
type Telemetry struct {
	// ServiceName reported to OTel. Defaults to "mortise".
	ServiceName string `yaml:"service_name"`
	// OTLPEndpoint is the collector endpoint (e.g. "localhost:4317"). Empty
	// disables OTLP export (traces/metrics still recorded to a no-op).
	OTLPEndpoint string `yaml:"otlp_endpoint"`
	// Insecure uses a non-TLS gRPC connection to the collector.
	Insecure bool `yaml:"insecure"`
}

// Load reads, parses, applies defaults, and validates a config file.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	// Nudge operators away from world-readable secret files.
	if info, statErr := os.Stat(path); statErr == nil {
		if mode := info.Mode().Perm(); mode&0o077 != 0 {
			fmt.Fprintf(os.Stderr, "mortise: warning: config %s is readable by group/other (mode %#o); tighten to 0600\n", path, mode)
		}
	}
	// Expand ${VAR}/$VAR references (e.g. api keys) from the environment.
	raw = []byte(os.ExpandEnv(string(raw)))
	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.Listen == "" {
		c.Listen = ":8080"
	}
	if c.Limits.RequestTimeout == 0 {
		c.Limits.RequestTimeout = 60 * time.Second
	}
	if c.Limits.IdempotencyTTL == 0 {
		c.Limits.IdempotencyTTL = 24 * time.Hour
	}
	if c.Limits.IdempotencyMaxEntries == 0 {
		c.Limits.IdempotencyMaxEntries = 10000
	}
	if c.Limits.IdempotencyMaxBodyBytes == 0 {
		c.Limits.IdempotencyMaxBodyBytes = 4 << 20 // 4 MiB
	}
	if c.Telemetry.ServiceName == "" {
		c.Telemetry.ServiceName = "mortise"
	}
	for i := range c.Pools {
		if c.Pools[i].Timeout == 0 {
			c.Pools[i].Timeout = c.Limits.RequestTimeout
		}
	}
	for i := range c.Keys {
		c.Keys[i].KeySHA256 = strings.ToLower(strings.TrimSpace(c.Keys[i].KeySHA256))
		if c.Keys[i].Burst == 0 && c.Keys[i].RPS > 0 {
			c.Keys[i].Burst = int(c.Keys[i].RPS)
			if c.Keys[i].Burst < 1 {
				c.Keys[i].Burst = 1
			}
		}
	}
}

func (c *Config) validate() error {
	if len(c.Pools) == 0 {
		return fmt.Errorf("config: at least one pool is required")
	}
	pools := make(map[string]bool, len(c.Pools))
	for _, p := range c.Pools {
		if p.Name == "" {
			return fmt.Errorf("config: pool with empty name")
		}
		if pools[p.Name] {
			return fmt.Errorf("config: duplicate pool %q", p.Name)
		}
		pools[p.Name] = true
		if len(p.Backends) == 0 {
			return fmt.Errorf("config: pool %q has no backends", p.Name)
		}
		for i, b := range p.Backends {
			if b.BaseURL == "" {
				return fmt.Errorf("config: pool %q backend %d missing base_url", p.Name, i)
			}
		}
	}
	if len(c.Routes) == 0 {
		return fmt.Errorf("config: at least one route is required")
	}
	seen := make(map[string]bool, len(c.Routes))
	for _, r := range c.Routes {
		if r.Model == "" {
			return fmt.Errorf("config: route with empty model")
		}
		if seen[r.Model] {
			return fmt.Errorf("config: duplicate route for model %q", r.Model)
		}
		seen[r.Model] = true
		if !pools[r.Pool] {
			return fmt.Errorf("config: route %q references unknown pool %q", r.Model, r.Pool)
		}
	}
	if len(c.Keys) == 0 {
		return fmt.Errorf("config: at least one key is required")
	}
	for i, k := range c.Keys {
		switch {
		case k.Key == "" && k.KeySHA256 == "":
			return fmt.Errorf("config: key %d must set key or key_sha256", i)
		case k.Key != "" && k.KeySHA256 != "":
			return fmt.Errorf("config: key %d sets both key and key_sha256 (choose one)", i)
		case k.KeySHA256 != "":
			if len(k.KeySHA256) != 64 {
				return fmt.Errorf("config: key %d key_sha256 must be a 64-char hex sha-256 digest", i)
			}
			if _, err := hex.DecodeString(k.KeySHA256); err != nil {
				return fmt.Errorf("config: key %d key_sha256 is not valid hex: %w", i, err)
			}
		}
	}
	return nil
}
