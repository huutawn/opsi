package commands

import (
	"bytes"
	"net"
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
