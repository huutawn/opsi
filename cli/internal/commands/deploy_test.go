package commands

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opsi-dev/opsi/cli/internal/keychain"
	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
	"google.golang.org/grpc"
)

func TestDeployCommandStreamsProgress(t *testing.T) {
	addr, stop := startCommandDeploymentServer(t)
	defer stop()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "cli.yaml")
	if err := os.WriteFile(configPath, []byte("agent_addr: "+addr+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := NewRootCommand(Options{KeychainFactory: func() (keychain.Store, error) {
		return keychain.NewFakeStore(), nil
	}})
	buf := bytes.NewBuffer(nil)
	cmd.SetOut(buf)
	cmd.SetArgs([]string{"--config", configPath, "deploy", "--project-id", "proj-dev", "--service-id", "svc-api", "--service-name", "api", "--repo-url", "https://example.test/repo.git", "--git-sha", "abc", "--manifest-path", "k8s/deploy.yaml", "--watch-path", "apps/api/**", "--depends-on", "mydb", "--termination-grace-period-seconds", "45", "--resource-request", "cpu=100m", "--resource-limit", "memory=512Mi"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"phase":"success"`) {
		t.Fatalf("expected success progress, got %q", buf.String())
	}
}

type commandDeploymentServer struct{}

func (commandDeploymentServer) Deploy(req *agentv1.DeployRequest, stream agentv1.DeploymentService_DeployServer) error {
	if req.ProjectID != "proj-dev" || req.ServiceID != "svc-api" || req.ServiceName != "api" || req.GitSHA != "abc" {
		return nil
	}
	if len(req.WatchPaths) != 1 || req.WatchPaths[0] != "apps/api/**" || req.TerminationGracePeriodSeconds != 45 || req.ResourceRequestsJSON != `{"cpu":"100m"}` || req.ResourceLimitsJSON != `{"memory":"512Mi"}` {
		return nil
	}
	if len(req.DependsOn) != 1 || req.DependsOn[0].Name != "mydb" {
		return nil
	}
	return stream.Send(&agentv1.ProgressEvent{OperationID: "dep_test", ProjectID: req.ProjectID, ServiceID: req.ServiceID, ServiceName: req.ServiceName, Phase: "success", Message: "ok", Percent: 100})
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

func startCommandDeploymentServer(t *testing.T) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	agentv1.RegisterDeploymentServiceServer(server, commandDeploymentServer{})
	go func() { _ = server.Serve(listener) }()
	return listener.Addr().String(), server.Stop
}
