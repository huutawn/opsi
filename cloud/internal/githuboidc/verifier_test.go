package githuboidc

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestVerifierValidAndNegativeClaims(t *testing.T) {
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var jwksRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/jwks" {
			http.NotFound(w, r)
			return
		}
		jwksRequests.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]string{"kty": "RSA", "use": "sig", "alg": "RS256", "kid": "kid-1", "n": base64.RawURLEncoding.EncodeToString(private.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(bigIntBytes(private.E))}}})
	}))
	defer server.Close()
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.Issuer = server.URL
	cfg.JWKSURL = server.URL + "/jwks"
	cfg.Audience = "opsi-test"
	cfg.CacheTTL = Duration(time.Hour)
	now := time.Unix(1_700_000_000, 0).UTC()
	verifier := newVerifier(cfg, server.Client(), func() time.Time { return now })
	valid := testToken(t, private, "kid-1", map[string]any{"iss": server.URL, "aud": "opsi-test", "sub": "repo:huutawn/opsi:ref:refs/heads/developer", "repository": "huutawn/opsi", "repository_owner": "huutawn", "repository_id": "7", "repository_owner_id": "8", "ref": "refs/heads/developer", "sha": strings.Repeat("a", 40), "event_name": "push", "workflow": "opsi-cd", "workflow_ref": "huutawn/opsi/.github/workflows/opsi-cd.yaml@refs/heads/developer", "run_id": "99", "run_attempt": "1", "nbf": now.Add(-time.Minute).Unix(), "iat": now.Add(-time.Second).Unix(), "exp": now.Add(time.Minute).Unix()})
	identity, err := verifier.Verify(context.Background(), valid)
	if err != nil || identity.RepositoryID != 7 || identity.RunAttempt != 1 {
		t.Fatalf("identity=%+v err=%v", identity, err)
	}

	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"wrong issuer", func(c map[string]any) { c["iss"] = "https://evil.example" }},
		{"wrong audience", func(c map[string]any) { c["aud"] = "opsi-other" }},
		{"split audience", func(c map[string]any) { c["aud"] = []string{"opsi-test", "opsi-other"} }},
		{"wrong repository", func(c map[string]any) { c["repository"] = "not-a-repository" }},
		{"wrong owner", func(c map[string]any) { c["repository_owner"] = "owner\n" }},
		{"wrong ref", func(c map[string]any) { c["ref"] = "refs/heads/" }},
		{"wrong event", func(c map[string]any) { c["event_name"] = "push!" }},
		{"wrong workflow", func(c map[string]any) { c["workflow_ref"] = "workflow" }},
		{"wrong run", func(c map[string]any) { c["run_id"] = "0" }},
		{"wrong run attempt", func(c map[string]any) { c["run_attempt"] = "2147483648" }},
		{"expired", func(c map[string]any) { c["exp"] = now.Add(-2 * time.Minute).Unix() }},
		{"not yet valid", func(c map[string]any) { c["nbf"] = now.Add(2 * time.Minute).Unix() }},
		{"future iat", func(c map[string]any) { c["iat"] = now.Add(2 * time.Minute).Unix() }},
		{"wrong claim type", func(c map[string]any) { c["run_id"] = float64(99) }},
		{"short sha", func(c map[string]any) { c["sha"] = "tag" }},
		{"missing workflow ref", func(c map[string]any) { delete(c, "workflow_ref") }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			claims := map[string]any{"iss": server.URL, "aud": "opsi-test", "sub": "repo:huutawn/opsi:ref:refs/heads/developer", "repository": "huutawn/opsi", "repository_owner": "huutawn", "repository_id": "7", "repository_owner_id": "8", "ref": "refs/heads/developer", "sha": strings.Repeat("a", 40), "event_name": "push", "workflow": "opsi-cd", "workflow_ref": "huutawn/opsi/.github/workflows/opsi-cd.yaml@refs/heads/developer", "run_id": "99", "run_attempt": "1", "nbf": now.Add(-time.Minute).Unix(), "iat": now.Add(-time.Second).Unix(), "exp": now.Add(time.Minute).Unix()}
			test.mutate(claims)
			if _, err := verifier.Verify(context.Background(), testToken(t, private, "kid-1", claims)); err == nil {
				t.Fatal("invalid token accepted")
			}
		})
	}
	badSignature := valid[:len(valid)-1] + "x"
	if _, err := verifier.Verify(context.Background(), badSignature); err == nil {
		t.Fatal("bad signature accepted")
	} else if strings.Contains(err.Error(), badSignature) {
		t.Fatal("verifier error reflected JWT")
	}
	if _, err := verifier.Verify(context.Background(), strings.Repeat("a", cfg.MaxTokenBytes+1)); err == nil {
		t.Fatal("oversized token accepted")
	}
	if jwksRequests.Load() < 1 {
		t.Fatal("JWKS was not fetched")
	}
}

func TestConfigValidationFailsClosed(t *testing.T) {
	base := DefaultConfig()
	if err := base.Validate(true); err == nil {
		t.Fatal("production accepted disabled OIDC")
	}
	base.Enabled = true
	if err := base.Validate(true); err == nil {
		t.Fatal("production accepted empty workload allowlist")
	}
	base.Workloads = []WorkloadPolicy{{RepositoryID: 7, ServiceKey: "api", WorkflowRefs: []string{"x"}, Refs: []string{"x"}, Events: []string{"push"}, OCIRepositories: []string{"ghcr.io/x/y"}}}
	for name, mutate := range map[string]func(*Config){"issuer": func(c *Config) { c.Issuer = "https://evil.example" }, "jwks": func(c *Config) { c.JWKSURL = "http://127.0.0.1/jwks" }, "timeout": func(c *Config) { c.HTTPTimeout = Duration(31 * time.Second) }, "cache": func(c *Config) { c.CacheTTL = Duration(30 * time.Second) }, "token": func(c *Config) { c.MaxTokenBytes = 65 << 10 }} {
		t.Run(name, func(t *testing.T) {
			config := base
			mutate(&config)
			if err := config.Validate(true); err == nil {
				t.Fatal("invalid production config accepted")
			}
		})
	}
}

func TestVerifierRejectsAlgorithmNoneUnknownKidAndRedirect(t *testing.T) {
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var redirects atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/jwks" {
			redirects.Add(1)
			http.Redirect(w, r, "/other", http.StatusFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{}})
	}))
	defer server.Close()
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.Issuer = server.URL
	cfg.JWKSURL = server.URL + "/jwks"
	cfg.Audience = "opsi-test"
	verifier := newVerifier(cfg, server.Client(), time.Now)
	claims := map[string]any{"iss": server.URL, "aud": "opsi-test", "sub": "repo:x/y:ref:refs/heads/main", "repository": "x/y", "repository_owner": "x", "repository_id": "1", "repository_owner_id": "2", "ref": "refs/heads/main", "sha": strings.Repeat("a", 40), "event_name": "push", "workflow": "w", "workflow_ref": "x/y/.github/workflows/w.yml@refs/heads/main", "run_id": "1", "run_attempt": "1", "nbf": time.Now().Add(-time.Minute).Unix(), "iat": time.Now().Add(-time.Second).Unix(), "exp": time.Now().Add(time.Minute).Unix()}
	none := testUnsignedToken(t, map[string]any{"alg": "none", "kid": "kid-1"}, claims)
	if _, err := verifier.Verify(context.Background(), none); err == nil {
		t.Fatal("alg none accepted")
	}
	if _, err := verifier.Verify(context.Background(), testToken(t, private, "unknown", claims)); err == nil {
		t.Fatal("unknown kid accepted")
	}
	missingKid := testUnsignedToken(t, map[string]any{"alg": "RS256"}, claims)
	if _, err := verifier.Verify(context.Background(), missingKid); err == nil {
		t.Fatal("missing kid accepted")
	}
	if redirects.Load() != 1 {
		t.Fatalf("redirects=%d", redirects.Load())
	}
}

func TestJWKSUnknownKidRefreshIsCoalescedAndBounded(t *testing.T) {
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]string{"kty": "RSA", "use": "sig", "alg": "RS256", "kid": "kid-1", "n": base64.RawURLEncoding.EncodeToString(private.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(bigIntBytes(private.E))}}})
	}))
	defer server.Close()
	now := time.Unix(1_700_000_000, 0).UTC()
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.Issuer = server.URL
	cfg.JWKSURL = server.URL
	cfg.Audience = "opsi-test"
	cfg.Workloads = []WorkloadPolicy{{RepositoryID: 7, ServiceKey: "api", WorkflowRefs: []string{"x"}, Refs: []string{"x"}, Events: []string{"x"}, OCIRepositories: []string{"x"}}}
	verifier := newVerifier(cfg, server.Client(), func() time.Time { return now })
	claims := map[string]any{"iss": server.URL, "aud": "opsi-test", "sub": "repo:huutawn/opsi:ref:refs/heads/developer", "repository": "huutawn/opsi", "repository_owner": "huutawn", "repository_id": "7", "repository_owner_id": "8", "ref": "refs/heads/developer", "sha": strings.Repeat("a", 40), "event_name": "push", "workflow": "opsi-cd", "workflow_ref": "huutawn/opsi/.github/workflows/opsi-cd.yaml@refs/heads/developer", "run_id": "99", "run_attempt": "1", "nbf": now.Add(-time.Minute).Unix(), "iat": now.Add(-time.Second).Unix(), "exp": now.Add(time.Minute).Unix()}
	if _, err := verifier.Verify(context.Background(), testToken(t, private, "kid-1", claims)); err != nil {
		t.Fatal(err)
	}
	unknown := testToken(t, private, "kid-2", claims)
	var wait sync.WaitGroup
	for range 16 {
		wait.Add(1)
		go func() { defer wait.Done(); _, _ = verifier.Verify(context.Background(), unknown) }()
	}
	wait.Wait()
	if requests.Load() != 2 {
		t.Fatalf("JWKS requests=%d want=2", requests.Load())
	}
}

func TestJWKSResponseSizeIsBounded(t *testing.T) {
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(strings.Repeat("x", 4097))) }))
	defer server.Close()
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.Issuer = server.URL
	cfg.JWKSURL = server.URL
	cfg.Audience = "opsi-test"
	cfg.MaxJWKSBytes = 4096
	verifier := newVerifier(cfg, server.Client(), time.Now)
	claims := map[string]any{"iss": server.URL, "aud": "opsi-test"}
	if _, err := verifier.Verify(context.Background(), testToken(t, private, "kid", claims)); err == nil {
		t.Fatal("oversized JWKS accepted")
	}
}

func TestJWKSRefreshFailureIsBackoffBounded(t *testing.T) {
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	var unavailable atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		if unavailable.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]string{"kty": "RSA", "use": "sig", "alg": "RS256", "kid": "kid-1", "n": base64.RawURLEncoding.EncodeToString(private.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(bigIntBytes(private.E))}}})
	}))
	defer server.Close()
	base := time.Unix(1_700_000_000, 0).UTC()
	var current atomic.Int64
	current.Store(base.Unix())
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.Issuer = server.URL
	cfg.JWKSURL = server.URL
	cfg.Audience = "opsi-test"
	verifier := newVerifier(cfg, server.Client(), func() time.Time { return time.Unix(current.Load(), 0).UTC() })
	claims := map[string]any{"iss": server.URL, "aud": "opsi-test", "sub": "repo:huutawn/opsi:ref:refs/heads/developer", "repository": "huutawn/opsi", "repository_owner": "huutawn", "repository_id": "7", "repository_owner_id": "8", "ref": "refs/heads/developer", "sha": strings.Repeat("a", 40), "event_name": "push", "workflow": "opsi-cd", "workflow_ref": "huutawn/opsi/.github/workflows/opsi-cd.yaml@refs/heads/developer", "run_id": "99", "run_attempt": "1", "nbf": base.Add(-time.Minute).Unix(), "iat": base.Add(-time.Second).Unix(), "exp": base.Add(time.Minute).Unix()}
	if _, err := verifier.Verify(context.Background(), testToken(t, private, "kid-1", claims)); err != nil {
		t.Fatal(err)
	}
	unavailable.Store(true)
	current.Store(base.Add(2 * time.Minute).Unix())
	unknown := testToken(t, private, "kid-2", claims)
	var wait sync.WaitGroup
	for range 16 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := verifier.Verify(context.Background(), unknown); err == nil {
				t.Error("unavailable JWKS accepted")
			}
		}()
	}
	wait.Wait()
	if requests.Load() != 2 {
		t.Fatalf("JWKS requests=%d want=2", requests.Load())
	}
}

func testToken(t *testing.T, private *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	header := map[string]any{"typ": "JWT", "alg": "RS256", "kid": kid}
	headerBytes, _ := json.Marshal(header)
	claimBytes, _ := json.Marshal(claims)
	encoded := base64.RawURLEncoding.EncodeToString(headerBytes) + "." + base64.RawURLEncoding.EncodeToString(claimBytes)
	digest := sha256Sum([]byte(encoded))
	signature, err := rsa.SignPKCS1v15(rand.Reader, private, crypto.SHA256, digest)
	if err != nil {
		t.Fatal(err)
	}
	return encoded + "." + base64.RawURLEncoding.EncodeToString(signature)
}
func testUnsignedToken(t *testing.T, header map[string]any, claims map[string]any) string {
	t.Helper()
	headerBytes, _ := json.Marshal(header)
	claimBytes, _ := json.Marshal(claims)
	return base64.RawURLEncoding.EncodeToString(headerBytes) + "." + base64.RawURLEncoding.EncodeToString(claimBytes) + "." + "x"
}
func bigIntBytes(_ int) []byte     { return []byte{1, 0, 1} }
func sha256Sum(data []byte) []byte { sum := sha256.Sum256(data); return sum[:] }
