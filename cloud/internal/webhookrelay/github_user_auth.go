package webhookrelay

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/opsi-dev/opsi/cloud/internal/auth"
)

const (
	githubAuthorizeURL = "https://github.com/login/oauth/authorize"
	githubTokenURL     = "https://github.com/login/oauth/access_token"
	githubUserURL      = "https://api.github.com/user"
	githubProvider     = "github"
	githubAPIVersion   = "2022-11-28"

	githubUserAgent        = "opsi-cloud"
	githubResponseLimit    = 1 << 20
	githubTokenLengthLimit = 16 << 10
	oauthStateTTL          = 5 * time.Minute
	authGrantTTL           = 90 * time.Second
	secureTokenBytes       = 32
)

type oauthState struct {
	LocalCallback string
	LocalState    string
	ProjectID     string
	CodeVerifier  string
	ExpiresAt     time.Time
}

type authGrant struct {
	Token     string
	Session   auth.VerifyResult
	ExpiresAt time.Time
}

func newGitHubHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func (s *Server) handleBrowserAuthStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if s.Auth == nil || !s.githubUserAuthorizationEnabled() {
		s.auditAuth("", "", "", "login_started", "failure", map[string]any{
			"provider": githubProvider,
			"reason":   "auth_not_configured",
		})
		writeError(w, http.StatusServiceUnavailable, "GitHub user authorization is not configured")
		return
	}

	var request struct {
		LocalCallback string `json:"local_callback"`
		LocalState    string `json:"local_state"`
		ProjectID     string `json:"project_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, githubResponseLimit)).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid auth start request")
		return
	}
	if !localCallbackAllowed(request.LocalCallback) || request.LocalState == "" {
		writeError(w, http.StatusBadRequest, "invalid local callback")
		return
	}

	verifier, err := secureRandomValue(s.randomSource(), secureTokenBytes)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "auth state generation failed")
		return
	}
	state, err := secureRandomValue(s.randomSource(), secureTokenBytes)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "auth state generation failed")
		return
	}
	expiresAt := s.clock().Add(oauthStateTTL)
	s.authMu.Lock()
	s.oauthStates[state] = oauthState{
		LocalCallback: request.LocalCallback,
		LocalState:    request.LocalState,
		ProjectID:     request.ProjectID,
		CodeVerifier:  verifier,
		ExpiresAt:     expiresAt,
	}
	s.authMu.Unlock()

	authorizeURL, _ := url.Parse(githubAuthorizeURL)
	query := authorizeURL.Query()
	query.Set("response_type", "code")
	query.Set("client_id", s.Config.GitHubApp.ClientID)
	query.Set("redirect_uri", s.Config.GitHubApp.CallbackURL)
	query.Set("state", state)
	query.Set("code_challenge", pkceChallenge(verifier))
	query.Set("code_challenge_method", "S256")
	authorizeURL.RawQuery = query.Encode()

	s.auditAuth("", "", request.ProjectID, "login_started", "success", map[string]any{"provider": githubProvider})
	writeJSON(w, http.StatusOK, map[string]any{
		"auth_url":   authorizeURL.String(),
		"expires_at": expiresAt,
		"status":     "pending",
	})
}

func (s *Server) handleBrowserAuthCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	state := r.URL.Query().Get("state")
	if state == "" {
		writeError(w, http.StatusUnauthorized, "auth state expired or invalid")
		return
	}
	pending, ok := s.consumeOAuthState(state)
	if !ok || !s.clock().Before(pending.ExpiresAt) {
		writeError(w, http.StatusUnauthorized, "auth state expired or invalid")
		return
	}
	if r.URL.Query().Get("error") != "" || r.URL.Query().Get("error_description") != "" {
		s.auditAuth("", "", pending.ProjectID, "auth_failure", "failure", map[string]any{
			"provider": githubProvider,
			"reason":   "github_authorization_denied",
		})
		writeError(w, http.StatusUnauthorized, "GitHub authorization was denied")
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		writeError(w, http.StatusUnauthorized, "auth state expired or invalid")
		return
	}

	subject, err := s.exchangeGitHubUser(r.Context(), code, pending.CodeVerifier)
	if err != nil {
		s.auditAuth("", "", pending.ProjectID, "auth_failure", "failure", map[string]any{
			"provider": githubProvider,
			"reason":   err.Error(),
		})
		writeError(w, http.StatusUnauthorized, "GitHub login failed")
		return
	}
	if s.Auth == nil {
		writeError(w, http.StatusServiceUnavailable, "auth service is not configured")
		return
	}
	grantCode, err := secureRandomValue(s.randomSource(), secureTokenBytes)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "auth grant generation failed")
		return
	}
	issued, err := s.Auth.IssuePATForOAuth(r.Context(), githubProvider, subject, pending.ProjectID, 90*24*time.Hour)
	if err != nil {
		s.auditAuth("", "", pending.ProjectID, "token_issued", "failure", map[string]any{
			"provider": githubProvider,
			"reason":   "identity_or_membership_not_found",
		})
		writeError(w, http.StatusForbidden, "OAuth identity or project membership not found")
		return
	}

	s.authMu.Lock()
	s.authGrants[grantCode] = authGrant{
		Token:     issued.Token,
		Session:   issued.Session,
		ExpiresAt: s.clock().Add(authGrantTTL),
	}
	s.authMu.Unlock()
	s.auditAuth(issued.Session.OrgID, issued.Session.UserID, issued.Session.ProjectID, "token_issued", "success", map[string]any{"provider": githubProvider})

	callback, _ := url.Parse(pending.LocalCallback)
	query := callback.Query()
	query.Set("code", grantCode)
	query.Set("state", pending.LocalState)
	callback.RawQuery = query.Encode()
	http.Redirect(w, r, callback.String(), http.StatusFound)
}

func (s *Server) handleBrowserAuthRedeem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var request struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, githubResponseLimit)).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid auth redeem request")
		return
	}
	s.authMu.Lock()
	grant, ok := s.authGrants[request.Code]
	if ok {
		delete(s.authGrants, request.Code)
	}
	s.authMu.Unlock()
	if !ok || !s.clock().Before(grant.ExpiresAt) {
		writeError(w, http.StatusUnauthorized, "auth grant expired or invalid")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": grant.Token, "session": grant.Session})
}

func (s *Server) exchangeGitHubUser(ctx context.Context, code, verifier string) (string, error) {
	accessToken, err := s.exchangeGitHubToken(ctx, code, verifier)
	if err != nil {
		return "", err
	}
	return s.githubUserSubject(ctx, accessToken)
}

func (s *Server) exchangeGitHubToken(ctx context.Context, code, verifier string) (string, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", s.Config.GitHubApp.ClientID)
	form.Set("client_secret", s.Config.GitHubApp.ClientSecret)
	form.Set("code", code)
	form.Set("redirect_uri", s.Config.GitHubApp.CallbackURL)
	form.Set("code_verifier", verifier)

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, githubTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", errors.New("github token request creation failed")
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", githubUserAgent)
	response, err := s.githubHTTPClient().Do(request)
	if err != nil {
		return "", errors.New("github token request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("github token exchange status %d", response.StatusCode)
	}
	body, err := readBoundedResponse(response.Body)
	if err != nil {
		return "", errors.New("github token response exceeds limit")
	}
	var tokenResponse struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &tokenResponse); err != nil {
		return "", errors.New("github token response is invalid")
	}
	if tokenResponse.AccessToken == "" {
		return "", errors.New("github token response is missing access token")
	}
	if len(tokenResponse.AccessToken) > githubTokenLengthLimit || strings.IndexFunc(tokenResponse.AccessToken, unicode.IsControl) >= 0 {
		return "", errors.New("github token response contains invalid access token")
	}
	return tokenResponse.AccessToken, nil
}

func (s *Server) githubUserSubject(ctx context.Context, accessToken string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, githubUserURL, nil)
	if err != nil {
		return "", errors.New("github user request creation failed")
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	request.Header.Set("User-Agent", githubUserAgent)
	response, err := s.githubHTTPClient().Do(request)
	if err != nil {
		return "", errors.New("github user request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("github user request status %d", response.StatusCode)
	}
	body, err := readBoundedResponse(response.Body)
	if err != nil {
		return "", errors.New("github user response exceeds limit")
	}
	var user struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(body, &user); err != nil {
		return "", errors.New("github user response is invalid")
	}
	id, err := strconv.ParseInt(string(user.ID), 10, 64)
	if err != nil || id <= 0 {
		return "", errors.New("github user response has invalid numeric id")
	}
	return strconv.FormatInt(id, 10), nil
}

func (s *Server) consumeOAuthState(state string) (oauthState, bool) {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	pending, ok := s.oauthStates[state]
	if ok {
		delete(s.oauthStates, state)
	}
	return pending, ok
}

func (s *Server) githubUserAuthorizationEnabled() bool {
	config := s.Config.GitHubApp
	return config.ClientID != "" && config.ClientSecret != "" && config.CallbackURL != ""
}

func (s *Server) githubHTTPClient() *http.Client {
	if s.HTTPClient != nil {
		return s.HTTPClient
	}
	return newGitHubHTTPClient()
}

func (s *Server) randomSource() io.Reader {
	if s.random != nil {
		return s.random
	}
	return rand.Reader
}

func secureRandomValue(source io.Reader, size int) (string, error) {
	value := make([]byte, size)
	if _, err := io.ReadFull(source, value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func pkceChallenge(verifier string) string {
	digest := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func readBoundedResponse(body io.Reader) ([]byte, error) {
	value, err := io.ReadAll(io.LimitReader(body, githubResponseLimit+1))
	if err != nil {
		return nil, err
	}
	if len(value) > githubResponseLimit {
		return nil, errors.New("response exceeds limit")
	}
	return value, nil
}

func localCallbackAllowed(raw string) bool {
	callback, err := url.Parse(raw)
	if err != nil || !callback.IsAbs() || callback.Host == "" || callback.User != nil || callback.Fragment != "" {
		return false
	}
	host := strings.ToLower(callback.Hostname())
	return callback.Scheme == "http" && (host == "127.0.0.1" || host == "localhost")
}
