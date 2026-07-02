package cloudrelay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestPollDeploymentAndComplete(t *testing.T) {
	var completed bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer agent-token" {
			t.Fatalf("missing bearer token: %q", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/agents/node-1/webhooks/next":
			_, _ = w.Write([]byte(`{"kind":"deployment","action":"deploy","deployment":{"id":"dep-1","deployment_plan_hash":"plan","manifest_hash":"manifest"},"service":{"id":"svc-api","name":"api","type":"application","source_type":"git","repo_url":"https://example.test/repo.git","branch":"main","git_sha":"abc","namespace":"default","health_path":"/health","replicas":2}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/agents/node-1/deployments/dep-1/result":
			completed = true
			if !strings.Contains(r.URL.RawQuery, "project_id=proj-dev") {
				t.Fatalf("missing project query: %s", r.URL.RawQuery)
			}
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client := Client{BaseURL: server.URL, ProjectID: "proj-dev", AgentToken: "agent-token"}
	lease, err := client.PollDeployment(context.Background(), "node-1", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if lease == nil || lease.Deployment.ID != "dep-1" || lease.Service.GitSHA != "abc" || lease.Action != "deploy" {
		t.Fatalf("unexpected lease: %+v", lease)
	}
	if err := client.CompleteDeployment(context.Background(), "node-1", "dep-1", DeploymentResult{Status: "succeeded", FinalRevisionRef: "rev-1", RollbackEligible: true}); err != nil {
		t.Fatal(err)
	}
	if !completed {
		t.Fatal("result was not submitted")
	}
}
