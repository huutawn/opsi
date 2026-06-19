package cloudrelay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPollWebhook(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/agents/node-1/webhooks/next" || r.URL.Query().Get("wait") != "1s" || r.URL.Query().Get("project_id") != "proj-dev" {
			t.Fatalf("unexpected request: %s", r.URL.String())
		}
		_, _ = w.Write([]byte(`{"project_id":"proj-dev","service_id":"svc-api","service_name":"api","service_type":"backend","repo_url":"https://example.test/repo.git","ref":"refs/heads/main","after":"abc","body":"{}","signature":"sha256=sig"}`))
	}))
	defer server.Close()

	event, err := Client{BaseURL: server.URL, ProjectID: "proj-dev"}.PollWebhook(context.Background(), "node-1", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if event == nil || event.ProjectID != "proj-dev" || event.ServiceID != "svc-api" || event.ServiceName != "api" || event.Ref != "refs/heads/main" || event.After != "abc" {
		t.Fatalf("unexpected event: %+v", event)
	}
}

func TestPollWebhookNoContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	event, err := Client{BaseURL: server.URL}.PollWebhook(context.Background(), "node-1", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if event != nil {
		t.Fatalf("expected no event, got %+v", event)
	}
}
