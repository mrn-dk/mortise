package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "mortise.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadDefaults(t *testing.T) {
	p := write(t, `
pools:
  - name: p
    backends:
      - base_url: http://x:8000/v1
routes:
  - model: m
    pool: p
keys:
  - key: sk-1
    rps: 3
`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":8080" {
		t.Errorf("default listen = %q", c.Listen)
	}
	if c.Limits.RequestTimeout != 60*time.Second {
		t.Errorf("default timeout = %v", c.Limits.RequestTimeout)
	}
	if c.Pools[0].Timeout != 60*time.Second {
		t.Errorf("pool timeout should inherit default, got %v", c.Pools[0].Timeout)
	}
	if c.Keys[0].Burst != 3 {
		t.Errorf("burst should default to ceil(rps)=3, got %d", c.Keys[0].Burst)
	}
}

func TestLoadHashedKey(t *testing.T) {
	p := write(t, `
pools:
  - name: p
    backends:
      - base_url: http://x:8000/v1
routes:
  - model: m
    pool: p
keys:
  - name: hashed
    key_sha256: 08025E34CE34BCA8A1CCE8EC834CB543077299E62FFA8C9CCFCA3D4437D42BE1
`)
	c, err := Load(p)
	if err != nil {
		t.Fatalf("hashed key config should load: %v", err)
	}
	// key_sha256 is normalized to lowercase; Identity uses the digest.
	want := "08025e34ce34bca8a1cce8ec834cb543077299e62ffa8c9ccfca3d4437d42be1"
	if c.Keys[0].KeySHA256 != want {
		t.Errorf("key_sha256 not normalized: %q", c.Keys[0].KeySHA256)
	}
	if c.Keys[0].Identity() != want {
		t.Errorf("identity should be the digest, got %q", c.Keys[0].Identity())
	}
}

func TestValidateErrors(t *testing.T) {
	cases := map[string]string{
		"unknown pool": `
pools:
  - name: p
    backends: [{base_url: http://x/v1}]
routes:
  - model: m
    pool: other
keys: [{key: sk-1}]
`,
		"no backends": `
pools:
  - name: p
    backends: []
routes:
  - model: m
    pool: p
keys: [{key: sk-1}]
`,
		"empty key": `
pools:
  - name: p
    backends: [{base_url: http://x/v1}]
routes:
  - model: m
    pool: p
keys: [{key: ""}]
`,
		"both key and hash": `
pools:
  - name: p
    backends: [{base_url: http://x/v1}]
routes:
  - model: m
    pool: p
keys: [{key: sk-1, key_sha256: 08025e34ce34bca8a1cce8ec834cb543077299e62ffa8c9ccfca3d4437d42be1}]
`,
		"bad hash length": `
pools:
  - name: p
    backends: [{base_url: http://x/v1}]
routes:
  - model: m
    pool: p
keys: [{key_sha256: deadbeef}]
`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(write(t, body)); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
