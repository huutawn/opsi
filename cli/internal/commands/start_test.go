package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opsi-dev/opsi/cli/internal/config"
	"github.com/opsi-dev/opsi/cli/internal/keychain"
	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
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
	if body.Error.Code != "SECRETS_OPERATION_UNSUPPORTED" {
		t.Fatalf("code = %q", body.Error.Code)
	}
}

func TestLocalSecretCreateUsesAgentNotCloudAndRedactsValue(t *testing.T) {
	agent := &localSecretServer{}
	agentAddr, stop := startLocalSecretServer(t, agent)
	defer stop()
	cloudCalled := false
	cloud := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		cloudCalled = true
	}))
	defer cloud.Close()
	store := keychain.NewFakeStore()
	if err := store.SetPAT("keychain-pat"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: agentAddr, CloudURL: cloud.URL}, func() (keychain.Store, error) {
		return store, nil
	}))
	defer server.Close()
	session := localTestSession(t, server.URL)

	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/local/projects/proj-1/secrets", strings.NewReader(`{"service_id":"svc-1","name":"db","namespace":"app","user_id":"owner","role":"Owner"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Local-Session", session)
	req.Header.Set("Idempotency-Key", "secret-create-1")
	res, err := http.DefaultClient.Do(req)
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
		t.Fatal("secret create must not call Cloud")
	}
	if agent.createCalls != 1 || agent.lastReq.ProjectID != "proj-1" || agent.lastAuth != "Bearer keychain-pat" {
		t.Fatalf("agent request not used: calls=%d auth=%q req=%+v", agent.createCalls, agent.lastAuth, agent.lastReq)
	}
	if strings.Contains(string(body), "agent-secret-password") {
		t.Fatalf("create response leaked secret value: %s", body)
	}
}

func TestLocalSecretRejectsBrowserSecretValuesBeforeAgentOrCloud(t *testing.T) {
	agent := &localSecretServer{}
	agentAddr, stop := startLocalSecretServer(t, agent)
	defer stop()
	cloudCalled := false
	cloud := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		cloudCalled = true
	}))
	defer cloud.Close()
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: agentAddr, CloudURL: cloud.URL}, nil))
	defer server.Close()
	session := localTestSession(t, server.URL)

	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/local/projects/proj-1/secrets", strings.NewReader(`{"service_id":"svc-1","name":"db","user_id":"owner","role":"Owner","password":"browser-secret"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Local-Session", session)
	req.Header.Set("Idempotency-Key", "secret-create-raw")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", res.StatusCode, body)
	}
	if cloudCalled || agent.createCalls != 0 {
		t.Fatalf("raw secret reached cloud=%v agent_calls=%d", cloudCalled, agent.createCalls)
	}
	if strings.Contains(string(body), "browser-secret") {
		t.Fatalf("error leaked browser secret: %s", body)
	}
}

func TestLocalSecretRevealRequiresExplicitIntent(t *testing.T) {
	agent := &localSecretServer{}
	agentAddr, stop := startLocalSecretServer(t, agent)
	defer stop()
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: agentAddr, CloudURL: "http://127.0.0.1:1"}, nil))
	defer server.Close()
	session := localTestSession(t, server.URL)

	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/local/projects/proj-1/secrets/db/reveal", strings.NewReader(`{"service_id":"svc-1","user_id":"owner","role":"Owner","totp_code":"123456"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Local-Session", session)
	req.Header.Set("Idempotency-Key", "secret-reveal-1")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if agent.revealCalls != 0 {
		t.Fatalf("reveal reached Agent without explicit intent")
	}
}

func TestLocalSecretRevealUsesAgentWithNoStorePolicy(t *testing.T) {
	agent := &localSecretServer{}
	agentAddr, stop := startLocalSecretServer(t, agent)
	defer stop()
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: agentAddr, CloudURL: "http://127.0.0.1:1"}, nil))
	defer server.Close()
	session := localTestSession(t, server.URL)

	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/local/projects/proj-1/secrets/db/reveal", strings.NewReader(`{"service_id":"svc-1","user_id":"owner","role":"Owner","totp_code":"123456","reveal":true}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Local-Session", session)
	req.Header.Set("Idempotency-Key", "secret-reveal-2")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.StatusCode, body)
	}
	if got := res.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache-control = %q", got)
	}
	if agent.revealCalls != 1 || !strings.Contains(string(body), "agent-secret-password") || !strings.Contains(string(body), `"ttl_seconds":60`) {
		t.Fatalf("unexpected reveal calls=%d body=%s", agent.revealCalls, body)
	}
}

func TestLocalSecretAgentErrorIsRedacted(t *testing.T) {
	agent := &localSecretServer{err: status.Error(codes.Internal, "backend saw agent-secret-password")}
	agentAddr, stop := startLocalSecretServer(t, agent)
	defer stop()
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: agentAddr, CloudURL: "http://127.0.0.1:1"}, nil))
	defer server.Close()
	session := localTestSession(t, server.URL)

	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/local/projects/proj-1/secrets/db/rotate", strings.NewReader(`{"service_id":"svc-1","user_id":"owner","role":"Owner","totp_code":"123456"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Local-Session", session)
	req.Header.Set("Idempotency-Key", "secret-rotate-1")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", res.StatusCode, body)
	}
	if strings.Contains(string(body), "agent-secret-password") {
		t.Fatalf("agent error leaked secret value: %s", body)
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
	if summary.ProjectID != "proj-1" || summary.SinceUnix != 41 || summary.RecordCount != 3 || summary.Source != "agent" {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	if agent.lastReceivedUnix != 41 {
		t.Fatalf("agent since_unix = %d", agent.lastReceivedUnix)
	}
}

func TestLocalLogsUseAgentNotCloudAndRedact(t *testing.T) {
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

	res, err := http.Get(server.URL + "/api/local/projects/proj-1/logs?service_id=svc-1&limit=1")
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
		t.Fatal("logs must not call Cloud")
	}
	if !agent.includeLogs || agent.serviceID != "svc-1" {
		t.Fatalf("agent logs request not used: include=%v service=%q", agent.includeLogs, agent.serviceID)
	}
	if strings.Contains(string(body), "super-secret") || strings.Contains(string(body), "browser-pat") {
		t.Fatalf("log response leaked secret-like value: %s", body)
	}
	if !strings.Contains(string(body), "[REDACTED]") {
		t.Fatalf("log response was not redacted: %s", body)
	}
}

func TestLocalTelemetryInvalidInputFailsClosed(t *testing.T) {
	agent := &localTelemetryServer{}
	agentAddr, stop := startCommandTelemetryServer(t, agent)
	defer stop()
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: agentAddr, CloudURL: "http://127.0.0.1:1"}, nil))
	defer server.Close()

	res, err := http.Get(server.URL + "/api/local/projects/proj-1/logs?cursor=not-a-time")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("status=%d body=%s", res.StatusCode, body)
	}
}

func TestLocalIncidentAnalyzeUsesAgentNotCloud(t *testing.T) {
	agent := &localIncidentServer{}
	agentAddr, stop := startLocalIncidentServer(t, agent)
	defer stop()
	cloudCalled := false
	cloud := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		cloudCalled = true
	}))
	defer cloud.Close()
	store := keychain.NewFakeStore()
	if err := store.SetPAT("keychain-pat"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: agentAddr, CloudURL: cloud.URL}, func() (keychain.Store, error) {
		return store, nil
	}))
	defer server.Close()
	session := localTestSession(t, server.URL)

	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/local/projects/proj-1/incidents/inc-1/analyze", strings.NewReader(`{"user_id":"dev","role":"Developer"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Local-Session", session)
	req.Header.Set("Idempotency-Key", "incident-analyze-1")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.StatusCode, body)
	}
	if cloudCalled {
		t.Fatal("incident analyze must not call Cloud from local backend")
	}
	if agent.analyzeCalls != 1 || agent.lastAnalyze.ProjectID != "proj-1" || agent.lastAnalyze.IncidentID != "inc-1" || agent.lastAuth != "Bearer keychain-pat" {
		t.Fatalf("agent analyze not used: calls=%d auth=%q req=%+v", agent.analyzeCalls, agent.lastAuth, agent.lastAnalyze)
	}
	if !strings.Contains(string(body), `"source":"agent"`) || !strings.Contains(string(body), `"advisory_only":true`) || !strings.Contains(string(body), `"fallback_used":true`) {
		t.Fatalf("missing boundary/advisory metadata: %s", body)
	}
}

func TestLocalIncidentListUsesAgentNotCloud(t *testing.T) {
	agent := &localIncidentServer{}
	agentAddr, stop := startLocalIncidentServer(t, agent)
	defer stop()
	cloudCalled := false
	cloud := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		cloudCalled = true
	}))
	defer cloud.Close()
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: agentAddr, CloudURL: cloud.URL}, nil))
	defer server.Close()

	res, err := http.Get(server.URL + "/api/local/projects/proj-1/incidents?user_id=viewer&role=Viewer")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || cloudCalled || agent.listCalls != 1 || !strings.Contains(string(body), `"incidents"`) {
		t.Fatalf("status=%d cloud=%v calls=%d body=%s", res.StatusCode, cloudCalled, agent.listCalls, body)
	}
}

func TestLocalIncidentApproveRequiresExplicitApproval(t *testing.T) {
	agent := &localIncidentServer{}
	agentAddr, stop := startLocalIncidentServer(t, agent)
	defer stop()
	cloudCalled := false
	cloud := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		cloudCalled = true
	}))
	defer cloud.Close()
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: agentAddr, CloudURL: cloud.URL}, nil))
	defer server.Close()
	session := localTestSession(t, server.URL)

	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/local/projects/proj-1/incidents/inc-1/actions/scale/approve", strings.NewReader(`{"user_id":"dev","role":"Developer","action_hash":"sha256:ok"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Local-Session", session)
	req.Header.Set("Idempotency-Key", "incident-approve-no")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("status=%d body=%s", res.StatusCode, body)
	}
	if cloudCalled || agent.approveCalls != 0 {
		t.Fatalf("unapproved mitigation reached cloud=%v agent_calls=%d", cloudCalled, agent.approveCalls)
	}
}

func TestLocalIncidentRejectsArbitraryCommand(t *testing.T) {
	agent := &localIncidentServer{}
	agentAddr, stop := startLocalIncidentServer(t, agent)
	defer stop()
	cloudCalled := false
	cloud := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		cloudCalled = true
	}))
	defer cloud.Close()
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: agentAddr, CloudURL: cloud.URL}, nil))
	defer server.Close()
	session := localTestSession(t, server.URL)

	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/local/projects/proj-1/incidents/inc-1/actions/shell/approve", strings.NewReader(`{"user_id":"dev","role":"Developer","approved":true,"action_hash":"sha256:ok","command":"kubectl delete pod x"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Local-Session", session)
	req.Header.Set("Idempotency-Key", "incident-command")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", res.StatusCode, body)
	}
	if cloudCalled || agent.approveCalls != 0 || strings.Contains(string(body), "kubectl delete") {
		t.Fatalf("arbitrary command leaked/reached backend cloud=%v agent=%d body=%s", cloudCalled, agent.approveCalls, body)
	}
}

func TestLocalIncidentApproveSendsAllowlistActionHashToAgent(t *testing.T) {
	agent := &localIncidentServer{}
	agentAddr, stop := startLocalIncidentServer(t, agent)
	defer stop()
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: agentAddr, CloudURL: "http://127.0.0.1:1"}, nil))
	defer server.Close()
	session := localTestSession(t, server.URL)

	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/local/projects/proj-1/incidents/inc-1/actions/scale/approve", strings.NewReader(`{"user_id":"dev","role":"Developer","approved":true,"action_hash":"sha256:ok"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Local-Session", session)
	req.Header.Set("Idempotency-Key", "incident-approve-1")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.StatusCode, body)
	}
	if agent.approveCalls != 1 || agent.lastAction.ActionID != "scale" || agent.lastAction.ActionHash != "sha256:ok" {
		t.Fatalf("approval not passed to Agent: calls=%d req=%+v", agent.approveCalls, agent.lastAction)
	}
	if !strings.Contains(string(body), `"audit_policy"`) {
		t.Fatalf("missing audit policy: %s", body)
	}
}

type localTelemetryServer struct {
	agentv1.UnimplementedTelemetryServiceServer
	lastReceivedUnix int64
	includeLogs      bool
	serviceID        string
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

func (s *localTelemetryServer) QueryTelemetry(_ context.Context, req *agentv1.TelemetryQueryRequest) (*agentv1.TelemetryQueryResponse, error) {
	s.lastReceivedUnix = req.SinceUnix
	s.includeLogs = req.IncludeLogs
	s.serviceID = req.ServiceID
	if req.Cursor != "" {
		return nil, status.Error(codes.InvalidArgument, "cursor is invalid")
	}
	resp := &agentv1.TelemetryQueryResponse{
		ProjectID:     req.ProjectID,
		Source:        "agent",
		PayloadPolicy: "raw telemetry payload remains local and is not returned to the browser",
	}
	if req.IncludeSummary {
		resp.Summary = &agentv1.TelemetryRuntimeSummary{SinceUnix: req.SinceUnix, EndUnix: 99, MetricCount: 2, LogCount: 1, ErrorCount: 1, ServiceCount: 1, Health: "degraded"}
	}
	if req.IncludeServices {
		resp.Services = []agentv1.TelemetryServiceStatus{{ServiceID: "svc-1", Health: "degraded", PodCount: 1, ReadyPods: 0, RestartCount: 2, RecentErrorCount: 1, LastSeenUnix: 99}}
	}
	if req.IncludeLogs {
		resp.Logs = []agentv1.TelemetryLogEntry{{ServiceID: "svc-1", PodID: "pod-1", Namespace: "app", Level: "error", Message: "password=super-secret Authorization: Bearer browser-pat", Fingerprint: "fp", ObservedUnix: 99}}
	}
	return resp, nil
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

func TestLocalBrowserLoginRedeemsToKeychainWithoutBrowserPAT(t *testing.T) {
	store := keychain.NewFakeStore()
	var localState string
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth/browser/start":
			var req struct {
				LocalState string `json:"local_state"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			localState = req.LocalState
			_, _ = w.Write([]byte(`{"auth_url":"https://cloud.example.test/login","status":"pending"}`))
		case "/v1/auth/browser/redeem":
			_, _ = w.Write([]byte(`{"token":"pat_secret_should_stay_local","session":{"user_id":"u","project_id":"proj"}}`))
		default:
			t.Fatalf("unexpected Cloud path %s", r.URL.Path)
		}
	}))
	defer cloud.Close()
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: "127.0.0.1:1", CloudURL: cloud.URL}, func() (keychain.Store, error) {
		return store, nil
	}))
	defer server.Close()

	res, err := http.Post(server.URL+"/api/local/session/login/start", "application/json", strings.NewReader(`{"project_id":"proj"}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if strings.Contains(string(body), "pat_secret") || localState == "" {
		t.Fatalf("login start leaked token or missed state: body=%s state=%q", body, localState)
	}

	client := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err = client.Get(server.URL + "/api/local/session/callback?code=grant-1&state=" + url.QueryEscape(localState))
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusFound {
		t.Fatalf("callback status = %d", res.StatusCode)
	}
	got, err := store.GetPAT()
	if err != nil {
		t.Fatal(err)
	}
	if got != "pat_secret_should_stay_local" {
		t.Fatalf("stored token = %q", got)
	}
}

func TestLocalPATRotateFailurePreservesOldToken(t *testing.T) {
	store := keychain.NewFakeStore()
	if err := store.SetPAT("old-pat"); err != nil {
		t.Fatal(err)
	}
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/auth/pat/rotate" {
			t.Fatalf("unexpected Cloud path %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer cloud.Close()
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: "127.0.0.1:1", CloudURL: cloud.URL}, func() (keychain.Store, error) {
		return store, nil
	}))
	defer server.Close()
	session := localTestSession(t, server.URL)

	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/local/session/token/rotate", strings.NewReader(`{"project_id":"proj"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Local-Session", session)
	req.Header.Set("Idempotency-Key", "rotate-1")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", res.StatusCode)
	}
	got, err := store.GetPAT()
	if err != nil {
		t.Fatal(err)
	}
	if got != "old-pat" {
		t.Fatalf("old token not preserved: %q", got)
	}
}

func TestLocalLogoutRevokesAndClearsKeychain(t *testing.T) {
	store := keychain.NewFakeStore()
	if err := store.SetPAT("old-pat"); err != nil {
		t.Fatal(err)
	}
	revoked := false
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/auth/pat/revoke" {
			t.Fatalf("unexpected Cloud path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer old-pat" {
			t.Fatalf("auth = %q", got)
		}
		revoked = true
		_, _ = w.Write([]byte(`{"revoked":true}`))
	}))
	defer cloud.Close()
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: "127.0.0.1:1", CloudURL: cloud.URL}, func() (keychain.Store, error) {
		return store, nil
	}))
	defer server.Close()
	session := localTestSession(t, server.URL)
	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/local/session/logout", strings.NewReader(`{"project_id":"proj"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Local-Session", session)
	req.Header.Set("Idempotency-Key", "logout-1")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || !revoked {
		t.Fatalf("status=%d revoked=%v", res.StatusCode, revoked)
	}
	if _, err := store.GetPAT(); err == nil {
		t.Fatal("token still present after logout")
	}
}

func TestBrowserUIDoesNotStorePATOrCallCloudDirectly(t *testing.T) {
	root := filepath.Clean("../../ui")
	forbidden := []string{"localStorage", "sessionStorage", "indexedDB", "document.cookie", "NEXT_PUBLIC_CLOUD", "CloudRegistryClient", "cloudURL", "localhost:9800", "127.0.0.1:9800"}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case "node_modules", "out", ".next":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".ts") && !strings.HasSuffix(path, ".tsx") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(data)
		for _, token := range forbidden {
			if strings.Contains(text, token) {
				t.Fatalf("%s contains forbidden browser auth/direct-cloud token %q", path, token)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

type localSecretServer struct {
	agentv1.UnimplementedSecretServiceServer
	createCalls int
	revealCalls int
	rotateCalls int
	lastReq     agentv1.SecretRequest
	lastAuth    string
	err         error
}

func (s *localSecretServer) CreateSecret(ctx context.Context, req *agentv1.SecretRequest) (*agentv1.SecretResponse, error) {
	s.createCalls++
	s.lastReq = *req
	s.lastAuth = localAuthHeader(ctx)
	if s.err != nil {
		return nil, s.err
	}
	return &agentv1.SecretResponse{ProjectID: req.ProjectID, ServiceID: req.ServiceID, Name: req.Name, Namespace: req.Namespace, Username: "agent-user", Password: "agent-secret-password"}, nil
}

func (s *localSecretServer) RevealSecret(ctx context.Context, req *agentv1.SecretRequest) (*agentv1.SecretResponse, error) {
	s.revealCalls++
	s.lastReq = *req
	s.lastAuth = localAuthHeader(ctx)
	if s.err != nil {
		return nil, s.err
	}
	return &agentv1.SecretResponse{ProjectID: req.ProjectID, ServiceID: req.ServiceID, Name: req.Name, Namespace: req.Namespace, Username: "agent-user", Password: "agent-secret-password"}, nil
}

func (s *localSecretServer) RotateSecret(ctx context.Context, req *agentv1.SecretRequest) (*agentv1.SecretResponse, error) {
	s.rotateCalls++
	s.lastReq = *req
	s.lastAuth = localAuthHeader(ctx)
	if s.err != nil {
		return nil, s.err
	}
	return &agentv1.SecretResponse{ProjectID: req.ProjectID, ServiceID: req.ServiceID, Name: req.Name, Namespace: req.Namespace, Username: "agent-user", Password: "agent-secret-password"}, nil
}

func localAuthHeader(ctx context.Context) string {
	md, _ := metadata.FromIncomingContext(ctx)
	values := md.Get("authorization")
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func startLocalSecretServer(t *testing.T, service agentv1.SecretServiceServer) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	agentv1.RegisterSecretServiceServer(server, service)
	go func() { _ = server.Serve(listener) }()
	return listener.Addr().String(), server.Stop
}

type localIncidentServer struct {
	agentv1.UnimplementedIncidentServiceServer
	listCalls    int
	getCalls     int
	analyzeCalls int
	approveCalls int
	resolveCalls int
	lastAnalyze  agentv1.IncidentAnalyzeRequest
	lastAction   agentv1.IncidentActionRequest
	lastAuth     string
	err          error
}

func (s *localIncidentServer) ListIncidents(ctx context.Context, req *agentv1.IncidentListRequest) (*agentv1.IncidentListResponse, error) {
	s.listCalls++
	s.lastAuth = localAuthHeader(ctx)
	if s.err != nil {
		return nil, s.err
	}
	return &agentv1.IncidentListResponse{Incidents: []agentv1.IncidentResponse{*localIncidentResponse(req.ProjectID, "inc-1")}}, nil
}

func (s *localIncidentServer) GetIncident(ctx context.Context, req *agentv1.IncidentGetRequest) (*agentv1.IncidentResponse, error) {
	s.getCalls++
	s.lastAuth = localAuthHeader(ctx)
	if s.err != nil {
		return nil, s.err
	}
	return localIncidentResponse(req.ProjectID, req.IncidentID), nil
}

func (s *localIncidentServer) AnalyzeIncident(ctx context.Context, req *agentv1.IncidentAnalyzeRequest) (*agentv1.IncidentResponse, error) {
	s.analyzeCalls++
	s.lastAnalyze = *req
	s.lastAuth = localAuthHeader(ctx)
	if s.err != nil {
		return nil, s.err
	}
	return localIncidentResponse(req.ProjectID, req.IncidentID), nil
}

func (s *localIncidentServer) ApproveIncidentAction(ctx context.Context, req *agentv1.IncidentActionRequest) (*agentv1.IncidentResponse, error) {
	s.approveCalls++
	s.lastAction = *req
	s.lastAuth = localAuthHeader(ctx)
	if s.err != nil {
		return nil, s.err
	}
	return localIncidentResponse(req.ProjectID, req.IncidentID), nil
}

func (s *localIncidentServer) ResolveIncident(ctx context.Context, req *agentv1.IncidentActionRequest) (*agentv1.IncidentResponse, error) {
	s.resolveCalls++
	s.lastAction = *req
	s.lastAuth = localAuthHeader(ctx)
	if s.err != nil {
		return nil, s.err
	}
	resp := localIncidentResponse(req.ProjectID, req.IncidentID)
	resp.Status = "resolved"
	return resp, nil
}

func localIncidentResponse(projectID, incidentID string) *agentv1.IncidentResponse {
	return &agentv1.IncidentResponse{
		ProjectID:   projectID,
		IncidentID:  incidentID,
		ServiceID:   "svc-1",
		Status:      "action_pending",
		RootCause:   "Local fallback analysis: crash_loop",
		Confidence:  0.62,
		RCAMetadata: &agentv1.RCAMetadata{Provider: "local", Model: "fallback", FallbackUsed: true, InputContextHash: "sha256:ctx", CreatedAt: "2026-01-01T00:00:00Z"},
		RecommendedActions: []agentv1.RecommendedAction{{
			ID:           "scale",
			Type:         "scale_replicas",
			Description:  "Scale replicas",
			RollbackSafe: true,
			ActionHash:   "sha256:ok",
			Params:       map[string]string{"service_id": "svc-1", "replicas": "2"},
		}},
	}
}

func startLocalIncidentServer(t *testing.T, service agentv1.IncidentServiceServer) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	agentv1.RegisterIncidentServiceServer(server, service)
	go func() { _ = server.Serve(listener) }()
	return listener.Addr().String(), server.Stop
}

func localTestSession(t *testing.T, baseURL string) string {
	t.Helper()
	var session struct {
		LocalSession string `json:"local_session"`
	}
	res, err := http.Get(baseURL + "/api/local/session")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if err := json.NewDecoder(res.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	if session.LocalSession == "" {
		t.Fatal("empty local session")
	}
	return session.LocalSession
}

func TestResolveUIDirUsesEnv(t *testing.T) {
	t.Setenv("OPSI_UI_DIR", "/tmp/opsi-ui")
	if got := resolveUIDir(); !strings.HasSuffix(got, "opsi-ui") {
		t.Fatalf("dir = %q", got)
	}
}
