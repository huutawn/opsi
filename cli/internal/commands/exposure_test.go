package commands

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opsi-dev/opsi/cli/internal/config"
	"github.com/opsi-dev/opsi/cli/internal/keychain"
	exposurev1 "github.com/opsi-dev/opsi/contracts/go/exposurev1"
)

func TestReadExposureRequestRequiresProjectAndRejectsUnknownJSON(t *testing.T) {
	spec, err := (exposurev1.ExposureSpec{SchemaVersion: exposurev1.SchemaVersion, ProjectID: "proj-1", EnvironmentID: "env-1", RuntimeID: "runtime-1", ServiceKey: "api", DeploymentJobID: "dep-exposure", Hostname: "api.example.com", Path: "/", ServicePort: 8080, TLS: exposurev1.TLSConfig{Mode: exposurev1.TLSDisabled}}).Canonicalize()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "exposure.json")
	data, _ := json.Marshal(spec)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	request, err := readExposureRequest(&exposureFlags{projectID: "proj-1", baseDeploymentID: "base-1", file: path})
	if err != nil || request.Exposure.SpecHash != spec.SpecHash || request.BaseDeploymentJobID != "base-1" {
		t.Fatalf("request=%+v err=%v", request, err)
	}
	unknownPath := filepath.Join(dir, "unknown.json")
	if err := os.WriteFile(unknownPath, []byte(strings.TrimSuffix(string(data), "}")+`,"unknown":"reject"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readExposureRequest(&exposureFlags{projectID: "proj-1", baseDeploymentID: "base-1", file: unknownPath}); err == nil {
		t.Fatal("unknown ExposureSpec field was accepted")
	}
}

func TestExposureApplyRequiresExplicitConfirmationAndIdempotency(t *testing.T) {
	spec, err := (exposurev1.ExposureSpec{SchemaVersion: exposurev1.SchemaVersion, ProjectID: "proj-1", EnvironmentID: "env-1", RuntimeID: "runtime-1", ServiceKey: "api", DeploymentJobID: "dep-confirm", Hostname: "api.example.com", Path: "/", ServicePort: 8080, TLS: exposurev1.TLSConfig{Mode: exposurev1.TLSDisabled}}).Canonicalize()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "exposure.json")
	data, _ := json.Marshal(spec)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := newExposureCommand(nil, nil)
	cmd.SetArgs([]string{"apply", "--project-id", "proj-1", "--base-deployment-id", "base-1", "--file", path})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "requires --yes, --idempotency-key, and --expected-state-hash") {
		t.Fatalf("unexpected confirmation error=%v", err)
	}
}

func TestExposureApplyUsesCanonicalDeploymentJobEndpoint(t *testing.T) {
	spec, err := (exposurev1.ExposureSpec{SchemaVersion: exposurev1.SchemaVersion, ProjectID: "proj-1", EnvironmentID: "env-1", RuntimeID: "runtime-1", ServiceKey: "api", DeploymentJobID: "dep-exposure", Hostname: "api.example.com", Path: "/v1", ServicePort: 8080, TLS: exposurev1.TLSConfig{Mode: exposurev1.TLSDisabled}}).Canonicalize()
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/projects/proj-1/exposures" || r.Header.Get("Idempotency-Key") != "exposure-key" {
			t.Fatalf("request=%s %s key=%q", r.Method, r.URL.Path, r.Header.Get("Idempotency-Key"))
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schema_version":"opsi.deployment_job/v1","mode":"rollout","id":"dep-exposure","project_id":"proj-1","environment_id":"env-1","runtime_id":"runtime-1","service_id":"svc-1","status":"queued","rollout_state":"prepared","desired_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","created_at":"2026-07-22T00:00:00Z","updated_at":"2026-07-22T00:00:00Z"}`))
	}))
	defer server.Close()
	dir := t.TempDir()
	filePath := filepath.Join(dir, "exposure.json")
	data, _ := json.Marshal(spec)
	if err := os.WriteFile(filePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "cli.yaml")
	if err := os.WriteFile(configPath, []byte("cloud_url: "+server.URL+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := NewRootCommand(Options{KeychainFactory: func() (keychain.Store, error) { return keychain.NewFakeStore(), nil }})
	output := bytes.NewBuffer(nil)
	command.SetOut(output)
	command.SetArgs([]string{"--config", configPath, "exposure", "apply", "--project-id", "proj-1", "--base-deployment-id", "base-1", "--file", filePath, "--expected-state-hash", strings.Repeat("b", 64), "--idempotency-key", "exposure-key", "--yes", "--json"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if body["base_deployment_job_id"] != "base-1" || !strings.Contains(output.String(), `"rollout_state":"prepared"`) {
		t.Fatalf("body=%+v output=%s", body, output.String())
	}
}

func TestLocalExposureProxyKeepsPATServerSideAndPreservesMutationAuthority(t *testing.T) {
	const pat = "local-exposure-pat"
	var paths, keys []string
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+pat {
			t.Fatalf("Cloud auth=%q", r.Header.Get("Authorization"))
		}
		paths = append(paths, r.URL.Path)
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/preview") {
			_, _ = io.WriteString(w, `{"schema_version":"opsi.exposure_preview/v1","base_deployment_job_id":"base-1","desired":{"schema_version":"opsi.exposure_spec/v1","project_id":"proj-1","environment_id":"env-1","runtime_id":"runtime-1","service_key":"api","deployment_job_id":"dep-exposure","hostname":"api.example.com","path":"/","service_port":8080,"tls":{"mode":"disabled"},"spec_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"changes":["create exposure"],"state_hash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","eligible":true,"decision_code":"EXPOSURE_READY","message":"ready","resolved_at":"2026-07-22T00:00:00Z"}`)
			return
		}
		_, _ = io.WriteString(w, `{"schema_version":"opsi.deployment_job/v1","mode":"rollout","id":"dep-exposure","project_id":"proj-1","environment_id":"env-1","runtime_id":"runtime-1","service_id":"svc-1","status":"queued","rollout_state":"prepared","created_at":"2026-07-22T00:00:00Z","updated_at":"2026-07-22T00:00:00Z"}`)
	}))
	defer cloud.Close()
	store := keychain.NewFakeStore()
	if err := store.SetPAT(pat); err != nil {
		t.Fatal(err)
	}
	local := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: "127.0.0.1:1", CloudURL: cloud.URL}, func() (keychain.Store, error) { return store, nil }))
	defer local.Close()
	body := `{"schema_version":"opsi.exposure_mutation/v1","base_deployment_job_id":"base-1","exposure":{"schema_version":"opsi.exposure_spec/v1","project_id":"proj-1","environment_id":"env-1","runtime_id":"runtime-1","service_key":"api","deployment_job_id":"dep-exposure","hostname":"api.example.com","path":"/","service_port":8080,"tls":{"mode":"disabled"},"spec_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`
	preview, err := http.Post(local.URL+"/api/local/projects/proj-1/exposures/preview", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	previewBody, _ := io.ReadAll(preview.Body)
	preview.Body.Close()
	if preview.StatusCode != http.StatusOK || strings.Contains(string(previewBody), pat) {
		t.Fatalf("preview status=%d body=%s", preview.StatusCode, previewBody)
	}
	request, err := http.NewRequest(http.MethodPost, local.URL+"/api/local/projects/proj-1/exposures", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Local-Session", localTestSession(t, local.URL))
	request.Header.Set("Idempotency-Key", "local-exposure-key")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	responseBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || strings.Contains(string(responseBody), pat) {
		t.Fatalf("apply status=%d body=%s", response.StatusCode, responseBody)
	}
	if strings.Join(paths, ",") != "/api/projects/proj-1/exposures/preview,/api/projects/proj-1/exposures" || keys[0] != "" || keys[1] != "local-exposure-key" {
		t.Fatalf("paths=%v keys=%v", paths, keys)
	}
}

func TestLocalExposureStateSurvivesBackendRestartBecauseCloudIsAuthority(t *testing.T) {
	const pat = "restart-pat"
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+pat {
			t.Fatalf("Cloud auth=%q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/events") {
			_, _ = io.WriteString(w, `{"events":[{"id":"evt-1","deployment_id":"dep-restart","step":"rolled_back","message_redacted":"known-good restored","progress_percent":100,"created_at":"2026-07-22T00:00:00Z"}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"schema_version":"opsi.deployment_job/v1","mode":"rollout","id":"dep-restart","project_id":"proj-1","environment_id":"env-1","runtime_id":"runtime-1","service_id":"svc-1","status":"rolled_back","rollout_state":"rolled_back","desired_digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","current_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","previous_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","created_at":"2026-07-22T00:00:00Z","updated_at":"2026-07-22T00:00:01Z"}`)
	}))
	defer cloud.Close()
	store := keychain.NewFakeStore()
	if err := store.SetPAT(pat); err != nil {
		t.Fatal(err)
	}
	factory := func() (keychain.Store, error) { return store, nil }
	read := func() (string, string) {
		local := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: "127.0.0.1:1", CloudURL: cloud.URL}, factory))
		defer local.Close()
		jobResponse, err := http.Get(local.URL + "/api/local/projects/proj-1/deployments/dep-restart")
		if err != nil {
			t.Fatal(err)
		}
		jobBody, _ := io.ReadAll(jobResponse.Body)
		jobResponse.Body.Close()
		eventResponse, err := http.Get(local.URL + "/api/local/projects/proj-1/deployments/dep-restart/events")
		if err != nil {
			t.Fatal(err)
		}
		eventBody, _ := io.ReadAll(eventResponse.Body)
		eventResponse.Body.Close()
		return string(jobBody), string(eventBody)
	}
	firstJob, firstEvents := read()
	secondJob, secondEvents := read()
	if firstJob != secondJob || firstEvents != secondEvents || !strings.Contains(secondJob, `"rollout_state":"rolled_back"`) || !strings.Contains(secondEvents, `"step":"rolled_back"`) || strings.Contains(secondJob+secondEvents, pat) {
		t.Fatalf("restart state diverged: first=%s/%s second=%s/%s", firstJob, firstEvents, secondJob, secondEvents)
	}
}
