package bootstrapworker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConfigValidation(t *testing.T) {
	if err := (Config{CloudURL: "https://cloud.example", BootstrapWorkerToken: strings.Repeat("x", 32), SessionID: "boot-1", Production: true}).Validate(); err != nil {
		t.Fatalf("valid production config failed: %v", err)
	}
	if err := (Config{CloudURL: "http://cloud.example", BootstrapWorkerToken: "short", SessionID: "boot-1", Production: true}).Validate(); err == nil {
		t.Fatal("missing production config did not fail closed")
	}
}

func TestRunOnceUnsupportedFailsSessionWithoutSecretLeak(t *testing.T) {
	var finishStatus, finishBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("X-Bootstrap-Worker-Token"), "worker-secret") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/take"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"session_id": "boot-1", "project_id": "proj-1", "node_id": "node-1", "public_host": "203.0.113.10", "ssh_port": 22, "agent_registration_token": "areg-secret",
				"ssh": map[string]string{"auth_method": "password", "username": "root", "password": "ssh-secret"},
			})
		case strings.HasSuffix(r.URL.Path, "/finish"):
			var req map[string]string
			_ = json.NewDecoder(r.Body).Decode(&req)
			finishStatus, finishBody = req["status"], req["message"]
			_ = json.NewEncoder(w).Encode(map[string]string{"status": req["status"]})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	err := RunOnce(context.Background(), Config{CloudURL: server.URL, BootstrapWorkerToken: "worker-secret", SessionID: "boot-1"})
	if !errors.Is(err, ErrRuntimeUnsupported) {
		t.Fatalf("expected unsupported error, got %v", err)
	}
	if finishStatus != "failed" || !strings.Contains(finishBody, "BOOTSTRAP_RUNTIME_UNSUPPORTED") {
		t.Fatalf("unexpected finish status/body: %q %q", finishStatus, finishBody)
	}
	if strings.Contains(finishBody, "ssh-secret") || strings.Contains(finishBody, "areg-secret") {
		t.Fatalf("finish leaked secret: %s", finishBody)
	}
}

func TestValidateBundleInvalidTargetFailsClosed(t *testing.T) {
	var b Bundle
	b.SessionID = "boot-1"
	b.ProjectID = "proj-1"
	b.NodeID = "node-1"
	b.AgentRegistrationToken = "areg"
	b.SSH.AuthMethod = "password"
	b.SSH.Username = "root"
	b.SSH.Password = "secret"
	if err := ValidateBundle(b); err == nil || !strings.Contains(err.Error(), "public_host") {
		t.Fatalf("expected invalid target error, got %v", err)
	}
}
