package webhookrelay

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/auth"
	"github.com/opsi-dev/opsi/cloud/internal/deploymentpolicy"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
	"github.com/opsi-dev/opsi/cloud/internal/topology"
)

func TestWriteRegistryFailurePreservesDeploymentPolicyBlocker(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/projects/project/deployments/preview", nil)
	request.Header.Set("X-Request-ID", "request-routing")
	response := httptest.NewRecorder()

	writeRegistryFailure(response, request, deploymentpolicy.Error{
		Status:  http.StatusConflict,
		Code:    "ROUTING_POLICY_MISMATCH",
		Message: "no active DeploymentPolicy exact-matches the BuildRecord, environment, and topology runtime",
	})

	if response.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"error_code":"ROUTING_POLICY_MISMATCH"`) || strings.Contains(response.Body.String(), "Internal server error") {
		t.Fatalf("unexpected body=%s", response.Body.String())
	}
}

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
	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+project.ID+"/services/"+serviceID+"/deployments", bytes.NewReader([]byte(`{}`)))
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("retired service-scoped deployment status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestCreateServiceDoesNotChangeAppliedTopology(t *testing.T) {
	server := NewServer(Config{})
	project, err := server.Registry.CreateProject("org-topology", "Topology", "topology", "owner", "project-topology")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+project.ID+"/services", bytes.NewReader([]byte(`{"name":"api","type":"application","source_type":"git","repo_url":"https://github.com/example/api","branch":"main","build_context":"services/api","dockerfile":"Dockerfile","container_port":8080,"health_path":"/health"}`)))
	req.Header.Set("Idempotency-Key", "create-api")
	req.Header.Set("X-Request-ID", "request-create-api")
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create service status=%d body=%s", w.Code, w.Body.String())
	}
	var created registry.ServiceRecord
	if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.EnvironmentID == "" || created.RuntimeID == "" {
		t.Fatalf("legacy scope fixture is missing: %+v", created)
	}
	after, err := server.Topology.Get(context.Background(), project.ID)
	if !errors.Is(err, topology.ErrNotFound) || len(after.Assignments) != 0 {
		t.Fatalf("service creation unexpectedly created applied placement: plan=%+v err=%v", after, err)
	}
}

func TestRegistryAPINodeOfflineReplayAuditsOnceAndRejectsInvalidKey(t *testing.T) {
	trustedHash, err := auth.HashPAT("offline_pat")
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(Config{})
	project, err := server.Registry.CreateProject("org-offline", "Offline", "offline", "owner-offline", "offline-project")
	if err != nil {
		t.Fatal(err)
	}
	node, err := server.Registry.UpsertNode(project.ID, "target", "server", registry.NodeHealthy, "203.0.113.12", "", "offline-node")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.Registry.RegisterAgent(project.ID, node.ID, "sha256:offline", "credential", "v1", "offline-agent", map[string]any{"deploy": true}); err != nil {
		t.Fatal(err)
	}
	other, err := server.Registry.UpsertNode(project.ID, "other", "worker", registry.NodeHealthy, "203.0.113.13", "", "offline-other")
	if err != nil {
		t.Fatal(err)
	}
	concurrent, err := server.Registry.UpsertNode(project.ID, "concurrent", "worker", registry.NodeHealthy, "203.0.113.14", "", "offline-concurrent")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.Registry.RegisterAgent(project.ID, concurrent.ID, "sha256:concurrent", "credential", "v1", "offline-concurrent-agent", map[string]any{"deploy": true}); err != nil {
		t.Fatal(err)
	}
	server.Auth = &auth.Service{Store: auth.MemoryStore{Candidates: []auth.Candidate{{UserID: "owner-offline", OrgID: project.OrgID, ProjectID: project.ID, Role: "Owner", Hash: trustedHash}}}}
	handler := server.Handler()
	request := func(nodeID, key, requestID string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/projects/"+project.ID+"/nodes/"+nodeID+"/offline", bytes.NewReader([]byte(`{"confirm_target_reset":true,"requested_by":"attacker"}`)))
		req.Header.Set("Authorization", "Bearer offline_pat")
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		req.Header.Set("X-Request-ID", requestID)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	sameNodes := func(left, right []registry.Node) bool {
		if len(left) != len(right) {
			return false
		}
		byID := make(map[string]registry.Node, len(left))
		for _, node := range left {
			byID[node.ID] = node
		}
		for _, node := range right {
			if byID[node.ID] != node {
				return false
			}
		}
		return true
	}
	beforeInvalid, err := server.Registry.ListNodes(project.ID)
	if err != nil {
		t.Fatal(err)
	}

	for _, invalid := range []struct {
		key  string
		code string
	}{{"", "IDEMPOTENCY_KEY_REQUIRED"}, {"invalid key", "IDEMPOTENCY_KEY_INVALID"}} {
		w := request(node.ID, invalid.key, "req-offline-invalid")
		if w.Code != http.StatusBadRequest || !bytes.Contains(w.Body.Bytes(), []byte(`"error_code":"`+invalid.code+`"`)) {
			t.Fatalf("invalid key %q status=%d body=%s", invalid.key, w.Code, w.Body.String())
		}
	}
	nodes, err := server.Registry.ListNodes(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !sameNodes(beforeInvalid, nodes) {
		t.Fatalf("invalid key changed nodes: before=%+v after=%+v", beforeInvalid, nodes)
	}

	key := "node-offline:" + node.ID
	first := request(node.ID, key, "req-offline-first")
	replay := request(node.ID, key, "req-offline-replay")
	if first.Code != http.StatusOK || replay.Code != http.StatusOK || first.Body.String() != replay.Body.String() {
		t.Fatalf("offline replay first=%d/%s replay=%d/%s", first.Code, first.Body.String(), replay.Code, replay.Body.String())
	}
	beforeConflict, err := server.Registry.ListNodes(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	conflict := request(other.ID, key, "req-offline-conflict")
	if conflict.Code != http.StatusConflict || !bytes.Contains(conflict.Body.Bytes(), []byte(`"error_code":"IDEMPOTENCY_CONFLICT"`)) {
		t.Fatalf("conflict status=%d body=%s", conflict.Code, conflict.Body.String())
	}
	afterConflict, err := server.Registry.ListNodes(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !sameNodes(beforeConflict, afterConflict) {
		t.Fatalf("conflict changed nodes: before=%+v after=%+v", beforeConflict, afterConflict)
	}

	responses := make(chan *httptest.ResponseRecorder, 2)
	var wait sync.WaitGroup
	for i := range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			responses <- request(concurrent.ID, "concurrent-offline", "req-concurrent-"+string(rune('a'+i)))
		}()
	}
	wait.Wait()
	close(responses)
	var concurrentBody string
	for response := range responses {
		if response.Code != http.StatusOK {
			t.Fatalf("concurrent status=%d body=%s", response.Code, response.Body.String())
		}
		if concurrentBody == "" {
			concurrentBody = response.Body.String()
		} else if response.Body.String() != concurrentBody {
			t.Fatalf("concurrent results differ: %s / %s", concurrentBody, response.Body.String())
		}
	}
	events, err := server.Registry.ListAudit(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	marked := map[string]int{}
	for _, event := range events {
		if event.Action == "NODE_MARKED_OFFLINE" {
			marked[event.ResourceID]++
			if event.ActorUserID != "owner-offline" || bytes.Contains(mustJSON(t, event), []byte("attacker")) {
				t.Fatalf("offline audit actor=%q event=%+v", event.ActorUserID, event)
			}
		}
	}
	if marked[node.ID] != 1 || marked[concurrent.ID] != 1 || len(marked) != 2 {
		t.Fatalf("NODE_MARKED_OFFLINE audit counts=%v events=%+v", marked, events)
	}
}

func TestBrowserAuthFlowUsesOneTimeGrantAndAuditsWithoutPAT(t *testing.T) {
	server := NewServer(Config{GitHubApp: GitHubAppConfig{
		ClientID:     "client",
		ClientSecret: "secret",
		CallbackURL:  "https://cloud.example.test/v1/auth/browser/callback",
	}})
	server.HTTPClient = newGitHubHTTPClient()
	server.HTTPClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.String() {
		case githubTokenURL:
			return githubJSONResponse(r, http.StatusOK, `{"access_token":"provider-token"}`), nil
		case githubUserURL:
			if r.Header.Get("Authorization") != "Bearer provider-token" {
				t.Fatalf("provider auth = %q", r.Header.Get("Authorization"))
			}
			return githubJSONResponse(r, http.StatusOK, `{"id":12345678,"email":"u@example.test"}`), nil
		default:
			t.Fatalf("provider URL = %s", r.URL)
			return nil, nil
		}
	})
	project, err := server.Registry.CreateProject("org", "Demo", "demo", "u", "project-key")
	if err != nil {
		t.Fatal(err)
	}
	store := &auth.MemoryStore{
		Candidates:      []auth.Candidate{{ID: "membership", UserID: "u", Email: "u@example.test", OrgID: "org", ProjectID: project.ID, Role: "Owner"}},
		OAuthIdentities: map[string]string{"github\x0012345678": "u"},
	}
	server.Auth = &auth.Service{Store: store}
	handler := server.Handler()

	req := httptest.NewRequest(http.MethodPost, "/v1/auth/browser/start", bytes.NewReader([]byte(`{"local_callback":"http://127.0.0.1:9780/api/local/session/callback","local_state":"local-state","project_id":"`+project.ID+`"}`)))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("start status=%d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "opsi_pat_") {
		t.Fatalf("start leaked PAT: %s", w.Body.String())
	}
	var start struct {
		AuthURL string `json:"auth_url"`
	}
	if err := json.NewDecoder(w.Body).Decode(&start); err != nil {
		t.Fatal(err)
	}
	authURL, err := url.Parse(start.AuthURL)
	if err != nil {
		t.Fatal(err)
	}
	state := authURL.Query().Get("state")
	if state == "" {
		t.Fatal("empty provider state")
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/auth/browser/callback?code=provider-code&state="+url.QueryEscape(state), nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusFound {
		t.Fatalf("callback status=%d body=%s", w.Code, w.Body.String())
	}
	location := w.Header().Get("Location")
	if strings.Contains(location, "opsi_pat_") {
		t.Fatalf("callback leaked PAT in redirect: %s", location)
	}
	localURL, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	grant := localURL.Query().Get("code")
	if grant == "" || localURL.Query().Get("state") != "local-state" {
		t.Fatalf("bad local redirect: %s", location)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/auth/browser/redeem", bytes.NewReader([]byte(`{"code":"`+grant+`"}`)))
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("redeem status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "opsi_pat_") {
		t.Fatalf("local-backend redeem did not receive PAT")
	}
	events, err := server.Registry.ListAudit(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 || events[len(events)-1].Action != "token_issued" {
		t.Fatalf("missing token issue audit: %+v", events)
	}
	data, _ := json.Marshal(events)
	if strings.Contains(string(data), "opsi_pat_") || strings.Contains(string(data), "provider-token") {
		t.Fatalf("audit leaked credential: %s", data)
	}
}

func TestBrowserAuthCallbackRejectsEmailWithoutStableSubject(t *testing.T) {
	server := NewServer(Config{GitHubApp: GitHubAppConfig{
		ClientID: "client", ClientSecret: "secret",
		CallbackURL: "https://cloud.example.test/v1/auth/browser/callback",
	}})
	server.HTTPClient = newGitHubHTTPClient()
	server.HTTPClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.String() {
		case githubTokenURL:
			return githubJSONResponse(r, http.StatusOK, `{"access_token":"provider-token"}`), nil
		case githubUserURL:
			return githubJSONResponse(r, http.StatusOK, `{"email":"u@example.test"}`), nil
		default:
			t.Fatalf("provider URL = %s", r.URL)
			return nil, nil
		}
	})
	project, err := server.Registry.CreateProject("org", "Demo", "demo", "u", "project-key")
	if err != nil {
		t.Fatal(err)
	}
	server.Auth = &auth.Service{Store: &auth.MemoryStore{
		Candidates: []auth.Candidate{{ID: "membership", UserID: "u", Email: "u@example.test", OrgID: "org", ProjectID: project.ID, Role: "Owner"}},
	}}
	handler := server.Handler()

	req := httptest.NewRequest(http.MethodPost, "/v1/auth/browser/start", bytes.NewReader([]byte(`{"local_callback":"http://127.0.0.1:9780/api/local/session/callback","local_state":"local-state","project_id":"`+project.ID+`"}`)))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("start status=%d body=%s", w.Code, w.Body.String())
	}
	var start struct {
		AuthURL string `json:"auth_url"`
	}
	if err := json.NewDecoder(w.Body).Decode(&start); err != nil {
		t.Fatal(err)
	}
	authURL, err := url.Parse(start.AuthURL)
	if err != nil {
		t.Fatal(err)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/auth/browser/callback?code=provider-code&state="+url.QueryEscape(authURL.Query().Get("state")), nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	location, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusFound || location.Query().Get("error") != "GITHUB_AUTH_FAILED" || strings.Contains(w.Body.String(), "opsi_pat_") {
		t.Fatalf("email-only callback status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestRegistryAPIReadModelsForUI(t *testing.T) {
	server := NewServer(Config{BootstrapWorkerToken: "worker-secret"})
	handler := server.Handler()

	req := httptest.NewRequest(http.MethodPost, "/api/orgs/org-1/projects", bytes.NewReader([]byte(`{"name":"Demo","slug":"demo","created_by":"user-1"}`)))
	req.Header.Set("Idempotency-Key", "ui-proj")
	req.Header.Set("X-Request-ID", "req-ui-proj")
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
	serviceID := createService(t, handler, project.ID)

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+project.ID+"/nodes", bytes.NewReader([]byte(`{"name":"vps-1","role":"server","status":"healthy","public_host":"203.0.113.10"}`)))
	req.Header.Set("Idempotency-Key", "ui-node")
	req.Header.Set("X-Request-ID", "req-ui-node")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("node status=%d body=%s", w.Code, w.Body.String())
	}
	var node struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(w.Body).Decode(&node); err != nil {
		t.Fatal(err)
	}
	_ = registerDeployAgent(t, handler, project.ID, node.ID, "ui-agent")

	req = httptest.NewRequest(http.MethodGet, "/api/projects/"+project.ID+"/nodes", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("nodes status=%d body=%s", w.Code, w.Body.String())
	}
	var nodes struct {
		Nodes []struct {
			ID                 string `json:"id"`
			AgentID            string `json:"agent_id"`
			AgentEndpoint      string `json:"agent_endpoint"`
			AgentPort          int    `json:"agent_port"`
			AgentTLSServerName string `json:"agent_tls_server_name"`
			AgentCertSHA256    string `json:"agent_cert_sha256"`
		} `json:"nodes"`
	}
	if err := json.NewDecoder(w.Body).Decode(&nodes); err != nil {
		t.Fatal(err)
	}
	if len(nodes.Nodes) != 1 || nodes.Nodes[0].ID != node.ID || nodes.Nodes[0].AgentID == "" || nodes.Nodes[0].AgentEndpoint != "203.0.113.10" || nodes.Nodes[0].AgentPort != 9443 || nodes.Nodes[0].AgentTLSServerName != "203.0.113.10" || nodes.Nodes[0].AgentCertSHA256 != strings.Repeat("a", 64) {
		t.Fatalf("unexpected node list: %+v", nodes)
	}

	for _, path := range []string{
		"/api/projects/" + project.ID + "/services",
		"/api/projects/" + project.ID + "/bootstrap-sessions",
		"/api/projects/" + project.ID + "/audit",
	} {
		req = httptest.NewRequest(http.MethodGet, path, nil)
		w = httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, w.Code, w.Body.String())
		}
		if !bytes.Contains(w.Body.Bytes(), []byte(serviceID)) && path == "/api/projects/"+project.ID+"/services" {
			t.Fatalf("%s missing service id: %s", path, w.Body.String())
		}
	}
}

func TestRegistryAPIListNodesEmptyEnvelope(t *testing.T) {
	server := NewServer(Config{})
	handler := server.Handler()

	req := httptest.NewRequest(http.MethodPost, "/api/orgs/org-1/projects", bytes.NewReader([]byte(`{"name":"Demo","slug":"empty-nodes","created_by":"user-1"}`)))
	req.Header.Set("Idempotency-Key", "empty-nodes-project")
	req.Header.Set("X-Request-ID", "req-empty-nodes-project")
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

	req = httptest.NewRequest(http.MethodGet, "/api/projects/"+project.ID+"/nodes", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("nodes status=%d content-type=%q body=%s", w.Code, w.Header().Get("Content-Type"), w.Body.String())
	}
	var response struct {
		Nodes []json.RawMessage `json:"nodes"`
	}
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Nodes == nil || len(response.Nodes) != 0 {
		t.Fatalf("empty node list=%+v", response.Nodes)
	}
}

func TestRegistryAPIListNodesContractWithCLIClient(t *testing.T) {
	server := NewServer(Config{})
	handler := server.Handler()

	req := httptest.NewRequest(http.MethodPost, "/api/orgs/org-1/projects", bytes.NewReader([]byte(`{"name":"Contract","slug":"node-list-contract","created_by":"user-1"}`)))
	req.Header.Set("Idempotency-Key", "node-list-contract-project")
	req.Header.Set("X-Request-ID", "req-node-list-contract-project")
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

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+project.ID+"/nodes", bytes.NewReader([]byte(`{"name":"contract-node","role":"server","status":"healthy","public_host":"52.77.226.123"}`)))
	req.Header.Set("Idempotency-Key", "node-list-contract-node")
	req.Header.Set("X-Request-ID", "req-node-list-contract-node")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create node status=%d body=%s", w.Code, w.Body.String())
	}
	var node struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(w.Body).Decode(&node); err != nil {
		t.Fatal(err)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+project.ID+"/agents", bytes.NewReader([]byte(`{"node_id":"`+node.ID+`","public_key_fingerprint":"sha256:contract","version":"v1","agent_endpoint":"52.77.226.123","agent_port":9443,"agent_tls_server_name":"52.77.226.123","agent_cert_sha256":"`+strings.Repeat("b", 64)+`"}`)))
	req.Header.Set("Idempotency-Key", "node-list-contract-agent")
	req.Header.Set("X-Request-ID", "req-node-list-contract-agent")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("register agent status=%d body=%s", w.Code, w.Body.String())
	}
	var agentResponse struct {
		Agent struct {
			ID string `json:"id"`
		} `json:"agent"`
	}
	if err := json.NewDecoder(w.Body).Decode(&agentResponse); err != nil || agentResponse.Agent.ID == "" {
		t.Fatalf("decode registered agent: agent=%+v err=%v", agentResponse, err)
	}

	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	repoRoot, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "test", "./internal/cloudclient", "-run", "^TestListNodesAgainstExternalHandler$", "-count=1")
	command.Dir = filepath.Join(repoRoot, "cli")
	command.Env = append(os.Environ(), "OPSI_CLOUDCLIENT_CONTRACT_URL="+httpServer.URL, "OPSI_CLOUDCLIENT_CONTRACT_PROJECT_ID="+project.ID, "OPSI_CLOUDCLIENT_CONTRACT_AGENT_ID="+agentResponse.Agent.ID)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("CLI cloudclient contract test failed: %v\n%s", err, output)
	}
}

func TestSupportSummaryAndMetrics(t *testing.T) {
	server := NewServer(Config{BootstrapWorkerToken: "worker-secret"})
	handler := server.Handler()

	req := httptest.NewRequest(http.MethodPost, "/api/orgs/org-1/projects", bytes.NewReader([]byte(`{"name":"Demo","slug":"demo","created_by":"user-1"}`)))
	req.Header.Set("Idempotency-Key", "support-proj")
	req.Header.Set("X-Request-ID", "req-support-proj")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Header().Get("X-Request-ID") != "req-support-proj" {
		t.Fatalf("request id was not echoed")
	}
	var project struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(w.Body).Decode(&project); err != nil {
		t.Fatal(err)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+project.ID+"/bootstrap-sessions", bytes.NewReader([]byte(`{"role":"first_server","public_host":"203.0.113.10","ssh_username":"root","auth_method":"password","ssh_password":"secret-password"}`)))
	req.Header.Set("Idempotency-Key", "support-boot")
	req.Header.Set("X-Request-ID", "req-support-boot")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("bootstrap status=%d body=%s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/projects/"+project.ID+"/support", nil)
	req.Header.Set("X-Request-ID", "req-support-summary")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("support status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.Bytes()
	for _, want := range [][]byte{
		[]byte("configured_alerts"),
		[]byte("dashboard"),
		[]byte("production_gates"),
		[]byte("break_glass_policy"),
		[]byte("credential-cleanup-failure"),
		[]byte("agent_heartbeat_lag_seconds"),
		[]byte("runbooks"),
	} {
		if !bytes.Contains(body, want) {
			t.Fatalf("support summary missing %q: %s", want, string(body))
		}
	}
	if bytes.Contains(body, []byte("secret-password")) {
		t.Fatalf("support summary leaked secret: %s", string(body))
	}

	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte("api_requests_total")) || !bytes.Contains(w.Body.Bytes(), []byte("api_request_duration_seconds_sum")) || !bytes.Contains(w.Body.Bytes(), []byte("bootstrap_sessions_total 1")) {
		t.Fatalf("metrics status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestSupportAlertsRouteToWebhookAndOutbox(t *testing.T) {
	var got []byte
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fail" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		got = append([]byte(nil), bytes.Clone(readBody(t, r))...)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer receiver.Close()

	server := NewServer(Config{Alerts: AlertConfig{WebhookURL: receiver.URL, MinSeverity: "medium"}})
	handler := server.Handler()
	projectID := createProject(t, handler, "alert-proj")
	createNode(t, handler, projectID, "pending-node", "pending")

	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/support", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("support status=%d body=%s", w.Code, w.Body.String())
	}
	if !bytes.Contains(got, []byte(`"title":"node not healthy"`)) || bytes.Contains(got, []byte("Authorization")) {
		t.Fatalf("bad alert webhook payload: %s", string(got))
	}

	outbox := filepath.Join(t.TempDir(), "alerts.jsonl")
	server = NewServer(Config{Alerts: AlertConfig{WebhookURL: receiver.URL + "/fail", MinSeverity: "medium", OutboxPath: outbox}})
	handler = server.Handler()
	projectID = createProject(t, handler, "alert-outbox")
	createNode(t, handler, projectID, "pending-node", "pending")
	req = httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/support", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	body, err := os.ReadFile(outbox)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte(`"title":"node not healthy"`)) {
		t.Fatalf("outbox missing alert: %s", string(body))
	}
}

func TestInternalAlertmanagerWebhookIsRedactedAndTokenGated(t *testing.T) {
	outbox := filepath.Join(t.TempDir(), "alerts.jsonl")
	server := NewServer(Config{Alerts: AlertConfig{OutboxPath: outbox, InternalToken: "12345678901234567890123456789012"}})
	handler := server.Handler()

	req := httptest.NewRequest(http.MethodPost, "/api/internal/alerts", bytes.NewReader([]byte(`{"alerts":[]}`)))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized alert status=%d body=%s", w.Code, w.Body.String())
	}

	payload := []byte(`{"alerts":[{"status":"firing","labels":{"alertname":"OpsiControlPlaneHighErrorRate","severity":"high","project_id":"proj-1","resource_id":"api","password":"secret"},"annotations":{"summary":"OPSI control plane high error rate","runbook":"control-plane-outage","raw_log":"token=abc"}}]}`)
	req = httptest.NewRequest(http.MethodPost, "/api/internal/alerts", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer 12345678901234567890123456789012")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("alert status=%d body=%s", w.Code, w.Body.String())
	}
	body, err := os.ReadFile(outbox)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range [][]byte{[]byte(`"project_id":"proj-1"`), []byte(`"title":"OPSI control plane high error rate"`), []byte(`"runbook_id":"control-plane-outage"`)} {
		if !bytes.Contains(body, want) {
			t.Fatalf("outbox missing %q: %s", want, string(body))
		}
	}
	for _, forbidden := range [][]byte{[]byte("secret"), []byte("token=abc"), []byte("raw_log"), []byte("password")} {
		if bytes.Contains(body, forbidden) {
			t.Fatalf("outbox leaked %q: %s", forbidden, string(body))
		}
	}
}

func TestCloudRootReturnsNotFound(t *testing.T) {
	handler := NewServer(Config{}).Handler()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("ui status=%d body=%s", w.Code, w.Body.String())
	}
}

func createProject(t *testing.T, handler http.Handler, key string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/orgs/org-1/projects", bytes.NewReader([]byte(`{"name":"Demo","slug":"demo"}`)))
	req.Header.Set("Idempotency-Key", key)
	req.Header.Set("X-Request-ID", "req-"+key)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("project status=%d body=%s", w.Code, w.Body.String())
	}
	var project struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(w.Body).Decode(&project); err != nil {
		t.Fatal(err)
	}
	return project.ID
}

func createNode(t *testing.T, handler http.Handler, projectID, name, status string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/nodes", bytes.NewReader([]byte(`{"name":"`+name+`","role":"server","status":"`+status+`","public_host":"203.0.113.10"}`)))
	req.Header.Set("Idempotency-Key", "node-"+name)
	req.Header.Set("X-Request-ID", "req-node-"+name)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("node status=%d body=%s", w.Code, w.Body.String())
	}
}

func readBody(t *testing.T, r *http.Request) []byte {
	t.Helper()
	defer r.Body.Close()
	body := new(bytes.Buffer)
	if _, err := body.ReadFrom(r.Body); err != nil {
		t.Fatal(err)
	}
	return body.Bytes()
}

func createService(t *testing.T, handler http.Handler, projectID string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/services", bytes.NewReader([]byte(`{"name":"api","type":"application","source_type":"git","repo_url":"https://github.com/example/api.git","branch":"main","git_sha":"a8f9c1d","container_port":8080,"health_path":"/health","replicas":2}`)))
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

func registerDeployAgent(t *testing.T, handler http.Handler, projectID, nodeID, key string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/agents", bytes.NewReader([]byte(`{"node_id":"`+nodeID+`","public_key_fingerprint":"sha256:test","version":"v1","capabilities":{"deploy":true},"agent_endpoint":"203.0.113.10","agent_port":9443,"agent_tls_server_name":"203.0.113.10","agent_cert_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)))
	req.Header.Set("Idempotency-Key", key)
	req.Header.Set("X-Request-ID", "req-"+key)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("agent status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		AgentToken string `json:"agent_token"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.AgentToken == "" {
		t.Fatal("missing agent token")
	}
	req = httptest.NewRequest(http.MethodPost, "/v1/agents/"+nodeID+"/heartbeat?project_id="+projectID, bytes.NewReader([]byte(`{"version":"v1","k3s_status":"ready","node_ready":true,"capacity":{"cpu_cores":2,"memory_mb":4096,"disk_total_gb":80},"capabilities":{"deploy":true}}`)))
	req.Header.Set("Authorization", "Bearer "+resp.AgentToken)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("heartbeat status=%d body=%s", w.Code, w.Body.String())
	}
	return resp.AgentToken
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

func TestRegistryAPIProjectCreationUsesAuthenticatedActor(t *testing.T) {
	trustedHash, err := auth.HashPAT("trusted_pat")
	if err != nil {
		t.Fatal(err)
	}
	newAuth := func(projectID string) *auth.Service {
		return &auth.Service{Store: auth.MemoryStore{Candidates: []auth.Candidate{{
			UserID: "authenticated-user", OrgID: "org-1", ProjectID: projectID, Role: "Owner", Hash: trustedHash,
		}}}}
	}

	server := NewServer(Config{})
	server.Auth = newAuth("")
	handler := server.Handler()
	create := func(key string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/orgs/org-1/projects", bytes.NewReader([]byte(`{"name":"Demo","slug":"demo","created_by":"attacker-user"}`)))
		req.Header.Set("Authorization", "Bearer trusted_pat")
		req.Header.Set("Idempotency-Key", key)
		req.Header.Set("X-Request-ID", "req-"+key)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}

	first := create("project-actor")
	if first.Code != http.StatusCreated || bytes.Contains(first.Body.Bytes(), []byte("attacker-user")) {
		t.Fatalf("project create status=%d body=%s", first.Code, first.Body.String())
	}
	var project registry.Project
	if err := json.NewDecoder(first.Body).Decode(&project); err != nil {
		t.Fatal(err)
	}
	if project.CreatedBy != "authenticated-user" {
		t.Fatalf("project actor=%q", project.CreatedBy)
	}
	server.Auth = newAuth(project.ID)
	replay := create("project-actor")
	if replay.Code != http.StatusCreated || bytes.Contains(replay.Body.Bytes(), []byte("attacker-user")) {
		t.Fatalf("project replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	var replayed registry.Project
	if err := json.NewDecoder(replay.Body).Decode(&replayed); err != nil {
		t.Fatal(err)
	}
	if replayed.ID != project.ID || replayed.CreatedBy != "authenticated-user" {
		t.Fatalf("project replay=%+v first=%+v", replayed, project)
	}
	projects, err := server.Registry.ListProjects("org-1")
	if err != nil || len(projects) != 1 || projects[0].CreatedBy != "authenticated-user" {
		t.Fatalf("persisted projects=%+v err=%v", projects, err)
	}
	events, err := server.Registry.ListAudit(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	projectEvents := 0
	for _, event := range events {
		if event.Action != "PROJECT_CREATED" {
			continue
		}
		projectEvents++
		if event.ActorUserID != "authenticated-user" {
			t.Fatalf("project audit actor=%q event=%+v", event.ActorUserID, event)
		}
		if bytes.Contains(mustJSON(t, event), []byte("attacker-user")) {
			t.Fatalf("project audit leaked spoofed actor: %+v", event)
		}
	}
	if projectEvents != 1 {
		t.Fatalf("project audit count=%d events=%+v", projectEvents, events)
	}

	emptyServer := NewServer(Config{})
	emptyServer.Auth = newAuth("")
	emptyHandler := emptyServer.Handler()
	emptyRequest := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/orgs/org-1/projects", bytes.NewReader([]byte(`{"name":"Empty","slug":"empty","created_by":"attacker-user"}`)))
		req.Header.Set("Authorization", "Bearer trusted_pat")
		req.Header.Set("Idempotency-Key", "empty-principal-project")
		req.Header.Set("X-Request-ID", "req-empty-principal-project")
		w := httptest.NewRecorder()
		emptyHandler.ServeHTTP(w, req)
		return w
	}
	emptyPrincipalRequest := httptest.NewRequest(http.MethodPost, "/api/orgs/org-1/projects", bytes.NewReader([]byte(`{"name":"Empty","slug":"empty","created_by":"attacker-user"}`)))
	emptyPrincipalRequest.Header.Set("Idempotency-Key", "empty-principal-project")
	emptyPrincipalRequest.Header.Set("X-Request-ID", "req-empty-principal-project")
	w := httptest.NewRecorder()
	emptyServer.handleOrgProjects(w, emptyPrincipalRequest, "org-1", auth.VerifyResult{OrgID: "org-1", Role: "owner"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("empty principal status=%d body=%s", w.Code, w.Body.String())
	}
	if projects, err := emptyServer.Registry.ListProjects("org-1"); err != nil || len(projects) != 0 {
		t.Fatalf("empty principal mutated projects=%+v err=%v", projects, err)
	}
	trustedRetry := emptyRequest()
	if trustedRetry.Code != http.StatusCreated || bytes.Contains(trustedRetry.Body.Bytes(), []byte("attacker-user")) {
		w := trustedRetry
		t.Fatalf("trusted retry status=%d body=%s", w.Code, w.Body.String())
	}
	var trustedProject registry.Project
	if err := json.NewDecoder(trustedRetry.Body).Decode(&trustedProject); err != nil {
		t.Fatal(err)
	}
	trustedEvents, err := emptyServer.Registry.ListAudit(trustedProject.ID)
	if err != nil || len(trustedEvents) != 1 || trustedEvents[0].ActorUserID != "authenticated-user" {
		t.Fatalf("trusted retry audit=%+v err=%v", trustedEvents, err)
	}
}

func TestRegistryAPINodeLifecycleUsesAuthenticatedActor(t *testing.T) {
	trustedHash, err := auth.HashPAT("trusted_pat")
	if err != nil {
		t.Fatal(err)
	}
	viewerHash, err := auth.HashPAT("viewer_pat")
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(Config{})
	project, err := server.Registry.CreateProject("org-1", "Demo", "demo", "authenticated-user", "lifecycle-project")
	if err != nil {
		t.Fatal(err)
	}
	executor, err := server.Registry.UpsertNode(project.ID, "executor", "server", registry.NodeHealthy, "203.0.113.10", "", "lifecycle-executor")
	if err != nil {
		t.Fatal(err)
	}
	agentHash, err := auth.HashPAT("agent-secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.Registry.RegisterAgent(project.ID, executor.ID, "sha256:lifecycle", agentHash, "v1", "lifecycle-agent", map[string]any{"node_lifecycle": true}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.Registry.RecordAgentHeartbeat(project.ID, executor.ID, registry.AgentHeartbeat{NodeReady: true, Capabilities: map[string]any{"node_lifecycle": true}}); err != nil {
		t.Fatal(err)
	}
	target, err := server.Registry.UpsertNode(project.ID, "target", "worker", registry.NodeHealthy, "203.0.113.11", "", "lifecycle-target")
	if err != nil {
		t.Fatal(err)
	}
	setAuth := func() {
		server.Auth = &auth.Service{Store: auth.MemoryStore{Candidates: []auth.Candidate{
			{UserID: "authenticated-user", OrgID: "org-1", ProjectID: project.ID, Role: "Owner", Hash: trustedHash},
			{UserID: "viewer-user", OrgID: "org-1", ProjectID: project.ID, Role: "Viewer", Hash: viewerHash},
		}}}
	}
	setAuth()
	handler := server.Handler()
	request := func(action, key, token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/projects/"+project.ID+"/nodes/"+target.ID+"/"+action, bytes.NewReader([]byte(body)))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Idempotency-Key", key)
		req.Header.Set("X-Request-ID", "req-"+key)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}

	drainBody := `{"requested_by":"attacker-user"}`
	drain := request("drain", "drain-actor", "trusted_pat", drainBody)
	if drain.Code != http.StatusAccepted || bytes.Contains(drain.Body.Bytes(), []byte("attacker-user")) {
		t.Fatalf("drain status=%d body=%s", drain.Code, drain.Body.String())
	}
	var drainJob registry.NodeLifecycleJob
	if err := json.NewDecoder(drain.Body).Decode(&drainJob); err != nil {
		t.Fatal(err)
	}
	if drainJob.RequestedBy != "authenticated-user" {
		t.Fatalf("drain actor=%q job=%+v", drainJob.RequestedBy, drainJob)
	}
	drainReplay := request("drain", "drain-actor", "trusted_pat", drainBody)
	var drainReplayJob registry.NodeLifecycleJob
	if drainReplay.Code != http.StatusAccepted || json.NewDecoder(drainReplay.Body).Decode(&drainReplayJob) != nil || drainReplayJob.ID != drainJob.ID || drainReplayJob.RequestedBy != "authenticated-user" {
		t.Fatalf("drain replay status=%d body=%s job=%+v", drainReplay.Code, drainReplay.Body.String(), drainReplayJob)
	}

	if w := request("drain", "viewer-drain", "viewer_pat", drainBody); w.Code != http.StatusForbidden {
		t.Fatalf("viewer drain status=%d body=%s", w.Code, w.Body.String())
	}
	if w := request("remove", "remove-missing-confirmation", "trusted_pat", `{"requested_by":"attacker-user"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("remove without confirmation status=%d body=%s", w.Code, w.Body.String())
	}
	removeBody := `{"requested_by":"attacker-user","confirm_remove":true}`
	remove := request("remove", "remove-actor", "trusted_pat", removeBody)
	if remove.Code != http.StatusAccepted || bytes.Contains(remove.Body.Bytes(), []byte("attacker-user")) {
		t.Fatalf("remove status=%d body=%s", remove.Code, remove.Body.String())
	}
	var removeJob registry.NodeLifecycleJob
	if err := json.NewDecoder(remove.Body).Decode(&removeJob); err != nil {
		t.Fatal(err)
	}
	if removeJob.RequestedBy != "authenticated-user" || !removeJob.ConfirmRemove {
		t.Fatalf("remove job=%+v", removeJob)
	}
	removeReplay := request("remove", "remove-actor", "trusted_pat", removeBody)
	var removeReplayJob registry.NodeLifecycleJob
	if removeReplay.Code != http.StatusAccepted || json.NewDecoder(removeReplay.Body).Decode(&removeReplayJob) != nil || removeReplayJob.ID != removeJob.ID || removeReplayJob.RequestedBy != "authenticated-user" {
		t.Fatalf("remove replay status=%d body=%s job=%+v", removeReplay.Code, removeReplay.Body.String(), removeReplayJob)
	}

	emptyPrincipalRequest := httptest.NewRequest(http.MethodPost, "/api/projects/"+project.ID+"/nodes/"+target.ID+"/drain", bytes.NewReader([]byte(`{"requested_by":"attacker-user"}`)))
	emptyPrincipalRequest.Header.Set("Idempotency-Key", "empty-principal-drain")
	emptyPrincipalRequest.Header.Set("X-Request-ID", "req-empty-principal-drain")
	w := httptest.NewRecorder()
	server.handleProjectAPI(w, emptyPrincipalRequest, []string{"projects", project.ID, "nodes", target.ID, "drain"}, auth.VerifyResult{OrgID: "org-1", ProjectID: project.ID, Role: "owner"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("empty principal drain status=%d body=%s", w.Code, w.Body.String())
	}
	emptyRetry := request("drain", "empty-principal-drain", "trusted_pat", `{"requested_by":"attacker-user"}`)
	if emptyRetry.Code != http.StatusAccepted || bytes.Contains(emptyRetry.Body.Bytes(), []byte("attacker-user")) {
		t.Fatalf("trusted drain retry status=%d body=%s", emptyRetry.Code, emptyRetry.Body.String())
	}
	var emptyRetryJob registry.NodeLifecycleJob
	if err := json.NewDecoder(emptyRetry.Body).Decode(&emptyRetryJob); err != nil || emptyRetryJob.RequestedBy != "authenticated-user" {
		t.Fatalf("trusted drain retry job=%+v err=%v", emptyRetryJob, err)
	}

	events, err := server.Registry.ListAudit(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	requested := map[string]int{}
	for _, event := range events {
		if bytes.Contains(mustJSON(t, event), []byte("attacker-user")) {
			t.Fatalf("lifecycle audit leaked spoofed actor: %+v", event)
		}
		if event.Action == "NODE_LIFECYCLE_REQUESTED" {
			if event.ActorUserID != "authenticated-user" {
				t.Fatalf("lifecycle audit actor=%q event=%+v", event.ActorUserID, event)
			}
			requested[event.ResourceID]++
		}
	}
	if requested[drainJob.ID] != 1 || requested[removeJob.ID] != 1 || requested[emptyRetryJob.ID] != 1 || len(requested) != 3 {
		t.Fatalf("lifecycle audit replay counts=%v events=%+v", requested, events)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
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
	privateKeyMarker := "-----BEGIN OPENSSH " + "PRIVATE KEY-----"
	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/bootstrap-sessions", bytes.NewReader([]byte(`{"role":"first_server","public_host":"203.0.113.10","ssh_username":"root","auth_method":"private_key","ssh_private_key":"`+privateKeyMarker+`\nsecret\n-----END OPENSSH PRIVATE KEY-----","ssh_password":"unexpected"}`)))
	req.Header.Set("Authorization", "Bearer owner_pat")
	req.Header.Set("Idempotency-Key", "boot-private-key")
	req.Header.Set("X-Request-ID", "req-boot-private-key")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest || !bytes.Contains(w.Body.Bytes(), []byte("private_key auth requires ssh_private_key only")) {
		t.Fatalf("private key bootstrap status=%d body=%s", w.Code, w.Body.String())
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

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/bootstrap-sessions", bytes.NewReader([]byte(`{"role":"worker","public_host":"203.0.113.11","ssh_username":"root","auth_method":"password","ssh_password":"secret"}`)))
	req.Header.Set("Authorization", "Bearer owner_pat")
	req.Header.Set("Idempotency-Key", "worker-before-server")
	req.Header.Set("X-Request-ID", "req-worker-before-server")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("worker before server status=%d body=%s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/internal/bootstrap/sessions/lease", bytes.NewReader([]byte(`{"worker_id":"worker-1"}`)))
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauth worker lease status=%d body=%s", w.Code, w.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/internal/bootstrap/sessions/"+session.ID+"/take", nil)
	req.Header.Set("X-Bootstrap-Worker-Token", "worker-secret")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("old worker take status=%d body=%s", w.Code, w.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/internal/bootstrap/sessions/lease", bytes.NewReader([]byte(`{"worker_id":"worker-1"}`)))
	req.Header.Set("X-Bootstrap-Worker-Token", "worker-secret")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("worker lease status=%d body=%s", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("secret")) {
		t.Fatalf("worker bundle missing password: %s", w.Body.String())
	}
	var bundle struct {
		Bundle struct {
			AgentRegistrationToken string `json:"agent_registration_token"`
		} `json:"bundle"`
		LeaseToken string `json:"lease_token"`
	}
	if err := json.NewDecoder(w.Body).Decode(&bundle); err != nil {
		t.Fatal(err)
	}
	if bundle.Bundle.AgentRegistrationToken == "" || bundle.LeaseToken == "" {
		t.Fatal("missing agent registration token")
	}
	req = httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/readiness", nil)
	req.Header.Set("Authorization", "Bearer owner_pat")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK || bytes.Contains(w.Body.Bytes(), []byte(`"status":"ready"`)) {
		t.Fatalf("add server claimed readiness before worker verification status=%d body=%s", w.Code, w.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/internal/bootstrap/sessions/"+session.ID+"/progress", bytes.NewReader([]byte(`{"project_id":"`+projectID+`","status":"connecting","message":"password=secret token=abc private_key=leak kubeconfig=leak pat=leak app_secret=leak"}`)))
	req.Header.Set("X-Bootstrap-Worker-Token", "worker-secret")
	req.Header.Set("X-Bootstrap-Worker-ID", "worker-1")
	req.Header.Set("X-Bootstrap-Lease-Token", bundle.LeaseToken)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("bootstrap progress status=%d body=%s", w.Code, w.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/internal/bootstrap/sessions/"+session.ID+"/progress", bytes.NewReader([]byte(`{"project_id":"`+projectID+`","status":"configure_swap","message":"configuring idempotent system swap"}`)))
	req.Header.Set("X-Bootstrap-Worker-Token", "worker-secret")
	req.Header.Set("X-Bootstrap-Worker-ID", "worker-1")
	req.Header.Set("X-Bootstrap-Lease-Token", bundle.LeaseToken)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("bootstrap configure_swap status=%d body=%s", w.Code, w.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/internal/bootstrap/sessions/"+session.ID+"/progress", bytes.NewReader([]byte(`{"project_id":"`+projectID+`","status":"installing_k3s","message":"installing k3s"}`)))
	req.Header.Set("X-Bootstrap-Worker-Token", "worker-secret")
	req.Header.Set("X-Bootstrap-Worker-ID", "worker-1")
	req.Header.Set("X-Bootstrap-Lease-Token", bundle.LeaseToken)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("bootstrap installing_k3s status=%d body=%s", w.Code, w.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/internal/bootstrap/sessions/lease", bytes.NewReader([]byte(`{"worker_id":"worker-1"}`)))
	req.Header.Set("X-Bootstrap-Worker-Token", "worker-secret")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("worker second lease status=%d body=%s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/agents/register", bytes.NewReader([]byte(`{"registration_token":"`+bundle.Bundle.AgentRegistrationToken+`","public_key_fingerprint":"sha256:abc","version":"v1","agent_endpoint":"203.0.113.10","agent_port":9443,"agent_tls_server_name":"203.0.113.10","agent_cert_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)))
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
	req = httptest.NewRequest(http.MethodPost, "/v1/agents/"+agentResp.Agent.NodeID+"/heartbeat?project_id="+projectID, bytes.NewReader([]byte(`{"version":"v1.1","k3s_status":"ready","node_ready":true,"capacity":{"cpu_cores":2,"memory_mb":4096,"disk_total_gb":80},"capabilities":{"deploy":true}}`)))
	req.Header.Set("Authorization", "Bearer "+agentResp.AgentToken)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("heartbeat status=%d body=%s", w.Code, w.Body.String())
	}
	var healthyNode struct {
		Status       string `json:"status"`
		LastSeenAt   string `json:"last_seen_at"`
		MemoryMB     int    `json:"memory_mb"`
		K3SStatus    string `json:"k3s_status"`
		AgentVersion string `json:"agent_version"`
	}
	if err := json.NewDecoder(w.Body).Decode(&healthyNode); err != nil {
		t.Fatal(err)
	}
	if healthyNode.Status != "healthy" || healthyNode.LastSeenAt == "" || healthyNode.MemoryMB != 4096 || healthyNode.K3SStatus != "ready" || healthyNode.AgentVersion != "v1.1" {
		t.Fatalf("unexpected heartbeat node: %+v", healthyNode)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/readiness", nil)
	req.Header.Set("Authorization", "Bearer owner_pat")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("readiness after heartbeat status=%d body=%s", w.Code, w.Body.String())
	}
	var ready struct {
		Status    string `json:"status"`
		CanDeploy bool   `json:"can_deploy"`
	}
	if err := json.NewDecoder(w.Body).Decode(&ready); err != nil {
		t.Fatal(err)
	}
	if ready.Status != "ready" || !ready.CanDeploy {
		t.Fatalf("unexpected readiness after heartbeat: %+v", ready)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/bootstrap-sessions/"+session.ID, nil)
	req.Header.Set("Authorization", "Bearer owner_pat")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("bootstrap session after heartbeat status=%d body=%s", w.Code, w.Body.String())
	}
	var afterHeartbeat struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(w.Body).Decode(&afterHeartbeat); err != nil {
		t.Fatal(err)
	}
	if afterHeartbeat.Status != "verifying" {
		t.Fatalf("heartbeat claimed bootstrap completion: %+v", afterHeartbeat)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/nodes/"+agentResp.Agent.NodeID, nil)
	req.Header.Set("Authorization", "Bearer owner_pat")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("diagnostics status=%d body=%s", w.Code, w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte("password=secret")) || bytes.Contains(w.Body.Bytes(), []byte("token=abc")) || !bytes.Contains(w.Body.Bytes(), []byte("agent heartbeat marked node healthy")) {
		t.Fatalf("bad diagnostics body: %s", w.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/bootstrap-sessions", bytes.NewReader([]byte(`{"role":"worker","public_host":"203.0.113.11","ssh_username":"root","auth_method":"password","ssh_password":"secret"}`)))
	req.Header.Set("Authorization", "Bearer owner_pat")
	req.Header.Set("Idempotency-Key", "worker-after-server")
	req.Header.Set("X-Request-ID", "req-worker-after-server")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("worker after server status=%d body=%s", w.Code, w.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/v1/agents/register", bytes.NewReader([]byte(`{"registration_token":"`+bundle.Bundle.AgentRegistrationToken+`","public_key_fingerprint":"sha256:abc","version":"v1"}`)))
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

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/nodes/"+agentResp.Agent.NodeID+"/remove", nil)
	req.Header.Set("Authorization", "Bearer owner_pat")
	req.Header.Set("Idempotency-Key", "node-remove-danger")
	req.Header.Set("X-Request-ID", "req-node-remove-danger")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("only server remove status=%d body=%s", w.Code, w.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/nodes/"+agentResp.Agent.NodeID+"/drain", nil)
	req.Header.Set("Authorization", "Bearer owner_pat")
	req.Header.Set("Idempotency-Key", "node-drain")
	req.Header.Set("X-Request-ID", "req-node-drain")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusConflict || !bytes.Contains(w.Body.Bytes(), []byte(`"error_code":"AGENT_NOT_READY"`)) {
		t.Fatalf("drain status=%d body=%s", w.Code, w.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/nodes/"+agentResp.Agent.NodeID+"/drain", nil)
	req.Header.Set("Authorization", "Bearer owner_pat")
	req.Header.Set("Idempotency-Key", "node-drain")
	req.Header.Set("X-Request-ID", "req-node-drain-retry")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusConflict || !bytes.Contains(w.Body.Bytes(), []byte(`"next_action":"wait_for_agent"`)) {
		t.Fatalf("drain retry status=%d body=%s", w.Code, w.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/nodes/"+agentResp.Agent.NodeID+"/remove?force=true", bytes.NewReader([]byte(`{"confirm_remove":true}`)))
	req.Header.Set("Authorization", "Bearer owner_pat")
	req.Header.Set("Idempotency-Key", "node-remove-force")
	req.Header.Set("X-Request-ID", "req-node-remove-force")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusConflict || !bytes.Contains(w.Body.Bytes(), []byte(`"error_code":"AGENT_NOT_READY"`)) {
		t.Fatalf("force remove status=%d body=%s", w.Code, w.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/nodes/"+agentResp.Agent.NodeID, nil)
	req.Header.Set("Authorization", "Bearer owner_pat")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK || bytes.Contains(w.Body.Bytes(), []byte(`"status":"draining"`)) || bytes.Contains(w.Body.Bytes(), []byte(`"status":"removed"`)) {
		t.Fatalf("blocked lifecycle mutated node status=%d body=%s", w.Code, w.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/audit", nil)
	req.Header.Set("Authorization", "Bearer owner_pat")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte("NODE_LIFECYCLE_REQUEST_REJECTED")) {
		t.Fatalf("missing blocked lifecycle audit status=%d body=%s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/internal/bootstrap/sessions/"+session.ID+"/finish", bytes.NewReader([]byte(`{"project_id":"`+projectID+`","status":"succeeded","message":"password=secret token=abc"}`)))
	req.Header.Set("X-Bootstrap-Worker-Token", "worker-secret")
	req.Header.Set("X-Bootstrap-Worker-ID", "worker-1")
	req.Header.Set("X-Bootstrap-Lease-Token", bundle.LeaseToken)
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
	if !bytes.Contains(w.Body.Bytes(), []byte(`"step":"connecting"`)) || !bytes.Contains(w.Body.Bytes(), []byte(`"step":"configure_swap"`)) || !bytes.Contains(w.Body.Bytes(), []byte(`"step":"installing_k3s"`)) {
		t.Fatalf("missing truthful bootstrap transitions: %s", w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte("password=secret")) || bytes.Contains(w.Body.Bytes(), []byte("token=abc")) || bytes.Contains(w.Body.Bytes(), []byte("private_key=leak")) || bytes.Contains(w.Body.Bytes(), []byte("kubeconfig=leak")) || bytes.Contains(w.Body.Bytes(), []byte("pat=leak")) || bytes.Contains(w.Body.Bytes(), []byte("app_secret=leak")) {
		t.Fatalf("bootstrap events leaked secret: %s", w.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/audit", nil)
	req.Header.Set("Authorization", "Bearer owner_pat")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte("BOOTSTRAP_STATE_CONNECTING")) || !bytes.Contains(w.Body.Bytes(), []byte("BOOTSTRAP_STATE_CONFIGURE_SWAP")) || !bytes.Contains(w.Body.Bytes(), []byte("BOOTSTRAP_STATE_INSTALLING_K3S")) {
		t.Fatalf("missing bootstrap transition audit status=%d body=%s", w.Code, w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte("password=secret")) || bytes.Contains(w.Body.Bytes(), []byte("token=abc")) || bytes.Contains(w.Body.Bytes(), []byte("private_key=leak")) || bytes.Contains(w.Body.Bytes(), []byte("kubeconfig=leak")) || bytes.Contains(w.Body.Bytes(), []byte("pat=leak")) || bytes.Contains(w.Body.Bytes(), []byte("app_secret=leak")) {
		t.Fatalf("bootstrap audit leaked secret: %s", w.Body.String())
	}
}

func TestBootstrapManualRetryOwnerAdminIdempotencyAndPreconditions(t *testing.T) {
	ownerHash, _ := auth.HashPAT("owner_pat")
	adminHash, _ := auth.HashPAT("admin_pat")
	developerHash, _ := auth.HashPAT("developer_pat")
	viewerHash, _ := auth.HashPAT("viewer_pat")
	server := NewServer(Config{BootstrapWorkerToken: "worker-secret"})
	project, err := server.Registry.CreateProject("org-1", "Demo", "demo", "", "project-key")
	if err != nil {
		t.Fatal(err)
	}
	server.Auth = &auth.Service{Store: auth.MemoryStore{Candidates: []auth.Candidate{
		{UserID: "owner", OrgID: "org-1", ProjectID: project.ID, Role: "Owner", Hash: ownerHash},
		{UserID: "admin", OrgID: "org-1", ProjectID: project.ID, Role: "Admin", Hash: adminHash},
		{UserID: "developer", OrgID: "org-1", ProjectID: project.ID, Role: "Developer", Hash: developerHash},
		{UserID: "viewer", OrgID: "org-1", ProjectID: project.ID, Role: "Viewer", Hash: viewerHash},
	}}}
	session, err := server.Registry.CreateBootstrapSession(project.ID, "first_server", "203.0.113.10", "root", "password", "", "boot-key", 22)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	lease, ok, err := server.Registry.LeaseNextBootstrapSession("worker-1", "", now, time.Minute)
	if err != nil || !ok {
		t.Fatalf("lease ok=%v err=%v", ok, err)
	}
	dead, err := server.Registry.FinishBootstrapSessionForLease(project.ID, session.ID, "worker-1", lease.LeaseToken, registry.BootstrapFinishResult{Status: "failed", FailureCode: "SSH_AUTH_METHOD_UNSUPPORTED", MessageRedacted: "unsupported"}, now.Add(time.Second))
	if err != nil || dead.Status != registry.BootstrapDeadLetter {
		t.Fatalf("dead=%+v err=%v", dead, err)
	}
	handler := server.Handler()
	retry := func(token, key string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/projects/"+project.ID+"/bootstrap-sessions/"+session.ID+"/retry", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Request-ID", "req-"+key)
		req.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	for _, token := range []string{"viewer_pat", "developer_pat"} {
		if w := retry(token, "denied-"+token); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "BOOTSTRAP_RETRY_FORBIDDEN") {
			t.Fatalf("denied token=%s status=%d body=%s", token, w.Code, w.Body.String())
		}
	}
	if w := retry("owner_pat", "missing-credential"); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "BOOTSTRAP_RETRY_CREDENTIAL_UNAVAILABLE") {
		t.Fatalf("missing credential status=%d body=%s", w.Code, w.Body.String())
	}
	server.credentials.Put(session.ID, BootstrapCredential{AuthMethod: "password", Username: "root", Password: []byte("ssh-secret")}, time.Hour)
	first := retry("admin_pat", "retry-1")
	if first.Code != http.StatusAccepted {
		t.Fatalf("manual retry status=%d body=%s", first.Code, first.Body.String())
	}
	duplicate := retry("admin_pat", "retry-1")
	if duplicate.Code != http.StatusAccepted || duplicate.Body.String() != first.Body.String() {
		t.Fatalf("duplicate status=%d body=%s first=%s", duplicate.Code, duplicate.Body.String(), first.Body.String())
	}
	if w := retry("owner_pat", "retry-2"); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "BOOTSTRAP_NOT_DEAD_LETTER") {
		t.Fatalf("non-dead-letter status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestAgentTokenGate(t *testing.T) {
	server := NewServer(Config{RequireAgentSignatures: true})
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
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unsigned agent status=%d body=%s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/agents/"+node.ID+"/webhooks/next?project_id="+project.ID+"&wait=0s", nil)
	req.Header.Set("Authorization", "Bearer agent-secret")
	signAgentRequest(req, "agent-secret")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("agent token status=%d body=%s", w.Code, w.Body.String())
	}
}

func signAgentRequest(req *http.Request, token string) {
	ts := time.Now().UTC().Format(time.RFC3339)
	req.Header.Set("X-Agent-Timestamp", ts)
	mac := hmac.New(sha256.New, []byte(token))
	_, _ = mac.Write([]byte(req.Method + "\n" + req.URL.RequestURI() + "\n" + ts))
	req.Header.Set("X-Agent-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
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

func TestBootstrapCredentialAcceptsPrivateKeyWithoutLoggingIt(t *testing.T) {
	key := "-----BEGIN OPENSSH " + "PRIVATE KEY-----\nsecret\n-----END OPENSSH PRIVATE KEY-----"
	credential, err := bootstrapCredential("private_key", "ubuntu", key, "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer clearBootstrapCredential(&credential)
	if credential.AuthMethod != "private_key" || credential.Username != "ubuntu" || string(credential.PrivateKey) != key || len(credential.Password) != 0 {
		t.Fatalf("unexpected private-key credential metadata: method=%q user=%q private_key_bytes=%d password_bytes=%d", credential.AuthMethod, credential.Username, len(credential.PrivateKey), len(credential.Password))
	}
}

func TestGitHubInventoryClaimBindingAPIAndRBAC(t *testing.T) {
	server, projectID, token, store := installationClaimServer(t, "owner", roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("GitHub network should not be called by inventory APIs")
		return nil, nil
	}))
	installation := registry.GitHubInstallation{InstallationID: 7001, AccountID: 8001, AccountLogin: "example", AccountType: "Organization", Status: registry.GitHubInstallationActive}
	repository := registry.GitHubRepository{RepositoryID: 9001, InstallationID: installation.InstallationID, OwnerID: 8001, OwnerLogin: "example", Name: "mono", FullName: "example/mono", Private: true, DefaultBranch: "main", Status: registry.GitHubRepositoryActive}
	if _, err := server.Registry.UpsertGitHubInstallation(installation); err != nil {
		t.Fatal(err)
	}
	if _, err := server.Registry.UpsertGitHubRepository(repository); err != nil {
		t.Fatal(err)
	}
	if _, err := server.Registry.ClaimGitHubInstallation(projectID, installation.InstallationID, "user-1"); err != nil {
		t.Fatal(err)
	}
	service, err := server.Registry.CreateService(projectID, registry.ServiceDraft{Name: "api"}, "service-key")
	if err != nil {
		t.Fatal(err)
	}

	for _, role := range []string{"owner", "admin", "developer", "viewer", "support"} {
		store.Candidates[0].Role = role
		response := serveGitHubAPI(server, token, http.MethodGet, "/v1/projects/"+projectID+"/github/installations", "")
		if response.Code != http.StatusOK {
			t.Fatalf("role=%s installations status=%d body=%s", role, response.Code, response.Body.String())
		}
		response = serveGitHubAPI(server, token, http.MethodGet, "/v1/projects/"+projectID+"/github/repositories", "")
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"repository_id":9001`) {
			t.Fatalf("role=%s repositories status=%d body=%s", role, response.Code, response.Body.String())
		}
	}
	for _, role := range []string{"viewer", "developer", "support"} {
		store.Candidates[0].Role = role
		response := serveGitHubAPI(server, token, http.MethodPost, "/v1/projects/"+projectID+"/github/repositories/9001/claim", "{}")
		if response.Code != http.StatusForbidden {
			t.Fatalf("role=%s claim status=%d body=%s", role, response.Code, response.Body.String())
		}
		response = serveGitHubAPI(server, token, http.MethodPost, "/v1/projects/"+projectID+"/github/bindings", `{"service_id":"`+service.ID+`","repository_id":9001,"service_key":"api"}`)
		if response.Code != http.StatusForbidden {
			t.Fatalf("role=%s binding status=%d body=%s", role, response.Code, response.Body.String())
		}
	}

	store.Candidates[0].Role = "owner"
	claimResponse := serveGitHubAPI(server, token, http.MethodPost, "/v1/projects/"+projectID+"/github/repositories/9001/claim", "{}")
	if claimResponse.Code != http.StatusOK {
		t.Fatalf("claim status=%d body=%s", claimResponse.Code, claimResponse.Body.String())
	}
	invalidKey := serveGitHubAPI(server, token, http.MethodPost, "/v1/projects/"+projectID+"/github/bindings", `{"service_id":"`+service.ID+`","repository_id":9001,"service_key":"Invalid"}`)
	if invalidKey.Code != http.StatusBadRequest {
		t.Fatalf("invalid key status=%d body=%s", invalidKey.Code, invalidKey.Body.String())
	}
	invalidPath := serveGitHubAPI(server, token, http.MethodPost, "/v1/projects/"+projectID+"/github/bindings", `{"service_id":"`+service.ID+`","repository_id":9001,"service_key":"api","config_path":"../opsi.yaml"}`)
	if invalidPath.Code != http.StatusBadRequest {
		t.Fatalf("invalid path status=%d body=%s", invalidPath.Code, invalidPath.Body.String())
	}
	invalidRoot := serveGitHubAPI(server, token, http.MethodPost, "/v1/projects/"+projectID+"/github/bindings", `{"service_id":"`+service.ID+`","repository_id":9001,"service_key":"api","application_root":"apps/api","build_context":"packages"}`)
	if invalidRoot.Code != http.StatusBadRequest {
		t.Fatalf("invalid root status=%d body=%s", invalidRoot.Code, invalidRoot.Body.String())
	}
	bindingDraft := `{"service_id":"` + service.ID + `","repository_id":9001,"service_key":"api","selected_ref":"main","application_root":"apps/api","build_context":".","build_strategy":"dockerfile","dockerfile_path":"apps/api/Dockerfile"}`
	bindingResponse := serveGitHubAPI(server, token, http.MethodPost, "/v1/projects/"+projectID+"/github/bindings", bindingDraft)
	if bindingResponse.Code != http.StatusCreated {
		t.Fatalf("binding status=%d body=%s", bindingResponse.Code, bindingResponse.Body.String())
	}
	var binding registry.GitHubServiceBinding
	if err := json.Unmarshal(bindingResponse.Body.Bytes(), &binding); err != nil || binding.ApplicationRoot != "apps/api" || binding.BuildContext != "." || binding.BuildStrategy != registry.BuildStrategyDockerfile || binding.DockerfilePath != "apps/api/Dockerfile" {
		t.Fatal(err)
	}
	duplicate := serveGitHubAPI(server, token, http.MethodPost, "/v1/projects/"+projectID+"/github/bindings", bindingDraft)
	var duplicateBinding registry.GitHubServiceBinding
	if duplicate.Code != http.StatusCreated || json.Unmarshal(duplicate.Body.Bytes(), &duplicateBinding) != nil || duplicateBinding.ID != binding.ID {
		t.Fatalf("duplicate status=%d body=%s", duplicate.Code, duplicate.Body.String())
	}
	bindingPath := "/v1/projects/" + projectID + "/github/bindings/" + binding.ID
	read := serveGitHubAPI(server, token, http.MethodGet, bindingPath, "")
	if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), `"application_root":"apps/api"`) {
		t.Fatalf("read status=%d body=%s", read.Code, read.Body.String())
	}
	update := serveGitHubAPI(server, token, http.MethodPut, bindingPath, `{"selected_ref":"release","application_root":"apps/api","build_context":"apps","build_strategy":"auto"}`)
	if update.Code != http.StatusOK || !strings.Contains(update.Body.String(), `"selected_ref":"release"`) || !strings.Contains(update.Body.String(), `"build_context":"apps"`) {
		t.Fatalf("update status=%d body=%s", update.Code, update.Body.String())
	}
	release := serveGitHubAPI(server, token, http.MethodDelete, "/v1/projects/"+projectID+"/github/repositories/9001/claim", "")
	if release.Code != http.StatusConflict {
		t.Fatalf("release with binding status=%d body=%s", release.Code, release.Body.String())
	}
	removePath := bindingPath
	for range 2 {
		remove := serveGitHubAPI(server, token, http.MethodDelete, removePath, "")
		if remove.Code != http.StatusOK {
			t.Fatalf("remove status=%d body=%s", remove.Code, remove.Body.String())
		}
	}
	release = serveGitHubAPI(server, token, http.MethodDelete, "/v1/projects/"+projectID+"/github/repositories/9001/claim", "")
	if release.Code != http.StatusOK {
		t.Fatalf("release status=%d body=%s", release.Code, release.Body.String())
	}

	inactive := repository
	inactive.RepositoryID, inactive.Status = 9002, registry.GitHubRepositoryRemoved
	if _, err := server.Registry.UpsertGitHubRepository(inactive); err != nil {
		t.Fatal(err)
	}
	response := serveGitHubAPI(server, token, http.MethodPost, "/v1/projects/"+projectID+"/github/repositories/9002/claim", "{}")
	if response.Code != http.StatusConflict {
		t.Fatalf("inactive claim status=%d body=%s", response.Code, response.Body.String())
	}
	archived := repository
	archived.RepositoryID, archived.Archived = 9003, true
	if _, err := server.Registry.UpsertGitHubRepository(archived); err != nil {
		t.Fatal(err)
	}
	response = serveGitHubAPI(server, token, http.MethodPost, "/v1/projects/"+projectID+"/github/repositories/9003/claim", "{}")
	if response.Code != http.StatusConflict {
		t.Fatalf("archived claim status=%d body=%s", response.Code, response.Body.String())
	}

	otherProject, err := server.Registry.CreateProject("org", "Other", "other", "user-2", "other-project")
	if err != nil {
		t.Fatal(err)
	}
	otherInstallation := registry.GitHubInstallation{InstallationID: 7002, AccountID: 8002, AccountLogin: "private-other", AccountType: "Organization", Status: registry.GitHubInstallationActive}
	otherRepository := registry.GitHubRepository{RepositoryID: 9010, InstallationID: 7002, OwnerID: 8002, OwnerLogin: "private-other", Name: "secret", FullName: "private-other/secret", DefaultBranch: "main", Status: registry.GitHubRepositoryActive}
	_, _ = server.Registry.UpsertGitHubInstallation(otherInstallation)
	_, _ = server.Registry.UpsertGitHubRepository(otherRepository)
	_, _ = server.Registry.ClaimGitHubInstallation(otherProject.ID, otherInstallation.InstallationID, "user-2")
	response = serveGitHubAPI(server, token, http.MethodGet, "/v1/projects/"+projectID+"/github/repositories", "")
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "private-other") || strings.Contains(response.Body.String(), "9010") {
		t.Fatalf("cross-project inventory leaked: %s", response.Body.String())
	}
}

func serveGitHubAPI(server *Server, token, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	return response
}
