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
	_ = response.Body.Close()
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/local/repository/apply", bytes.NewReader(bytes.Replace(body, []byte("}"), []byte(",\"confirm\":true}"), 1)))
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing session status=%d", response.StatusCode)
	}
	_ = response.Body.Close()
	applyBody, _ := json.Marshal(map[string]any{"service": service, "confirm": true})
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
