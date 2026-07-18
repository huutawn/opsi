package webhookrelay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/auth"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
)

const (
	testGitHubClientID     = "github-client-id"
	testGitHubClientSecret = "github-client-secret"
	testGitHubCallbackURL  = "https://cloud.example.test/v1/auth/browser/callback"
	testGitHubAccessToken  = "github-user-access-token"
	testGitHubUserID       = "123456"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("random source unavailable")
}

type browserStartResponse struct {
	AuthURL   string    `json:"auth_url"`
	ExpiresAt time.Time `json:"expires_at"`
	Status    string    `json:"status"`
}

func configuredGitHubServer() *Server {
	server := NewServer(Config{GitHubApp: GitHubAppConfig{
		ClientID:     testGitHubClientID,
		ClientSecret: testGitHubClientSecret,
		CallbackURL:  testGitHubCallbackURL,
	}})
	server.Auth = &auth.Service{Store: &auth.MemoryStore{}}
	return server
}

func linkedGitHubServer(t *testing.T, transport http.RoundTripper) (*Server, string, *auth.MemoryStore) {
	t.Helper()
	server := configuredGitHubServer()
	project, err := server.Registry.CreateProject("org", "Demo", "demo", "user-1", "project-key")
	if err != nil {
		t.Fatal(err)
	}
	store := &auth.MemoryStore{
		Candidates: []auth.Candidate{{
			ID: "membership", UserID: "user-1", Email: "user@example.test", OrgID: "org", ProjectID: project.ID, Role: "Owner",
		}},
		OAuthIdentities: map[string]string{githubProvider + "\x00" + testGitHubUserID: "user-1"},
	}
	server.Auth = &auth.Service{Store: store}
	server.HTTPClient = newGitHubHTTPClient()
	server.HTTPClient.Transport = transport
	return server, project.ID, store
}

func startBrowserAuth(t *testing.T, server *Server, projectID string) (browserStartResponse, *url.URL, *httptest.ResponseRecorder) {
	t.Helper()
	body := `{"local_callback":"http://127.0.0.1:9780/api/local/session/callback","local_state":"local-state","project_id":"` + projectID + `"}`
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/browser/start", strings.NewReader(body))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("start status=%d body=%s", response.Code, response.Body.String())
	}
	var started browserStartResponse
	if err := json.NewDecoder(bytes.NewReader(response.Body.Bytes())).Decode(&started); err != nil {
		t.Fatal(err)
	}
	authorizeURL, err := url.Parse(started.AuthURL)
	if err != nil {
		t.Fatal(err)
	}
	return started, authorizeURL, response
}

func callbackRequest(server *Server, state, code string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/v1/auth/browser/callback?state="+url.QueryEscape(state)+"&code="+url.QueryEscape(code), nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	return response
}

func requireBrowserAuthErrorRedirect(t *testing.T, response *httptest.ResponseRecorder, code string) {
	t.Helper()
	if response.Code != http.StatusFound {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	location, err := url.Parse(response.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if location.Hostname() != "127.0.0.1" || location.Query().Get("error") != code || location.Query().Get("state") != "local-state" {
		t.Fatalf("unexpected error redirect %s", location.Redacted())
	}
}

func githubJSONResponse(request *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}
}

func successfulGitHubTransport(t *testing.T) http.RoundTripper {
	t.Helper()
	return roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.String() {
		case githubTokenURL:
			return githubJSONResponse(request, http.StatusOK, `{"access_token":"`+testGitHubAccessToken+`"}`), nil
		case githubUserURL:
			if request.Header.Get("Authorization") != "Bearer "+testGitHubAccessToken {
				t.Fatalf("GitHub user authorization=%q", request.Header.Get("Authorization"))
			}
			return githubJSONResponse(request, http.StatusOK, `{"id":`+testGitHubUserID+`}`), nil
		default:
			t.Fatalf("unexpected GitHub URL %s", request.URL)
			return nil, nil
		}
	})
}

func TestBrowserAuthStartReturnsUnavailableWhenGitHubAuthDisabled(t *testing.T) {
	server := NewServer(Config{})
	server.Auth = &auth.Service{Store: &auth.MemoryStore{}}
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/browser/start", strings.NewReader(`{"local_callback":"http://127.0.0.1:9780/api/local/session/callback","local_state":"state"}`))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestBrowserAuthStartUsesFixedGitHubAuthorizationAndPKCE(t *testing.T) {
	server := configuredGitHubServer()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	server.now = func() time.Time { return now }
	first, firstURL, firstResponse := startBrowserAuth(t, server, "project-1")
	second, secondURL, _ := startBrowserAuth(t, server, "project-1")

	if firstURL.Scheme+"://"+firstURL.Host+firstURL.Path != githubAuthorizeURL {
		t.Fatalf("authorization URL=%s", firstURL)
	}
	query := firstURL.Query()
	if query.Get("client_id") != testGitHubClientID || query.Get("redirect_uri") != testGitHubCallbackURL || query.Get("state") == "" {
		t.Fatalf("authorization query=%v", query)
	}
	if query.Get("code_challenge_method") != "S256" || query.Get("code_challenge") == "" {
		t.Fatalf("PKCE query=%v", query)
	}
	for _, forbidden := range []string{"scope", "provider", "auth_url", "token_url", "userinfo_url"} {
		if query.Has(forbidden) {
			t.Fatalf("authorization URL contains %s: %s", forbidden, firstURL)
		}
	}
	if strings.Contains(first.AuthURL, testGitHubClientSecret) || strings.Contains(firstResponse.Body.String(), testGitHubClientSecret) {
		t.Fatal("authorization response leaked client secret")
	}
	if !first.ExpiresAt.Equal(now.Add(oauthStateTTL)) {
		t.Fatalf("expires_at=%s", first.ExpiresAt)
	}

	server.authMu.Lock()
	firstPending := server.oauthStates[query.Get("state")]
	secondPending := server.oauthStates[secondURL.Query().Get("state")]
	server.authMu.Unlock()
	pkceVerifier := regexp.MustCompile(`^[A-Za-z0-9_-]{43,128}$`)
	if !pkceVerifier.MatchString(firstPending.CodeVerifier) || pkceChallenge(firstPending.CodeVerifier) != query.Get("code_challenge") {
		t.Fatalf("invalid verifier/challenge pair")
	}
	if strings.Contains(firstResponse.Body.String(), firstPending.CodeVerifier) {
		t.Fatal("authorization response leaked code verifier")
	}
	if query.Get("state") == secondURL.Query().Get("state") || firstPending.CodeVerifier == secondPending.CodeVerifier ||
		query.Get("code_challenge") == secondURL.Query().Get("code_challenge") {
		t.Fatal("separate auth starts reused state or PKCE material")
	}
	if first.Status != "pending" || second.Status != "pending" {
		t.Fatalf("unexpected start statuses: %q %q", first.Status, second.Status)
	}
}

func TestBrowserAuthStateRejectsExpiredUnknownReusedAndMissingCode(t *testing.T) {
	t.Run("unknown", func(t *testing.T) {
		server := configuredGitHubServer()
		response := callbackRequest(server, "unknown", "code")
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d", response.Code)
		}
	})

	t.Run("expired", func(t *testing.T) {
		server := configuredGitHubServer()
		now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
		server.now = func() time.Time { return now }
		_, authorizeURL, _ := startBrowserAuth(t, server, "project")
		now = now.Add(oauthStateTTL)
		response := callbackRequest(server, authorizeURL.Query().Get("state"), "code")
		requireBrowserAuthErrorRedirect(t, response, "AUTH_SESSION_EXPIRED")
	})

	t.Run("exchange failure consumes state", func(t *testing.T) {
		var calls atomic.Int32
		server := configuredGitHubServer()
		server.HTTPClient = newGitHubHTTPClient()
		server.HTTPClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
			calls.Add(1)
			return githubJSONResponse(request, http.StatusBadGateway, `provider-secret-body`), nil
		})
		_, authorizeURL, _ := startBrowserAuth(t, server, "project")
		state := authorizeURL.Query().Get("state")
		first := callbackRequest(server, state, "code")
		second := callbackRequest(server, state, "code")
		if second.Code != http.StatusUnauthorized || calls.Load() != 1 {
			t.Fatalf("first=%d second=%d exchange calls=%d", first.Code, second.Code, calls.Load())
		}
		requireBrowserAuthErrorRedirect(t, first, "GITHUB_AUTH_FAILED")
		if strings.Contains(first.Body.String(), "provider-secret-body") {
			t.Fatalf("provider response leaked: %s", first.Body.String())
		}
	})

	t.Run("missing code consumes state", func(t *testing.T) {
		server := configuredGitHubServer()
		_, authorizeURL, _ := startBrowserAuth(t, server, "project")
		state := authorizeURL.Query().Get("state")
		response := callbackRequest(server, state, "")
		requireBrowserAuthErrorRedirect(t, response, "GITHUB_AUTH_FAILED")
		if reused := callbackRequest(server, state, "code"); reused.Code != http.StatusUnauthorized {
			t.Fatalf("reused state status=%d", reused.Code)
		}
	})
}

func TestBrowserAuthGitHubDenialConsumesStateWithoutExchange(t *testing.T) {
	var calls atomic.Int32
	server := configuredGitHubServer()
	server.HTTPClient = newGitHubHTTPClient()
	server.HTTPClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("must not exchange")
	})
	_, authorizeURL, _ := startBrowserAuth(t, server, "project")
	state := authorizeURL.Query().Get("state")
	request := httptest.NewRequest(http.MethodGet, "/v1/auth/browser/callback?state="+url.QueryEscape(state)+"&error=access_denied&error_description="+url.QueryEscape("arbitrary secret description"), nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if calls.Load() != 0 {
		t.Fatalf("status=%d exchange calls=%d", response.Code, calls.Load())
	}
	requireBrowserAuthErrorRedirect(t, response, "GITHUB_AUTH_DENIED")
	if strings.Contains(response.Body.String(), "arbitrary secret description") {
		t.Fatalf("denial description reflected: %s", response.Body.String())
	}
	if reused := callbackRequest(server, state, "code"); reused.Code != http.StatusUnauthorized {
		t.Fatalf("denied state was reusable: %d", reused.Code)
	}
}

func TestBrowserAuthRandomFailureFailsClosed(t *testing.T) {
	server := configuredGitHubServer()
	server.random = failingReader{}
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/browser/start", strings.NewReader(`{"local_callback":"http://127.0.0.1:9780/api/local/session/callback","local_state":"state"}`))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if len(server.oauthStates) != 0 {
		t.Fatal("random failure left pending state")
	}
}

func TestGitHubTokenAndUserRequestsUseFixedEndpointsAndHeaders(t *testing.T) {
	var tokenForm url.Values
	var verifier string
	var tokenRequests, userRequests atomic.Int32
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.String() {
		case githubTokenURL:
			tokenRequests.Add(1)
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatal(err)
			}
			tokenForm, err = url.ParseQuery(string(body))
			if err != nil {
				t.Fatal(err)
			}
			if request.Method != http.MethodPost || request.Header.Get("Accept") != "application/json" ||
				request.Header.Get("Content-Type") != "application/x-www-form-urlencoded" || request.Header.Get("User-Agent") != githubUserAgent {
				t.Fatalf("token request method/headers: %s %v", request.Method, request.Header)
			}
			return githubJSONResponse(request, http.StatusOK, `{"access_token":"`+testGitHubAccessToken+`"}`), nil
		case githubUserURL:
			userRequests.Add(1)
			if request.Method != http.MethodGet || request.Header.Get("Authorization") != "Bearer "+testGitHubAccessToken ||
				request.Header.Get("Accept") != "application/vnd.github+json" ||
				request.Header.Get("X-GitHub-Api-Version") != githubAPIVersion || request.Header.Get("User-Agent") != githubUserAgent {
				t.Fatalf("user request method/headers: %s %v", request.Method, request.Header)
			}
			return githubJSONResponse(request, http.StatusOK, `{"id":123456,"login":"ignored","email":"ignored@example.test"}`), nil
		default:
			t.Fatalf("unexpected URL %s", request.URL)
			return nil, nil
		}
	})
	server, projectID, _ := linkedGitHubServer(t, transport)
	_, authorizeURL, startResponse := startBrowserAuth(t, server, projectID)
	state := authorizeURL.Query().Get("state")
	server.authMu.Lock()
	verifier = server.oauthStates[state].CodeVerifier
	server.authMu.Unlock()
	callback := callbackRequest(server, state, "github-code")
	if callback.Code != http.StatusFound {
		t.Fatalf("callback status=%d body=%s", callback.Code, callback.Body.String())
	}
	if tokenRequests.Load() != 1 || userRequests.Load() != 1 {
		t.Fatalf("token requests=%d user requests=%d", tokenRequests.Load(), userRequests.Load())
	}
	if tokenForm.Get("client_id") != testGitHubClientID || tokenForm.Get("client_secret") != testGitHubClientSecret ||
		tokenForm.Get("code") != "github-code" || tokenForm.Get("redirect_uri") != testGitHubCallbackURL || tokenForm.Get("code_verifier") != verifier {
		t.Fatalf("token form=%v", tokenForm)
	}
	if strings.Contains(startResponse.Body.String(), verifier) || strings.Contains(callback.Body.String(), verifier) ||
		strings.Contains(callback.Header().Get("Location"), verifier) {
		t.Fatal("code verifier leaked to client")
	}
	if strings.Contains(authorizeURL.String(), testGitHubClientSecret) || strings.Contains(callback.Body.String(), testGitHubClientSecret) {
		t.Fatal("client secret appeared outside token request")
	}
}

func TestGitHubTokenResponseValidation(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		body      string
		forbidden string
	}{
		{name: "non-2xx", status: http.StatusBadGateway, body: `provider-sensitive-body`, forbidden: "provider-sensitive-body"},
		{name: "invalid JSON", status: http.StatusOK, body: `{`},
		{name: "missing token", status: http.StatusOK, body: `{}`},
		{name: "oversized", status: http.StatusOK, body: strings.Repeat("x", githubResponseLimit+1)},
		{name: "token too long", status: http.StatusOK, body: `{"access_token":"` + strings.Repeat("t", githubTokenLengthLimit+1) + `"}`},
		{name: "token control", status: http.StatusOK, body: `{"access_token":"token\nvalue"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := configuredGitHubServer()
			server.HTTPClient = newGitHubHTTPClient()
			server.HTTPClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.URL.String() != githubTokenURL {
					t.Fatalf("token URL=%s", request.URL)
				}
				return githubJSONResponse(request, test.status, test.body), nil
			})
			_, err := server.exchangeGitHubToken(t.Context(), "code", "verifier")
			if err == nil {
				t.Fatal("invalid token response was accepted")
			}
			if test.forbidden != "" && strings.Contains(err.Error(), test.forbidden) {
				t.Fatalf("token error leaked response body: %q", err)
			}
			if strings.Contains(err.Error(), testGitHubClientSecret) {
				t.Fatalf("token error leaked client secret: %q", err)
			}
		})
	}
}

func TestGitHubTokenRedirectIsNotFollowed(t *testing.T) {
	var calls atomic.Int32
	server := configuredGitHubServer()
	server.HTTPClient = newGitHubHTTPClient()
	server.HTTPClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		response := githubJSONResponse(request, http.StatusFound, `redirect body`)
		response.Header.Set("Location", githubUserURL)
		return response, nil
	})
	_, err := server.exchangeGitHubToken(t.Context(), "code", "verifier")
	if err == nil || calls.Load() != 1 {
		t.Fatalf("redirect error=%v calls=%d", err, calls.Load())
	}
}

func TestGitHubUserIDValidationAndCanonicalization(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "numeric", body: `{"id":123456}`, want: "123456"},
		{name: "string", body: `{"id":"123456"}`},
		{name: "sub", body: `{"sub":123456}`},
		{name: "login and email", body: `{"login":"huutawn","email":"user@example.test"}`},
		{name: "zero", body: `{"id":0}`},
		{name: "negative", body: `{"id":-1}`},
		{name: "decimal", body: `{"id":1.5}`},
		{name: "scientific", body: `{"id":1e6}`},
		{name: "overflow", body: `{"id":9223372036854775808}`},
		{name: "invalid JSON", body: `{`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := configuredGitHubServer()
			server.HTTPClient = newGitHubHTTPClient()
			server.HTTPClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.URL.String() != githubUserURL {
					t.Fatalf("user URL=%s", request.URL)
				}
				return githubJSONResponse(request, http.StatusOK, test.body), nil
			})
			got, err := server.githubUserSubject(t.Context(), "access-token")
			if test.want == "" {
				if err == nil {
					t.Fatalf("invalid user body accepted with subject %q", got)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("subject=%q error=%v", got, err)
			}
		})
	}
}

func TestBrowserAuthUsesGitHubProviderAndRequiresPrelinkedIdentity(t *testing.T) {
	t.Run("fixed provider", func(t *testing.T) {
		server, projectID, _ := linkedGitHubServer(t, successfulGitHubTransport(t))
		_, authorizeURL, _ := startBrowserAuth(t, server, projectID)
		response := callbackRequest(server, authorizeURL.Query().Get("state"), "code")
		if response.Code != http.StatusFound {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})

	t.Run("unlinked", func(t *testing.T) {
		server, projectID, store := linkedGitHubServer(t, successfulGitHubTransport(t))
		store.OAuthIdentities = map[string]string{"generic\x00" + testGitHubUserID: "user-1"}
		candidateCount := len(store.Candidates)
		_, authorizeURL, _ := startBrowserAuth(t, server, projectID)
		response := callbackRequest(server, authorizeURL.Query().Get("state"), "code")
		requireBrowserAuthErrorRedirect(t, response, "GITHUB_ACCOUNT_UNLINKED")
		if len(store.Candidates) != candidateCount || len(store.OAuthIdentities) != 1 {
			t.Fatal("unlinked login mutated users or OAuth identities")
		}
	})
}

func TestBrowserAuthGrantIsOneTimeExpiresAndDoesNotPersistGitHubToken(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	server, projectID, _ := linkedGitHubServer(t, successfulGitHubTransport(t))
	server.now = func() time.Time { return now }
	_, authorizeURL, startResponse := startBrowserAuth(t, server, projectID)
	state := authorizeURL.Query().Get("state")
	server.authMu.Lock()
	verifier := server.oauthStates[state].CodeVerifier
	server.authMu.Unlock()
	callback := callbackRequest(server, state, "code")
	if callback.Code != http.StatusFound {
		t.Fatalf("callback status=%d body=%s", callback.Code, callback.Body.String())
	}
	location, err := url.Parse(callback.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	grantCode := location.Query().Get("code")
	if grantCode == "" {
		t.Fatal("callback did not return local grant")
	}
	server.authMu.Lock()
	if len(server.oauthStates) != 0 || len(server.authGrants) != 1 {
		t.Fatalf("OAuth states=%d grants=%d", len(server.oauthStates), len(server.authGrants))
	}
	grant := server.authGrants[grantCode]
	server.authMu.Unlock()
	if grant.Token == testGitHubAccessToken || !grant.ExpiresAt.Equal(now.Add(authGrantTTL)) {
		t.Fatalf("grant retained GitHub token or wrong expiry: %#v", grant)
	}

	for _, value := range []string{startResponse.Body.String(), callback.Body.String(), callback.Header().Get("Location")} {
		if strings.Contains(value, testGitHubAccessToken) || strings.Contains(value, testGitHubClientSecret) || strings.Contains(value, verifier) {
			t.Fatalf("browser response leaked secret material: %s", value)
		}
	}

	redeem := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/v1/auth/browser/redeem", strings.NewReader(`{"code":"`+grantCode+`"}`))
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		return response
	}
	first := redeem()
	second := redeem()
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), "opsi_pat_") || strings.Contains(first.Body.String(), testGitHubAccessToken) {
		t.Fatalf("first redeem status=%d body=%s", first.Code, first.Body.String())
	}
	if second.Code != http.StatusUnauthorized {
		t.Fatalf("second redeem status=%d body=%s", second.Code, second.Body.String())
	}

	events, err := server.Registry.ListAudit(projectID)
	if err != nil {
		t.Fatal(err)
	}
	auditJSON, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{testGitHubAccessToken, testGitHubClientSecret, verifier, grantCode, "generic"} {
		if bytes.Contains(auditJSON, []byte(forbidden)) {
			t.Fatalf("audit leaked %q: %s", forbidden, auditJSON)
		}
	}
	if !bytes.Contains(auditJSON, []byte(githubProvider)) {
		t.Fatalf("audit is missing fixed provider: %s", auditJSON)
	}
}

func TestBrowserAuthGrantExpires(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	server, projectID, _ := linkedGitHubServer(t, successfulGitHubTransport(t))
	server.now = func() time.Time { return now }
	_, authorizeURL, _ := startBrowserAuth(t, server, projectID)
	callback := callbackRequest(server, authorizeURL.Query().Get("state"), "code")
	location, err := url.Parse(callback.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(authGrantTTL)
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/browser/redeem", strings.NewReader(`{"code":"`+location.Query().Get("code")+`"}`))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expired grant status=%d body=%s", response.Code, response.Body.String())
	}
}

type installationClaimStartResponse struct {
	AuthorizationURL string    `json:"authorization_url"`
	ExpiresAt        time.Time `json:"expires_at"`
}

func TestInstallationClaimStartRequiresPATRoleAndBindsPurpose(t *testing.T) {
	server, projectID, token, store := installationClaimServer(t, "owner", successfulInstallationClaimTransport(t, "Organization", 222, []int64{333}))
	path := "/v1/projects/" + projectID + "/github/installations/101/claim/start"
	body := `{"local_callback":"http://127.0.0.1:49152/callback","local_state":"opaque-cli-state"}`

	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("missing PAT status=%d body=%s", response.Code, response.Body.String())
	}

	store.Candidates[0].Role = "viewer"
	request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("viewer status=%d body=%s", response.Code, response.Body.String())
	}

	store.Candidates[0].Role = "owner"
	request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("owner status=%d body=%s", response.Code, response.Body.String())
	}
	var started installationClaimStartResponse
	if err := json.Unmarshal(response.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	authorizationURL, err := url.Parse(started.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	state := authorizationURL.Query().Get("state")
	server.authMu.Lock()
	pending := server.oauthStates[state]
	server.authMu.Unlock()
	if pending.Purpose != oauthPurposeInstallationClaim || pending.ActorUserID != "user-1" || pending.ProjectID != projectID || pending.InstallationID != 101 || pending.LocalState != "opaque-cli-state" || pending.CodeVerifier == "" {
		t.Fatalf("pending=%+v", pending)
	}
}

func TestInstallationClaimCallbackVerifiesIdentitySyncsRepositoriesAndUsesOneTimeGrant(t *testing.T) {
	server, projectID, token, store := installationClaimServer(t, "owner", successfulInstallationClaimTransport(t, "Organization", 222, []int64{333}))
	oldInstallation := registry.GitHubInstallation{InstallationID: 101, AccountID: 222, AccountLogin: "old", AccountType: "Organization", Status: registry.GitHubInstallationActive}
	if _, err := server.Registry.UpsertGitHubInstallation(oldInstallation); err != nil {
		t.Fatal(err)
	}
	oldRepository := registry.GitHubRepository{RepositoryID: 999, InstallationID: 101, OwnerID: 222, OwnerLogin: "example", Name: "old", FullName: "example/old", DefaultBranch: "main", Status: registry.GitHubRepositoryActive}
	if _, err := server.Registry.UpsertGitHubRepository(oldRepository); err != nil {
		t.Fatal(err)
	}
	started := startInstallationClaim(t, server, projectID, token, 101)
	authorizationURL, _ := url.Parse(started.AuthorizationURL)
	callback := callbackRequest(server, authorizationURL.Query().Get("state"), "claim-code")
	if callback.Code != http.StatusFound {
		t.Fatalf("callback status=%d body=%s", callback.Code, callback.Body.String())
	}
	location, err := url.Parse(callback.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	grant := location.Query().Get("grant")
	if grant == "" || location.Query().Get("state") != "opaque-cli-state" || strings.Contains(location.RawQuery, testGitHubAccessToken) {
		t.Fatalf("redirect=%s", location.String())
	}
	if len(store.Candidates) != 1 {
		t.Fatalf("claim flow issued PAT candidates: %+v", store.Candidates)
	}

	loginRedeem := httptest.NewRequest(http.MethodPost, "/v1/auth/browser/redeem", strings.NewReader(`{"code":"`+grant+`"}`))
	loginResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(loginResponse, loginRedeem)
	if loginResponse.Code != http.StatusUnauthorized {
		t.Fatalf("claim grant crossed into login redeem: status=%d", loginResponse.Code)
	}

	redeemBody := `{"grant":"` + grant + `","state":"opaque-cli-state"}`
	redeem := httptest.NewRequest(http.MethodPost, "/v1/github/installations/claim/redeem", strings.NewReader(redeemBody))
	redeemResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(redeemResponse, redeem)
	if redeemResponse.Code != http.StatusOK || strings.Contains(redeemResponse.Body.String(), testGitHubAccessToken) || strings.Contains(redeemResponse.Body.String(), "opsi_pat_") {
		t.Fatalf("redeem status=%d body=%s", redeemResponse.Code, redeemResponse.Body.String())
	}
	var result struct {
		Installation       registry.GitHubInstallation            `json:"installation"`
		ProjectLink        registry.GitHubInstallationProjectLink `json:"project_link"`
		RepositoriesSynced int                                    `json:"repositories_synced"`
	}
	if err := json.Unmarshal(redeemResponse.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Installation.InstallationID != 101 || result.ProjectLink.ProjectID != projectID || result.RepositoriesSynced != 1 {
		t.Fatalf("result=%+v", result)
	}
	redeem = httptest.NewRequest(http.MethodPost, "/v1/github/installations/claim/redeem", strings.NewReader(redeemBody))
	redeemResponse = httptest.NewRecorder()
	server.Handler().ServeHTTP(redeemResponse, redeem)
	if redeemResponse.Code != http.StatusUnauthorized {
		t.Fatalf("grant reused status=%d", redeemResponse.Code)
	}
	repositories, err := server.Registry.ListGitHubRepositories(projectID)
	if err != nil || len(repositories) != 2 {
		t.Fatalf("repositories=%+v err=%v", repositories, err)
	}
}

func TestInstallationClaimGrantExpiresAndLoginGrantCannotCrossRedeem(t *testing.T) {
	server, projectID, token, _ := installationClaimServer(t, "owner", successfulInstallationClaimTransport(t, "Organization", 222, nil))
	started := startInstallationClaim(t, server, projectID, token, 101)
	authorizationURL, _ := url.Parse(started.AuthorizationURL)
	callback := callbackRequest(server, authorizationURL.Query().Get("state"), "claim-code")
	location, _ := url.Parse(callback.Header().Get("Location"))
	grantCode := location.Query().Get("grant")
	server.authMu.Lock()
	grant := server.installationClaimGrants[grantCode]
	grant.ExpiresAt = server.clock().Add(-time.Second)
	server.installationClaimGrants[grantCode] = grant
	server.authMu.Unlock()
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/github/installations/claim/redeem", strings.NewReader(`{"grant":"`+grantCode+`","state":"opaque-cli-state"}`)))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expired claim grant status=%d body=%s", response.Code, response.Body.String())
	}

	loginServer, loginProjectID, _ := linkedGitHubServer(t, successfulGitHubTransport(t))
	_, loginAuthorizationURL, _ := startBrowserAuth(t, loginServer, loginProjectID)
	loginCallback := callbackRequest(loginServer, loginAuthorizationURL.Query().Get("state"), "login-code")
	loginLocation, _ := url.Parse(loginCallback.Header().Get("Location"))
	loginCode := loginLocation.Query().Get("code")
	response = httptest.NewRecorder()
	loginServer.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/github/installations/claim/redeem", strings.NewReader(`{"grant":"`+loginCode+`","state":"local-state"}`)))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("login grant crossed into claim redeem: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestInstallationClaimCallbackRejectsIdentityAndInstallationProofFailures(t *testing.T) {
	t.Run("Opsi identity mismatch", func(t *testing.T) {
		server, projectID, token, store := installationClaimServer(t, "owner", successfulInstallationClaimTransport(t, "Organization", 222, nil))
		store.OAuthIdentities[githubProvider+"\x00"+testGitHubUserID] = "another-user"
		response := completeInstallationClaimRequest(t, server, projectID, token)
		if response.Code != http.StatusForbidden {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})
	t.Run("installation absent", func(t *testing.T) {
		transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
			switch request.URL.Path {
			case "/login/oauth/access_token":
				return githubJSONResponse(request, http.StatusOK, `{"access_token":"`+testGitHubAccessToken+`"}`), nil
			case "/user":
				return githubJSONResponse(request, http.StatusOK, `{"id":`+testGitHubUserID+`}`), nil
			case "/user/installations":
				return githubJSONResponse(request, http.StatusOK, `{"total_count":0,"installations":[]}`), nil
			default:
				t.Fatalf("unexpected URL %s", request.URL)
				return nil, nil
			}
		})
		server, projectID, token, _ := installationClaimServer(t, "owner", transport)
		response := completeInstallationClaimRequest(t, server, projectID, token)
		if response.Code != http.StatusForbidden {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})
	t.Run("user installation account mismatch", func(t *testing.T) {
		server, projectID, token, _ := installationClaimServer(t, "owner", successfulInstallationClaimTransport(t, "User", 999999, nil))
		response := completeInstallationClaimRequest(t, server, projectID, token)
		if response.Code != http.StatusForbidden {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})
}

func TestInstallationClaimPaginationAndFailureSanitization(t *testing.T) {
	t.Run("target on second page", func(t *testing.T) {
		server := configuredGitHubServer()
		server.HTTPClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
			page, _ := strconv.Atoi(request.URL.Query().Get("page"))
			installations := make([]map[string]any, 0, 100)
			if page == 1 {
				for id := int64(1); id <= 100; id++ {
					installations = append(installations, map[string]any{"id": id, "account": map[string]any{"id": id + 1000, "login": "owner", "type": "Organization"}})
				}
			} else {
				installations = append(installations, map[string]any{"id": int64(101), "account": map[string]any{"id": int64(222), "login": "example", "type": "Organization"}})
			}
			body, _ := json.Marshal(map[string]any{"total_count": 101, "installations": installations})
			return githubJSONResponse(request, http.StatusOK, string(body)), nil
		})
		installation, err := server.findGitHubUserInstallation(context.Background(), testGitHubAccessToken, 101)
		if err != nil || installation.ID != 101 {
			t.Fatalf("installation=%+v err=%v", installation, err)
		}
	})
	t.Run("over twenty pages fails closed", func(t *testing.T) {
		server := configuredGitHubServer()
		server.HTTPClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
			page, _ := strconv.Atoi(request.URL.Query().Get("page"))
			installations := make([]map[string]any, 0, 100)
			for offset := 0; offset < 100; offset++ {
				id := int64((page-1)*100 + offset + 1)
				installations = append(installations, map[string]any{"id": id, "account": map[string]any{"id": id + 3000, "login": "owner", "type": "Organization"}})
			}
			body, _ := json.Marshal(map[string]any{"total_count": 2001, "installations": installations})
			return githubJSONResponse(request, http.StatusOK, string(body)), nil
		})
		if _, err := server.findGitHubUserInstallation(context.Background(), testGitHubAccessToken, 2001); err == nil {
			t.Fatal("pagination beyond twenty pages succeeded")
		}
	})
	t.Run("GitHub body and token are not reflected", func(t *testing.T) {
		secretBody := `{"secret":"remote-sensitive-body"}`
		transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
			switch request.URL.Path {
			case "/login/oauth/access_token":
				return githubJSONResponse(request, http.StatusOK, `{"access_token":"`+testGitHubAccessToken+`"}`), nil
			case "/user":
				return githubJSONResponse(request, http.StatusOK, `{"id":`+testGitHubUserID+`}`), nil
			default:
				return githubJSONResponse(request, http.StatusInternalServerError, secretBody), nil
			}
		})
		server, projectID, token, _ := installationClaimServer(t, "owner", transport)
		response := completeInstallationClaimRequest(t, server, projectID, token)
		if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "remote-sensitive-body") || strings.Contains(response.Body.String(), testGitHubAccessToken) {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})
}

func installationClaimServer(t *testing.T, role string, transport http.RoundTripper) (*Server, string, string, *auth.MemoryStore) {
	t.Helper()
	server := configuredGitHubServer()
	project, err := server.Registry.CreateProject("org", "Demo", "demo", "user-1", "project-key")
	if err != nil {
		t.Fatal(err)
	}
	token := "claim-pat"
	hash, err := auth.HashPAT(token)
	if err != nil {
		t.Fatal(err)
	}
	store := &auth.MemoryStore{
		Candidates:      []auth.Candidate{{ID: "pat-1", UserID: "user-1", OrgID: "org", ProjectID: project.ID, Role: role, Hash: hash, ExpiresAt: time.Now().Add(time.Hour)}},
		OAuthIdentities: map[string]string{githubProvider + "\x00" + testGitHubUserID: "user-1"},
	}
	server.Auth = &auth.Service{Store: store}
	server.HTTPClient = newGitHubHTTPClient()
	server.HTTPClient.Transport = transport
	return server, project.ID, token, store
}

func successfulInstallationClaimTransport(t *testing.T, accountType string, accountID int64, repositoryIDs []int64) http.RoundTripper {
	t.Helper()
	return roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/login/oauth/access_token" && request.Header.Get("Authorization") != "Bearer "+testGitHubAccessToken {
			t.Fatalf("authorization=%q for %s", request.Header.Get("Authorization"), request.URL)
		}
		switch request.URL.Path {
		case "/login/oauth/access_token":
			return githubJSONResponse(request, http.StatusOK, `{"access_token":"`+testGitHubAccessToken+`"}`), nil
		case "/user":
			return githubJSONResponse(request, http.StatusOK, `{"id":`+testGitHubUserID+`}`), nil
		case "/user/installations":
			body, _ := json.Marshal(map[string]any{"total_count": 1, "installations": []map[string]any{{"id": int64(101), "account": map[string]any{"id": accountID, "login": "example", "type": accountType}}}})
			return githubJSONResponse(request, http.StatusOK, string(body)), nil
		case "/user/installations/101/repositories":
			repositories := make([]map[string]any, 0, len(repositoryIDs))
			for _, repositoryID := range repositoryIDs {
				repositories = append(repositories, map[string]any{"id": repositoryID, "node_id": "R_1", "name": "repo-" + strconv.FormatInt(repositoryID, 10), "full_name": "example/repo-" + strconv.FormatInt(repositoryID, 10), "private": true, "archived": false, "disabled": false, "default_branch": "main", "owner": map[string]any{"id": accountID, "login": "example"}})
			}
			body, _ := json.Marshal(map[string]any{"total_count": len(repositories), "repositories": repositories})
			return githubJSONResponse(request, http.StatusOK, string(body)), nil
		default:
			t.Fatalf("unexpected GitHub URL %s", request.URL)
			return nil, nil
		}
	})
}

func startInstallationClaim(t *testing.T, server *Server, projectID, token string, installationID int64) installationClaimStartResponse {
	t.Helper()
	path := "/v1/projects/" + projectID + "/github/installations/" + strconv.FormatInt(installationID, 10) + "/claim/start"
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"local_callback":"http://127.0.0.1:49152/callback","local_state":"opaque-cli-state"}`))
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("claim start status=%d body=%s", response.Code, response.Body.String())
	}
	var started installationClaimStartResponse
	if err := json.Unmarshal(response.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	return started
}

func completeInstallationClaimRequest(t *testing.T, server *Server, projectID, token string) *httptest.ResponseRecorder {
	t.Helper()
	started := startInstallationClaim(t, server, projectID, token, 101)
	authorizationURL, err := url.Parse(started.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	return callbackRequest(server, authorizationURL.Query().Get("state"), "claim-code")
}

func TestDefaultGitHubHTTPClientHasTimeoutAndRejectsRedirects(t *testing.T) {
	client := newGitHubHTTPClient()
	if client.Timeout <= 0 || client.CheckRedirect == nil {
		t.Fatalf("unsafe GitHub HTTP client: %#v", client)
	}
	request := httptest.NewRequest(http.MethodGet, githubTokenURL, nil)
	if err := client.CheckRedirect(request, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("redirect policy error=%v", err)
	}
}

func TestGitHubUserResponseLimit(t *testing.T) {
	server := configuredGitHubServer()
	server.HTTPClient = newGitHubHTTPClient()
	server.HTTPClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return githubJSONResponse(request, http.StatusOK, strings.Repeat("x", githubResponseLimit+1)), nil
	})
	if _, err := server.githubUserSubject(context.Background(), "access-token"); err == nil {
		t.Fatal("oversized GitHub user response was accepted")
	}
}
