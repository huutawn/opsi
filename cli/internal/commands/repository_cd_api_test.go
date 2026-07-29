package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opsi-dev/opsi/cli/internal/repository"
)

type localPlanRunner struct{ step int }

func (r *localPlanRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	r.step++
	switch r.step {
	case 1:
		return []byte("false\n"), nil
	case 2, 3:
		return nil, nil
	case 4:
		if len(args) < 5 || args[2] != "diff" {
			return nil, errors.New("unexpected git argv")
		}
		return []byte("M\x00Dockerfile\x00"), nil
	default:
		return nil, errors.New("unexpected git command")
	}
}

func TestLocalRepositoryPreviewApplyAndCLIPlanParity(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &localPlanRunner{}
	mux := http.NewServeMux()
	registerRepositoryCDRoutesAt(mux, "local-session", root, nil, repository.CDService{Runner: runner})
	server := httptest.NewServer(mux)
	defer server.Close()
	guard, err := http.Get(server.URL + "/api/local/repository/config?config_path=README.md")
	if err != nil {
		t.Fatal(err)
	}
	if guard.StatusCode != http.StatusBadRequest {
		t.Fatalf("arbitrary path status=%d", guard.StatusCode)
	}
	_ = guard.Body.Close()
	service := repository.ServiceV2{Key: "api", Build: repository.BuildV2{Context: ".", Dockerfile: "Dockerfile", Platform: "linux/amd64"}, WatchPaths: []string{}, SharedPaths: []string{}, Dependencies: []string{}, Deploy: repository.DeployV2{Production: repository.ProductionV2{Enabled: true, Branches: []string{"main"}}, Preview: repository.PreviewV2{}}}
	body, _ := json.Marshal(map[string]any{"service": service})
	response, err := http.Post(server.URL+"/api/local/repository/config/preview", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("preview status=%d", response.StatusCode)
	}
	var preview repository.MutationPreview
	if err := json.NewDecoder(response.Body).Decode(&preview); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if len(preview.PreviewHash) != 64 {
		t.Fatalf("preview hash = %q", preview.PreviewHash)
	}
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/local/repository/apply", bytes.NewReader(bytes.Replace(body, []byte("}"), []byte(",\"confirm\":true}"), 1)))
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing session status=%d", response.StatusCode)
	}
	_ = response.Body.Close()
	applyBody, _ := json.Marshal(map[string]any{"service": service, "confirm": true, "preview_hash": preview.PreviewHash})
	request, _ = http.NewRequest(http.MethodPost, server.URL+"/api/local/repository/apply", bytes.NewReader(applyBody))
	request.Header.Set("X-Local-Session", "local-session")
	request.Header.Set("Idempotency-Key", "apply-1")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("apply status=%d", response.StatusCode)
	}
	_ = response.Body.Close()
	if _, err := os.Stat(filepath.Join(root, defaultConfigPath)); err != nil {
		t.Fatal(err)
	}
	base, head := strings.Repeat("a", 40), strings.Repeat("b", 40)
	planBody, _ := json.Marshal(map[string]any{"event": "push", "base": base, "head": head})
	response, err = http.Post(server.URL+"/api/local/repository/plan/preview", "application/json", bytes.NewReader(planBody))
	if err != nil {
		t.Fatal(err)
	}
	var apiPlan repository.ChangedServicePlan
	if err := json.NewDecoder(response.Body).Decode(&apiPlan); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	cfg, _, _, err := repository.LoadConfig(root, defaultConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	cliPlan, err := (repository.CDService{Runner: &localPlanRunner{}}).Plan(context.Background(), repository.PlanRequest{Repository: root, Config: cfg, Event: repository.EventPush, Base: base, Head: head})
	if err != nil {
		t.Fatal(err)
	}
	if apiPlan.PlanHash != cliPlan.PlanHash || strings.Join(apiPlan.AffectedServiceKeys, ",") != strings.Join(cliPlan.AffectedServiceKeys, ",") {
		t.Fatalf("api=%+v cli=%+v", apiPlan, cliPlan)
	}
}

func TestLocalRepositoryApplyIdempotencyAndValidation(t *testing.T) {
	root, server, service := localRepositoryMutationServer(t)
	preview := previewLocalRepositoryMutation(t, server.URL, service)
	applyBody, _ := json.Marshal(map[string]any{"service": service, "confirm": true, "preview_hash": preview.PreviewHash})

	request := localRepositoryApplyRequest(t, server.URL, applyBody, "", "local-session")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	assertLocalError(t, response, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED")

	for _, key := range []string{"unsafe key", strings.Repeat("a", 129)} {
		request = localRepositoryApplyRequest(t, server.URL, applyBody, key, "local-session")
		response, err = http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		assertLocalError(t, response, http.StatusBadRequest, "INVALID_IDEMPOTENCY_KEY")
	}

	missingPreviewBody, _ := json.Marshal(map[string]any{"service": service, "confirm": true})
	request = localRepositoryApplyRequest(t, server.URL, missingPreviewBody, "apply-no-preview", "local-session")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	assertLocalError(t, response, http.StatusBadRequest, "PREVIEW_HASH_REQUIRED")

	request = localRepositoryApplyRequest(t, server.URL, applyBody, "apply-replay", "local-session")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	first := decodeLocalApplyResult(t, response)
	if first.Reused || first.Files[0].Action != "created" {
		t.Fatalf("first apply = %+v", first)
	}

	request = localRepositoryApplyRequest(t, server.URL, applyBody, "apply-replay", "local-session")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	replay := decodeLocalApplyResult(t, response)
	if !replay.Reused || replay.PreviewHash != first.PreviewHash || replay.Files[0].Action != "created" {
		t.Fatalf("replay = %+v", replay)
	}

	changed := service
	changed.SharedPaths = []string{"shared"}
	conflictingBody, _ := json.Marshal(map[string]any{"service": changed, "confirm": true, "preview_hash": preview.PreviewHash})
	request = localRepositoryApplyRequest(t, server.URL, conflictingBody, "apply-replay", "local-session")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	assertLocalError(t, response, http.StatusConflict, "IDEMPOTENCY_KEY_CONFLICT")

	if _, err := os.Stat(filepath.Join(root, defaultConfigPath)); err != nil {
		t.Fatal(err)
	}
}

func TestLocalRepositoryApplyRejectsStalePreviewWithoutWriting(t *testing.T) {
	root, server, service := localRepositoryMutationServer(t)
	preview := previewLocalRepositoryMutation(t, server.URL, service)
	configPath := filepath.Join(root, defaultConfigPath)
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	external := preview.ConfigYAML + "# external edit after preview\n"
	if err := os.WriteFile(configPath, []byte(external), 0o644); err != nil {
		t.Fatal(err)
	}
	applyBody, _ := json.Marshal(map[string]any{"service": service, "confirm": true, "preview_hash": preview.PreviewHash})
	request := localRepositoryApplyRequest(t, server.URL, applyBody, "apply-stale", "local-session")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	assertLocalError(t, response, http.StatusConflict, "STALE_PREVIEW")
	current, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != external {
		t.Fatalf("stale apply changed config: %q", current)
	}
	if _, err := os.Stat(filepath.Join(root, defaultWorkflowPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale apply wrote workflow: %v", err)
	}
}

func TestRepositoryCDUISendsDisplayedPreviewHashAndStableApplyKey(t *testing.T) {
	clientSource, err := os.ReadFile(filepath.Join("..", "..", "ui", "lib", "api", "local-client.ts"))
	if err != nil {
		t.Fatal(err)
	}
	featureSource, err := os.ReadFile(filepath.Join("..", "..", "ui", "features", "github", "repository-cd.tsx"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"preview_hash: previewHash", "idempotencyKey", "init.idempotencyKey ?? crypto.randomUUID()"} {
		if !strings.Contains(string(clientSource), expected) {
			t.Fatalf("local client missing %q", expected)
		}
	}
	for _, expected := range []string{"preview.preview_hash, applyKey", "setApplyKey(crypto.randomUUID())"} {
		if !strings.Contains(string(featureSource), expected) {
			t.Fatalf("repository UI missing %q", expected)
		}
	}
}

type localApplyResult struct {
	repository.MutationPreview
	Reused bool `json:"reused"`
}

func localRepositoryMutationServer(t *testing.T) (string, *httptest.Server, repository.ServiceV2) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	registerRepositoryCDRoutesAt(mux, "local-session", root, nil, repository.CDService{})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	service := repository.ServiceV2{Key: "api", Build: repository.BuildV2{Context: ".", Dockerfile: "Dockerfile", Platform: "linux/amd64"}, WatchPaths: []string{}, SharedPaths: []string{}, Dependencies: []string{}, Deploy: repository.DeployV2{Production: repository.ProductionV2{Enabled: true, Branches: []string{"main"}}, Preview: repository.PreviewV2{}}}
	return root, server, service
}

func previewLocalRepositoryMutation(t *testing.T, baseURL string, service repository.ServiceV2) repository.MutationPreview {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"service": service})
	response, err := http.Post(baseURL+"/api/local/repository/config/preview", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("preview status=%d", response.StatusCode)
	}
	var preview repository.MutationPreview
	if err := json.NewDecoder(response.Body).Decode(&preview); err != nil {
		t.Fatal(err)
	}
	return preview
}

func localRepositoryApplyRequest(t *testing.T, baseURL string, body []byte, key, session string) *http.Request {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, baseURL+"/api/local/repository/apply", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	if session != "" {
		request.Header.Set("X-Local-Session", session)
	}
	return request
}

func decodeLocalApplyResult(t *testing.T, response *http.Response) localApplyResult {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("apply status=%d", response.StatusCode)
	}
	var result localApplyResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func assertLocalError(t *testing.T, response *http.Response, status int, code string) {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != status {
		t.Fatalf("status=%d want=%d", response.StatusCode, status)
	}
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error.Code != code {
		t.Fatalf("code=%q want=%q", payload.Error.Code, code)
	}
}
