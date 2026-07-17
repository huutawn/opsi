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

	"github.com/opsi-dev/opsi/cli/internal/agentclient"
	"github.com/opsi-dev/opsi/cli/internal/config"
	"github.com/opsi-dev/opsi/cli/internal/keychain"
	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
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
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	defer listener.Close()

	server := &http.Server{
		Handler:           newStartMux(resolveUIDir(), devUI, cfg, factory),
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

func newStartMux(uiDir, devUI string, cfg config.Config, factory func() (keychain.Store, error)) *http.ServeMux {
	localSession := newLocalSessionToken()
	authFlow := &localAuthFlow{states: map[string]time.Time{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","service":"opsi-cli"}`))
	})
	mux.HandleFunc("/api/local/session/login", func(w http.ResponseWriter, r *http.Request) {
		startLocalBrowserLogin(w, r, cfg, authFlow)
	})
	mux.HandleFunc("/api/local/session/login/start", func(w http.ResponseWriter, r *http.Request) {
		startLocalBrowserLogin(w, r, cfg, authFlow)
	})
	mux.HandleFunc("/api/local/session/callback", func(w http.ResponseWriter, r *http.Request) {
		completeLocalBrowserLogin(w, r, cfg, factory, authFlow)
	})
	mux.HandleFunc("/api/local/session/logout", func(w http.ResponseWriter, r *http.Request) {
		if !requireLocalSession(w, r, localSession) {
			return
		}
		logoutLocalSession(w, r, cfg, factory)
	})
	mux.HandleFunc("/api/local/session/token/rotate", func(w http.ResponseWriter, r *http.Request) {
		if !requireLocalSession(w, r, localSession) {
			return
		}
		rotateLocalPAT(w, r, cfg, factory)
	})
	mux.HandleFunc("/api/local/session/token/revoke", func(w http.ResponseWriter, r *http.Request) {
		if !requireLocalSession(w, r, localSession) {
			return
		}
		logoutLocalSession(w, r, cfg, factory)
	})
	mux.HandleFunc("/api/local/session", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			startLocalBrowserLogin(w, r, cfg, authFlow)
			return
		}
		if r.Method != http.MethodGet {
			writeLocalError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method is not allowed")
			return
		}
		w.Header().Set("content-type", "application/json")
		authenticated := optionalPAT(factory) != ""
		_ = json.NewEncoder(w).Encode(map[string]any{
			"authenticated":   authenticated,
			"cloud_connected": "unknown",
			"agent_connected": probeAgent(r.Context(), cfg, factory),
			"token_status":    tokenStatus(factory),
			"local_session":   localSession,
			"capabilities":    []string{"projects", "nodes", "services", "deployments", "secrets", "telemetry", "logs", "incidents", "audit", "support"},
		})
	})
	mux.HandleFunc("/api/local/status", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 800*time.Millisecond)
		defer cancel()
		ctx = agentclient.WithPAT(ctx, optionalPAT(factory))
		status, err := agentclient.New(cfg).Status(ctx)
		w.Header().Set("content-type", "application/json")
		if err != nil {
			writeLocalError(w, r, http.StatusBadGateway, "AGENT_UNAVAILABLE", "Agent status is unavailable")
			return
		}
		_ = json.NewEncoder(w).Encode(status)
	})
	mux.HandleFunc("/api/local/", func(w http.ResponseWriter, r *http.Request) {
		proxyLocalRegistry(w, r, cfg, factory, localSession)
	})
	mux.Handle("/", newUIHandler(uiDir, devUI))
	return mux
}

type localAuthFlow struct {
	mu     sync.Mutex
	states map[string]time.Time
}

func startLocalBrowserLogin(w http.ResponseWriter, r *http.Request, cfg config.Config, flow *localAuthFlow) {
	if r.Method != http.MethodPost {
		writeLocalError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method is not allowed")
		return
	}
	var body struct {
		ProjectID string `json:"project_id"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body)
	state := newLocalSessionToken()
	callback := "http://" + r.Host + "/api/local/session/callback"
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
	flow.states[state] = time.Now().UTC().Add(5 * time.Minute)
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
	if r.Header.Get("Idempotency-Key") == "" {
		writeLocalError(w, r, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "mutating local requests require Idempotency-Key")
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
	flow.mu.Lock()
	expiresAt, ok := flow.states[state]
	if ok {
		delete(flow.states, state)
	}
	flow.mu.Unlock()
	if !ok || time.Now().UTC().After(expiresAt) || code == "" {
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
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil || out.Token == "" {
		writeLocalError(w, r, http.StatusBadGateway, "AUTH_REDEEM_FAILED", "Cloud auth response was invalid")
		return
	}
	store, err := storeFromFactory(factory)
	if err != nil || store.SetPAT(out.Token) != nil {
		writeLocalError(w, r, http.StatusInternalServerError, "LOCAL_CREDENTIAL_STORE_FAILED", "could not store credential in OS keychain")
		return
	}
	http.Redirect(w, r, "/?auth=ok", http.StatusFound)
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

func probeCloud(ctx context.Context, cloudURL string) string {
	ctx, cancel := context.WithTimeout(ctx, 800*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(cloudURL, "/")+"/health", nil)
	if err != nil {
		return "unknown"
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "failed"
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return "ok"
	}
	return "failed"
}

func probeAgent(ctx context.Context, cfg config.Config, factory func() (keychain.Store, error)) string {
	ctx, cancel := context.WithTimeout(ctx, 800*time.Millisecond)
	defer cancel()
	ctx = agentclient.WithPAT(ctx, optionalPAT(factory))
	if _, err := agentclient.New(cfg).Status(ctx); err != nil {
		return "failed"
	}
	return "ok"
}

func tokenStatus(factory func() (keychain.Store, error)) string {
	if optionalPAT(factory) == "" {
		return "missing"
	}
	return "present"
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

func writeLocalJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func proxyLocalRegistry(w http.ResponseWriter, r *http.Request, cfg config.Config, factory func() (keychain.Store, error), localSession string) {
	if isMutation(r.Method) {
		if r.Header.Get("X-Local-Session") != localSession {
			writeLocalError(w, r, http.StatusUnauthorized, "LOCAL_SESSION_REQUIRED", "mutating local requests require X-Local-Session")
			return
		}
		if r.Header.Get("Idempotency-Key") == "" {
			writeLocalError(w, r, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "mutating local requests require Idempotency-Key")
			return
		}
	}
	if localTelemetry(w, r, cfg, factory) {
		return
	}
	if localSecretOperation(w, r, cfg, factory) {
		return
	}
	if localIncidentOperation(w, r, cfg, factory) {
		return
	}
	cloud, err := url.Parse(cfg.CloudURL)
	if err != nil || cloud.Scheme == "" || cloud.Host == "" {
		writeLocalError(w, r, http.StatusBadGateway, "INVALID_CLOUD_URL", "local cloud_url is invalid")
		return
	}
	path, rawQuery, err := localToCloudPath(r.URL)
	if err != nil {
		writeLocalError(w, r, http.StatusNotFound, "LOCAL_ROUTE_NOT_FOUND", err.Error())
		return
	}
	target := *cloud
	target.Path = path
	target.RawQuery = rawQuery

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if projectID, serviceID, ok := localDeploymentIDs(r.URL.Path, r.Method); ok {
		sourceType, found, err := fetchCloudServiceSourceType(ctx, *cloud, projectID, serviceID, r.Header, factory)
		if err != nil {
			writeLocalError(w, r, http.StatusBadGateway, "LOCAL_DEPLOY_VALIDATION_FAILED", "could not validate service deployment source")
			return
		}
		if found && sourceType == "image" {
			writeLocalError(w, r, http.StatusBadRequest, "IMAGE_DEPLOY_NOT_SUPPORTED", "Image-source deploy is not supported by the current Agent runner. Use Git source or enable the image deploy capability.")
			return
		}
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, target.String(), r.Body)
	if err != nil {
		writeLocalError(w, r, http.StatusBadGateway, "LOCAL_PROXY_REQUEST_FAILED", "could not create Cloud request")
		return
	}
	copyProxyHeaders(req.Header, r.Header)
	req.Header.Del("Authorization")
	if pat := optionalPAT(factory); pat != "" {
		req.Header.Set("Authorization", "Bearer "+pat)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeLocalError(w, r, http.StatusBadGateway, "CLOUD_UNAVAILABLE", "Cloud registry is unavailable")
		return
	}
	defer resp.Body.Close()
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	if requestID := r.Header.Get("X-Request-ID"); requestID != "" {
		w.Header().Set("X-Request-ID", requestID)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func localSecretOperation(w http.ResponseWriter, r *http.Request, cfg config.Config, factory func() (keychain.Store, error)) bool {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 5 || parts[0] != "api" || parts[1] != "local" || parts[2] != "projects" || parts[4] != "secrets" {
		return false
	}
	projectID, err := url.PathUnescape(parts[3])
	if err != nil || projectID == "" {
		writeLocalError(w, r, http.StatusBadRequest, "PROJECT_ID_REQUIRED", "project_id is required")
		return true
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		writeLocalError(w, r, http.StatusNotImplemented, "SECRETS_OPERATION_UNSUPPORTED", "this secret operation is not supported by the local Agent API")
		return true
	}
	if len(parts) == 5 {
		req, ok := readLocalSecretRequest(w, r, projectID, "")
		if !ok {
			return true
		}
		callLocalSecretAgent(w, r, cfg, factory, "created", req.SecretRequest, false)
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
		callLocalSecretAgent(w, r, cfg, factory, "revealed", req.SecretRequest, true)
	case "rotate":
		if !req.hasSecondFactor() {
			writeLocalError(w, r, http.StatusBadRequest, "SECRET_SECOND_FACTOR_REQUIRED", "secret rotation requires OTP or TOTP")
			return true
		}
		callLocalSecretAgent(w, r, cfg, factory, "rotated", req.SecretRequest, false)
	default:
		writeLocalError(w, r, http.StatusNotImplemented, "SECRETS_OPERATION_UNSUPPORTED", "this secret operation is not supported by the local Agent API")
	}
	return true
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
		"service_id": true, "name": true, "namespace": true, "user_id": true, "role": true,
		"otp_code": true, "otp_request_id": true, "totp_code": true, "reveal": true, "explicit_intent": true,
	}
	for key := range raw {
		lower := strings.ToLower(key)
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
		UserID:       jsonString(raw, "user_id"),
		Role:         jsonString(raw, "role"),
		OTPCode:      jsonString(raw, "otp_code"),
		OTPRequestID: jsonString(raw, "otp_request_id"),
		TOTPCode:     jsonString(raw, "totp_code"),
	}
	if req.ServiceID == "" || req.Name == "" || req.UserID == "" || req.Role == "" {
		writeLocalError(w, r, http.StatusBadRequest, "SECRET_REQUIRED_FIELDS_MISSING", "service_id, name, user_id and role are required")
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

func callLocalSecretAgent(w http.ResponseWriter, r *http.Request, cfg config.Config, factory func() (keychain.Store, error), statusText string, req *agentv1.SecretRequest, includePassword bool) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if pat := optionalPAT(factory); pat != "" {
		ctx = agentclient.WithPAT(ctx, pat)
	}
	client := agentclient.New(cfg)
	var (
		resp *agentv1.SecretResponse
		err  error
	)
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

func localIncidentOperation(w http.ResponseWriter, r *http.Request, cfg config.Config, factory func() (keychain.Store, error)) bool {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 5 || parts[0] != "api" || parts[1] != "local" || parts[2] != "projects" || parts[4] != "incidents" {
		return false
	}
	projectID, err := url.PathUnescape(parts[3])
	if err != nil || projectID == "" {
		writeLocalError(w, r, http.StatusBadRequest, "PROJECT_ID_REQUIRED", "project_id is required")
		return true
	}
	if r.Method == http.MethodGet {
		req, ok := readLocalIncidentQuery(w, r, projectID)
		if !ok {
			return true
		}
		if len(parts) == 5 {
			callLocalIncidentListAgent(w, r, cfg, factory, req)
			return true
		}
		if len(parts) == 6 {
			incidentID, err := url.PathUnescape(parts[5])
			if err != nil || incidentID == "" {
				writeLocalError(w, r, http.StatusBadRequest, "INCIDENT_ID_REQUIRED", "incident_id is required")
				return true
			}
			callLocalIncidentGetAgent(w, r, cfg, factory, &agentv1.IncidentGetRequest{ProjectID: projectID, IncidentID: incidentID, UserID: req.UserID, Role: req.Role})
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
	callLocalIncidentResolveAgent(w, r, cfg, factory, &agentv1.IncidentResolveRequest{ProjectID: req.ProjectID, IncidentID: req.IncidentID, UserID: req.UserID, Role: req.Role})
	return true
}

type localIncidentRequest struct {
	ProjectID  string
	IncidentID string
	UserID     string
	Role       string
}

func readLocalIncidentQuery(w http.ResponseWriter, r *http.Request, projectID string) (*agentv1.IncidentListRequest, bool) {
	query := r.URL.Query()
	limit, _ := strconv.ParseInt(query.Get("limit"), 10, 32)
	req := &agentv1.IncidentListRequest{ProjectID: projectID, Status: strings.TrimSpace(query.Get("status")), Limit: int32(limit), UserID: strings.TrimSpace(query.Get("user_id")), Role: strings.TrimSpace(query.Get("role"))}
	if req.UserID == "" || req.Role == "" {
		writeLocalError(w, r, http.StatusBadRequest, "INCIDENT_REQUIRED_FIELDS_MISSING", "user_id and role are required")
		return nil, false
	}
	return req, true
}

func readLocalIncidentRequest(w http.ResponseWriter, r *http.Request, projectID, incidentID string) (localIncidentRequest, bool) {
	var raw map[string]json.RawMessage
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&raw); err != nil {
		writeLocalError(w, r, http.StatusBadRequest, "INVALID_INCIDENT_REQUEST", "invalid incident request")
		return localIncidentRequest{}, false
	}
	allowed := map[string]bool{"user_id": true, "role": true}
	for key := range raw {
		if !allowed[strings.ToLower(key)] {
			writeLocalError(w, r, http.StatusBadRequest, "INCIDENT_INPUT_UNSUPPORTED", "incident request contains an unsupported field")
			return localIncidentRequest{}, false
		}
	}
	req := localIncidentRequest{ProjectID: projectID, IncidentID: incidentID, UserID: jsonString(raw, "user_id"), Role: jsonString(raw, "role")}
	if req.UserID == "" || req.Role == "" {
		writeLocalError(w, r, http.StatusBadRequest, "INCIDENT_REQUIRED_FIELDS_MISSING", "user_id and role are required")
		return localIncidentRequest{}, false
	}
	return req, true
}

func callLocalIncidentListAgent(w http.ResponseWriter, r *http.Request, cfg config.Config, factory func() (keychain.Store, error), req *agentv1.IncidentListRequest) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if pat := optionalPAT(factory); pat != "" {
		ctx = agentclient.WithPAT(ctx, pat)
	}
	resp, err := agentclient.New(cfg).ListIncidents(ctx, req)
	if err != nil {
		writeLocalAgentIncidentError(w, r, err)
		return
	}
	w.Header().Set("content-type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"source": "agent", "payload_policy": "incident records contain factual Agent runtime state only", "incidents": resp.Incidents})
}

func callLocalIncidentGetAgent(w http.ResponseWriter, r *http.Request, cfg config.Config, factory func() (keychain.Store, error), req *agentv1.IncidentGetRequest) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if pat := optionalPAT(factory); pat != "" {
		ctx = agentclient.WithPAT(ctx, pat)
	}
	resp, err := agentclient.New(cfg).GetIncident(ctx, req)
	if err != nil {
		writeLocalAgentIncidentError(w, r, err)
		return
	}
	w.Header().Set("content-type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"source": "agent", "payload_policy": "incident records contain factual Agent runtime state only", "incident": resp})
}

func callLocalIncidentResolveAgent(w http.ResponseWriter, r *http.Request, cfg config.Config, factory func() (keychain.Store, error), req *agentv1.IncidentResolveRequest) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if pat := optionalPAT(factory); pat != "" {
		ctx = agentclient.WithPAT(ctx, pat)
	}
	resp, err := agentclient.New(cfg).ResolveIncident(ctx, req)
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
	switch grpcstatus.Code(err) {
	case codes.InvalidArgument:
		statusCode, code = http.StatusBadRequest, "INVALID_INCIDENT_REQUEST"
	case codes.Unauthenticated:
		statusCode, code = http.StatusUnauthorized, "AGENT_AUTH_REQUIRED"
	case codes.PermissionDenied:
		statusCode, code = http.StatusForbidden, "INCIDENT_ACCESS_DENIED"
	case codes.FailedPrecondition:
		statusCode, code = http.StatusPreconditionFailed, "INCIDENT_PRECONDITION_FAILED"
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

func localTelemetry(w http.ResponseWriter, r *http.Request, cfg config.Config, factory func() (keychain.Store, error)) bool {
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
	resp, err := agentclient.New(cfg).QueryTelemetry(ctx, req)
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

func localDeploymentIDs(path, method string) (string, string, bool) {
	if method != http.MethodPost {
		return "", "", false
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 7 || parts[0] != "api" || parts[1] != "local" || parts[2] != "projects" || parts[4] != "services" || parts[6] != "deployments" {
		return "", "", false
	}
	return parts[3], parts[5], true
}

func fetchCloudServiceSourceType(ctx context.Context, cloud url.URL, projectID, serviceID string, headers http.Header, factory func() (keychain.Store, error)) (string, bool, error) {
	cloud.Path = "/api/projects/" + url.PathEscape(projectID) + "/services"
	cloud.RawQuery = ""
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cloud.String(), nil)
	if err != nil {
		return "", false, err
	}
	copyProxyHeaders(req.Header, headers)
	req.Header.Del("Authorization")
	if pat := optionalPAT(factory); pat != "" {
		req.Header.Set("Authorization", "Bearer "+pat)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false, fmt.Errorf("service list status %d", resp.StatusCode)
	}
	var payload struct {
		Services []struct {
			ID         string `json:"id"`
			SourceType string `json:"source_type"`
		} `json:"services"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", false, err
	}
	for _, service := range payload.Services {
		if service.ID == serviceID {
			return service.SourceType, true, nil
		}
	}
	return "", false, nil
}

func localToCloudPath(u *url.URL) (string, string, error) {
	path := strings.TrimPrefix(u.Path, "/api/local")
	if path == "/projects" || path == "/projects/" {
		q := u.Query()
		orgID := q.Get("org_id")
		if orgID == "" {
			orgID = "org-1"
		}
		q.Del("org_id")
		return "/api/orgs/" + url.PathEscape(orgID) + "/projects", q.Encode(), nil
	}
	if strings.HasPrefix(path, "/projects/") && strings.HasSuffix(path, "/nodes/bootstrap") {
		return "/api" + strings.TrimSuffix(path, "/nodes/bootstrap") + "/bootstrap-sessions", u.RawQuery, nil
	}
	if strings.HasPrefix(path, "/projects/") {
		return "/api" + path, u.RawQuery, nil
	}
	return "", "", fmt.Errorf("local route %s is not implemented", u.Path)
}

func isMutation(method string) bool {
	return method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch || method == http.MethodDelete
}

func copyProxyHeaders(dst, src http.Header) {
	for _, key := range []string{"Content-Type", "X-Request-ID", "Idempotency-Key"} {
		if value := src.Get(key); value != "" {
			dst.Set(key, value)
		}
	}
}

func writeLocalError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	requestID := r.Header.Get("X-Request-ID")
	w.Header().Set("content-type", "application/json")
	if requestID != "" {
		w.Header().Set("X-Request-ID", requestID)
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": message, "retryable": status >= 500, "request_id": requestID}})
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
	return http.FileServer(http.Dir(uiDir))
}

func resolveUIDir() string {
	if dir := os.Getenv("OPSI_UI_DIR"); dir != "" {
		return dir
	}
	candidates := []string{"ui/out", "cli/ui/out"}
	if _, file, _, ok := runtime.Caller(0); ok {
		candidates = append(candidates, filepath.Join(filepath.Dir(file), "..", "..", "ui", "out"))
	}
	for _, dir := range candidates {
		if _, err := os.Stat(filepath.Join(dir, "index.html")); err == nil {
			return dir
		}
	}
	return candidates[0]
}
