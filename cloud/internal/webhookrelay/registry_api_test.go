package webhookrelay

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/opsi-dev/opsi/cloud/internal/auth"
)

func TestRegistryAPIProjectReadinessAndDeploymentGuard(t *testing.T) {
	server := NewServer(Config{})
	handler := server.Handler()

	req := httptest.NewRequest(http.MethodPost, "/api/orgs/org-1/projects", bytes.NewReader([]byte(`{"name":"Demo","slug":"demo","created_by":"user-1"}`)))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected missing idempotency rejected, got %d", w.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/orgs/org-1/projects", bytes.NewReader([]byte(`{"name":"Demo","slug":"demo","created_by":"user-1"}`)))
	req.Header.Set("Idempotency-Key", "proj-key")
	req.Header.Set("X-Request-ID", "req-1")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create project status=%d body=%s", w.Code, w.Body.String())
	}
	var project struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(w.Body).Decode(&project); err != nil {
		t.Fatal(err)
	}
	if project.ID == "" || project.Status != "no_nodes" {
		t.Fatalf("unexpected project: %+v", project)
	}

	serviceID := createService(t, handler, project.ID)
	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+project.ID+"/services/"+serviceID+"/deployments", bytes.NewReader([]byte(`{"requested_by":"user-1"}`)))
	req.Header.Set("Idempotency-Key", "dep-key")
	req.Header.Set("X-Request-ID", "req-2")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected project readiness guard, got %d body=%s", w.Code, w.Body.String())
	}
	var apiErr struct {
		Code       string `json:"error_code"`
		NextAction string `json:"next_action"`
	}
	if err := json.NewDecoder(w.Body).Decode(&apiErr); err != nil {
		t.Fatal(err)
	}
	if apiErr.Code != "PROJECT_NOT_READY" || apiErr.NextAction != "add_first_server" {
		t.Fatalf("unexpected error: %+v", apiErr)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+project.ID+"/nodes", bytes.NewReader([]byte(`{"name":"vps-1","role":"server","status":"healthy","public_host":"203.0.113.10","agent_id":"agent-1"}`)))
	req.Header.Set("Idempotency-Key", "node-key")
	req.Header.Set("X-Request-ID", "req-3")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("node status=%d body=%s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/projects/"+project.ID+"/readiness", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("readiness status=%d body=%s", w.Code, w.Body.String())
	}
	var readiness struct {
		Status    string `json:"status"`
		CanDeploy bool   `json:"can_deploy"`
	}
	if err := json.NewDecoder(w.Body).Decode(&readiness); err != nil {
		t.Fatal(err)
	}
	if readiness.Status != "ready" || !readiness.CanDeploy {
		t.Fatalf("unexpected readiness: %+v", readiness)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+project.ID+"/services/"+serviceID+"/deployments", bytes.NewReader([]byte(`{"requested_by":"user-1"}`)))
	req.Header.Set("Idempotency-Key", "dep-key-ready")
	req.Header.Set("X-Request-ID", "req-4")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("deploy status=%d body=%s", w.Code, w.Body.String())
	}
	var deploy struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(w.Body).Decode(&deploy); err != nil {
		t.Fatal(err)
	}
	if deploy.Status != "queued" {
		t.Fatalf("unexpected deploy: %+v", deploy)
	}
}

func createService(t *testing.T, handler http.Handler, projectID string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/services", bytes.NewReader([]byte(`{"name":"api","type":"application","source_type":"git","repo_url":"https://github.com/example/api.git"}`)))
	req.Header.Set("Idempotency-Key", "svc-key")
	req.Header.Set("X-Request-ID", "req-svc")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("service status=%d body=%s", w.Code, w.Body.String())
	}
	var service struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(w.Body).Decode(&service); err != nil {
		t.Fatal(err)
	}
	if service.ID == "" {
		t.Fatal("service id is empty")
	}
	return service.ID
}

func TestRegistryAPIRBACCrossTenantAndIdempotency(t *testing.T) {
	ownerHash, err := auth.HashPAT("owner_pat")
	if err != nil {
		t.Fatal(err)
	}
	viewerHash, err := auth.HashPAT("viewer_pat")
	if err != nil {
		t.Fatal(err)
	}
	otherHash, err := auth.HashPAT("other_pat")
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(Config{})
	server.Auth = &auth.Service{Store: auth.MemoryStore{Candidates: []auth.Candidate{
		{UserID: "owner", OrgID: "org-1", Role: "Owner", Hash: ownerHash},
		{UserID: "viewer", OrgID: "org-1", Role: "Viewer", Hash: viewerHash},
		{UserID: "other", OrgID: "org-2", Role: "Owner", Hash: otherHash},
	}}}
	handler := server.Handler()

	projectA := createProjectWithToken(t, handler, "org-1", "owner_pat", "same-key")
	projectAgain := createProjectWithToken(t, handler, "org-1", "owner_pat", "same-key")
	if projectA != projectAgain {
		t.Fatalf("idempotency returned different project ids: %s != %s", projectA, projectAgain)
	}
	projectB := createProjectWithToken(t, handler, "org-2", "other_pat", "other-key")

	server.Auth = &auth.Service{Store: auth.MemoryStore{Candidates: []auth.Candidate{
		{UserID: "owner", OrgID: "org-1", ProjectID: projectA, Role: "Owner", Hash: ownerHash},
		{UserID: "viewer", OrgID: "org-1", ProjectID: projectA, Role: "Viewer", Hash: viewerHash},
		{UserID: "other", OrgID: "org-2", ProjectID: projectB, Role: "Owner", Hash: otherHash},
	}}}

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectA+"/services", bytes.NewReader([]byte(`{"name":"api"}`)))
	req.Header.Set("Authorization", "Bearer viewer_pat")
	req.Header.Set("Idempotency-Key", "svc-viewer")
	req.Header.Set("X-Request-ID", "req-viewer")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("viewer write status=%d body=%s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/projects/"+projectB+"/nodes", nil)
	req.Header.Set("Authorization", "Bearer owner_pat")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-project status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestBootstrapCredentialVaultAndRBAC(t *testing.T) {
	ownerHash, err := auth.HashPAT("owner_pat")
	if err != nil {
		t.Fatal(err)
	}
	devHash, err := auth.HashPAT("dev_pat")
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(Config{})
	server.Auth = &auth.Service{Store: auth.MemoryStore{Candidates: []auth.Candidate{
		{UserID: "owner", OrgID: "org-1", Role: "Owner", Hash: ownerHash},
	}}}
	handler := server.Handler()
	projectID := createProjectWithToken(t, handler, "org-1", "owner_pat", "boot-proj")
	server.Auth = &auth.Service{Store: auth.MemoryStore{Candidates: []auth.Candidate{
		{UserID: "owner", OrgID: "org-1", ProjectID: projectID, Role: "Owner", Hash: ownerHash},
		{UserID: "dev", OrgID: "org-1", ProjectID: projectID, Role: "Developer", Hash: devHash},
	}}}

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/nodes", bytes.NewReader([]byte(`{"name":"vps-1","role":"server","status":"healthy"}`)))
	req.Header.Set("Authorization", "Bearer dev_pat")
	req.Header.Set("Idempotency-Key", "node-dev")
	req.Header.Set("X-Request-ID", "req-node-dev")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("developer node status=%d body=%s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/bootstrap-sessions", bytes.NewReader([]byte(`{"role":"first_server","public_host":"203.0.113.10","ssh_username":"root","auth_method":"password","ssh_password":"secret"}`)))
	req.Header.Set("Authorization", "Bearer dev_pat")
	req.Header.Set("Idempotency-Key", "boot-dev")
	req.Header.Set("X-Request-ID", "req-boot-dev")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("developer bootstrap status=%d body=%s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/bootstrap-sessions", bytes.NewReader([]byte(`{"role":"first_server","public_host":"203.0.113.10","ssh_username":"root","auth_method":"password","ssh_password":"secret","k3s_token":"leak"}`)))
	req.Header.Set("Authorization", "Bearer owner_pat")
	req.Header.Set("Idempotency-Key", "boot-bad")
	req.Header.Set("X-Request-ID", "req-boot-bad")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("k3s token status=%d body=%s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/bootstrap-sessions", bytes.NewReader([]byte(`{"role":"first_server","public_host":"203.0.113.10","ssh_username":"root","auth_method":"password","ssh_password":"secret"}`)))
	req.Header.Set("Authorization", "Bearer owner_pat")
	req.Header.Set("Idempotency-Key", "boot-owner")
	req.Header.Set("X-Request-ID", "req-boot-owner")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("owner bootstrap status=%d body=%s", w.Code, w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte("secret")) {
		t.Fatalf("bootstrap response leaked password: %s", w.Body.String())
	}
	var session struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(w.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	credential, ok := server.credentials.Take(session.ID)
	if !ok || string(credential.Password) != "secret" {
		t.Fatalf("credential missing from memory vault")
	}
	if _, ok := server.credentials.Take(session.ID); ok {
		t.Fatalf("credential vault did not read-once purge")
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/nodes", bytes.NewReader([]byte(`{"name":"vps-owner","role":"server","status":"healthy"}`)))
	req.Header.Set("Authorization", "Bearer owner_pat")
	req.Header.Set("Idempotency-Key", "node-owner")
	req.Header.Set("X-Request-ID", "req-node-owner")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("owner node status=%d body=%s", w.Code, w.Body.String())
	}
	var node struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(w.Body).Decode(&node); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/agents", bytes.NewReader([]byte(`{"node_id":"`+node.ID+`","public_key_fingerprint":"sha256:abc","version":"v1"}`)))
	req.Header.Set("Authorization", "Bearer owner_pat")
	req.Header.Set("Idempotency-Key", "agent-owner")
	req.Header.Set("X-Request-ID", "req-agent-owner")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("agent register status=%d body=%s", w.Code, w.Body.String())
	}
	var agent struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(w.Body).Decode(&agent); err != nil {
		t.Fatal(err)
	}
	if agent.ID == "" || agent.Status != "active" {
		t.Fatalf("unexpected agent: %+v", agent)
	}
	for _, op := range []string{"rotate", "revoke"} {
		req = httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/agents/"+agent.ID+"/"+op, nil)
		req.Header.Set("Authorization", "Bearer owner_pat")
		req.Header.Set("Idempotency-Key", "agent-"+op)
		req.Header.Set("X-Request-ID", "req-agent-"+op)
		w = httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("agent %s status=%d body=%s", op, w.Code, w.Body.String())
		}
	}

	req = httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/bootstrap-sessions/"+session.ID+"/events", nil)
	req.Header.Set("Authorization", "Bearer owner_pat")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("events status=%d body=%s", w.Code, w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte("secret")) {
		t.Fatalf("bootstrap events leaked password: %s", w.Body.String())
	}
}

func TestAgentTokenGate(t *testing.T) {
	server := NewServer(Config{AgentTokens: []string{"agent-secret"}})
	handler := server.Handler()

	req := httptest.NewRequest(http.MethodGet, "/v1/agents/node-1/webhooks/next?project_id=proj&wait=0s", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("missing agent token status=%d", w.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/agents/node-1/webhooks/next?project_id=proj&wait=0s", nil)
	req.Header.Set("Authorization", "Bearer agent-secret")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("agent token status=%d body=%s", w.Code, w.Body.String())
	}
}

func createProjectWithToken(t *testing.T, handler http.Handler, orgID, token, key string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/orgs/"+orgID+"/projects", bytes.NewReader([]byte(`{"name":"Demo","slug":"demo"}`)))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", key)
	req.Header.Set("X-Request-ID", "req-"+key)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create project status=%d body=%s", w.Code, w.Body.String())
	}
	var project struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(w.Body).Decode(&project); err != nil {
		t.Fatal(err)
	}
	return project.ID
}
