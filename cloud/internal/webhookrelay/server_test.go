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
	"strings"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/auth"
	"github.com/opsi-dev/opsi/cloud/internal/otp"
)

func TestHealthFailsClosedWhenDependencyCheckFails(t *testing.T) {
	server := NewServer(Config{})
	server.SetHealthCheck(func(context.Context) error { return errors.New("database unavailable") })
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "database unavailable") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGitHubWebhookQueuesEnvelopeAndLongPollReturnsIt(t *testing.T) {
	server := NewServer(Config{TTL: Duration(time.Hour)})
	webhookSecret := strings.Repeat("w", 32)
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
		ProjectID:     project.ID,
		ServiceID:     "svc-api",
		ServiceName:   "api",
		ServiceType:   "backend",
		RepoFullName:  "example/api",
		Branch:        "main",
		WebhookSecret: webhookSecret,
	}}

	body := []byte(`{"ref":"refs/heads/main","after":"abc123","repository":{"clone_url":"https://github.com/example/api.git","full_name":"example/api"},"pusher":{"name":"alice"}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/github", bytes.NewReader(body))
	mac := hmac.New(sha256.New, []byte(webhookSecret))
	_, _ = mac.Write(body)
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	req.Header.Set("X-Hub-Signature-256", signature)
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
	if env.ProjectID != project.ID || env.ServiceID != "svc-api" || env.Branch != "main" || env.Signature != signature || env.TriggeredBy != "alice" {
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

func TestGitHubWebhookRejectsInvalidSignature(t *testing.T) {
	server := NewServer(Config{TTL: Duration(time.Hour)})
	server.Config.Routes = []Route{{
		ProjectID: "proj-1", ServiceID: "svc-1", RepoFullName: "example/api", Branch: "main", WebhookSecret: strings.Repeat("w", 32),
	}}
	body := []byte(`{"ref":"refs/heads/main","repository":{"full_name":"example/api"}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/github", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", "sha256=deadbeef")
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized || server.Queue.Len() != 0 {
		t.Fatalf("status=%d queued=%d body=%s", w.Code, server.Queue.Len(), w.Body.String())
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

func TestOTPRequiresPATAndUsesAuthenticatedEmail(t *testing.T) {
	hash, err := auth.HashPAT("owner-pat")
	if err != nil {
		t.Fatal(err)
	}
	sender := &captureOTPSender{}
	server := NewServer(Config{})
	server.Auth = &auth.Service{Store: auth.MemoryStore{Candidates: []auth.Candidate{{
		ID: "pat-1", UserID: "user-1", Email: "owner@example.test", OrgID: "org-1", ProjectID: "proj-1", Role: "Owner", Hash: hash,
	}}}}
	server.OTP.Sender = sender
	handler := server.Handler()

	req := httptest.NewRequest(http.MethodPost, "/v1/otp/request", bytes.NewReader([]byte(`{"project_id":"proj-1","purpose":"secret.reveal"}`)))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated OTP request status=%d body=%s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/otp/request", bytes.NewReader([]byte(`{"project_id":"proj-1","user_id":"other-user","purpose":"secret.reveal"}`)))
	req.Header.Set("Authorization", "Bearer owner-pat")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-user OTP request status=%d body=%s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/otp/request", bytes.NewReader([]byte(`{"project_id":"proj-1","purpose":"secret.reveal"}`)))
	req.Header.Set("Authorization", "Bearer owner-pat")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("authenticated OTP request status=%d body=%s", w.Code, w.Body.String())
	}
	if sender.req.UserID != "user-1" || sender.req.Email != "owner@example.test" || sender.code == "" {
		t.Fatalf("OTP identity was not derived from PAT: %+v", sender.req)
	}
	var requested struct {
		RequestID string `json:"request_id"`
	}
	if err := json.NewDecoder(w.Body).Decode(&requested); err != nil {
		t.Fatal(err)
	}

	verifyBody := []byte(`{"request_id":"` + requested.RequestID + `","project_id":"proj-1","purpose":"secret.reveal","code":"` + sender.code + `"}`)
	req = httptest.NewRequest(http.MethodPost, "/v1/otp/verify", bytes.NewReader(verifyBody))
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated OTP verify status=%d body=%s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/otp/verify", bytes.NewReader(verifyBody))
	req.Header.Set("Authorization", "Bearer owner-pat")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("authenticated OTP verify status=%d body=%s", w.Code, w.Body.String())
	}
}

type captureOTPSender struct {
	req  otp.Request
	code string
}

func (s *captureOTPSender) SendOTP(_ context.Context, req otp.Request, code string, _ time.Time) error {
	s.req = req
	s.code = code
	return nil
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
