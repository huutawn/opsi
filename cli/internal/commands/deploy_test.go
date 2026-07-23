package commands

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opsi-dev/opsi/cli/internal/keychain"
)

func TestDeployRejectsLegacyRootFlagsBeforeNetwork(t *testing.T) {
	cmd := NewRootCommand(Options{KeychainFactory: func() (keychain.Store, error) {
		return keychain.NewFakeStore(), nil
	}})
	cmd.SetArgs([]string{"deploy", "--repo-url", "https://example.test/repo.git"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("error = %v", err)
	}
}

func TestDeployWithoutSubcommandOnlyShowsHelp(t *testing.T) {
	command := NewRootCommand(Options{KeychainFactory: func() (keychain.Store, error) { return keychain.NewFakeStore(), nil }})
	output := bytes.NewBuffer(nil)
	command.SetOut(output)
	command.SetArgs([]string{"deploy"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Available Commands") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestDeployHelpDoesNotAdvertiseIngress(t *testing.T) {
	cmd := NewRootCommand(Options{KeychainFactory: func() (keychain.Store, error) {
		return keychain.NewFakeStore(), nil
	}})
	buf := bytes.NewBuffer(nil)
	cmd.SetOut(buf)
	cmd.SetArgs([]string{"deploy", "--help"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	help := buf.String()
	for _, unwanted := range []string{"--ingress", "Traefik-safe", "gateway"} {
		if strings.Contains(help, unwanted) {
			t.Fatalf("deploy help advertises removed capability %q:\n%s", unwanted, help)
		}
	}
}

func TestDeployApplyUsesCanonicalCloudImmutableEndpoint(t *testing.T) {
	var received map[string]any
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/projects/proj-1/deployments" || r.Header.Get("Idempotency-Key") != "deploy-key" {
			t.Fatalf("request=%s %s key=%q", r.Method, r.URL.Path, r.Header.Get("Idempotency-Key"))
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"schema_version":"opsi.deployment_job/v1","mode":"immutable_image","id":"dep-1","project_id":"proj-1","environment_id":"env-1","runtime_id":"rt-1","service_id":"svc-1","status":"queued","agent_id":"agent-1","node_id":"node-1","spec_hash":"spec","attempt_count":0,"max_attempts":3,"created_at":"2026-07-19T00:00:00Z","updated_at":"2026-07-19T00:00:00Z"}`))
	}))
	defer cloud.Close()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "cli.yaml")
	if err := os.WriteFile(configPath, []byte("cloud_url: "+cloud.URL+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := NewRootCommand(Options{KeychainFactory: func() (keychain.Store, error) { return keychain.NewFakeStore(), nil }})
	output := bytes.NewBuffer(nil)
	command.SetOut(output)
	command.SetArgs([]string{"--config", configPath, "deploy", "apply", "--project-id", "proj-1", "--build-record-id", "br-1", "--environment-id", "env-1", "--service-key", "api", "--replicas", "1", "--container-port", "8080", "--cpu-request", "100m", "--memory-request", "128Mi", "--cpu-limit", "500m", "--memory-limit", "512Mi", "--idempotency-key", "deploy-key", "--yes", "--json"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if received["build_record_id"] != "br-1" || received["environment_id"] != "env-1" {
		t.Fatalf("request=%+v", received)
	}
	if _, exists := received["repo_url"]; exists {
		t.Fatal("canonical production request included Git source")
	}
	if !strings.Contains(output.String(), `"id":"dep-1"`) {
		t.Fatalf("output=%s", output.String())
	}
}
