package webhookrelay

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+project.ID+"/nodes", bytes.NewReader([]byte(`{"name":"vps-1","role":"server","status":"healthy","public_host":"203.0.113.10"}`)))
	req.Header.Set("Idempotency-Key", "node-key")
	req.Header.Set("X-Request-ID", "req-3")
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
	agentToken := registerDeployAgent(t, handler, project.ID, node.ID, "agent-key")

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
		ID                 string `json:"id"`
		Status             string `json:"status"`
		DeploymentPlanHash string `json:"deployment_plan_hash"`
		ManifestHash       string `json:"manifest_hash"`
		IntentHash         string `json:"intent_hash"`
		NodeID             string `json:"node_id"`
		AgentID            string `json:"agent_id"`
		DeploymentIntent   struct {
			IntentVersion string `json:"intent_version"`
			Source        struct {
				BuildContext string `json:"build_context"`
				Dockerfile   string `json:"dockerfile"`
				ManifestPath string `json:"manifest_path"`
			} `json:"source"`
		} `json:"deployment_intent"`
	}
	if err := json.NewDecoder(w.Body).Decode(&deploy); err != nil {
		t.Fatal(err)
	}
	if deploy.Status != "queued" || deploy.DeploymentPlanHash == "" || deploy.ManifestHash == "" || deploy.IntentHash == "" || deploy.NodeID == "" || deploy.AgentID == "" || deploy.DeploymentIntent.IntentVersion == "" {
		t.Fatalf("unexpected deploy: %+v", deploy)
	}
	if deploy.DeploymentIntent.Source.BuildContext != "." || deploy.DeploymentIntent.Source.Dockerfile != "Dockerfile" || deploy.DeploymentIntent.Source.ManifestPath == "" {
		t.Fatalf("unexpected deployment intent: %+v", deploy.DeploymentIntent)
	}
	req = httptest.NewRequest(http.MethodGet, "/v1/agents/"+node.ID+"/webhooks/next?project_id="+project.ID+"&wait=0s", nil)
	req.Header.Set("Authorization", "Bearer "+agentToken)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte(`"kind":"deployment"`)) || !bytes.Contains(w.Body.Bytes(), []byte(`"intent_hash":"`)) || !bytes.Contains(w.Body.Bytes(), []byte(`"deployment_intent"`)) {
		t.Fatalf("lease status=%d body=%s", w.Code, w.Body.String())
	}
	var lease struct {
		LeaseToken string `json:"lease_token"`
	}
	if err := json.NewDecoder(w.Body).Decode(&lease); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodPost, "/v1/agents/"+node.ID+"/deployments/"+deploy.ID+"/result?project_id="+project.ID, bytes.NewReader([]byte(`{"status":"succeeded","lease_token":"`+lease.LeaseToken+`","final_revision_ref":"rev-1","rollback_eligible":true}`)))
	req.Header.Set("Authorization", "Bearer "+agentToken)
	req.Header.Set("X-Request-ID", "req-result")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte(`"status":"succeeded"`)) {
		t.Fatalf("result status=%d body=%s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+project.ID+"/services/"+serviceID+"/deployments", bytes.NewReader([]byte(`{"requested_by":"user-1"}`)))
	req.Header.Set("Idempotency-Key", "dep-key-locked")
	req.Header.Set("X-Request-ID", "req-5")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected lock released after result, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestBrowserAuthFlowUsesOneTimeGrantAndAuditsWithoutPAT(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_, _ = w.Write([]byte(`{"access_token":"provider-token"}`))
		case "/userinfo":
			if r.Header.Get("Authorization") != "Bearer provider-token" {
				t.Fatalf("provider auth = %q", r.Header.Get("Authorization"))
			}
			_, _ = w.Write([]byte(`{"email":"u@example.test"}`))
		default:
			t.Fatalf("provider path = %s", r.URL.Path)
		}
	}))
	defer provider.Close()

	server := NewServer(Config{Auth: AuthConfig{
		Provider:     "generic",
		ClientID:     "client",
		ClientSecret: "secret",
		AuthURL:      provider.URL + "/authorize",
		TokenURL:     provider.URL + "/token",
		UserInfoURL:  provider.URL + "/userinfo",
		RedirectURL:  "https://cloud.example.test/v1/auth/browser/callback",
	}})
	project, err := server.Registry.CreateProject("org", "Demo", "demo", "u", "project-key")
	if err != nil {
		t.Fatal(err)
	}
	store := &auth.MemoryStore{Candidates: []auth.Candidate{{ID: "membership", UserID: "u", Email: "u@example.test", OrgID: "org", ProjectID: project.ID, Role: "Owner"}}}
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

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+project.ID+"/services/"+serviceID+"/deployments", bytes.NewReader([]byte(`{"requested_by":"ui"}`)))
	req.Header.Set("Idempotency-Key", "ui-deploy")
	req.Header.Set("X-Request-ID", "req-ui-deploy")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("deploy status=%d body=%s", w.Code, w.Body.String())
	}

	for _, path := range []string{
		"/api/projects/" + project.ID + "/services",
		"/api/projects/" + project.ID + "/deployments",
		"/api/projects/" + project.ID + "/bootstrap-sessions",
		"/api/projects/" + project.ID + "/audit",
	} {
		req = httptest.NewRequest(http.MethodGet, path, nil)
		w = httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, w.Code, w.Body.String())
		}
		if !bytes.Contains(w.Body.Bytes(), []byte(serviceID)) && path != "/api/projects/"+project.ID+"/audit" && path != "/api/projects/"+project.ID+"/bootstrap-sessions" {
			t.Fatalf("%s missing service id: %s", path, w.Body.String())
		}
	}

	req = httptest.NewRequest(http.MethodGet, "/api/projects/"+project.ID+"/deployments", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("deployments status=%d body=%s", w.Code, w.Body.String())
	}
	var deploys struct {
		Deployments []struct {
			ID string `json:"id"`
		} `json:"deployments"`
	}
	if err := json.NewDecoder(w.Body).Decode(&deploys); err != nil {
		t.Fatal(err)
	}
	if len(deploys.Deployments) != 1 {
		t.Fatalf("expected one deployment, got %+v", deploys)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/projects/"+project.ID+"/deployments/"+deploys.Deployments[0].ID+"/events", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte("deployment queued")) {
		t.Fatalf("deployment events status=%d body=%s", w.Code, w.Body.String())
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

func TestUIShellRequiresDebugFlag(t *testing.T) {
	handler := NewServer(Config{}).Handler()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("ui status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestDebugUIShellServesWorkflow(t *testing.T) {
	handler := NewServer(Config{EnableDebugUI: true}).Handler()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ui status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.Bytes()
	for _, want := range [][]byte{
		[]byte("Servers / Nodes"),
		[]byte("Add first server"),
		[]byte("Topology will appear after at least one healthy server and one deployed service."),
		[]byte("type=\"password\""),
		[]byte("Reconnect-safe"),
	} {
		if !bytes.Contains(body, want) {
			t.Fatalf("ui missing %q", want)
		}
	}
	for _, forbidden := range [][]byte{
		[]byte("ssh "),
		[]byte("opsi deploy"),
		[]byte("localStorage.setItem(\"opsi_pat\""),
	} {
		if bytes.Contains(body, forbidden) {
			t.Fatalf("ui contains forbidden workflow/text %q", forbidden)
		}
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
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/agents", bytes.NewReader([]byte(`{"node_id":"`+nodeID+`","public_key_fingerprint":"sha256:test","version":"v1","capabilities":{"deploy":true}}`)))
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
	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/bootstrap-sessions", bytes.NewReader([]byte(`{"role":"first_server","public_host":"203.0.113.10","ssh_username":"root","auth_method":"private_key","ssh_private_key":"`+privateKeyMarker+`\nsecret\n-----END OPENSSH PRIVATE KEY-----"}`)))
	req.Header.Set("Authorization", "Bearer owner_pat")
	req.Header.Set("Idempotency-Key", "boot-private-key")
	req.Header.Set("X-Request-ID", "req-boot-private-key")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest || !bytes.Contains(w.Body.Bytes(), []byte("ssh_private_key cannot be stored by Cloud")) {
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

	req = httptest.NewRequest(http.MethodPost, "/v1/agents/register", bytes.NewReader([]byte(`{"registration_token":"`+bundle.Bundle.AgentRegistrationToken+`","public_key_fingerprint":"sha256:abc","version":"v1"}`)))
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
	if !bytes.Contains(w.Body.Bytes(), []byte(`"step":"connecting"`)) || !bytes.Contains(w.Body.Bytes(), []byte(`"step":"installing_k3s"`)) {
		t.Fatalf("missing truthful bootstrap transitions: %s", w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte("password=secret")) || bytes.Contains(w.Body.Bytes(), []byte("token=abc")) || bytes.Contains(w.Body.Bytes(), []byte("private_key=leak")) || bytes.Contains(w.Body.Bytes(), []byte("kubeconfig=leak")) || bytes.Contains(w.Body.Bytes(), []byte("pat=leak")) || bytes.Contains(w.Body.Bytes(), []byte("app_secret=leak")) {
		t.Fatalf("bootstrap events leaked secret: %s", w.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/audit", nil)
	req.Header.Set("Authorization", "Bearer owner_pat")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte("BOOTSTRAP_STATE_CONNECTING")) || !bytes.Contains(w.Body.Bytes(), []byte("BOOTSTRAP_STATE_INSTALLING_K3S")) {
		t.Fatalf("missing bootstrap transition audit status=%d body=%s", w.Code, w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte("password=secret")) || bytes.Contains(w.Body.Bytes(), []byte("token=abc")) || bytes.Contains(w.Body.Bytes(), []byte("private_key=leak")) || bytes.Contains(w.Body.Bytes(), []byte("kubeconfig=leak")) || bytes.Contains(w.Body.Bytes(), []byte("pat=leak")) || bytes.Contains(w.Body.Bytes(), []byte("app_secret=leak")) {
		t.Fatalf("bootstrap audit leaked secret: %s", w.Body.String())
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
