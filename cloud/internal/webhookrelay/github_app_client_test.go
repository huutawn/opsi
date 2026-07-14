package webhookrelay

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type githubAppRoundTripFunc func(*http.Request) (*http.Response, error)

func (f githubAppRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func generateGitHubAppTestKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func writeGitHubAppTestPEM(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "github-app.pem")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeGitHubAppTestKey(t *testing.T, key *rsa.PrivateKey, pkcs8 bool) string {
	t.Helper()
	if pkcs8 {
		data, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		return writeGitHubAppTestPEM(t, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: data}))
	}
	return writeGitHubAppTestPEM(t, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}

func newGitHubAppTestClient(t *testing.T, transport http.RoundTripper, now time.Time) (*GitHubAppClient, *rsa.PrivateKey) {
	t.Helper()
	key := generateGitHubAppTestKey(t)
	client, err := NewGitHubAppClient(GitHubAppConfig{AppID: 12345, PrivateKeyPath: writeGitHubAppTestKey(t, key, false)}, &http.Client{Transport: transport}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return client, key
}

func githubAppResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestLoadGitHubAppPrivateKeyAcceptsPKCS1AndPKCS8RSA(t *testing.T) {
	key := generateGitHubAppTestKey(t)
	for _, pkcs8 := range []bool{false, true} {
		path := writeGitHubAppTestKey(t, key, pkcs8)
		parsed, err := loadGitHubAppPrivateKey(path)
		if err != nil {
			t.Fatal(err)
		}
		if parsed.N.Cmp(key.N) != 0 {
			t.Fatal("parsed RSA key does not match")
		}
	}
}

func TestLoadGitHubAppPrivateKeyRejectsUnsupportedPEM(t *testing.T) {
	rsaKey := generateGitHubAppTestKey(t)
	publicDER, err := x509.MarshalPKIXPublicKey(&rsaKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecDER, err := x509.MarshalPKCS8PrivateKey(ecKey)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := x509.EncryptPEMBlock(rand.Reader, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(rsaKey), []byte("passphrase"), x509.PEMCipherAES256)
	if err != nil {
		t.Fatal(err)
	}
	valid := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rsaKey)})
	tests := map[string][]byte{
		"EC key":        pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: ecDER}),
		"public key":    pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER}),
		"encrypted PEM": pem.EncodeToMemory(encrypted),
		"multiple keys": append(append([]byte{}, valid...), valid...),
		"trailing data": append(append([]byte{}, valid...), []byte("not-whitespace")...),
		"invalid PEM":   []byte("not a PEM file"),
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := loadGitHubAppPrivateKey(writeGitHubAppTestPEM(t, data)); err == nil {
				t.Fatal("expected key rejection")
			}
		})
	}
}

func TestGitHubAppJWTClaimsAndRS256Signature(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	client, key := newGitHubAppTestClient(t, nil, now)
	token, err := client.appJWT()
	if err != nil {
		t.Fatal(err)
	}
	segments := strings.Split(token, ".")
	if len(segments) != 3 {
		t.Fatalf("JWT segments=%d", len(segments))
	}
	decode := func(segment string) []byte {
		data, err := base64.RawURLEncoding.DecodeString(segment)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	var header map[string]string
	if err := json.Unmarshal(decode(segments[0]), &header); err != nil {
		t.Fatal(err)
	}
	if header["alg"] != "RS256" || header["typ"] != "JWT" || len(header) != 2 {
		t.Fatalf("JWT header=%v", header)
	}
	var claims map[string]any
	if err := json.Unmarshal(decode(segments[1]), &claims); err != nil {
		t.Fatal(err)
	}
	if len(claims) != 3 || claims["iss"] != "12345" || int64(claims["iat"].(float64)) != now.Add(-time.Minute).Unix() || int64(claims["exp"].(float64)) != now.Add(9*time.Minute).Unix() {
		t.Fatalf("JWT claims=%v", claims)
	}
	digest := sha256.Sum256([]byte(segments[0] + "." + segments[1]))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], decode(segments[2])); err != nil {
		t.Fatalf("JWT signature: %v", err)
	}
	if strings.Contains(token, "PRIVATE KEY") || strings.Contains(token, strconv.FormatInt(key.D.Int64(), 10)) {
		t.Fatal("JWT exposed private key material")
	}
}

func TestGitHubAppJWTSigningFailureIsSanitized(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	client, _ := newGitHubAppTestClient(t, nil, now)
	secret := "private-signing-material"
	client.sign = func([]byte) ([]byte, error) { return nil, errors.New(secret) }
	_, err := client.appJWT()
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("signing error=%v", err)
	}
}

func TestInstallationTokenRequestUsesFixedEndpointHeadersAndBody(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	var requestCount atomic.Int32
	client, _ := newGitHubAppTestClient(t, githubAppRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestCount.Add(1)
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		if request.URL.String() != githubInstallationTokenBaseURL+"6789/access_tokens" || request.Method != http.MethodPost || string(body) != "{}" {
			t.Fatalf("request=%s %s body=%q", request.Method, request.URL, body)
		}
		if !strings.HasPrefix(request.Header.Get("Authorization"), "Bearer ") || len(strings.Split(strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer "), ".")) != 3 ||
			request.Header.Get("Accept") != "application/vnd.github+json" || request.Header.Get("X-GitHub-Api-Version") != githubAPIVersion ||
			request.Header.Get("User-Agent") != githubUserAgent || request.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("headers=%v", request.Header)
		}
		return githubAppResponse(http.StatusCreated, `{"token":"installation-token","expires_at":"`+now.Add(time.Hour).Format(time.RFC3339)+`"}`), nil
	}), now)
	token, expiresAt, err := client.InstallationToken(t.Context(), 6789)
	if err != nil {
		t.Fatal(err)
	}
	if token != "installation-token" || !expiresAt.Equal(now.Add(time.Hour)) || requestCount.Load() != 1 {
		t.Fatalf("token=%q expiry=%s requests=%d", token, expiresAt, requestCount.Load())
	}
}

func TestInstallationTokenRejectsNonPositiveInstallationID(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	client, _ := newGitHubAppTestClient(t, githubAppRoundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid installation ID reached transport")
		return nil, nil
	}), now)
	for _, installationID := range []int64{0, -1} {
		if _, _, err := client.InstallationToken(t.Context(), installationID); err == nil {
			t.Fatalf("installation ID %d was accepted", installationID)
		}
	}
}

func TestInstallationTokenRejectsUnsafeResponsesWithoutLeaks(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	secretBody := "remote-secret-response-body"
	tests := map[string]*http.Response{
		"non success":    githubAppResponse(http.StatusForbidden, secretBody),
		"missing token":  githubAppResponse(http.StatusCreated, `{"expires_at":"`+now.Add(time.Hour).Format(time.RFC3339)+`"}`),
		"control token":  githubAppResponse(http.StatusCreated, `{"token":"secret\nvalue","expires_at":"`+now.Add(time.Hour).Format(time.RFC3339)+`"}`),
		"invalid expiry": githubAppResponse(http.StatusCreated, `{"token":"secret-token","expires_at":"invalid"}`),
		"expired":        githubAppResponse(http.StatusCreated, `{"token":"secret-token","expires_at":"`+now.Add(-time.Second).Format(time.RFC3339)+`"}`),
		"oversized":      githubAppResponse(http.StatusCreated, strings.Repeat("x", githubResponseMaxBytes+1)),
	}
	for name, response := range tests {
		t.Run(name, func(t *testing.T) {
			client, _ := newGitHubAppTestClient(t, githubAppRoundTripFunc(func(*http.Request) (*http.Response, error) { return response, nil }), now)
			_, _, err := client.InstallationToken(t.Context(), 42)
			if err == nil {
				t.Fatal("expected response rejection")
			}
			for _, sensitive := range []string{secretBody, "secret-token", "secret\nvalue"} {
				if strings.Contains(err.Error(), sensitive) {
					t.Fatalf("error leaked token/body: %q", err)
				}
			}
		})
	}
}

func TestInstallationTokenRedirectIsNotFollowed(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	var requests atomic.Int32
	client, _ := newGitHubAppTestClient(t, githubAppRoundTripFunc(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		response := githubAppResponse(http.StatusFound, "redirect body")
		response.Header.Set("Location", "https://example.test/capture")
		return response, nil
	}), now)
	_, _, err := client.InstallationToken(t.Context(), 42)
	if err == nil || requests.Load() != 1 || !strings.Contains(err.Error(), "HTTP status 302") {
		t.Fatalf("redirect requests=%d error=%v", requests.Load(), err)
	}
}

func TestInstallationTokenCacheReuseRefreshAndFailure(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	var current = now
	var requests atomic.Int32
	var fail atomic.Bool
	key := generateGitHubAppTestKey(t)
	client, err := NewGitHubAppClient(
		GitHubAppConfig{AppID: 12345, PrivateKeyPath: writeGitHubAppTestKey(t, key, false)},
		&http.Client{Transport: githubAppRoundTripFunc(func(*http.Request) (*http.Response, error) {
			requestNumber := requests.Add(1)
			if fail.Load() {
				return githubAppResponse(http.StatusBadGateway, "do-not-leak"), nil
			}
			return githubAppResponse(http.StatusCreated, `{"token":"token-`+strconv.Itoa(int(requestNumber))+`","expires_at":"`+current.Add(10*time.Minute).Format(time.RFC3339)+`"}`), nil
		})},
		func() time.Time { return current },
	)
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := client.InstallationToken(t.Context(), 7)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := client.InstallationToken(t.Context(), 7)
	if err != nil || first != second || requests.Load() != 1 {
		t.Fatalf("cache token=%q/%q requests=%d error=%v", first, second, requests.Load(), err)
	}
	current = current.Add(9 * time.Minute)
	third, _, err := client.InstallationToken(t.Context(), 7)
	if err != nil || third == first || requests.Load() != 2 {
		t.Fatalf("refresh token=%q requests=%d error=%v", third, requests.Load(), err)
	}
	fail.Store(true)
	delete(client.tokens, 8)
	if _, _, err := client.InstallationToken(t.Context(), 8); err == nil {
		t.Fatal("expected token request failure")
	}
	if _, _, err := client.InstallationToken(t.Context(), 8); err == nil || requests.Load() != 4 {
		t.Fatalf("failure was cached: requests=%d error=%v", requests.Load(), err)
	}
}

func TestInstallationTokenConcurrentCallersShareRequest(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	var requests atomic.Int32
	release := make(chan struct{})
	client, _ := newGitHubAppTestClient(t, githubAppRoundTripFunc(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		<-release
		return githubAppResponse(http.StatusCreated, `{"token":"shared-token","expires_at":"`+now.Add(time.Hour).Format(time.RFC3339)+`"}`), nil
	}), now)

	const callers = 8
	var wait sync.WaitGroup
	wait.Add(callers)
	errorsSeen := make(chan error, callers)
	for range callers {
		go func() {
			defer wait.Done()
			token, _, err := client.InstallationToken(context.Background(), 99)
			if err == nil && token != "shared-token" {
				err = errors.New("caller received wrong token")
			}
			errorsSeen <- err
		}()
	}
	for requests.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	close(release)
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("requests=%d", requests.Load())
	}
}

func TestInstallationTokenDifferentInstallationsRunIndependently(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	client, _ := newGitHubAppTestClient(t, githubAppRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		started <- struct{}{}
		<-release
		return githubAppResponse(http.StatusCreated, `{"token":"parallel-token","expires_at":"`+now.Add(time.Hour).Format(time.RFC3339)+`"}`), nil
	}), now)
	var wait sync.WaitGroup
	for _, id := range []int64{1, 2} {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, _, _ = client.InstallationToken(context.Background(), id)
		}()
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first installation did not start")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("second installation was blocked")
	}
	close(release)
	wait.Wait()
}

func TestInstallationTokenRequestBodyIsCanonicalEmptyObject(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	client, _ := newGitHubAppTestClient(t, githubAppRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(request.Body)
		if !bytes.Equal(body, []byte("{}")) {
			t.Fatalf("body=%q", body)
		}
		return githubAppResponse(http.StatusCreated, `{"token":"token","expires_at":"`+now.Add(time.Hour).Format(time.RFC3339)+`"}`), nil
	}), now)
	_, _, err := client.InstallationToken(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
}
