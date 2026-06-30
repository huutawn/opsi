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
	server := NewServer(Config{BootstrapWorkerToken: "worker-secret"})
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
	server := NewServer(Config{BootstrapWorkerToken: "worker-secret"})
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
	server := NewServer(Config{BootstrapWorkerToken: "worker-secret"})
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

	req = httptest.NewRequest(http.MethodPost, "/internal/bootstrap/sessions/"+session.ID+"/take", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauth worker take status=%d body=%s", w.Code, w.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/internal/bootstrap/sessions/"+session.ID+"/take", nil)
	req.Header.Set("X-Bootstrap-Worker-Token", "worker-secret")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("worker take status=%d body=%s", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("secret")) {
		t.Fatalf("worker bundle missing password: %s", w.Body.String())
	}
	var bundle struct {
		AgentRegistrationToken string `json:"agent_registration_token"`
	}
	if err := json.NewDecoder(w.Body).Decode(&bundle); err != nil {
		t.Fatal(err)
	}
	if bundle.AgentRegistrationToken == "" {
		t.Fatal("missing agent registration token")
	}
	req = httptest.NewRequest(http.MethodPost, "/internal/bootstrap/sessions/"+session.ID+"/take", nil)
	req.Header.Set("X-Bootstrap-Worker-Token", "worker-secret")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusGone {
		t.Fatalf("worker second take status=%d body=%s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/agents/register", bytes.NewReader([]byte(`{"registration_token":"`+bundle.AgentRegistrationToken+`","public_key_fingerprint":"sha256:abc","version":"v1"}`)))
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("agent exchange status=%d body=%s", w.Code, w.Body.String())
	}
	var agentResp struct {
		Agent struct {
			ID     string `json:"id"`
			NodeID string `json:"node_id"`
			Status string `json:"status"`
		} `json:"agent"`
		AgentToken string `json:"agent_token"`
	}
	if err := json.NewDecoder(w.Body).Decode(&agentResp); err != nil {
		t.Fatal(err)
	}
	if agentResp.Agent.ID == "" || agentResp.Agent.NodeID == "" || agentResp.AgentToken == "" || agentResp.Agent.Status != "active" {
		t.Fatalf("unexpected agent exchange: %+v", agentResp)
	}
	req = httptest.NewRequest(http.MethodPost, "/v1/agents/register", bytes.NewReader([]byte(`{"registration_token":"`+bundle.AgentRegistrationToken+`","public_key_fingerprint":"sha256:abc","version":"v1"}`)))
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("agent token replay status=%d body=%s", w.Code, w.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/v1/agents/"+agentResp.Agent.NodeID+"/webhooks/next?project_id="+projectID+"&wait=0s", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
		t.Fatalf("agent poll without token status=%d body=%s", w.Code, w.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/v1/agents/"+agentResp.Agent.NodeID+"/webhooks/next?project_id="+projectID+"&wait=0s", nil)
	req.Header.Set("Authorization", "Bearer "+agentResp.AgentToken)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("agent poll status=%d body=%s", w.Code, w.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/agents/"+agentResp.Agent.ID+"/rotate", nil)
	req.Header.Set("Authorization", "Bearer owner_pat")
	req.Header.Set("Idempotency-Key", "agent-rotate")
	req.Header.Set("X-Request-ID", "req-agent-rotate")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("agent rotate status=%d body=%s", w.Code, w.Body.String())
	}
	var rotateResp struct {
		AgentToken string `json:"agent_token"`
	}
	if err := json.NewDecoder(w.Body).Decode(&rotateResp); err != nil {
		t.Fatal(err)
	}
	if rotateResp.AgentToken == "" {
		t.Fatal("missing rotated agent token")
	}
	req = httptest.NewRequest(http.MethodGet, "/v1/agents/"+agentResp.Agent.NodeID+"/webhooks/next?project_id="+projectID+"&wait=0s", nil)
	req.Header.Set("Authorization", "Bearer "+agentResp.AgentToken)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("old rotated token status=%d body=%s", w.Code, w.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/v1/agents/"+agentResp.Agent.NodeID+"/webhooks/next?project_id="+projectID+"&wait=0s", nil)
	req.Header.Set("Authorization", "Bearer "+rotateResp.AgentToken)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("rotated agent poll status=%d body=%s", w.Code, w.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/agents/"+agentResp.Agent.ID+"/revoke", nil)
	req.Header.Set("Authorization", "Bearer owner_pat")
	req.Header.Set("Idempotency-Key", "agent-revoke")
	req.Header.Set("X-Request-ID", "req-agent-revoke")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("agent revoke status=%d body=%s", w.Code, w.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/v1/agents/"+agentResp.Agent.NodeID+"/webhooks/next?project_id="+projectID+"&wait=0s", nil)
	req.Header.Set("Authorization", "Bearer "+rotateResp.AgentToken)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("revoked agent poll status=%d body=%s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/internal/bootstrap/sessions/"+session.ID+"/finish", bytes.NewReader([]byte(`{"project_id":"`+projectID+`","status":"succeeded","message":"password=secret token=abc"}`)))
	req.Header.Set("X-Bootstrap-Worker-Token", "worker-secret")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("bootstrap finish status=%d body=%s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/bootstrap-sessions/"+session.ID+"/events", nil)
	req.Header.Set("Authorization", "Bearer owner_pat")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("events status=%d body=%s", w.Code, w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte("secret")) || bytes.Contains(w.Body.Bytes(), []byte("token=abc")) {
		t.Fatalf("bootstrap events leaked secret: %s", w.Body.String())
	}
}

func TestAgentTokenGate(t *testing.T) {
	server := NewServer(Config{})
	hash, err := auth.HashPAT("agent-secret")
	if err != nil {
		t.Fatal(err)
	}
	project, err := server.Registry.CreateProject("org-1", "Demo", "demo", "user-1", "proj")
	if err != nil {
		t.Fatal(err)
	}
	node, err := server.Registry.UpsertNode(project.ID, "vps", "server", "healthy", "203.0.113.10", "", "node")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := server.Registry.RegisterAgent(project.ID, node.ID, "sha256:abc", hash, "v1", "agent", nil)
	if err != nil || agent.ID == "" {
		t.Fatalf("register agent err=%v agent=%+v", err, agent)
	}
	handler := server.Handler()

	req := httptest.NewRequest(http.MethodGet, "/v1/agents/"+node.ID+"/webhooks/next?project_id="+project.ID+"&wait=0s", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
		t.Fatalf("missing agent token status=%d", w.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/agents/"+node.ID+"/webhooks/next?project_id="+project.ID+"&wait=0s", nil)
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
