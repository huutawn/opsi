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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/auth"
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
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
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
		if first.Code != http.StatusUnauthorized || second.Code != http.StatusUnauthorized || calls.Load() != 1 {
			t.Fatalf("first=%d second=%d exchange calls=%d", first.Code, second.Code, calls.Load())
		}
		if strings.Contains(first.Body.String(), "provider-secret-body") {
			t.Fatalf("provider response leaked: %s", first.Body.String())
		}
	})

	t.Run("missing code consumes state", func(t *testing.T) {
		server := configuredGitHubServer()
		_, authorizeURL, _ := startBrowserAuth(t, server, "project")
		state := authorizeURL.Query().Get("state")
		response := callbackRequest(server, state, "")
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d", response.Code)
		}
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
	if response.Code != http.StatusUnauthorized || calls.Load() != 0 {
		t.Fatalf("status=%d exchange calls=%d", response.Code, calls.Load())
	}
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
		if response.Code != http.StatusForbidden {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
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
