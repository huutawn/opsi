package webhookrelay

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	githubInstallationTokenBaseURL = "https://api.github.com/app/installations/"
	githubPrivateKeyMaxBytes       = 64 << 10
	githubResponseMaxBytes         = 1 << 20
	githubInstallationTokenSkew    = 2 * time.Minute
	githubSharedRequestTimeout     = 30 * time.Second
)

type installationToken struct {
	Token     string
	ExpiresAt time.Time
}

type installationTokenRequest struct {
	done  chan struct{}
	token installationToken
	err   error
}

type GitHubAppClient struct {
	appID      int64
	httpClient *http.Client
	clock      func() time.Time
	sign       func([]byte) ([]byte, error)

	mu       sync.Mutex
	tokens   map[int64]installationToken
	inFlight map[int64]*installationTokenRequest
}

func NewGitHubAppClient(cfg GitHubAppConfig, httpClient *http.Client, clock func() time.Time) (*GitHubAppClient, error) {
	if cfg.AppID <= 0 {
		return nil, fmt.Errorf("configure GitHub App client: app_id must be positive")
	}
	privateKey, err := loadGitHubAppPrivateKey(cfg.PrivateKeyPath)
	if err != nil {
		return nil, err
	}
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	clientCopy := *httpClient
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	if clientCopy.Timeout == 0 {
		clientCopy.Timeout = githubSharedRequestTimeout
	}
	client := &GitHubAppClient{
		appID:      cfg.AppID,
		httpClient: &clientCopy,
		clock:      clock,
		tokens:     make(map[int64]installationToken),
		inFlight:   make(map[int64]*installationTokenRequest),
	}
	client.sign = func(signingInput []byte) ([]byte, error) {
		digest := sha256.Sum256(signingInput)
		return rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, digest[:])
	}
	return client, nil
}

func validateGitHubAppPrivateKeyFile(path string) error {
	if path == "" || !filepath.IsAbs(path) {
		return fmt.Errorf("github_app.private_key_path must be an absolute path")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("github_app.private_key_path cannot be accessed: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("github_app.private_key_path must not be a symlink")
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("github_app.private_key_path must be a regular file")
	}
	permissions := info.Mode().Perm()
	if permissions&0o400 == 0 {
		return fmt.Errorf("github_app.private_key_path must be readable by its owner")
	}
	if permissions&0o022 != 0 {
		return fmt.Errorf("github_app.private_key_path must not be group/world writable")
	}
	if permissions&0o015 != 0 {
		return fmt.Errorf("github_app.private_key_path must not grant group execute or world access")
	}
	if info.Size() == 0 {
		return fmt.Errorf("github_app.private_key_path must not be empty")
	}
	if info.Size() > githubPrivateKeyMaxBytes {
		return fmt.Errorf("github_app.private_key_path exceeds the 64 KiB limit")
	}
	return nil
}

func loadGitHubAppPrivateKey(path string) (*rsa.PrivateKey, error) {
	if err := validateGitHubAppPrivateKeyFile(path); err != nil {
		return nil, err
	}
	before, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("load GitHub App private key: file metadata changed")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("load GitHub App private key: file read failed")
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || !after.Mode().IsRegular() {
		return nil, fmt.Errorf("load GitHub App private key: file metadata changed")
	}
	data, err := io.ReadAll(io.LimitReader(file, githubPrivateKeyMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("load GitHub App private key: file read failed")
	}
	if len(data) > githubPrivateKeyMaxBytes {
		return nil, fmt.Errorf("load GitHub App private key: file exceeds limit")
	}
	block, rest := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("load GitHub App private key: invalid PEM")
	}
	if len(bytes.TrimSpace(rest)) != 0 {
		return nil, fmt.Errorf("load GitHub App private key: multiple PEM blocks or trailing data")
	}
	if x509.IsEncryptedPEMBlock(block) || strings.Contains(block.Type, "ENCRYPTED") {
		return nil, fmt.Errorf("load GitHub App private key: encrypted PEM is not supported")
	}

	var privateKey *rsa.PrivateKey
	switch block.Type {
	case "RSA PRIVATE KEY":
		privateKey, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		var parsed any
		parsed, err = x509.ParsePKCS8PrivateKey(block.Bytes)
		if err == nil {
			var ok bool
			privateKey, ok = parsed.(*rsa.PrivateKey)
			if !ok {
				return nil, fmt.Errorf("load GitHub App private key: PKCS#8 key is not RSA")
			}
		}
	default:
		return nil, fmt.Errorf("load GitHub App private key: unsupported PEM block type")
	}
	if err != nil || privateKey == nil {
		return nil, fmt.Errorf("load GitHub App private key: invalid RSA private key")
	}
	if err := privateKey.Validate(); err != nil {
		return nil, fmt.Errorf("load GitHub App private key: invalid RSA private key")
	}
	return privateKey, nil
}

func (c *GitHubAppClient) now() time.Time {
	if c.clock != nil {
		return c.clock().UTC()
	}
	return time.Now().UTC()
}

func (c *GitHubAppClient) appJWT() (string, error) {
	now := c.now()
	claims := struct {
		IssuedAt  int64  `json:"iat"`
		ExpiresAt int64  `json:"exp"`
		Issuer    string `json:"iss"`
	}{
		IssuedAt:  now.Add(-time.Minute).Unix(),
		ExpiresAt: now.Add(9 * time.Minute).Unix(),
		Issuer:    strconv.FormatInt(c.appID, 10),
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("create GitHub App JWT: encode claims failed")
	}
	encode := base64.RawURLEncoding.EncodeToString
	signingInput := encode([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + encode(claimsJSON)
	signature, err := c.sign([]byte(signingInput))
	if err != nil {
		return "", fmt.Errorf("create GitHub App JWT: signing failed")
	}
	return signingInput + "." + encode(signature), nil
}

func (c *GitHubAppClient) InstallationToken(ctx context.Context, installationID int64) (string, time.Time, error) {
	if installationID <= 0 {
		return "", time.Time{}, fmt.Errorf("get installation token: installation ID must be positive")
	}
	now := c.now()
	c.mu.Lock()
	if cached, ok := c.tokens[installationID]; ok && cached.ExpiresAt.After(now.Add(githubInstallationTokenSkew)) {
		c.mu.Unlock()
		return cached.Token, cached.ExpiresAt, nil
	}
	request := c.inFlight[installationID]
	if request == nil {
		request = &installationTokenRequest{done: make(chan struct{})}
		c.inFlight[installationID] = request
		// The shared fetch outlives any one waiter; each caller still cancels its own wait.
		go c.fetchInstallationToken(context.WithoutCancel(ctx), installationID, request)
	}
	c.mu.Unlock()

	select {
	case <-request.done:
		if request.err != nil {
			return "", time.Time{}, request.err
		}
		return request.token.Token, request.token.ExpiresAt, nil
	case <-ctx.Done():
		return "", time.Time{}, fmt.Errorf("wait for installation token for installation %d: %w", installationID, ctx.Err())
	}
}

func (c *GitHubAppClient) fetchInstallationToken(ctx context.Context, installationID int64, request *installationTokenRequest) {
	ctx, cancel := context.WithTimeout(ctx, githubSharedRequestTimeout)
	defer cancel()
	token, err := c.requestInstallationToken(ctx, installationID)

	c.mu.Lock()
	if err == nil {
		c.tokens[installationID] = token
	}
	request.token = token
	request.err = err
	delete(c.inFlight, installationID)
	close(request.done)
	c.mu.Unlock()
}

func (c *GitHubAppClient) requestInstallationToken(ctx context.Context, installationID int64) (installationToken, error) {
	jwt, err := c.appJWT()
	if err != nil {
		return installationToken{}, fmt.Errorf("request installation token for installation %d: JWT creation failed", installationID)
	}
	endpoint := githubInstallationTokenBaseURL + strconv.FormatInt(installationID, 10) + "/access_tokens"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader("{}"))
	if err != nil {
		return installationToken{}, fmt.Errorf("request installation token for installation %d: request creation failed", installationID)
	}
	request.Header.Set("Authorization", "Bearer "+jwt)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	request.Header.Set("User-Agent", githubUserAgent)
	request.Header.Set("Content-Type", "application/json")

	response, err := c.httpClient.Do(request)
	if err != nil {
		return installationToken{}, fmt.Errorf("request installation token for installation %d: transport failed", installationID)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return installationToken{}, fmt.Errorf("request installation token for installation %d: HTTP status %d", installationID, response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, githubResponseMaxBytes+1))
	if err != nil {
		return installationToken{}, fmt.Errorf("parse installation token for installation %d: response read failed", installationID)
	}
	if len(body) > githubResponseMaxBytes {
		return installationToken{}, fmt.Errorf("parse installation token for installation %d: response exceeds limit", installationID)
	}
	var payload struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return installationToken{}, fmt.Errorf("parse installation token for installation %d: invalid response", installationID)
	}
	if payload.Token == "" || strings.IndexFunc(payload.Token, unicode.IsControl) >= 0 {
		return installationToken{}, fmt.Errorf("parse installation token for installation %d: invalid token", installationID)
	}
	expiresAt, err := time.Parse(time.RFC3339, payload.ExpiresAt)
	if err != nil || !expiresAt.After(c.now()) {
		return installationToken{}, fmt.Errorf("parse installation token for installation %d: invalid expiry", installationID)
	}
	return installationToken{Token: payload.Token, ExpiresAt: expiresAt.UTC()}, nil
}
