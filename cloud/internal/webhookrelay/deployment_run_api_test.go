package webhookrelay

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/auth"
	"github.com/opsi-dev/opsi/cloud/internal/deploymentworkflow"
	"github.com/opsi-dev/opsi/cloud/internal/repositoryanalysis"
)

func TestDeploymentRunAPIViewerReadOnlyAndProjectScoped(t *testing.T) {
	server := NewServer(Config{})
	projectA, err := server.Registry.CreateProject("org-1", "A", "project-a", "owner", "project-a")
	if err != nil {
		t.Fatal(err)
	}
	projectB, err := server.Registry.CreateProject("org-1", "B", "project-b", "owner", "project-b")
	if err != nil {
		t.Fatal(err)
	}
	viewerHash, _ := auth.HashPAT("viewer-pat")
	ownerHash, _ := auth.HashPAT("owner-pat")
	server.Auth = &auth.Service{Store: auth.MemoryStore{Candidates: []auth.Candidate{
		{ID: "viewer", UserID: "viewer", OrgID: "org-1", ProjectID: projectA.ID, Role: "viewer", Hash: viewerHash, ExpiresAt: time.Now().Add(time.Hour)},
		{ID: "owner", UserID: "owner", OrgID: "org-1", ProjectID: projectA.ID, Role: "owner", Hash: ownerHash, ExpiresAt: time.Now().Add(time.Hour)},
	}}}
	run, _, err := server.DeploymentRuns.Create(context.Background(), projectA.ID, "owner", "create-run", deploymentworkflow.Source{RepositoryID: 1, InstallationID: 2, Repository: "owner/repo", SelectedRef: "main"}, deploymentworkflow.Target{EnvironmentID: "env-1", RuntimeID: "runtime-1", Exposure: "public"})
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewReader([]byte(`{}`)))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "request-key")
		req.Header.Set("X-Request-ID", "request-id")
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, req)
		return response
	}

	if response := request(http.MethodGet, "/api/projects/"+projectA.ID+"/deployment-runs/"+run.ID, "viewer-pat"); response.Code != http.StatusOK {
		t.Fatalf("viewer read status=%d body=%s", response.Code, response.Body.String())
	}
	if response := request(http.MethodPost, "/api/projects/"+projectA.ID+"/deployment-runs/"+run.ID+"/cancel", "viewer-pat"); response.Code != http.StatusForbidden {
		t.Fatalf("viewer cancel status=%d body=%s", response.Code, response.Body.String())
	}
	if response := request(http.MethodGet, "/api/projects/"+projectB.ID+"/deployment-runs/"+run.ID, "owner-pat"); response.Code == http.StatusOK {
		t.Fatalf("cross-project run was exposed: %s", response.Body.String())
	}
	if response := request(http.MethodGet, "/api/projects/"+projectA.ID+"/deployment-runs/"+run.ID+"/events", "viewer-pat"); response.Code != http.StatusOK {
		t.Fatalf("viewer events status=%d body=%s", response.Code, response.Body.String())
	}
	if response := request(http.MethodGet, "/api/projects/"+projectA.ID+"/deployment-runs/"+run.ID+"/result", "viewer-pat"); response.Code != http.StatusOK {
		t.Fatalf("viewer result status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestWorkflowHostnameIsDeterministicAndProjectScoped(t *testing.T) {
	first := workflowHostname("owner/identity-service", "project-a", "apps.example.test")
	if first != workflowHostname("owner/identity-service", "project-a", "apps.example.test") {
		t.Fatal("workflow hostname changed for identical authority")
	}
	if first == workflowHostname("owner/identity-service", "project-b", "apps.example.test") {
		t.Fatal("workflow hostname was not project scoped")
	}
	if want := ".apps.example.test"; len(first) <= len(want) || first[len(first)-len(want):] != want {
		t.Fatalf("hostname=%q", first)
	}
}

func TestDeploymentPlanUpdateRequiresExactRevisionAndReplaysSemantically(t *testing.T) {
	server := NewServer(Config{})
	project, err := server.Registry.CreateProject("org-1", "A", "plan-project", "owner", "plan-project")
	if err != nil {
		t.Fatal(err)
	}
	ownerHash, _ := auth.HashPAT("plan-owner-pat")
	server.Auth = &auth.Service{Store: auth.MemoryStore{Candidates: []auth.Candidate{{
		ID: "owner", UserID: "owner", OrgID: "org-1", ProjectID: project.ID, Role: "owner", Hash: ownerHash, ExpiresAt: time.Now().Add(time.Hour),
	}}}}
	run, _, err := server.DeploymentRuns.Create(context.Background(), project.ID, "owner", "plan-create", deploymentworkflow.Source{
		RepositoryID: 1, InstallationID: 2, Repository: "owner/repo", SelectedRef: "main",
	}, deploymentworkflow.Target{EnvironmentID: "env-1", RuntimeID: "runtime-1", Exposure: "internal"})
	if err != nil {
		t.Fatal(err)
	}
	analysis := repositoryanalysis.Result{
		SchemaVersion: repositoryanalysis.SchemaVersion,
		RepositoryID:  1,
		Repository:    "owner/repo",
		SelectedRef:   "main",
		CommitSHA:     strings.Repeat("a", 40),
		Applications: []repositoryanalysis.Application{{
			SourceKey: "api", Key: "repo-api", Name: "api", Root: ".", Port: 8080,
			Build: repositoryanalysis.Build{Context: ".", Strategy: "dockerfile", DockerfilePath: "Dockerfile", Platform: "linux/amd64"},
		}},
	}
	run, err = server.DeploymentRuns.SetAnalysis(context.Background(), project.ID, run.ID, analysis, deploymentworkflow.AuthorityRevisions{SourceCommitSHA: analysis.CommitSHA}, run.Plan.Target)
	if err != nil {
		t.Fatal(err)
	}
	draft := run.Plan
	draft.Target.Hostname = "api.apps.example.test"
	body, err := json.Marshal(map[string]any{"expected_plan_hash": run.Plan.Hash, "plan": draft})
	if err != nil {
		t.Fatal(err)
	}
	request := func(ifMatch string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/api/projects/"+project.ID+"/deployment-runs/"+run.ID+"/plan", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer plan-owner-pat")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "plan-update")
		req.Header.Set("X-Request-ID", "plan-request")
		if ifMatch != "" {
			req.Header.Set("If-Match", ifMatch)
		}
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, req)
		return response
	}
	if response := request(""); response.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing If-Match status=%d body=%s", response.Code, response.Body.String())
	}
	if response := request("1"); response.Code != http.StatusConflict {
		t.Fatalf("stale If-Match status=%d body=%s", response.Code, response.Body.String())
	}
	updated := request(strconv.FormatUint(run.Revision, 10))
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), "api.apps.example.test") {
		t.Fatalf("exact update status=%d body=%s", updated.Code, updated.Body.String())
	}
	if replay := request(strconv.FormatUint(run.Revision, 10)); replay.Code != http.StatusOK || replay.Body.String() != updated.Body.String() {
		t.Fatalf("semantic replay status=%d body=%s", replay.Code, replay.Body.String())
	}
}
