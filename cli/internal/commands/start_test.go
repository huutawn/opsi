package commands

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opsi-dev/opsi/cli/internal/config"
	"github.com/opsi-dev/opsi/cli/internal/keychain"
	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
)

func TestStartMuxServesHealthAndBuiltUI(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html><title>Opsi Console</title>"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(newStartMux(dir, "", config.Default(), nil))
	defer server.Close()

	res, err := http.Get(server.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d", res.StatusCode)
	}
	_ = res.Body.Close()

	res, err = http.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("ui status = %d", res.StatusCode)
	}
}

func TestStartMuxReportsMissingUIBuild(t *testing.T) {
	server := httptest.NewServer(newStartMux(filepath.Join(t.TempDir(), "missing"), "", config.Default(), nil))
	defer server.Close()

	res, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", res.StatusCode)
	}
}

func TestStartMuxLocalStatusReportsAgentUnavailable(t *testing.T) {
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: "127.0.0.1:1", CloudURL: "http://127.0.0.1:9800"}, nil))
	defer server.Close()
	res, err := http.Get(server.URL + "/api/local/status")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d", res.StatusCode)
	}
}

func TestStartMuxProxiesDevUI(t *testing.T) {
	dev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("dev-ui"))
	}))
	defer dev.Close()
	server := httptest.NewServer(newStartMux(t.TempDir(), dev.URL, config.Default(), nil))
	defer server.Close()
	res, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if string(body) != "dev-ui" {
		t.Fatalf("body = %q", body)
	}
}

func TestLocalRegistryProxyUsesKeychainPAT(t *testing.T) {
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/orgs/org-1/projects" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer keychain-pat" {
			t.Fatalf("authorization = %q", got)
		}
		_, _ = w.Write([]byte(`{"projects":[]}`))
	}))
	defer cloud.Close()
	store := keychain.NewFakeStore()
	if err := store.SetPAT("keychain-pat"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: "127.0.0.1:1", CloudURL: cloud.URL}, func() (keychain.Store, error) {
		return store, nil
	}))
	defer server.Close()

	req, err := http.NewRequest(http.MethodGet, server.URL+"/api/local/projects?org_id=org-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer browser-pat")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
}

func TestLocalMutationRequiresSession(t *testing.T) {
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("Cloud should not receive unauthenticated local mutation")
	}))
	defer cloud.Close()
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: "127.0.0.1:1", CloudURL: cloud.URL}, nil))
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/local/projects?org_id=org-1", bytes.NewReader([]byte(`{"name":"Demo"}`)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Idempotency-Key", "project-1")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", res.StatusCode)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "LOCAL_SESSION_REQUIRED" {
		t.Fatalf("code = %q", body.Error.Code)
	}
}

func TestLocalMutationWithSessionProxies(t *testing.T) {
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Idempotency-Key"); got != "project-1" {
			t.Fatalf("idempotency = %q", got)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"proj_1"}`))
	}))
	defer cloud.Close()
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: "127.0.0.1:1", CloudURL: cloud.URL}, nil))
	defer server.Close()

	var session struct {
		LocalSession string `json:"local_session"`
	}
	res, err := http.Get(server.URL + "/api/local/session")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewDecoder(res.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if session.LocalSession == "" {
		t.Fatal("empty local session")
	}

	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/local/projects?org_id=org-1", bytes.NewReader([]byte(`{"name":"Demo"}`)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Idempotency-Key", "project-1")
	req.Header.Set("X-Local-Session", session.LocalSession)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d", res.StatusCode)
	}
}

func TestLocalDeploymentRejectsImageBeforeCloudPost(t *testing.T) {
	cloudPost := false
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/projects/proj-1/services" {
			_, _ = w.Write([]byte(`{"services":[{"id":"svc-1","source_type":"image"}]}`))
			return
		}
		if r.Method == http.MethodPost {
			cloudPost = true
		}
		t.Fatalf("unexpected Cloud request: %s %s", r.Method, r.URL.Path)
	}))
	defer cloud.Close()
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: "127.0.0.1:1", CloudURL: cloud.URL}, nil))
	defer server.Close()

	var session struct {
		LocalSession string `json:"local_session"`
	}
	res, err := http.Get(server.URL + "/api/local/session")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewDecoder(res.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/local/projects/proj-1/services/svc-1/deployments", bytes.NewReader([]byte(`{"requested_by":"ui"}`)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Idempotency-Key", "dep-1")
	req.Header.Set("X-Local-Session", session.LocalSession)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", res.StatusCode)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "IMAGE_DEPLOY_NOT_SUPPORTED" || cloudPost {
		t.Fatalf("code=%q cloudPost=%v", body.Error.Code, cloudPost)
	}
}

func TestLocalDisabledAgentEndpointIsTyped(t *testing.T) {
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Default(), nil))
	defer server.Close()
	res, err := http.Get(server.URL + "/api/local/projects/proj_1/secrets")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d", res.StatusCode)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "SECRETS_LOCAL_API_NOT_IMPLEMENTED" {
		t.Fatalf("code = %q", body.Error.Code)
	}
}

func TestLocalTelemetrySummaryUsesAgentAndHidesRawPayload(t *testing.T) {
	agent := &localTelemetryServer{}
	agentAddr, stop := startCommandTelemetryServer(t, agent)
	defer stop()
	cloudCalled := false
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		cloudCalled = true
		w.WriteHeader(http.StatusTeapot)
	}))
	defer cloud.Close()
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: agentAddr, CloudURL: cloud.URL}, nil))
	defer server.Close()

	res, err := http.Get(server.URL + "/api/local/projects/proj-1/telemetry/summary?since_unix=41")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.StatusCode, body)
	}
	if cloudCalled {
		t.Fatal("telemetry summary must not call Cloud")
	}
	if strings.Contains(string(body), "raw-metric-password") {
		t.Fatalf("response leaked raw payload: %s", body)
	}
	var summary struct {
		ProjectID   string `json:"project_id"`
		SinceUnix   int64  `json:"since_unix"`
		ChunkCount  int    `json:"chunk_count"`
		RecordCount int    `json:"record_count"`
		Source      string `json:"source"`
	}
	if err := json.Unmarshal(body, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.ProjectID != "proj-1" || summary.SinceUnix != 41 || summary.ChunkCount != 1 || summary.RecordCount != 3 || summary.Source != "agent" {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	if agent.lastReceivedUnix != 41 {
		t.Fatalf("agent since_unix = %d", agent.lastReceivedUnix)
	}
}

type localTelemetryServer struct {
	lastReceivedUnix int64
}

func (s *localTelemetryServer) Sync(req *agentv1.SyncRequest, stream agentv1.TelemetryService_SyncServer) error {
	s.lastReceivedUnix = req.LastReceivedUnix
	return stream.Send(&agentv1.SyncChunk{
		ProjectID:   req.ProjectID,
		StartUnix:   req.LastReceivedUnix,
		EndUnix:     99,
		RecordCount: 3,
		Compression: "zstd",
		Payload:     []byte("raw-metric-password=secret"),
		Done:        true,
	})
}

func TestLocalSessionDoesNotExposePAT(t *testing.T) {
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Default(), nil))
	defer server.Close()
	res, err := http.Get(server.URL + "/api/local/session")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(body)), "pat") {
		t.Fatalf("session leaked PAT: %s", body)
	}
}

func TestResolveUIDirUsesEnv(t *testing.T) {
	t.Setenv("OPSI_UI_DIR", "/tmp/opsi-ui")
	if got := resolveUIDir(); !strings.HasSuffix(got, "opsi-ui") {
		t.Fatalf("dir = %q", got)
	}
}
