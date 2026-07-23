package cloudrelay

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPollJobNoContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	job, err := Client{BaseURL: server.URL}.PollJob(context.Background(), "node-1", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if job != nil {
		t.Fatalf("expected no job, got %+v", job)
	}
}

func TestPollJobAndCompleteDeployment(t *testing.T) {
	var completed bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer agent-token" {
			t.Fatalf("missing bearer token: %q", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/agents/node-1/webhooks/next":
			_, _ = w.Write([]byte(`{"kind":"deployment","action":"deploy","lease_token":"lease-1","deployment":{"id":"dep-1","deployment_plan_hash":"plan"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/agents/node-1/deployments/dep-1/result":
			completed = true
			if !strings.Contains(r.URL.RawQuery, "project_id=proj-dev") {
				t.Fatalf("missing project query: %s", r.URL.RawQuery)
			}
			var result DeploymentResult
			if err := json.NewDecoder(r.Body).Decode(&result); err != nil {
				t.Fatal(err)
			}
			if result.LeaseToken != "lease-1" {
				t.Fatalf("lease token = %q", result.LeaseToken)
			}
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client := Client{BaseURL: server.URL, ProjectID: "proj-dev", AgentToken: "agent-token"}
	job, err := client.PollJob(context.Background(), "node-1", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if job == nil || job.Deployment == nil || job.Deployment.Deployment.ID != "dep-1" || job.Deployment.Action != "deploy" || job.Deployment.LeaseToken != "lease-1" {
		t.Fatalf("unexpected job: %+v", job)
	}
	if err := client.CompleteDeployment(context.Background(), "node-1", "dep-1", DeploymentResult{Status: "succeeded", LeaseToken: job.Deployment.LeaseToken, FinalRevisionRef: "rev-1", RollbackEligible: true}); err != nil {
		t.Fatal(err)
	}
	if !completed {
		t.Fatal("result was not submitted")
	}
}

func TestClientSignsAgentRequests(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ts := r.Header.Get("X-Agent-Timestamp")
		mac := hmac.New(sha256.New, []byte("agent-token"))
		_, _ = mac.Write([]byte(r.Method + "\n" + r.URL.RequestURI() + "\n" + ts))
		if r.Header.Get("X-Agent-Signature") != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
			t.Fatalf("bad signature headers: ts=%q sig=%q", ts, r.Header.Get("X-Agent-Signature"))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	_, err := Client{BaseURL: server.URL, ProjectID: "proj-dev", AgentToken: "agent-token", SignRequests: true}.PollJob(context.Background(), "node-1", time.Second)
	if err != nil {
		t.Fatal(err)
	}
}
