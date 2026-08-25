package webhookrelay

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/auth"
	"github.com/opsi-dev/opsi/cloud/internal/deploymentworkflow"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
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

func TestScopedReanalysisKeepsExactSHAAndPersistsScope(t *testing.T) {
	server := NewServer(Config{})
	project, err := server.Registry.CreateProject("org-1", "Scoped", "scoped-project", "owner", "scoped-project")
	if err != nil {
		t.Fatal(err)
	}
	ownerHash, _ := auth.HashPAT("scope-owner-pat")
	server.Auth = &auth.Service{Store: auth.MemoryStore{Candidates: []auth.Candidate{{ID: "owner", UserID: "owner", OrgID: "org-1", ProjectID: project.ID, Role: "owner", Hash: ownerHash, ExpiresAt: time.Now().Add(time.Hour)}}}}
	installation := registry.GitHubInstallation{InstallationID: 42, AccountID: 5, AccountLogin: "owner", AccountType: "User", Status: registry.GitHubInstallationActive}
	repository := registry.GitHubRepository{RepositoryID: 77, InstallationID: 42, OwnerID: 5, OwnerLogin: "owner", Name: "repo", FullName: "owner/repo", DefaultBranch: "main", Status: registry.GitHubRepositoryActive}
	if _, err = server.Registry.UpsertGitHubInstallation(installation); err != nil {
		t.Fatal(err)
	}
	if _, err = server.Registry.UpsertGitHubRepository(repository); err != nil {
		t.Fatal(err)
	}
	if _, err = server.Registry.ClaimGitHubInstallation(project.ID, installation.InstallationID, "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err = server.Registry.ClaimGitHubRepository(project.ID, repository.RepositoryID, "owner"); err != nil {
		t.Fatal(err)
	}
	sha := strings.Repeat("a", 40)
	client, _ := newGitHubAppTestClient(t, githubAppRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if strings.Contains(request.URL.Path, "/commits/") {
			t.Fatal("scoped reanalysis resolved a new ref instead of retaining the exact SHA")
		}
		switch {
		case strings.Contains(request.URL.Path, "/git/trees/"):
			return githubAppResponse(http.StatusOK, `{"truncated":false,"tree":[{"path":"api/Dockerfile","mode":"100644","type":"blob","size":25},{"path":"web/Dockerfile","mode":"100644","type":"blob","size":25}]}`), nil
		case request.URL.Path == "/repos/owner/repo/contents/api/Dockerfile":
			content := base64.StdEncoding.EncodeToString([]byte("FROM scratch\nEXPOSE 8080\n"))
			return githubAppResponse(http.StatusOK, `{"type":"file","path":"api/Dockerfile","encoding":"base64","size":25,"content":"`+content+`"}`), nil
		default:
			t.Fatalf("unexpected GitHub request %s", request.URL.String())
			return nil, nil
		}
	}), time.Now().UTC())
	client.tokens[installation.InstallationID] = installationToken{Token: "read-token", ExpiresAt: time.Now().Add(time.Hour)}
	server.githubAppClient = client
	server.RepositoryAnalyzer.Repository = client

	run, _, err := server.DeploymentRuns.Create(context.Background(), project.ID, "owner", "scope-create", deploymentworkflow.Source{RepositoryID: repository.RepositoryID, InstallationID: installation.InstallationID, Repository: repository.FullName, SelectedRef: "main", CommitSHA: sha}, deploymentworkflow.Target{Exposure: "internal"})
	if err != nil {
		t.Fatal(err)
	}
	initial := repositoryanalysis.Result{SchemaVersion: repositoryanalysis.SchemaVersion, RepositoryID: repository.RepositoryID, Repository: repository.FullName, SelectedRef: "main", CommitSHA: sha, Applications: []repositoryanalysis.Application{{SourceKey: "old", Key: "repo-old", Root: ".", Port: 8080, Build: repositoryanalysis.Build{Context: ".", DockerfilePath: "Dockerfile", Strategy: "dockerfile", Platform: "linux/amd64"}}}}
	run, err = server.DeploymentRuns.SetAnalysis(context.Background(), project.ID, run.ID, initial, deploymentworkflow.AuthorityRevisions{SourceCommitSHA: sha}, run.Plan.Target)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"scope":{"application_roots":["api"],"exclude_paths":[]}}`
	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+project.ID+"/deployment-runs/"+run.ID+"/analyze", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer scope-owner-pat")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "scope-analysis")
	request.Header.Set("X-Request-ID", "scope-request")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var analyzed deploymentworkflow.Run
	if json.Unmarshal(response.Body.Bytes(), &analyzed) != nil || analyzed.Plan.Source.CommitSHA != sha || len(analyzed.Plan.AnalysisScope.ApplicationRoots) != 1 || analyzed.Plan.AnalysisScope.ApplicationRoots[0] != "api" || analyzed.Plan.AnalysisScopeHash == "" || len(analyzed.Plan.Applications) != 1 || analyzed.Plan.Applications[0].Root != "api" {
		t.Fatalf("run=%+v", analyzed)
	}
}

func TestRepositoryExportAPIRolesAndProjectBoundary(t *testing.T) {
	server := NewServer(Config{})
	projectA, err := server.Registry.CreateProject("org-1", "Export A", "export-a", "owner-a", "export-a")
	if err != nil {
		t.Fatal(err)
	}
	projectB, err := server.Registry.CreateProject("org-1", "Export B", "export-b", "owner-b", "export-b")
	if err != nil {
		t.Fatal(err)
	}
	viewerHash, _ := auth.HashPAT("export-viewer-pat")
	ownerBHash, _ := auth.HashPAT("export-owner-b-pat")
	server.Auth = &auth.Service{Store: auth.MemoryStore{Candidates: []auth.Candidate{
		{ID: "viewer", UserID: "viewer", OrgID: "org-1", ProjectID: projectA.ID, Role: "viewer", Hash: viewerHash, ExpiresAt: time.Now().Add(time.Hour)},
		{ID: "owner-b", UserID: "owner-b", OrgID: "org-1", ProjectID: projectB.ID, Role: "owner", Hash: ownerBHash, ExpiresAt: time.Now().Add(time.Hour)},
	}}}
	installation := registry.GitHubInstallation{InstallationID: 52, AccountID: 5, AccountLogin: "owner", AccountType: "User", Status: registry.GitHubInstallationActive}
	repository := registry.GitHubRepository{RepositoryID: 87, InstallationID: 52, OwnerID: 5, OwnerLogin: "owner", Name: "repo", FullName: "owner/repo", DefaultBranch: "main", Status: registry.GitHubRepositoryActive}
	_, _ = server.Registry.UpsertGitHubInstallation(installation)
	_, _ = server.Registry.UpsertGitHubRepository(repository)
	_, _ = server.Registry.ClaimGitHubInstallation(projectA.ID, installation.InstallationID, "owner-a")
	_, _ = server.Registry.ClaimGitHubRepository(projectA.ID, repository.RepositoryID, "owner-a")

	now := time.Now().UTC()
	client, _ := newGitHubAppTestClient(t, githubAppRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch {
		case request.URL.Path == "/repos/owner/repo/contents/.opsi/opsi-cd.yaml":
			return githubAppResponse(http.StatusNotFound, `{}`), nil
		case request.URL.Path == "/app/installations/52/access_tokens":
			return githubAppResponse(http.StatusCreated, `{"token":"write-token","expires_at":"`+now.Add(time.Hour).Format(time.RFC3339)+`"}`), nil
		default:
			t.Fatalf("unexpected GitHub request %s", request.URL.String())
			return nil, nil
		}
	}), now)
	client.tokens[installation.InstallationID] = installationToken{Token: "read-token", ExpiresAt: now.Add(time.Hour)}
	server.githubAppClient = client

	sha := strings.Repeat("a", 40)
	run, _, err := server.DeploymentRuns.Create(context.Background(), projectA.ID, "owner-a", "export-create", deploymentworkflow.Source{RepositoryID: repository.RepositoryID, InstallationID: installation.InstallationID, Repository: repository.FullName, SelectedRef: "main"}, deploymentworkflow.Target{Exposure: "internal"})
	if err != nil {
		t.Fatal(err)
	}
	analysis := repositoryanalysis.Result{SchemaVersion: repositoryanalysis.SchemaVersion, RepositoryID: repository.RepositoryID, Repository: repository.FullName, SelectedRef: "main", CommitSHA: sha, Applications: []repositoryanalysis.Application{{SourceKey: "api", Key: "repo-api", Root: ".", Port: 8080, Build: repositoryanalysis.Build{Context: ".", DockerfilePath: "Dockerfile", Strategy: "dockerfile", Platform: "linux/amd64"}}}}
	run, err = server.DeploymentRuns.SetAnalysis(context.Background(), projectA.ID, run.ID, analysis, deploymentworkflow.AuthorityRevisions{SourceCommitSHA: sha}, run.Plan.Target)
	if err != nil {
		t.Fatal(err)
	}
	request := func(projectID, token, path, body string, write bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		if write {
			req.Header.Set("Idempotency-Key", "export-request")
			req.Header.Set("X-Request-ID", "export-request")
		}
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, req)
		return response
	}
	preview := request(projectA.ID, "export-viewer-pat", "/repository-export/preview", `{"run_id":"`+run.ID+`"}`, false)
	if preview.Code != http.StatusOK || !strings.Contains(preview.Body.String(), `"export_enabled":true`) || !strings.Contains(preview.Body.String(), `"preview_hash"`) {
		t.Fatalf("viewer preview status=%d body=%s", preview.Code, preview.Body.String())
	}
	viewerCreate := request(projectA.ID, "export-viewer-pat", "/repository-export", `{}`, true)
	if viewerCreate.Code != http.StatusForbidden {
		t.Fatalf("viewer create status=%d body=%s", viewerCreate.Code, viewerCreate.Body.String())
	}
	crossProject := request(projectB.ID, "export-owner-b-pat", "/repository-export/preview", `{"run_id":"`+run.ID+`"}`, false)
	if crossProject.Code != http.StatusNotFound {
		t.Fatalf("cross-project preview status=%d body=%s", crossProject.Code, crossProject.Body.String())
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
