package bootstrapworker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/opsi-dev/opsi/cloud/internal/registry"
)

func TestHTTPCloudClientCheckpoint(t *testing.T) {
	checkpoint := registry.BootstrapCheckpoint{
		SchemaVersion:     registry.BootstrapCheckpointSchemaVersion,
		PlanVersion:       registry.FirstServerBootstrapPlanVersion,
		PlanFingerprint:   strings.Repeat("a", 64),
		NextStepIndex:     1,
		LastCompletedStep: "preflight",
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/internal/bootstrap/sessions/boot-1/checkpoint" {
			t.Fatalf("path=%q", r.URL.Path)
		}
		if r.Header.Get("X-Bootstrap-Worker-Token") != "worker-token" || r.Header.Get("X-Bootstrap-Worker-ID") != "worker-1" || r.Header.Get("X-Bootstrap-Lease-Token") != "lease-token" {
			t.Fatalf("headers=%v", r.Header)
		}
		var body struct {
			ProjectID string `json:"project_id"`
			registry.BootstrapCheckpoint
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.ProjectID != "proj-1" || body.NextStepIndex != 1 || body.LastCompletedStep != "preflight" {
			t.Fatalf("body=%+v", body)
		}
		_ = json.NewEncoder(w).Encode(checkpoint)
	}))
	defer server.Close()
	client := httpCloudClient{client: server.Client(), cfg: Config{CloudURL: server.URL, BootstrapWorkerToken: "worker-token", WorkerID: "worker-1"}}
	lease := Lease{Bundle: Bundle{SessionID: "boot-1", ProjectID: "proj-1"}, LeaseToken: "lease-token"}
	saved, err := client.Checkpoint(context.Background(), lease, checkpoint)
	if err != nil || saved.NextStepIndex != 1 || requests != 1 {
		t.Fatalf("saved=%+v requests=%d err=%v", saved, requests, err)
	}
}

func TestHTTPCloudClientCheckpointParsesConflictWithoutRetry(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"error_code": "BOOTSTRAP_PLAN_MISMATCH", "message": "plan mismatch"})
	}))
	defer server.Close()
	client := httpCloudClient{client: server.Client(), cfg: Config{CloudURL: server.URL, BootstrapWorkerToken: "worker-token", WorkerID: "worker-1"}}
	lease := Lease{Bundle: Bundle{SessionID: "boot-1", ProjectID: "proj-1"}, LeaseToken: "lease-token"}
	_, err := client.Checkpoint(context.Background(), lease, registry.BootstrapCheckpoint{})
	if cloudErrorCode(err) != "BOOTSTRAP_PLAN_MISMATCH" || requests != 1 || isLeaseLossError(err) {
		t.Fatalf("code=%q requests=%d lease_loss=%v err=%v", cloudErrorCode(err), requests, isLeaseLossError(err), err)
	}
}
