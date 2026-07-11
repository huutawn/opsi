package webhookrelay

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
	if env.Body != "" {
		t.Fatal("raw webhook body must not be persisted or delivered")
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

func TestQueueSanitizesEnvelopeAndPreservesChangedFiles(t *testing.T) {
	queue := NewQueue()
	body := `{"commits":[{"modified":["apps/api/main.go"],"added":["packages/shared/a.go"],"removed":["old.go"]}],"password":"hunter2"}`
	if err := queue.Enqueue(Envelope{ProjectID: "proj", ServiceID: "svc", Body: body, IdempotencyKey: "delivery-1", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	env, err := queue.Next(t.Context(), "proj", 0)
	if err != nil {
		t.Fatal(err)
	}
	if env == nil || env.Body != "" || len(env.Modified) != 3 {
		t.Fatalf("bad sanitized env: %+v", env)
	}
	data, _ := json.Marshal(env)
	if strings.Contains(string(data), "hunter2") || strings.Contains(string(data), `"body"`) {
		t.Fatalf("sensitive raw payload persisted: %s", data)
	}
}

func TestQueueIdempotencyRejectsDifferentBody(t *testing.T) {
	queue := NewQueue()
	base := Envelope{ProjectID: "proj", ServiceID: "svc", IdempotencyKey: "delivery-1", ExpiresAt: time.Now().Add(time.Hour)}
	base.Body = `{"after":"a"}`
	if err := queue.Enqueue(base); err != nil {
		t.Fatal(err)
	}
	again := base
	again.Body = `{"after":"b"}`
	if err := queue.Enqueue(again); err == nil {
		t.Fatal("expected idempotency conflict")
	}
	same := base
	if err := queue.Enqueue(same); err != nil {
		t.Fatal(err)
	}
	if got := queue.Len(); got != 1 {
		t.Fatalf("duplicate idempotency key should not enqueue twice, got %d", got)
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

func TestAIRoutesRemoved(t *testing.T) {
	handler := NewServer(Config{}).Handler()
	tests := []struct {
		method string
		path   string
	}{
		{method: http.MethodPost, path: "/v1/ai/incidents/analyze"},
		{method: http.MethodGet, path: "/v1/ai/incidents/analyze"},
		{method: http.MethodGet, path: "/v1/ai/unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tt.method, tt.path, nil)
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status=%d, want %d", rec.Code, http.StatusNotFound)
			}
		})
	}
}
