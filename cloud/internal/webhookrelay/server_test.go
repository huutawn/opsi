package webhookrelay

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/auth"
)

func TestGitHubWebhookQueuesEnvelopeAndLongPollReturnsIt(t *testing.T) {
	server := NewServer(Config{TTL: Duration(time.Hour)})
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
	if _, err := server.Registry.RegisterAgent(project.ID, node.ID, "sha256:abc", hash, "v1", "agent", nil); err != nil {
		t.Fatal(err)
	}
	server.Config.Routes = []Route{{
		ProjectID:    project.ID,
		ServiceID:    "svc-api",
		ServiceName:  "api",
		ServiceType:  "backend",
		RepoFullName: "example/api",
		Branch:       "main",
	}}

	body := []byte(`{"ref":"refs/heads/main","after":"abc123","repository":{"clone_url":"https://github.com/example/api.git","full_name":"example/api"},"pusher":{"name":"alice"}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/github", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", "sha256=test")
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("unexpected status: %d body=%s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/agents/"+node.ID+"/webhooks/next?project_id="+project.ID+"&wait=0s", nil)
	req.Header.Set("Authorization", "Bearer agent-secret")
	w = httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("unexpected poll status: %d body=%s", w.Code, w.Body.String())
	}
	var env Envelope
	if err := json.NewDecoder(w.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	if env.ProjectID != project.ID || env.ServiceID != "svc-api" || env.Branch != "main" || env.Signature != "sha256=test" || env.TriggeredBy != "alice" {
		t.Fatalf("unexpected envelope: %+v", env)
	}
}

func TestGitHubWebhookRejectsUnknownRoute(t *testing.T) {
	server := NewServer(Config{TTL: Duration(time.Hour)})
	req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/github", bytes.NewReader([]byte(`{"ref":"refs/heads/main","repository":{"full_name":"example/api"}}`)))
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unexpected status: %d", w.Code)
	}
}

func TestQueuePurgesExpiredEnvelopes(t *testing.T) {
	queue := NewQueue()
	now := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	queue.now = func() time.Time { return now }
	if err := queue.Enqueue(Envelope{ProjectID: "proj", ServiceID: "svc", ExpiresAt: now.Add(-time.Second)}); err != nil {
		t.Fatal(err)
	}
	if got := queue.Len(); got != 0 {
		t.Fatalf("expected expired item purged, got %d", got)
	}
}

func TestOTPRequestOmitsCodeWithoutDevEcho(t *testing.T) {
	server := NewServer(Config{})
	req := httptest.NewRequest(http.MethodPost, "/v1/otp/request", bytes.NewReader([]byte(`{"ProjectID":"proj","UserID":"user","Purpose":"secret_reveal"}`)))
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("unexpected status: %d body=%s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["code"]; ok {
		t.Fatalf("code leaked in non-dev response: %v", body)
	}
}
