package commands

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/opsi-dev/opsi/cli/internal/agentclient"
	"github.com/opsi-dev/opsi/cli/internal/assistant"
	"github.com/opsi-dev/opsi/cli/internal/cloudclient"
	"github.com/opsi-dev/opsi/cli/internal/config"
	"github.com/opsi-dev/opsi/cli/internal/keychain"
	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

var (
	localUIVersion  = "dev"
	localUIRevision = "unknown"
)

const (
	localRequestBodyLimit         = 1 << 20
	localResponseBodyLimit        = 2 << 20
	incidentEvidenceResponseLimit = 256 << 10
)

func newStartCommand(configPath *string, factory func() (keychain.Store, error)) *cobra.Command {
	var addr, devUI string
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the local Opsi web server",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runStart(cmd.Context(), addr, devUI, *configPath, cmd.OutOrStdout(), factory)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:9780", "local web server address")
	cmd.Flags().StringVar(&devUI, "dev-ui", "", "proxy UI requests to a local dev server")
	return cmd
}

func runStart(ctx context.Context, addr, devUI, configPath string, out io.Writer, factory func() (keychain.Store, error)) error {
	cfg := config.Default()
	if configPath != "" {
		loaded, err := config.Load(configPath)
		if err != nil {
			return err
		}
		cfg = loaded
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	defer listener.Close()

	mux, assistantMgr := newStartServer(resolveUIDir(), devUI, cfg, factory, configPath)
	defer assistantMgr.Close()
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Serve(listener)
	}()

	_, _ = fmt.Fprintf(out, "Local Web UI listening on http://%s\n", listener.Addr().String())

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

type agentConfigReloadError struct{}

func (agentConfigReloadError) Error() string { return "Agent configuration reload failed" }

type agentConfigResolver struct {
	startup    config.Config
	configPath string
}

func newAgentConfigResolver(startup config.Config, configPath string) agentConfigResolver {
	return agentConfigResolver{startup: startup, configPath: configPath}
}

func (r agentConfigResolver) snapshot() (config.Config, error) {
	if r.configPath == "" {
		return r.startup, nil
	}
	loaded, err := config.Load(r.configPath)
	if err != nil {
		return config.Config{}, agentConfigReloadError{}
	}
	if strings.TrimSpace(r.startup.AgentAddr) != "" && strings.TrimSpace(loaded.AgentAddr) == "" {
		return config.Config{}, agentConfigReloadError{}
	}
	snapshot := r.startup
	snapshot.AgentAddr = loaded.AgentAddr
	snapshot.TLS = loaded.TLS
	return snapshot, nil
}

func resolveAgentSnapshot(startup config.Config, resolver ...agentConfigResolver) (config.Config, error) {
	if len(resolver) == 0 {
		return startup, nil
	}
	return resolver[0].snapshot()
}

func newStartMux(uiDir, devUI string, cfg config.Config, factory func() (keychain.Store, error), configPaths ...string) *http.ServeMux {
	mux, _ := newStartServer(uiDir, devUI, cfg, factory, configPaths...)
	return mux
}

func newStartServer(uiDir, devUI string, cfg config.Config, factory func() (keychain.Store, error), configPaths ...string) (*http.ServeMux, *assistant.Manager) {
	configPath := ""
	if len(configPaths) > 0 {
		configPath = configPaths[0]
	}
	agentResolver := newAgentConfigResolver(cfg, configPath)
	repoRoot, _ := os.Getwd()
	opsiBinary, _ := os.Executable()
	assistantManager := assistant.NewManager(assistant.NewCodexProvider(assistant.CodexOptions{OpsiBinary: opsiBinary, ConfigPath: configPath, RepoRoot: repoRoot}))
	assistantManager.SetRepositoryRoot(repoRoot)
	localSession := newLocalSessionToken()
	authFlow := &localAuthFlow{
		states:                  map[string]localAuthPending{},
		installationClaims:      map[string]localInstallationClaimPending{},
		installationDiscoveries: map[string]localInstallationDiscoveryPending{},
		discoveredInstallations: map[string]localInstallationDiscoveryResult{},
		selections:              map[string]localSelectionState{},
	}
	routes := http.NewServeMux()
	routes.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","service":"opsi-cli"}`))
	})
	routes.HandleFunc("/api/local/session/login", func(w http.ResponseWriter, r *http.Request) {
		startLocalBrowserLogin(w, r, cfg, authFlow)
	})
	routes.HandleFunc("/api/local/session/login/start", func(w http.ResponseWriter, r *http.Request) {
		startLocalBrowserLogin(w, r, cfg, authFlow)
	})
	routes.HandleFunc("/api/local/session/callback", func(w http.ResponseWriter, r *http.Request) {
		completeLocalBrowserLogin(w, r, cfg, factory, authFlow)
	})
	routes.HandleFunc("/api/local/session/selection", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeLocalError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method is not allowed")
			return
		}
		selectionID := strings.TrimSpace(r.URL.Query().Get("selection_id"))
		if selectionID == "" {
			writeLocalError(w, r, http.StatusBadRequest, "SELECTION_ID_REQUIRED", "selection_id is required")
			return
		}
		state, ok := authFlow.getSelection(selectionID)
		if !ok {
			writeLocalError(w, r, http.StatusUnauthorized, "AUTH_SESSION_EXPIRED", "The project selection session has expired. Start a new sign-in.")
			return
		}
		writeLocalJSON(w, http.StatusOK, map[string]any{
			"selection_id": selectionID,
			"projects":     state.Projects,
		})
	})
	routes.HandleFunc("/api/local/session/select-project", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeLocalError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method is not allowed")
			return
		}
		var body struct {
			SelectionID string `json:"selection_id"`
			ProjectID   string `json:"project_id"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
			writeLocalError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
			return
		}
		body.SelectionID = strings.TrimSpace(body.SelectionID)
		body.ProjectID = strings.TrimSpace(body.ProjectID)
		if body.SelectionID == "" || body.ProjectID == "" {
			writeLocalError(w, r, http.StatusBadRequest, "PROJECT_ID_REQUIRED", "selection_id and project_id are required")
			return
		}
		state, ok := authFlow.getSelection(body.SelectionID)
		if !ok {
			writeLocalError(w, r, http.StatusUnauthorized, "AUTH_SESSION_EXPIRED", "The project selection session has expired. Start a new sign-in.")
			return
		}
		payload, _ := json.Marshal(map[string]string{
			"selection_token": state.CloudSelectionToken,
			"project_id":      body.ProjectID,
		})
		resp, err := postCloudJSON(r.Context(), cfg.CloudURL, "/v1/auth/browser/select-project", "", payload)
		if err != nil {
			writeLocalError(w, r, http.StatusBadGateway, "AUTH_SELECT_PROJECT_FAILED", "Cloud auth project selection is unavailable")
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			var errPayload struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&errPayload)
			code := errPayload.Error.Code
			if code == "" {
				code = "AUTH_SELECT_PROJECT_FAILED"
			}
			msg := errPayload.Error.Message
			if msg == "" {
				msg = "Cloud rejected the selected project"
			}
			writeLocalError(w, r, resp.StatusCode, code, msg)
			return
		}
		var out struct {
			Token   string               `json:"token"`
			Session localSessionIdentity `json:"session"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil || out.Token == "" || out.Session.ProjectID == "" || out.Session.OrgID == "" {
			writeLocalError(w, r, http.StatusBadGateway, "AUTH_SELECT_PROJECT_FAILED", "Cloud auth response was invalid")
			return
		}
		store, err := storeFromFactory(factory)
		if err != nil || store.SetPAT(out.Token) != nil {
			writeLocalError(w, r, http.StatusInternalServerError, "LOCAL_CREDENTIAL_STORE_FAILED", "could not store credential in OS keychain")
			return
		}
		authFlow.deleteSelection(body.SelectionID)
		authFlow.setSession(out.Session)
		writeLocalJSON(w, http.StatusOK, map[string]any{
			"authenticated": true,
			"session":       out.Session,
		})
	})
	routes.HandleFunc("/api/local/github/installations/claim/callback", func(w http.ResponseWriter, r *http.Request) {
		completeLocalInstallationClaim(w, r, cfg, factory, authFlow)
	})
	routes.HandleFunc("/api/local/github/installations/discover/callback", func(w http.ResponseWriter, r *http.Request) {
		completeLocalInstallationDiscovery(w, r, cfg, factory, authFlow)
	})
	routes.HandleFunc("/api/local/session/logout", func(w http.ResponseWriter, r *http.Request) {
		if !requireLocalSession(w, r, localSession) {
			return
		}
		logoutLocalSession(w, r, cfg, factory)
		authFlow.clearSession()
	})
	routes.HandleFunc("/api/local/session/token/rotate", func(w http.ResponseWriter, r *http.Request) {
		if !requireLocalSession(w, r, localSession) {
			return
		}
		rotateLocalPAT(w, r, cfg, factory)
	})
	routes.HandleFunc("/api/local/session/token/revoke", func(w http.ResponseWriter, r *http.Request) {
		if !requireLocalSession(w, r, localSession) {
			return
		}
		logoutLocalSession(w, r, cfg, factory)
		authFlow.clearSession()
	})
	routes.HandleFunc("/api/local/session/project", func(w http.ResponseWriter, r *http.Request) {
		if !requireLocalSession(w, r, localSession) {
			return
		}
		if r.Method != http.MethodPost {
			writeLocalError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method is not allowed")
			return
		}
		var body struct {
			ProjectID string `json:"project_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.ProjectID) == "" {
			writeLocalError(w, r, http.StatusBadRequest, "PROJECT_ID_REQUIRED", "project_id is required")
			return
		}
		authenticated, tokenState, cloudState, identity := localPATStatus(r.Context(), cfg, factory, strings.TrimSpace(body.ProjectID))
		if !authenticated {
			writeLocalError(w, r, http.StatusUnauthorized, "PROJECT_SWITCH_REJECTED", "Cloud rejected the selected project")
			return
		}
		authFlow.setSession(identity)
		writeLocalJSON(w, http.StatusOK, map[string]any{
			"authenticated": true, "token_status": tokenState, "cloud_connected": cloudState,
			"agent_connected": probeAgent(r.Context(), cfg, factory, agentResolver),
			"org_id":          identity.OrgID, "project_id": identity.ProjectID, "role": identity.Role,
		})
	})
	routes.HandleFunc("/api/local/session", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			startLocalBrowserLogin(w, r, cfg, authFlow)
			return
		}
		if r.Method != http.MethodGet {
			writeLocalError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method is not allowed")
			return
		}
		w.Header().Set("content-type", "application/json")
		projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
		if projectID == "" {
			projectID = authFlow.session().ProjectID
		}
		authenticated, tokenState, cloudState, identity := false, "missing", "unknown", localSessionIdentity{}
		if optionalPAT(factory) != "" {
			tokenState = "unverified"
		}
		if projectID != "" || r.URL.Query().Get("verify") == "1" {
			authenticated, tokenState, cloudState, identity = localPATStatus(r.Context(), cfg, factory, projectID)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"authenticated":   authenticated,
			"cloud_connected": cloudState,
			"agent_connected": probeAgent(r.Context(), cfg, factory, agentResolver),
			"token_status":    tokenState,
			"user_id":         identity.UserID,
			"org_id":          identity.OrgID,
			"project_id":      identity.ProjectID,
			"role":            identity.Role,
			"local_session":   localSession,
			"capabilities":    []string{"projects", "nodes", "services", "deployments", "github_app", "build_records", "topology", "deployment_policy", "routing_preflight", "secrets", "telemetry", "logs", "incidents", "audit", "support"},
		})
	})
	routes.HandleFunc("/api/local/settings", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeLocalError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method is not allowed")
			return
		}
		writeLocalJSON(w, http.StatusOK, map[string]any{
			"version": localUIVersion, "revision": localUIRevision, "go_version": runtime.Version(),
			"cloud_authority": configuredAuthority(cfg.CloudURL), "cloud_configured": strings.TrimSpace(cfg.CloudURL) != "",
			"agent_configured": strings.TrimSpace(cfg.AgentAddr) != "", "agent_tls_pinned": strings.TrimSpace(cfg.TLS.PinnedServerCertSHA256) != "",
			"config_selected": configPath != "", "ui_assets": filepath.Base(uiDir),
			"backend_gaps": []map[string]string{
				{"capability": "organization listing", "status": "BACKEND_GAP", "roadmap": "R5-017+"},
				{"capability": "members/RBAC", "status": "BACKEND_GAP", "roadmap": "R5-017+"},
				{"capability": "secret metadata/listing", "status": "BACKEND_GAP", "roadmap": "R5-017+"},
			},
		})
	})
	routes.HandleFunc("/api/local/status", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 800*time.Millisecond)
		defer cancel()
		ctx = agentclient.WithPAT(ctx, optionalPAT(factory))
		agentCfg, err := agentResolver.snapshot()
		if err != nil {
			writeLocalError(w, r, http.StatusBadGateway, "AGENT_CONFIG_RELOAD_FAILED", "Agent configuration is unavailable")
			return
		}
		status, err := agentclient.New(agentCfg).Status(ctx)
		w.Header().Set("content-type", "application/json")
		if err != nil {
			writeLocalError(w, r, http.StatusBadGateway, "AGENT_UNAVAILABLE", "Agent status is unavailable")
			return
		}
		_ = json.NewEncoder(w).Encode(status)
	})
	registerRepositoryCDRoutes(routes, localSession)
	routes.HandleFunc("/api/local/", func(w http.ResponseWriter, r *http.Request) {
		if (r.URL.Path == "/api/local/projects" || r.URL.Path == "/api/local/projects/") && strings.TrimSpace(r.URL.Query().Get("org_id")) == "" {
			writeLocalError(w, r, http.StatusBadRequest, "ORG_ID_REQUIRED", "org_id must come from the authenticated session")
			return
		}
		if handleLocalAssistantRoutes(w, r, assistantManager, cfg, factory, localSession) {
			return
		}
		if handleLocalAgentRoutes(w, r, cfg, factory, localSession, agentResolver) {
			return
		}
		proxyLocalRegistry(w, r, cfg, factory, localSession, authFlow)
	})

	routes.Handle("/", newUIHandler(uiDir, devUI))
	return routes, assistantManager
}

func configuredAuthority(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

type localResponseBuffer struct {
	header   http.Header
	status   int
	body     bytes.Buffer
	overflow bool
}

func (b *localResponseBuffer) Header() http.Header { return b.header }

func (b *localResponseBuffer) WriteHeader(status int) {
	if b.status == 0 {
		b.status = status
	}
}

func (b *localResponseBuffer) Write(data []byte) (int, error) {
	if b.status == 0 {
		b.status = http.StatusOK
	}
	if b.body.Len()+len(data) > localResponseBodyLimit {
		b.overflow = true
		return len(data), nil
	}
	return b.body.Write(data)
}

func boundedLocalAPI(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSpace(r.Header.Get("X-Request-ID")) == "" {
			r.Header.Set("X-Request-ID", newLocalSessionToken())
		}
		if r.Body != nil {
			body, err := io.ReadAll(io.LimitReader(r.Body, localRequestBodyLimit+1))
			_ = r.Body.Close()
			if err != nil || len(body) > localRequestBodyLimit {
				writeLocalAPIError(w, r, http.StatusRequestEntityTooLarge, "REQUEST_BODY_TOO_LARGE", "local request body exceeds 1 MiB", "Reduce the request and review it again.")
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		buffer := &localResponseBuffer{header: make(http.Header)}
		next.ServeHTTP(buffer, r)
		if buffer.overflow {
			writeLocalAPIError(w, r, http.StatusBadGateway, "RESPONSE_BODY_TOO_LARGE", "downstream response exceeds 2 MiB", "Narrow the request and retry.")
			return
		}
		status := buffer.status
		if status == 0 {
			status = http.StatusOK
		}
		if status >= 400 {
			writeNormalizedLocalError(w, r, status, buffer.body.Bytes())
			return
		}
		for key, values := range buffer.header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.Header().Del("Content-Length")
		w.WriteHeader(status)
		_, _ = w.Write(buffer.body.Bytes())
	})
}

func writeNormalizedLocalError(w http.ResponseWriter, r *http.Request, status int, body []byte) {
	var payload struct {
		Error struct {
			Code       string `json:"code"`
			Message    string `json:"message"`
			NextAction string `json:"next_action"`
		} `json:"error"`
		Code       string `json:"error_code"`
		Message    string `json:"message"`
		NextAction string `json:"next_action"`
	}
	_ = json.Unmarshal(body, &payload)
	code := strings.TrimSpace(payload.Error.Code)
	if code == "" {
		code = strings.TrimSpace(payload.Code)
	}
	if code == "" {
		code = "DOWNSTREAM_REQUEST_FAILED"
	}
	messageValue := payload.Error.Message
	if messageValue == "" {
		messageValue = payload.Message
	}
	message := strings.TrimSpace(redactLocalTelemetryText(messageValue))
	if message == "" {
		message = http.StatusText(status)
	}
	if len(message) > 512 {
		message = message[:512]
	}
	nextActionValue := payload.Error.NextAction
	if nextActionValue == "" {
		nextActionValue = payload.NextAction
	}
	nextAction := strings.TrimSpace(nextActionValue)
	if nextAction == "" {
		nextAction = localNextAction(status)
	}
	writeLocalAPIError(w, r, status, code, message, nextAction)
}

func localNextAction(status int) string {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "Sign in again or ask a project owner to grant access."
	case http.StatusConflict, http.StatusPreconditionFailed:
		return "Refresh factual state, create a new review, and retry."
	case http.StatusNotFound:
		return "Refresh inventory and select an existing target."
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return "Correct the input and create a new review."
	default:
		return "Retry after checking Local backend, Cloud, and Agent connectivity."
	}
}

func writeLocalAPIError(w http.ResponseWriter, r *http.Request, status int, code, message, nextAction string) {
	requestID := r.Header.Get("X-Request-ID")
	w.Header().Set("content-type", "application/json")
	w.Header().Set("X-Request-ID", requestID)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
		"code": code, "message": message, "request_id": requestID,
		"next_action": nextAction, "retryable": status >= 500 || status == http.StatusConflict || status == http.StatusPreconditionFailed,
	}})
}

type localSelectionState struct {
	CloudSelectionToken string
	Projects            []map[string]any
	ExpiresAt           time.Time
}

type localAuthPending struct {
	ExpiresAt   time.Time
	ReturnQuery string
}

type localInstallationDiscoveryPending struct {
	ProjectID string
	ExpiresAt time.Time
}

type localInstallationClaimPending struct {
	ProjectID string
	ExpiresAt time.Time
}

type localInstallationDiscoveryResult struct {
	Installations []cloudclient.GitHubInstallation
	ExpiresAt     time.Time
}

type localAuthFlow struct {
	mu                      sync.Mutex
	states                  map[string]localAuthPending
	installationClaims      map[string]localInstallationClaimPending
	installationDiscoveries map[string]localInstallationDiscoveryPending
	discoveredInstallations map[string]localInstallationDiscoveryResult
	selections              map[string]localSelectionState
	currentSession          localSessionIdentity
}

type localSessionIdentity struct {
	UserID    string `json:"user_id,omitempty"`
	OrgID     string `json:"org_id,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
	Role      string `json:"role,omitempty"`
}

func (f *localAuthFlow) session() localSessionIdentity {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.currentSession
}

func (f *localAuthFlow) setSession(session localSessionIdentity) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.currentSession = session
}

func (f *localAuthFlow) clearSession() {
	f.setSession(localSessionIdentity{})
}

func (f *localAuthFlow) setSelection(id, cloudToken string, projects []map[string]any, expiresAt time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.selections == nil {
		f.selections = map[string]localSelectionState{}
	}
	f.selections[id] = localSelectionState{
		CloudSelectionToken: cloudToken,
		Projects:            projects,
		ExpiresAt:           expiresAt,
	}
}

func (f *localAuthFlow) getSelection(id string) (localSelectionState, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.selections == nil {
		return localSelectionState{}, false
	}
	state, ok := f.selections[id]
	if !ok || time.Now().UTC().After(state.ExpiresAt) {
		delete(f.selections, id)
		return localSelectionState{}, false
	}
	return state, true
}

func (f *localAuthFlow) deleteSelection(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.selections != nil {
		delete(f.selections, id)
	}
}

func startLocalBrowserLogin(w http.ResponseWriter, r *http.Request, cfg config.Config, flow *localAuthFlow) {
	if r.Method != http.MethodPost {
		writeLocalError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method is not allowed")
		return
	}
	var body struct {
		ProjectID   string `json:"project_id"`
		ReturnQuery string `json:"return_query"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body)
	body.ProjectID = strings.TrimSpace(body.ProjectID)
	body.ReturnQuery = canonicalLocalReturnQuery(body.ReturnQuery)
	state := newLocalSessionToken()
	callback := "http://" + r.Host + "/api/local/session/callback"
	if body.ProjectID != "" {
		callback += "?project=" + url.QueryEscape(body.ProjectID)
	}
	payload, _ := json.Marshal(map[string]any{"local_callback": callback, "local_state": state, "project_id": body.ProjectID})
	resp, err := postCloudJSON(r.Context(), cfg.CloudURL, "/v1/auth/browser/start", "", payload)
	if err != nil {
		writeLocalError(w, r, http.StatusBadGateway, "AUTH_START_FAILED", "Cloud auth start is unavailable")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		writeLocalError(w, r, resp.StatusCode, "AUTH_UNAVAILABLE", "Cloud auth flow is unavailable")
		return
	}
	flow.mu.Lock()
	flow.states[state] = localAuthPending{ExpiresAt: time.Now().UTC().Add(5 * time.Minute), ReturnQuery: body.ReturnQuery}
	flow.mu.Unlock()
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, resp.Body)
}

func requireLocalSession(w http.ResponseWriter, r *http.Request, localSession string) bool {
	if r.Header.Get("X-Local-Session") != localSession {
		writeLocalError(w, r, http.StatusUnauthorized, "LOCAL_SESSION_REQUIRED", "mutating local requests require X-Local-Session")
		return false
	}
	if !requireLocalIdempotencyKey(w, r) {
		return false
	}
	return true
}

func completeLocalBrowserLogin(w http.ResponseWriter, r *http.Request, cfg config.Config, factory func() (keychain.Store, error), flow *localAuthFlow) {
	if r.Method != http.MethodGet {
		writeLocalError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method is not allowed")
		return
	}
	code, state := r.URL.Query().Get("code"), r.URL.Query().Get("state")
	projectID := strings.TrimSpace(r.URL.Query().Get("project"))
	flow.mu.Lock()
	pending, ok := flow.states[state]
	if ok {
		delete(flow.states, state)
	}
	flow.mu.Unlock()
	if !ok || time.Now().UTC().After(pending.ExpiresAt) {
		writeLocalError(w, r, http.StatusUnauthorized, "AUTH_CALLBACK_INVALID", "auth callback expired or invalid")
		return
	}
	if authError := browserAuthErrorCode(r.URL.Query().Get("error")); authError != "" {
		http.Redirect(w, r, localBrowserAuthRedirect(projectID, "auth_error", authError, pending.ReturnQuery), http.StatusFound)
		return
	}
	if code == "" {
		writeLocalError(w, r, http.StatusUnauthorized, "AUTH_CALLBACK_INVALID", "auth callback expired or invalid")
		return
	}
	payload, _ := json.Marshal(map[string]string{"code": code})
	resp, err := postCloudJSON(r.Context(), cfg.CloudURL, "/v1/auth/browser/redeem", "", payload)
	if err != nil {
		writeLocalError(w, r, http.StatusBadGateway, "AUTH_REDEEM_FAILED", "Cloud auth redeem is unavailable")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		writeLocalError(w, r, http.StatusUnauthorized, "AUTH_REDEEM_FAILED", "Cloud auth grant was rejected")
		return
	}
	var out struct {
		Status         string               `json:"status"`
		SelectionToken string               `json:"selection_token"`
		Projects       []map[string]any     `json:"projects"`
		Token          string               `json:"token"`
		Session        localSessionIdentity `json:"session"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		writeLocalError(w, r, http.StatusBadGateway, "AUTH_REDEEM_FAILED", "Cloud auth response was invalid")
		return
	}
	if out.Status == "select_project" || (out.SelectionToken != "" && len(out.Projects) > 0) {
		selectionID := newLocalSessionToken()
		flow.setSelection(selectionID, out.SelectionToken, out.Projects, time.Now().UTC().Add(5*time.Minute))
		query := url.Values{
			"auth":         []string{"select_project"},
			"selection_id": []string{selectionID},
		}
		http.Redirect(w, r, "/?"+query.Encode(), http.StatusFound)
		return
	}
	if out.Token == "" || out.Session.ProjectID == "" || out.Session.OrgID == "" {
		writeLocalError(w, r, http.StatusBadGateway, "AUTH_REDEEM_FAILED", "Cloud auth response was invalid")
		return
	}
	store, err := storeFromFactory(factory)
	if err != nil || store.SetPAT(out.Token) != nil {
		writeLocalError(w, r, http.StatusInternalServerError, "LOCAL_CREDENTIAL_STORE_FAILED", "could not store credential in OS keychain")
		return
	}
	flow.setSession(out.Session)
	http.Redirect(w, r, localBrowserAuthRedirect(out.Session.ProjectID, "auth", "ok", pending.ReturnQuery), http.StatusFound)
}

func localBrowserAuthRedirect(projectID, key, value, returnQuery string) string {
	query, _ := url.ParseQuery(returnQuery)
	query.Set(key, value)
	if projectID != "" {
		query.Set("project", projectID)
	}
	return "/?" + query.Encode()
}

func canonicalLocalReturnQuery(raw string) string {
	if len(raw) > 4096 {
		return ""
	}
	values, err := url.ParseQuery(strings.TrimPrefix(raw, "?"))
	if err != nil {
		return ""
	}
	allowed := map[string]bool{"project": true, "view": true, "source_project": true, "source_installation": true, "source_repository": true, "source_ref": true, "source_hostname": true}
	canonical := url.Values{}
	for name, entries := range values {
		if allowed[name] && len(entries) == 1 && len(entries[0]) <= 512 && strings.IndexFunc(entries[0], unicode.IsControl) < 0 {
			canonical.Set(name, entries[0])
		}
	}
	return canonical.Encode()
}

func browserAuthErrorCode(code string) string {
	switch code {
	case "AUTH_SESSION_EXPIRED", "GITHUB_AUTH_DENIED", "GITHUB_AUTH_FAILED", "GITHUB_ACCOUNT_UNLINKED", "OPSI_MEMBERSHIP_REQUIRED", "PROJECT_SELECTION_REQUIRED", "AUTH_UNAVAILABLE":
		return code
	default:
		return ""
	}
}

func rotateLocalPAT(w http.ResponseWriter, r *http.Request, cfg config.Config, factory func() (keychain.Store, error)) {
	if r.Method != http.MethodPost {
		writeLocalError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method is not allowed")
		return
	}
	store, err := storeFromFactory(factory)
	if err != nil {
		writeLocalError(w, r, http.StatusUnauthorized, "LOCAL_CREDENTIAL_MISSING", "local credential is unavailable")
		return
	}
	oldPAT, err := store.GetPAT()
	if err != nil || oldPAT == "" {
		writeLocalError(w, r, http.StatusUnauthorized, "LOCAL_CREDENTIAL_MISSING", "local credential is unavailable")
		return
	}
	var body struct {
		ProjectID string `json:"project_id"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body)
	payload, _ := json.Marshal(map[string]string{"project_id": body.ProjectID})
	resp, err := postCloudJSON(r.Context(), cfg.CloudURL, "/v1/auth/pat/rotate", oldPAT, payload)
	if err != nil {
		writeLocalError(w, r, http.StatusBadGateway, "PAT_ROTATE_FAILED", "Cloud PAT rotation is unavailable")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		writeLocalError(w, r, http.StatusUnauthorized, "PAT_ROTATE_FAILED", "Cloud PAT rotation failed")
		return
	}
	var out struct {
		Token   string         `json:"token"`
		Session map[string]any `json:"session"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil || out.Token == "" {
		writeLocalError(w, r, http.StatusBadGateway, "PAT_ROTATE_FAILED", "Cloud PAT rotation response was invalid")
		return
	}
	if err := store.SetPAT(out.Token); err != nil {
		writeLocalError(w, r, http.StatusInternalServerError, "LOCAL_CREDENTIAL_STORE_FAILED", "old credential was preserved because keychain update failed")
		return
	}
	revokedOld := revokeCloudPAT(r.Context(), cfg.CloudURL, oldPAT, body.ProjectID) == nil
	writeLocalJSON(w, http.StatusOK, map[string]any{"rotated": true, "revoked_old": revokedOld, "session": out.Session})
}

func logoutLocalSession(w http.ResponseWriter, r *http.Request, cfg config.Config, factory func() (keychain.Store, error)) {
	if r.Method != http.MethodPost {
		writeLocalError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method is not allowed")
		return
	}
	store, err := storeFromFactory(factory)
	if err == nil {
		if pat, getErr := store.GetPAT(); getErr == nil && pat != "" {
			var body struct {
				ProjectID string `json:"project_id"`
			}
			_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body)
			_ = revokeCloudPAT(r.Context(), cfg.CloudURL, pat, body.ProjectID)
		}
		_ = store.DeletePAT()
	}
	writeLocalJSON(w, http.StatusOK, map[string]any{"authenticated": false, "revoked": true})
}

func probeAgent(ctx context.Context, cfg config.Config, factory func() (keychain.Store, error), resolver ...agentConfigResolver) string {
	agentCfg, err := resolveAgentSnapshot(cfg, resolver...)
	if err != nil {
		return "failed"
	}
	if strings.TrimSpace(agentCfg.AgentAddr) == "" {
		return "not connected"
	}
	ctx, cancel := context.WithTimeout(ctx, 800*time.Millisecond)
	defer cancel()
	ctx = agentclient.WithPAT(ctx, optionalPAT(factory))
	if _, err := agentclient.New(agentCfg).Status(ctx); err != nil {
		return "failed"
	}
	return "ok"
}

func localPATStatus(ctx context.Context, cfg config.Config, factory func() (keychain.Store, error), projectID string) (bool, string, string, localSessionIdentity) {
	pat := optionalPAT(factory)
	if pat == "" {
		return false, "missing", "unknown", localSessionIdentity{}
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	status, identity, err := verifyCloudPAT(ctx, cfg.CloudURL, pat, projectID)
	if err != nil {
		return false, "unverified", "failed", localSessionIdentity{}
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return false, "invalid", "ok", localSessionIdentity{}
	}
	if status >= 200 && status < 300 {
		return true, "valid", "ok", identity
	}
	return false, "unverified", "failed", localSessionIdentity{}
}

func verifyCloudPAT(ctx context.Context, cloudURL, pat, projectID string) (int, localSessionIdentity, error) {
	payload, _ := json.Marshal(map[string]string{"project_id": projectID})
	resp, err := postCloudJSON(ctx, cloudURL, "/v1/auth/pat/verify", pat, payload)
	if err != nil {
		return 0, localSessionIdentity{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return resp.StatusCode, localSessionIdentity{}, nil
	}
	var session localSessionIdentity
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&session); err != nil {
		return resp.StatusCode, localSessionIdentity{}, err
	}
	wrongProject := projectID != "" && session.ProjectID != projectID
	if session.UserID == "" || session.OrgID == "" || session.ProjectID == "" || wrongProject || session.Role == "" {
		return resp.StatusCode, localSessionIdentity{}, fmt.Errorf("invalid PAT verification response")
	}
	return resp.StatusCode, session, nil
}

func postCloudJSON(ctx context.Context, cloudURL, path, pat string, payload []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(cloudURL, "/")+path, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if pat != "" {
		req.Header.Set("Authorization", "Bearer "+pat)
	}
	return http.DefaultClient.Do(req)
}

func revokeCloudPAT(ctx context.Context, cloudURL, pat, projectID string) error {
	payload, _ := json.Marshal(map[string]string{"project_id": projectID})
	resp, err := postCloudJSON(ctx, cloudURL, "/v1/auth/pat/revoke", pat, payload)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("revoke status %d", resp.StatusCode)
	}
	return nil
}

func storeFromFactory(factory func() (keychain.Store, error)) (keychain.Store, error) {
	if factory == nil {
		return nil, fmt.Errorf("keychain is not configured")
	}
	return factory()
}

func handleLocalAgentRoutes(w http.ResponseWriter, r *http.Request, cfg config.Config, factory func() (keychain.Store, error), localSession string, resolver agentConfigResolver) bool {
	if retiredLocalServiceDeployment(r.URL.Path, r.Method) {
		return false
	}
	if isMutation(r.Method) && !isPlacementPreview(r.URL.Path) {
		if r.Header.Get("X-Local-Session") != localSession {
			writeLocalError(w, r, http.StatusUnauthorized, "LOCAL_SESSION_REQUIRED", "mutating local requests require X-Local-Session")
			return true
		}
		if !requireLocalIdempotencyKey(w, r) {
			return true
		}
	}
	return localTelemetry(w, r, cfg, factory, resolver) ||
		localSecretOperation(w, r, cfg, factory, resolver) ||
		localIncidentOperation(w, r, cfg, factory, resolver)
}

func localSecretOperation(w http.ResponseWriter, r *http.Request, cfg config.Config, factory func() (keychain.Store, error), resolver ...agentConfigResolver) bool {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 5 || parts[0] != "api" || parts[1] != "local" || parts[2] != "projects" || parts[4] != "secrets" {
		return false
	}
	projectID, err := url.PathUnescape(parts[3])
	if err != nil || projectID == "" {
		writeLocalError(w, r, http.StatusBadRequest, "PROJECT_ID_REQUIRED", "project_id is required")
		return true
	}
	if rejectCallerAuthorityQuery(w, r) {
		return true
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		writeLocalError(w, r, http.StatusNotImplemented, "SECRETS_OPERATION_UNSUPPORTED", "this secret operation is not supported by the local Agent API")
		return true
	}
	if len(parts) == 6 && parts[5] == "setup-totp" {
		var raw map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			writeLocalError(w, r, http.StatusBadRequest, "INVALID_TOTP_REQUEST", "invalid TOTP setup request")
			return true
		}
		if len(raw) != 0 {
			writeLocalError(w, r, http.StatusBadRequest, "TOTP_INPUT_UNSUPPORTED", "TOTP setup accepts no caller-controlled fields")
			return true
		}
		callLocalTOTPAgent(w, r, cfg, factory, projectID, resolver...)
		return true
	}
	if len(parts) == 5 {
		req, ok := readLocalSecretRequest(w, r, projectID, "")
		if !ok {
			return true
		}
		callLocalSecretAgent(w, r, cfg, factory, "created", req.SecretRequest, false, resolver...)
		return true
	}
	if len(parts) != 7 {
		writeLocalError(w, r, http.StatusNotFound, "LOCAL_ROUTE_NOT_FOUND", "local secret route is not implemented")
		return true
	}
	name, err := url.PathUnescape(parts[5])
	if err != nil || name == "" {
		writeLocalError(w, r, http.StatusBadRequest, "SECRET_NAME_REQUIRED", "secret name is required")
		return true
	}
	req, ok := readLocalSecretRequest(w, r, projectID, name)
	if !ok {
		return true
	}
	switch parts[6] {
	case "reveal":
		if !req.explicitReveal {
			writeLocalError(w, r, http.StatusBadRequest, "SECRET_REVEAL_INTENT_REQUIRED", "secret reveal requires explicit reveal intent")
			return true
		}
		if !req.hasSecondFactor() {
			writeLocalError(w, r, http.StatusBadRequest, "SECRET_SECOND_FACTOR_REQUIRED", "secret reveal requires OTP or TOTP")
			return true
		}
		callLocalSecretAgent(w, r, cfg, factory, "revealed", req.SecretRequest, true, resolver...)
	case "rotate":
		if !req.hasSecondFactor() {
			writeLocalError(w, r, http.StatusBadRequest, "SECRET_SECOND_FACTOR_REQUIRED", "secret rotation requires OTP or TOTP")
			return true
		}
		callLocalSecretAgent(w, r, cfg, factory, "rotated", req.SecretRequest, false, resolver...)
	default:
		writeLocalError(w, r, http.StatusNotImplemented, "SECRETS_OPERATION_UNSUPPORTED", "this secret operation is not supported by the local Agent API")
	}
	return true
}

func callLocalTOTPAgent(w http.ResponseWriter, r *http.Request, cfg config.Config, factory func() (keychain.Store, error), projectID string, resolver ...agentConfigResolver) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if pat := optionalPAT(factory); pat != "" {
		ctx = agentclient.WithPAT(ctx, pat)
	}
	agentCfg, err := resolveAgentSnapshot(cfg, resolver...)
	if err != nil {
		writeLocalAgentSecretError(w, r, err)
		return
	}
	resp, err := agentclient.New(agentCfg).SetupTOTP(ctx, &agentv1.SetupTOTPRequest{ProjectID: projectID})
	if err != nil {
		writeLocalAgentSecretError(w, r, err)
		return
	}
	w.Header().Set("content-type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "created", "source": "agent", "project_id": projectID,
		"secret": resp.Secret, "uri": resp.URI, "ttl_seconds": 300,
	})
}

type localSecretRequest struct {
	*agentv1.SecretRequest
	explicitReveal bool
}

func (req localSecretRequest) hasSecondFactor() bool {
	return req.TOTPCode != "" || (req.OTPRequestID != "" && req.OTPCode != "")
}

func readLocalSecretRequest(w http.ResponseWriter, r *http.Request, projectID, pathName string) (localSecretRequest, bool) {
	var raw map[string]json.RawMessage
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&raw); err != nil {
		writeLocalError(w, r, http.StatusBadRequest, "INVALID_SECRET_REQUEST", "invalid secret request")
		return localSecretRequest{}, false
	}
	allowed := map[string]bool{
		"service_id": true, "name": true, "namespace": true,
		"otp_code": true, "otp_request_id": true, "totp_code": true, "reveal": true, "explicit_intent": true,
	}
	for key := range raw {
		lower := strings.ToLower(key)
		if isCallerAuthorityField(lower) {
			writeLocalError(w, r, http.StatusBadRequest, "CALLER_AUTHORITY_FORBIDDEN", "authentication and identity fields are managed by the local backend")
			return localSecretRequest{}, false
		}
		if !allowed[lower] {
			writeLocalError(w, r, http.StatusBadRequest, "SECRET_INPUT_UNSUPPORTED", "secret request contains an unsupported field")
			return localSecretRequest{}, false
		}
	}
	name := pathName
	if name == "" {
		name = jsonString(raw, "name")
	}
	req := &agentv1.SecretRequest{
		ProjectID:    projectID,
		ServiceID:    jsonString(raw, "service_id"),
		Name:         name,
		Namespace:    jsonString(raw, "namespace"),
		OTPCode:      jsonString(raw, "otp_code"),
		OTPRequestID: jsonString(raw, "otp_request_id"),
		TOTPCode:     jsonString(raw, "totp_code"),
	}
	if req.ServiceID == "" || req.Name == "" {
		writeLocalError(w, r, http.StatusBadRequest, "SECRET_REQUIRED_FIELDS_MISSING", "service_id and name are required")
		return localSecretRequest{}, false
	}
	return localSecretRequest{SecretRequest: req, explicitReveal: jsonBool(raw, "reveal") || strings.EqualFold(jsonString(raw, "explicit_intent"), "reveal")}, true
}

func jsonString(raw map[string]json.RawMessage, key string) string {
	data, ok := raw[key]
	if !ok {
		return ""
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func jsonBool(raw map[string]json.RawMessage, key string) bool {
	data, ok := raw[key]
	if !ok {
		return false
	}
	var value bool
	return json.Unmarshal(data, &value) == nil && value
}

func callLocalSecretAgent(w http.ResponseWriter, r *http.Request, cfg config.Config, factory func() (keychain.Store, error), statusText string, req *agentv1.SecretRequest, includePassword bool, resolver ...agentConfigResolver) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if pat := optionalPAT(factory); pat != "" {
		ctx = agentclient.WithPAT(ctx, pat)
	}
	agentCfg, err := resolveAgentSnapshot(cfg, resolver...)
	if err != nil {
		writeLocalAgentSecretError(w, r, err)
		return
	}
	client := agentclient.New(agentCfg)
	var resp *agentv1.SecretResponse
	switch statusText {
	case "created":
		resp, err = client.CreateSecret(ctx, req)
	case "revealed":
		resp, err = client.RevealSecret(ctx, req)
	case "rotated":
		resp, err = client.RotateSecret(ctx, req)
	}
	if err != nil {
		writeLocalAgentSecretError(w, r, err)
		return
	}
	out := map[string]any{
		"status":     statusText,
		"source":     "agent",
		"project_id": resp.ProjectID,
		"service_id": resp.ServiceID,
		"name":       resp.Name,
		"namespace":  resp.Namespace,
		"username":   resp.Username,
	}
	if includePassword {
		out["password"] = resp.Password
		out["ttl_seconds"] = 60
		out["reveal_expires_at"] = time.Now().UTC().Add(time.Minute).Format(time.RFC3339)
	}
	w.Header().Set("content-type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(out)
}

func writeLocalAgentSecretError(w http.ResponseWriter, r *http.Request, err error) {
	statusCode := http.StatusBadGateway
	code := "AGENT_SECRET_OPERATION_FAILED"
	if _, ok := err.(agentConfigReloadError); ok {
		writeLocalError(w, r, http.StatusBadGateway, "AGENT_CONFIG_RELOAD_FAILED", "Agent configuration is unavailable")
		return
	}
	switch grpcstatus.Code(err) {
	case codes.InvalidArgument:
		statusCode, code = http.StatusBadRequest, "INVALID_SECRET_REQUEST"
	case codes.Unauthenticated:
		statusCode, code = http.StatusUnauthorized, "AGENT_AUTH_REQUIRED"
	case codes.PermissionDenied:
		statusCode, code = http.StatusForbidden, "SECRET_ACCESS_DENIED"
	case codes.FailedPrecondition:
		statusCode, code = http.StatusPreconditionFailed, "SECRET_PRECONDITION_FAILED"
	case codes.Unimplemented:
		statusCode, code = http.StatusNotImplemented, "AGENT_SECRET_UNSUPPORTED"
	case codes.DeadlineExceeded:
		statusCode, code = http.StatusGatewayTimeout, "AGENT_SECRET_TIMEOUT"
	case codes.Unavailable:
		statusCode, code = http.StatusBadGateway, "AGENT_UNAVAILABLE"
	}
	writeLocalError(w, r, statusCode, code, "Agent secret operation failed")
}

func localIncidentOperation(w http.ResponseWriter, r *http.Request, cfg config.Config, factory func() (keychain.Store, error), resolver ...agentConfigResolver) bool {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 5 || parts[0] != "api" || parts[1] != "local" || parts[2] != "projects" || parts[4] != "incidents" {
		return false
	}
	projectID, err := url.PathUnescape(parts[3])
	if err != nil || projectID == "" {
		writeLocalError(w, r, http.StatusBadRequest, "PROJECT_ID_REQUIRED", "project_id is required")
		return true
	}
	if rejectCallerAuthorityQuery(w, r) {
		return true
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		req, ok := readLocalIncidentQuery(w, r, projectID)
		if !ok {
			return true
		}
		if len(parts) == 5 {
			callLocalIncidentListAgent(w, r, cfg, factory, req, resolver...)
			return true
		}
		if len(parts) == 6 {
			incidentID, err := url.PathUnescape(parts[5])
			if err != nil || incidentID == "" {
				writeLocalError(w, r, http.StatusBadRequest, "INCIDENT_ID_REQUIRED", "incident_id is required")
				return true
			}
			callLocalIncidentGetAgent(w, r, cfg, factory, &agentv1.IncidentGetRequest{ProjectID: projectID, IncidentID: incidentID}, resolver...)
			return true
		}
		if len(parts) == 7 && parts[6] == "evidence" {
			incidentID, err := url.PathUnescape(parts[5])
			if err != nil || incidentID == "" {
				writeLocalError(w, r, http.StatusBadRequest, "INCIDENT_ID_REQUIRED", "incident_id is required")
				return true
			}
			callLocalIncidentEvidenceAgent(w, r, cfg, factory, &agentv1.IncidentGetRequest{ProjectID: projectID, IncidentID: incidentID}, resolver...)
			return true
		}
		writeLocalError(w, r, http.StatusNotFound, "LOCAL_ROUTE_NOT_FOUND", "local incident route is not implemented")
		return true
	}
	if r.Method != http.MethodPost {
		writeLocalError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method is not allowed")
		return true
	}
	if len(parts) != 7 || parts[6] != "resolve" {
		writeLocalError(w, r, http.StatusNotFound, "LOCAL_ROUTE_NOT_FOUND", "local incident route is not implemented")
		return true
	}
	incidentID, err := url.PathUnescape(parts[5])
	if err != nil || incidentID == "" {
		writeLocalError(w, r, http.StatusBadRequest, "INCIDENT_ID_REQUIRED", "incident_id is required")
		return true
	}
	req, ok := readLocalIncidentRequest(w, r, projectID, incidentID)
	if !ok {
		return true
	}
	callLocalIncidentResolveAgent(w, r, cfg, factory, req, resolver...)
	return true
}

func readLocalIncidentQuery(w http.ResponseWriter, r *http.Request, projectID string) (*agentv1.IncidentListRequest, bool) {
	query := r.URL.Query()
	limit, _ := strconv.ParseInt(query.Get("limit"), 10, 32)
	return &agentv1.IncidentListRequest{ProjectID: projectID, Status: strings.TrimSpace(query.Get("status")), Limit: int32(limit)}, true
}

func readLocalIncidentRequest(w http.ResponseWriter, r *http.Request, projectID, incidentID string) (*agentv1.IncidentResolveRequest, bool) {
	var raw map[string]json.RawMessage
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&raw); err != nil {
		writeLocalError(w, r, http.StatusBadRequest, "INVALID_INCIDENT_REQUEST", "invalid incident request")
		return nil, false
	}
	for key := range raw {
		lower := strings.ToLower(key)
		if isCallerAuthorityField(lower) {
			writeLocalError(w, r, http.StatusBadRequest, "CALLER_AUTHORITY_FORBIDDEN", "authentication and identity fields are managed by the local backend")
			return nil, false
		}
		if lower != "" {
			writeLocalError(w, r, http.StatusBadRequest, "INCIDENT_INPUT_UNSUPPORTED", "incident request contains an unsupported field")
			return nil, false
		}
	}
	return &agentv1.IncidentResolveRequest{ProjectID: projectID, IncidentID: incidentID}, true
}

func isCallerAuthorityField(field string) bool {
	return field == "pat" || field == "user_id" || field == "role"
}

func rejectCallerAuthorityQuery(w http.ResponseWriter, r *http.Request) bool {
	for field := range r.URL.Query() {
		if isCallerAuthorityField(strings.ToLower(field)) {
			writeLocalError(w, r, http.StatusBadRequest, "CALLER_AUTHORITY_FORBIDDEN", "authentication and identity fields are managed by the local backend")
			return true
		}
	}
	return false
}

func callLocalIncidentListAgent(w http.ResponseWriter, r *http.Request, cfg config.Config, factory func() (keychain.Store, error), req *agentv1.IncidentListRequest, resolver ...agentConfigResolver) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if pat := optionalPAT(factory); pat != "" {
		ctx = agentclient.WithPAT(ctx, pat)
	}
	agentCfg, err := resolveAgentSnapshot(cfg, resolver...)
	if err != nil {
		writeLocalAgentIncidentError(w, r, err)
		return
	}
	resp, err := agentclient.New(agentCfg).ListIncidents(ctx, req)
	if err != nil {
		writeLocalAgentIncidentError(w, r, err)
		return
	}
	w.Header().Set("content-type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"source": "agent", "payload_policy": "incident records contain factual Agent runtime state only", "incidents": resp.Incidents})
}

func callLocalIncidentGetAgent(w http.ResponseWriter, r *http.Request, cfg config.Config, factory func() (keychain.Store, error), req *agentv1.IncidentGetRequest, resolver ...agentConfigResolver) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if pat := optionalPAT(factory); pat != "" {
		ctx = agentclient.WithPAT(ctx, pat)
	}
	agentCfg, err := resolveAgentSnapshot(cfg, resolver...)
	if err != nil {
		writeLocalAgentIncidentError(w, r, err)
		return
	}
	resp, err := agentclient.New(agentCfg).GetIncident(ctx, req)
	if err != nil {
		writeLocalAgentIncidentError(w, r, err)
		return
	}
	w.Header().Set("content-type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"source": "agent", "payload_policy": "incident records contain factual Agent runtime state only", "incident": resp})
}

func callLocalIncidentEvidenceAgent(w http.ResponseWriter, r *http.Request, cfg config.Config, factory func() (keychain.Store, error), req *agentv1.IncidentGetRequest, resolver ...agentConfigResolver) {
	ctx, cancel := context.WithTimeout(r.Context(), incidentEvidenceOperationTimeout)
	defer cancel()
	pat, err := resolvePAT("", factory)
	if err != nil {
		writeLocalAgentIncidentError(w, r, err)
		return
	}
	ctx = agentclient.WithPAT(ctx, pat)
	agentCfg, err := resolveAgentSnapshot(cfg, resolver...)
	if err != nil {
		writeLocalAgentIncidentError(w, r, err)
		return
	}
	response, err := agentclient.New(agentCfg).GetIncidentEvidence(ctx, req)
	if err != nil {
		writeLocalAgentIncidentError(w, r, err)
		return
	}
	body, err := json.Marshal(response)
	if err != nil || len(body) > incidentEvidenceResponseLimit {
		writeLocalError(w, r, http.StatusBadGateway, "INCIDENT_EVIDENCE_INVALID", "Agent incident evidence response is invalid")
		return
	}
	w.Header().Set("content-type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(append(body, '\n'))
}

func callLocalIncidentResolveAgent(w http.ResponseWriter, r *http.Request, cfg config.Config, factory func() (keychain.Store, error), req *agentv1.IncidentResolveRequest, resolver ...agentConfigResolver) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if pat := optionalPAT(factory); pat != "" {
		ctx = agentclient.WithPAT(ctx, pat)
	}
	agentCfg, err := resolveAgentSnapshot(cfg, resolver...)
	if err != nil {
		writeLocalAgentIncidentError(w, r, err)
		return
	}
	resp, err := agentclient.New(agentCfg).ResolveIncident(ctx, req)
	if err != nil {
		writeLocalAgentIncidentError(w, r, err)
		return
	}
	w.Header().Set("content-type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":         "resolved",
		"source":         "agent",
		"payload_policy": "incident records contain factual Agent runtime state only",
		"incident":       resp,
	})
}

func writeLocalAgentIncidentError(w http.ResponseWriter, r *http.Request, err error) {
	statusCode := http.StatusBadGateway
	code := "AGENT_INCIDENT_OPERATION_FAILED"
	if _, ok := err.(agentConfigReloadError); ok {
		writeLocalError(w, r, http.StatusBadGateway, "AGENT_CONFIG_RELOAD_FAILED", "Agent configuration is unavailable")
		return
	}
	switch grpcstatus.Code(err) {
	case codes.InvalidArgument:
		statusCode, code = http.StatusBadRequest, "INVALID_INCIDENT_REQUEST"
	case codes.Unauthenticated:
		statusCode, code = http.StatusUnauthorized, "AGENT_AUTH_REQUIRED"
	case codes.PermissionDenied:
		statusCode, code = http.StatusForbidden, "INCIDENT_ACCESS_DENIED"
	case codes.FailedPrecondition:
		statusCode, code = http.StatusPreconditionFailed, "INCIDENT_PRECONDITION_FAILED"
	case codes.ResourceExhausted:
		statusCode, code = http.StatusBadGateway, "INCIDENT_EVIDENCE_TOO_LARGE"
	case codes.NotFound:
		statusCode, code = http.StatusNotFound, "INCIDENT_NOT_FOUND"
	case codes.Unimplemented:
		statusCode, code = http.StatusNotImplemented, "AGENT_INCIDENT_UNSUPPORTED"
	case codes.DeadlineExceeded:
		statusCode, code = http.StatusGatewayTimeout, "AGENT_INCIDENT_TIMEOUT"
	case codes.Unavailable:
		statusCode, code = http.StatusBadGateway, "AGENT_UNAVAILABLE"
	}
	writeLocalError(w, r, statusCode, code, "Agent incident operation failed")
}

func localTelemetry(w http.ResponseWriter, r *http.Request, cfg config.Config, factory func() (keychain.Store, error), resolver ...agentConfigResolver) bool {
	req, view, ok := localTelemetryRequest(w, r)
	if !ok {
		return false
	}
	if req == nil {
		return true
	}
	if r.Method != http.MethodGet {
		writeLocalError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method is not allowed")
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if pat := optionalPAT(factory); pat != "" {
		ctx = agentclient.WithPAT(ctx, pat)
	}
	agentCfg, err := resolveAgentSnapshot(cfg, resolver...)
	if err != nil {
		writeLocalAgentTelemetryError(w, r, err)
		return true
	}
	resp, err := agentclient.New(agentCfg).QueryTelemetry(ctx, req)
	w.Header().Set("content-type", "application/json")
	if err != nil {
		writeLocalAgentTelemetryError(w, r, err)
		return true
	}
	sanitizeTelemetryResponse(resp)
	if view == "summary" && resp.Summary != nil {
		_ = json.NewEncoder(w).Encode(telemetrySummary{
			ProjectID:     resp.ProjectID,
			SinceUnix:     resp.Summary.SinceUnix,
			RecordCount:   int(resp.Summary.MetricCount + resp.Summary.LogCount),
			StartUnix:     resp.Summary.SinceUnix,
			EndUnix:       resp.Summary.EndUnix,
			Done:          true,
			Source:        "agent",
			PayloadPolicy: resp.PayloadPolicy,
			Health:        resp.Summary.Health,
			MetricCount:   int(resp.Summary.MetricCount),
			LogCount:      int(resp.Summary.LogCount),
			ErrorCount:    int(resp.Summary.ErrorCount),
			ServiceCount:  int(resp.Summary.ServiceCount),
		})
		return true
	}
	_ = json.NewEncoder(w).Encode(resp)
	return true
}

type telemetrySummary struct {
	ProjectID     string `json:"project_id"`
	SinceUnix     int64  `json:"since_unix"`
	ChunkCount    int    `json:"chunk_count"`
	RecordCount   int    `json:"record_count"`
	StartUnix     int64  `json:"start_unix"`
	EndUnix       int64  `json:"end_unix"`
	Done          bool   `json:"done"`
	Source        string `json:"source"`
	PayloadPolicy string `json:"payload_policy"`
	Health        string `json:"health,omitempty"`
	MetricCount   int    `json:"metric_count,omitempty"`
	LogCount      int    `json:"log_count,omitempty"`
	ErrorCount    int    `json:"error_count,omitempty"`
	ServiceCount  int    `json:"service_count,omitempty"`
}

func localTelemetryRequest(w http.ResponseWriter, r *http.Request) (*agentv1.TelemetryQueryRequest, string, bool) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 5 || parts[0] != "api" || parts[1] != "local" || parts[2] != "projects" {
		return nil, "", false
	}
	projectID, err := url.PathUnescape(parts[3])
	if err != nil || projectID == "" {
		writeLocalError(w, r, http.StatusBadRequest, "PROJECT_ID_REQUIRED", "project_id is required")
		return nil, "", true
	}
	query := r.URL.Query()
	sinceUnix, _ := strconv.ParseInt(query.Get("since_unix"), 10, 64)
	limit, _ := strconv.ParseInt(query.Get("limit"), 10, 32)
	req := &agentv1.TelemetryQueryRequest{ProjectID: projectID, SinceUnix: sinceUnix, Cursor: query.Get("cursor"), Limit: int32(limit)}
	switch {
	case len(parts) == 6 && parts[4] == "telemetry" && parts[5] == "summary":
		req.IncludeSummary = true
		req.IncludeServices = true
		return req, "summary", true
	case len(parts) == 7 && parts[4] == "telemetry" && parts[5] == "services":
		serviceID, err := url.PathUnescape(parts[6])
		if err != nil || serviceID == "" {
			writeLocalError(w, r, http.StatusBadRequest, "SERVICE_ID_REQUIRED", "service_id is required")
			return nil, "", true
		}
		req.ServiceID = serviceID
		req.IncludeSummary = true
		req.IncludeServices = true
		return req, "service", true
	case len(parts) == 5 && parts[4] == "logs":
		req.ServiceID = query.Get("service_id")
		req.IncludeLogs = true
		return req, "logs", true
	case len(parts) == 6 && parts[4] == "logs" && parts[5] == "query":
		req.ServiceID = query.Get("service_id")
		req.IncludeLogs = true
		return req, "logs", true
	}
	if parts[4] == "telemetry" || parts[4] == "logs" {
		writeLocalError(w, r, http.StatusNotImplemented, "TELEMETRY_OPERATION_UNSUPPORTED", "this telemetry/logs operation is not supported by the local Agent API")
		return nil, "", true
	}
	return nil, "", false
}

func writeLocalAgentTelemetryError(w http.ResponseWriter, r *http.Request, err error) {
	statusCode := http.StatusBadGateway
	code := "AGENT_TELEMETRY_UNAVAILABLE"
	if _, ok := err.(agentConfigReloadError); ok {
		writeLocalError(w, r, http.StatusBadGateway, "AGENT_CONFIG_RELOAD_FAILED", "Agent configuration is unavailable")
		return
	}
	switch grpcstatus.Code(err) {
	case codes.InvalidArgument:
		statusCode, code = http.StatusBadRequest, "INVALID_TELEMETRY_REQUEST"
	case codes.Unauthenticated:
		statusCode, code = http.StatusUnauthorized, "AGENT_AUTH_REQUIRED"
	case codes.PermissionDenied:
		statusCode, code = http.StatusForbidden, "TELEMETRY_ACCESS_DENIED"
	case codes.Unimplemented:
		statusCode, code = http.StatusNotImplemented, "AGENT_TELEMETRY_UNSUPPORTED"
	case codes.DeadlineExceeded:
		statusCode, code = http.StatusGatewayTimeout, "AGENT_TELEMETRY_TIMEOUT"
	case codes.Unavailable:
		statusCode, code = http.StatusBadGateway, "AGENT_UNAVAILABLE"
	}
	writeLocalError(w, r, statusCode, code, "Agent telemetry operation failed")
}

func sanitizeTelemetryResponse(resp *agentv1.TelemetryQueryResponse) {
	if resp == nil {
		return
	}
	resp.PayloadPolicy = localTelemetryPayloadPolicy(resp.PayloadPolicy)
	for i := range resp.Logs {
		resp.Logs[i].Message = redactLocalTelemetryText(resp.Logs[i].Message)
	}
}

func localTelemetryPayloadPolicy(value string) string {
	if value != "" {
		return value
	}
	return "raw logs and raw metric streams remain Agent-local; browser responses are redacted summaries/windows"
}

var localTelemetryRedactors = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`(?i)(authorization:\s*bearer\s+)[^\s,;]+`), `${1}[REDACTED]`},
	{regexp.MustCompile(`(?i)(password|passwd|pwd|token|pat|api[_-]?key|secret|authorization|bearer)\s*[:=]\s*("[^"]+"|'[^']+'|[^\s,;]+)`), `$1=[REDACTED]`},
	{regexp.MustCompile(`-----BEGIN [^-]*PRIVATE KEY-----[\s\S]*?-----END [^-]*PRIVATE KEY-----`), `[REDACTED_PRIVATE_KEY]`},
}

func redactLocalTelemetryText(value string) string {
	out := value
	for _, redactor := range localTelemetryRedactors {
		out = redactor.re.ReplaceAllString(out, redactor.repl)
	}
	return strings.ReplaceAll(out, "kubeconfig", "[REDACTED]")
}

func newLocalSessionToken() string {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("local-%d", time.Now().UnixNano())
	}
	return base64.RawURLEncoding.EncodeToString(raw[:])
}

func newUIHandler(uiDir, devUI string) http.Handler {
	if devUI != "" {
		target, err := url.Parse(devUI)
		if err != nil || target.Scheme == "" || target.Host == "" {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "invalid --dev-ui URL", http.StatusBadRequest)
			})
		}
		return httputil.NewSingleHostReverseProxy(target)
	}
	if _, err := os.Stat(filepath.Join(uiDir, "index.html")); err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "Opsi UI build not found. Run `npm run build` in cli/ui first.", http.StatusServiceUnavailable)
		})
	}
	files := http.FileServer(http.Dir(uiDir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/" || strings.HasSuffix(r.URL.Path, ".html"):
			w.Header().Set("Cache-Control", "no-store")
		case strings.HasPrefix(r.URL.Path, "/_next/static/"):
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		default:
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}

func resolveUIDir() string {
	if dir := os.Getenv("OPSI_UI_DIR"); dir != "" {
		return dir
	}
	candidates := []string{}
	if executable, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(executable), "opsi-ui"))
	}
	for _, dir := range candidates {
		if _, err := os.Stat(filepath.Join(dir, "index.html")); err == nil {
			return dir
		}
	}
	if len(candidates) != 0 {
		return candidates[0]
	}
	return "opsi-ui"
}
