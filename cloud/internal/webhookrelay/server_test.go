package webhookrelay

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGitHubWebhookQueuesEnvelopeAndLongPollReturnsIt(t *testing.T) {
	server := NewServer(Config{TTL: Duration(time.Hour), Routes: []Route{{
		ProjectID:    "proj-dev",
		ServiceID:    "svc-api",
		ServiceName:  "api",
		ServiceType:  "backend",
		RepoFullName: "example/api",
		Branch:       "main",
	}}})

	body := []byte(`{"ref":"refs/heads/main","after":"abc123","repository":{"clone_url":"https://github.com/example/api.git","full_name":"example/api"},"pusher":{"name":"alice"}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/github", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", "sha256=test")
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("unexpected status: %d body=%s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/agents/node-1/webhooks/next?project_id=proj-dev&wait=0s", nil)
	w = httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("unexpected poll status: %d body=%s", w.Code, w.Body.String())
	}
	var env Envelope
	if err := json.NewDecoder(w.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	if env.ProjectID != "proj-dev" || env.ServiceID != "svc-api" || env.Branch != "main" || env.Signature != "sha256=test" || env.TriggeredBy != "alice" {
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
