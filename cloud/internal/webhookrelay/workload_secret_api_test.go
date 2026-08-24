package webhookrelay

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/auth"
	"github.com/opsi-dev/opsi/cloud/internal/deploymentworkflow"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
	"github.com/opsi-dev/opsi/cloud/internal/repositoryanalysis"
)

func TestWorkloadSecretAPIRedactsValueScopesRolesAndReplays(t *testing.T) {
	server := NewServer(Config{})
	project, _ := server.Registry.CreateProject("org-1", "A", "secret-project", "owner", "secret-project")
	other, _ := server.Registry.CreateProject("org-1", "B", "secret-other", "owner", "secret-other")
	application, err := server.Registry.CreateService(project.ID, registry.ServiceDraft{Name: "app", Type: "application", SourceType: "git", RepoURL: "https://github.com/owner/repo.git", Branch: "main", GitSHA: strings.Repeat("a", 40), BuildMethod: "dockerfile", BuildContext: ".", Dockerfile: "Dockerfile", ContainerPort: 8080, HealthPath: "/health", Replicas: 1}, "app-create")
	if err != nil {
		t.Fatal(err)
	}
	ownerHash, _ := auth.HashPAT("secret-owner-pat")
	viewerHash, _ := auth.HashPAT("secret-viewer-pat")
	server.Auth = &auth.Service{Store: auth.MemoryStore{Candidates: []auth.Candidate{
		{ID: "owner", UserID: "owner", OrgID: "org-1", ProjectID: project.ID, Role: "owner", Hash: ownerHash, ExpiresAt: time.Now().Add(time.Hour)},
		{ID: "viewer", UserID: "viewer", OrgID: "org-1", ProjectID: project.ID, Role: "viewer", Hash: viewerHash, ExpiresAt: time.Now().Add(time.Hour)},
	}}}
	request := func(method, path, token, key, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		req.Header.Set("X-Request-ID", "request-id")
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, req)
		return response
	}
	path := "/api/projects/" + project.ID + "/applications/" + application.ID + "/workload-secrets"
	secretValue := "never-return-this-value"
	first := request(http.MethodPut, path, "secret-owner-pat", "secret-upsert-1", `{"logical_name":"api-token","value":"`+secretValue+`"}`)
	if first.Code != http.StatusOK || strings.Contains(first.Body.String(), secretValue) {
		t.Fatalf("status=%d body=%s", first.Code, first.Body.String())
	}
	var firstBody struct {
		WorkloadSecret struct {
			Revision  uint64 `json:"revision"`
			Reference string `json:"reference"`
		} `json:"workload_secret"`
		Reused bool `json:"reused"`
	}
	if json.Unmarshal(first.Body.Bytes(), &firstBody) != nil || firstBody.WorkloadSecret.Revision != 1 || !strings.HasPrefix(firstBody.WorkloadSecret.Reference, "workload-secret://") || firstBody.Reused {
		t.Fatalf("body=%s", first.Body.String())
	}
	replay := request(http.MethodPut, path, "secret-owner-pat", "secret-upsert-1", `{"logical_name":"api-token","value":"`+secretValue+`"}`)
	if replay.Code != http.StatusOK || strings.Contains(replay.Body.String(), secretValue) || !strings.Contains(replay.Body.String(), `"reused":true`) {
		t.Fatalf("replay=%s", replay.Body.String())
	}
	rotated := request(http.MethodPut, path, "secret-owner-pat", "secret-upsert-2", `{"logical_name":"api-token","value":"rotated-value"}`)
	if rotated.Code != http.StatusOK || !strings.Contains(rotated.Body.String(), `"revision":2`) {
		t.Fatalf("rotate=%s", rotated.Body.String())
	}
	if response := request(http.MethodGet, path, "secret-viewer-pat", "", ""); response.Code != http.StatusOK || strings.Contains(response.Body.String(), secretValue) || strings.Contains(response.Body.String(), "rotated-value") {
		t.Fatalf("viewer list=%s", response.Body.String())
	}
	if response := request(http.MethodPut, path, "secret-viewer-pat", "viewer-write", `{"logical_name":"api-token","value":"forbidden"}`); response.Code != http.StatusForbidden {
		t.Fatalf("viewer write status=%d body=%s", response.Code, response.Body.String())
	}
	if response := request(http.MethodGet, "/api/projects/"+other.ID+"/applications/"+application.ID+"/workload-secrets", "secret-owner-pat", "", ""); response.Code == http.StatusOK {
		t.Fatalf("cross-project secret metadata exposed: %s", response.Body.String())
	}

	run, _, err := server.DeploymentRuns.Create(context.Background(), project.ID, "owner", "planned-run", deploymentworkflow.Source{RepositoryID: 7, InstallationID: 9, Repository: "owner/planned", SelectedRef: "main"}, deploymentworkflow.Target{EnvironmentID: "env-1", RuntimeID: "runtime-1", Exposure: "internal"})
	if err != nil {
		t.Fatal(err)
	}
	analysis := repositoryanalysis.Result{
		SchemaVersion: repositoryanalysis.SchemaVersion,
		RepositoryID:  7,
		Repository:    "owner/planned",
		SelectedRef:   "main",
		CommitSHA:     strings.Repeat("b", 40),
		Applications: []repositoryanalysis.Application{{
			SourceKey: "app",
			Key:       "planned-app",
			Name:      "app",
			Root:      ".",
			Port:      8080,
			Build: repositoryanalysis.Build{
				Context:        ".",
				Strategy:       "dockerfile",
				DockerfilePath: "Dockerfile",
				Platform:       "linux/amd64",
			},
		}},
	}
	if _, err = server.DeploymentRuns.SetAnalysis(context.Background(), project.ID, run.ID, analysis, deploymentworkflow.AuthorityRevisions{SourceCommitSHA: analysis.CommitSHA}, run.Plan.Target); err != nil {
		t.Fatal(err)
	}
	plannedPath := "/api/projects/" + project.ID + "/applications/planned-app/workload-secrets"
	planned := request(http.MethodPut, plannedPath, "secret-owner-pat", "planned-secret-upsert", `{"logical_name":"oauth-client","value":"planned-value"}`)
	if planned.Code != http.StatusOK || strings.Contains(planned.Body.String(), "planned-value") || !strings.Contains(planned.Body.String(), `"service_id":"planned:planned-app"`) {
		t.Fatalf("planned secret status=%d body=%s", planned.Code, planned.Body.String())
	}
	var plannedBody struct {
		WorkloadSecret struct {
			Reference string `json:"reference"`
			Revision  uint64 `json:"revision"`
		} `json:"workload_secret"`
	}
	if err := json.Unmarshal(planned.Body.Bytes(), &plannedBody); err != nil {
		t.Fatal(err)
	}
	plannedRun, _ := server.DeploymentRuns.Get(context.Background(), project.ID, run.ID)
	draft := plannedRun.Plan
	draft.Secrets = []repositoryanalysis.Secret{{Name: "oauth-client", ApplicationKey: "planned-app", EnvironmentName: "OAUTH_CLIENT_SECRET", SecretRef: plannedBody.WorkloadSecret.Reference, Revision: plannedBody.WorkloadSecret.Revision, Display: "Securely stored"}}
	plannedRun, err = server.DeploymentRuns.UpdatePlan(context.Background(), project.ID, run.ID, "owner", plannedRun.Plan.Hash, draft)
	if err != nil {
		t.Fatal(err)
	}
	if rotated := request(http.MethodPut, plannedPath, "secret-owner-pat", "planned-secret-rotate", `{"logical_name":"oauth-client","value":"rotated-planned-value"}`); rotated.Code != http.StatusOK {
		t.Fatalf("rotate planned=%s", rotated.Body.String())
	}
	approve := request(http.MethodPost, "/api/projects/"+project.ID+"/deployment-runs/"+run.ID+"/approve", "secret-owner-pat", "approve-planned", `{"plan_hash":"`+plannedRun.Plan.Hash+`"}`)
	if approve.Code != http.StatusOK || !strings.Contains(approve.Body.String(), `"state":"stale"`) || strings.Contains(approve.Body.String(), "rotated-planned-value") {
		t.Fatalf("stale approval status=%d body=%s", approve.Code, approve.Body.String())
	}
}
