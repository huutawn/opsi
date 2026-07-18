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
	"github.com/opsi-dev/opsi/cloud/internal/registry"
)

const (
	githubAuthorizeURL         = "https://github.com/login/oauth/authorize"
	githubTokenURL             = "https://github.com/login/oauth/access_token"
	githubUserURL              = "https://api.github.com/user"
	githubUserInstallationsURL = "https://api.github.com/user/installations"
	githubProvider             = "github"
	githubAPIVersion           = "2022-11-28"

	githubUserAgent        = "opsi-cloud"
	githubResponseLimit    = 2 << 20
	githubTokenLengthLimit = 16 << 10
	oauthStateTTL          = 5 * time.Minute
	authGrantTTL           = 90 * time.Second
	secureTokenBytes       = 32
)

type oauthStatePurpose string

const (
	oauthPurposeLogin             oauthStatePurpose = "login"
	oauthPurposeInstallationClaim oauthStatePurpose = "installation_claim"
)

type oauthState struct {
	Purpose        oauthStatePurpose
	ActorUserID    string
	LocalCallback  string
	LocalState     string
	ProjectID      string
	InstallationID int64
	CodeVerifier   string
	ExpiresAt      time.Time
}

type authGrant struct {
	Purpose   oauthStatePurpose
	Token     string
	Session   auth.VerifyResult
	ExpiresAt time.Time
}

type installationClaimGrant struct {
	Purpose            oauthStatePurpose
	LocalState         string
	Installation       registry.GitHubInstallation
	ProjectLink        registry.GitHubInstallationProjectLink
	RepositoriesSynced int
	ExpiresAt          time.Time
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
		Purpose:       oauthPurposeLogin,
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
	if !ok {
		writeError(w, http.StatusUnauthorized, "auth state expired or invalid")
		return
	}
	if !s.clock().Before(pending.ExpiresAt) {
		if pending.Purpose == oauthPurposeLogin {
			redirectBrowserAuthError(w, r, pending, "AUTH_SESSION_EXPIRED")
			return
		}
		writeError(w, http.StatusUnauthorized, "auth state expired or invalid")
		return
	}
	if r.URL.Query().Get("error") != "" || r.URL.Query().Get("error_description") != "" {
		s.auditAuth("", "", pending.ProjectID, "auth_failure", "failure", map[string]any{
			"provider": githubProvider,
			"reason":   "github_authorization_denied",
		})
		if pending.Purpose == oauthPurposeLogin {
			redirectBrowserAuthError(w, r, pending, "GITHUB_AUTH_DENIED")
			return
		}
		writeError(w, http.StatusUnauthorized, "GitHub authorization was denied")
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		if pending.Purpose == oauthPurposeLogin {
			redirectBrowserAuthError(w, r, pending, "GITHUB_AUTH_FAILED")
			return
		}
		writeError(w, http.StatusUnauthorized, "auth state expired or invalid")
		return
	}
	switch pending.Purpose {
	case oauthPurposeLogin:
		s.completeBrowserLogin(w, r, pending, code)
	case oauthPurposeInstallationClaim:
		s.completeInstallationClaim(w, r, pending, code)
	default:
		writeError(w, http.StatusUnauthorized, "auth state purpose is invalid")
	}
}

func (s *Server) completeBrowserLogin(w http.ResponseWriter, r *http.Request, pending oauthState, code string) {
	subject, err := s.exchangeGitHubUser(r.Context(), code, pending.CodeVerifier)
	if err != nil {
		s.auditAuth("", "", pending.ProjectID, "auth_failure", "failure", map[string]any{
			"provider": githubProvider,
			"reason":   err.Error(),
		})
		redirectBrowserAuthError(w, r, pending, "GITHUB_AUTH_FAILED")
		return
	}
	if s.Auth == nil {
		redirectBrowserAuthError(w, r, pending, "AUTH_UNAVAILABLE")
		return
	}
	grantCode, err := secureRandomValue(s.randomSource(), secureTokenBytes)
	if err != nil {
		redirectBrowserAuthError(w, r, pending, "AUTH_UNAVAILABLE")
		return
	}
	issued, err := s.Auth.IssuePATForOAuth(r.Context(), githubProvider, subject, pending.ProjectID, 90*24*time.Hour)
	if err != nil {
		s.auditAuth("", "", pending.ProjectID, "token_issued", "failure", map[string]any{
			"provider": githubProvider,
			"reason":   "identity_or_membership_not_found",
		})
		switch {
		case errors.Is(err, auth.ErrOAuthIdentity):
			redirectBrowserAuthError(w, r, pending, "GITHUB_ACCOUNT_UNLINKED")
		case errors.Is(err, auth.ErrNoMembership):
			redirectBrowserAuthError(w, r, pending, "OPSI_MEMBERSHIP_REQUIRED")
		case errors.Is(err, auth.ErrProjectChoice):
			redirectBrowserAuthError(w, r, pending, "PROJECT_SELECTION_REQUIRED")
		default:
			redirectBrowserAuthError(w, r, pending, "AUTH_UNAVAILABLE")
		}
		return
	}

	s.authMu.Lock()
	s.authGrants[grantCode] = authGrant{
		Purpose:   oauthPurposeLogin,
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

func redirectBrowserAuthError(w http.ResponseWriter, r *http.Request, pending oauthState, code string) {
	callback, err := url.Parse(pending.LocalCallback)
	if err != nil || !localCallbackAllowed(callback.String()) {
		writeError(w, http.StatusUnauthorized, "browser authorization failed")
		return
	}
	query := callback.Query()
	query.Set("error", code)
	query.Set("state", pending.LocalState)
	callback.RawQuery = query.Encode()
	http.Redirect(w, r, callback.String(), http.StatusFound)
}

func (s *Server) handleInstallationClaimStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	projectID := r.PathValue("project_id")
	principal, ok := s.authorizeGitHubProject(w, r, projectID)
	if !ok {
		return
	}
	if !s.requireRole(w, r, principal, projectID, "github_installation", r.PathValue("installation_id"), "owner", "admin") {
		return
	}
	if !s.githubUserAuthorizationEnabled() {
		writeRegistryError(w, registry.APIError{Status: http.StatusServiceUnavailable, Code: "GITHUB_USER_AUTH_UNAVAILABLE", Message: "GitHub user authorization is not configured", RequestID: r.Header.Get("X-Request-ID")})
		return
	}
	installationID, err := strconv.ParseInt(r.PathValue("installation_id"), 10, 64)
	if err != nil || installationID <= 0 {
		writeRegistryError(w, registry.APIError{Status: http.StatusBadRequest, Code: "GITHUB_INSTALLATION_ID_INVALID", Message: "installation_id must be a positive integer", RequestID: r.Header.Get("X-Request-ID")})
		return
	}
	var request struct {
		LocalCallback string `json:"local_callback"`
		LocalState    string `json:"local_state"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, githubResponseLimit)).Decode(&request); err != nil {
		writeRegistryError(w, registry.APIError{Status: http.StatusBadRequest, Code: "INVALID_JSON", Message: "Request body is not valid JSON", RequestID: r.Header.Get("X-Request-ID")})
		return
	}
	if !localCallbackAllowed(request.LocalCallback) || request.LocalState == "" || len(request.LocalState) > 4096 || strings.IndexFunc(request.LocalState, unicode.IsControl) >= 0 {
		writeRegistryError(w, registry.APIError{Status: http.StatusBadRequest, Code: "GITHUB_CLAIM_CALLBACK_INVALID", Message: "local callback or state is invalid", RequestID: r.Header.Get("X-Request-ID")})
		return
	}
	verifier, err := secureRandomValue(s.randomSource(), secureTokenBytes)
	if err != nil {
		writeRegistryError(w, registry.APIError{Status: http.StatusServiceUnavailable, Code: "GITHUB_CLAIM_STATE_UNAVAILABLE", Message: "claim state generation failed"})
		return
	}
	state, err := secureRandomValue(s.randomSource(), secureTokenBytes)
	if err != nil {
		writeRegistryError(w, registry.APIError{Status: http.StatusServiceUnavailable, Code: "GITHUB_CLAIM_STATE_UNAVAILABLE", Message: "claim state generation failed"})
		return
	}
	expiresAt := s.clock().Add(oauthStateTTL)
	s.authMu.Lock()
	s.oauthStates[state] = oauthState{Purpose: oauthPurposeInstallationClaim, ActorUserID: principal.UserID, LocalCallback: request.LocalCallback, LocalState: request.LocalState, ProjectID: projectID, InstallationID: installationID, CodeVerifier: verifier, ExpiresAt: expiresAt}
	s.authMu.Unlock()
	authorizationURL, _ := url.Parse(githubAuthorizeURL)
	query := authorizationURL.Query()
	query.Set("response_type", "code")
	query.Set("client_id", s.Config.GitHubApp.ClientID)
	query.Set("redirect_uri", s.Config.GitHubApp.CallbackURL)
	query.Set("state", state)
	query.Set("code_challenge", pkceChallenge(verifier))
	query.Set("code_challenge_method", "S256")
	authorizationURL.RawQuery = query.Encode()
	writeJSON(w, http.StatusOK, map[string]any{"authorization_url": authorizationURL.String(), "expires_at": expiresAt})
}

func (s *Server) completeInstallationClaim(w http.ResponseWriter, r *http.Request, pending oauthState, code string) {
	if s.Auth == nil || s.Registry == nil {
		writeError(w, http.StatusServiceUnavailable, "installation claim service is unavailable")
		return
	}
	accessToken, err := s.exchangeGitHubToken(r.Context(), code, pending.CodeVerifier)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "GitHub installation verification failed")
		return
	}
	githubUserID, err := s.githubUserID(r.Context(), accessToken)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "GitHub installation verification failed")
		return
	}
	resolvedUserID, err := s.Auth.ResolveOAuthUser(r.Context(), githubProvider, strconv.FormatInt(githubUserID, 10))
	if err != nil || resolvedUserID != pending.ActorUserID {
		writeError(w, http.StatusForbidden, "GitHub identity does not match the authenticated Opsi user")
		return
	}
	installation, repositories, err := s.verifyGitHubInstallationAccess(r.Context(), accessToken, pending.InstallationID, githubUserID)
	if err != nil {
		if errors.Is(err, errGitHubInstallationAccessDenied) {
			writeError(w, http.StatusForbidden, "GitHub user cannot access the requested installation")
			return
		}
		writeError(w, http.StatusServiceUnavailable, "GitHub installation verification failed")
		return
	}
	installation, err = s.Registry.UpsertGitHubInstallation(installation)
	if err != nil {
		writeRegistryFailure(w, r, err)
		return
	}
	for _, repository := range repositories {
		if _, err := s.Registry.UpsertGitHubRepository(repository); err != nil {
			writeRegistryFailure(w, r, err)
			return
		}
	}
	link, err := s.Registry.ClaimGitHubInstallation(pending.ProjectID, pending.InstallationID, pending.ActorUserID)
	if err != nil {
		writeRegistryFailure(w, r, err)
		return
	}
	grantCode, err := secureRandomValue(s.randomSource(), secureTokenBytes)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "installation claim grant generation failed")
		return
	}
	s.authMu.Lock()
	s.installationClaimGrants[grantCode] = installationClaimGrant{Purpose: oauthPurposeInstallationClaim, LocalState: pending.LocalState, Installation: installation, ProjectLink: link, RepositoriesSynced: len(repositories), ExpiresAt: s.clock().Add(authGrantTTL)}
	s.authMu.Unlock()
	callback, _ := url.Parse(pending.LocalCallback)
	query := callback.Query()
	query.Set("grant", grantCode)
	query.Set("state", pending.LocalState)
	callback.RawQuery = query.Encode()
	http.Redirect(w, r, callback.String(), http.StatusFound)
}

func (s *Server) handleInstallationClaimRedeem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var request struct {
		Grant string `json:"grant"`
		State string `json:"state"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, githubResponseLimit)).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid installation claim redeem request")
		return
	}
	s.authMu.Lock()
	grant, ok := s.installationClaimGrants[request.Grant]
	if ok {
		delete(s.installationClaimGrants, request.Grant)
	}
	s.authMu.Unlock()
	if !ok || grant.Purpose != oauthPurposeInstallationClaim || request.State == "" || request.State != grant.LocalState || !s.clock().Before(grant.ExpiresAt) {
		writeError(w, http.StatusUnauthorized, "installation claim grant expired or invalid")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"installation": grant.Installation, "project_link": grant.ProjectLink, "repositories_synced": grant.RepositoriesSynced})
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
	if !ok || grant.Purpose != oauthPurposeLogin || !s.clock().Before(grant.ExpiresAt) {
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
	userID, err := s.githubUserID(ctx, accessToken)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(userID, 10), nil
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
	userID, err := s.githubUserID(ctx, accessToken)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(userID, 10), nil
}

func (s *Server) githubUserID(ctx context.Context, accessToken string) (int64, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, githubUserURL, nil)
	if err != nil {
		return 0, errors.New("github user request creation failed")
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	request.Header.Set("User-Agent", githubUserAgent)
	response, err := s.githubHTTPClient().Do(request)
	if err != nil {
		return 0, errors.New("github user request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return 0, fmt.Errorf("github user request status %d", response.StatusCode)
	}
	body, err := readBoundedResponse(response.Body)
	if err != nil {
		return 0, errors.New("github user response exceeds limit")
	}
	var user struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(body, &user); err != nil {
		return 0, errors.New("github user response is invalid")
	}
	id, err := strconv.ParseInt(string(user.ID), 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("github user response has invalid numeric id")
	}
	return id, nil
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
