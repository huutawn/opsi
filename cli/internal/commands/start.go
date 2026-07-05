package commands

import (
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
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/opsi-dev/opsi/cli/internal/agentclient"
	"github.com/opsi-dev/opsi/cli/internal/config"
	"github.com/opsi-dev/opsi/cli/internal/keychain"
	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
	"github.com/spf13/cobra"
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
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","service":"opsi-cli"}`))
	})
	mux.HandleFunc("/api/local/session/login", func(w http.ResponseWriter, r *http.Request) {
		writeLocalError(w, r, http.StatusNotImplemented, "LOGIN_NOT_IMPLEMENTED", "Browser login is not implemented; run opsi login to store a PAT in the OS keychain")
	})
	mux.HandleFunc("/api/local/session", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			writeLocalError(w, r, http.StatusNotImplemented, "LOGIN_NOT_IMPLEMENTED", "Browser login is not implemented; run opsi login to store a PAT in the OS keychain")
			return
		}
		if r.Method != http.MethodGet {
			writeLocalError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method is not allowed")
			return
		}
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"authenticated":   optionalPAT(factory) != "",
			"cloud_connected": true,
			"agent_connected": true,
			"local_session":   localSession,
			"capabilities":    []string{"projects", "nodes", "services", "deployments", "audit", "support"},
		})
	})
	mux.HandleFunc("/api/local/status", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 800*time.Millisecond)
		defer cancel()
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
	if localTelemetrySummary(w, r, cfg, factory) {
		return
	}
	cloud, err := url.Parse(cfg.CloudURL)
	if err != nil || cloud.Scheme == "" || cloud.Host == "" {
		writeLocalError(w, r, http.StatusBadGateway, "INVALID_CLOUD_URL", "local cloud_url is invalid")
		return
	}
	path, rawQuery, err := localToCloudPath(r.URL)
	if err != nil {
		if isLocalCapability(path) {
			writeLocalError(w, r, http.StatusNotImplemented, path, err.Error())
			return
		}
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

func localTelemetrySummary(w http.ResponseWriter, r *http.Request, cfg config.Config, factory func() (keychain.Store, error)) bool {
	projectID, ok := telemetrySummaryProjectID(r.URL.Path)
	if !ok {
		return false
	}
	if r.Method != http.MethodGet {
		writeLocalError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method is not allowed")
		return true
	}
	query := r.URL.Query()
	sinceUnix, _ := strconv.ParseInt(query.Get("since_unix"), 10, 64)
	maxChunkBytes, _ := strconv.ParseInt(query.Get("max_chunk_bytes"), 10, 32)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if pat := optionalPAT(factory); pat != "" {
		ctx = agentclient.WithPAT(ctx, pat)
	}
	summary := telemetrySummary{ProjectID: projectID, SinceUnix: sinceUnix, Source: "agent", PayloadPolicy: "raw telemetry payload remains local and is not returned to the browser"}
	err := agentclient.New(cfg).Sync(ctx, &agentv1.SyncRequest{ProjectID: projectID, LastReceivedUnix: sinceUnix, MaxChunkBytes: int32(maxChunkBytes)}, func(chunk *agentv1.SyncChunk) error {
		summary.ChunkCount++
		summary.RecordCount += int(chunk.RecordCount)
		if summary.StartUnix == 0 || chunk.StartUnix < summary.StartUnix {
			summary.StartUnix = chunk.StartUnix
		}
		if chunk.EndUnix > summary.EndUnix {
			summary.EndUnix = chunk.EndUnix
		}
		if chunk.Done {
			summary.Done = true
		}
		return nil
	})
	w.Header().Set("content-type", "application/json")
	if err != nil {
		writeLocalError(w, r, http.StatusBadGateway, "AGENT_TELEMETRY_UNAVAILABLE", "Agent telemetry summary is unavailable")
		return true
	}
	_ = json.NewEncoder(w).Encode(summary)
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
}

func telemetrySummaryProjectID(path string) (string, bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 6 || parts[0] != "api" || parts[1] != "local" || parts[2] != "projects" || parts[4] != "telemetry" || parts[5] != "summary" {
		return "", false
	}
	projectID, err := url.PathUnescape(parts[3])
	return projectID, err == nil && projectID != ""
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
	if disabledCode, ok := disabledLocalCapability(path); ok {
		return disabledCode, "", fmt.Errorf("local endpoint %s is not wired to the Agent yet", u.Path)
	}
	if strings.HasPrefix(path, "/projects/") {
		return "/api" + path, u.RawQuery, nil
	}
	return "", "", fmt.Errorf("local route %s is not implemented", u.Path)
}

func disabledLocalCapability(path string) (string, bool) {
	for _, marker := range []struct {
		fragment string
		code     string
	}{
		{"/secrets", "SECRETS_LOCAL_API_NOT_IMPLEMENTED"},
		{"/incidents", "INCIDENTS_LOCAL_API_NOT_IMPLEMENTED"},
		{"/telemetry", "TELEMETRY_LOCAL_API_NOT_IMPLEMENTED"},
		{"/logs", "LOGS_LOCAL_API_NOT_IMPLEMENTED"},
	} {
		if strings.Contains(path, marker.fragment) {
			return marker.code, true
		}
	}
	return "", false
}

func isLocalCapability(code string) bool {
	return strings.HasSuffix(code, "_LOCAL_API_NOT_IMPLEMENTED")
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
