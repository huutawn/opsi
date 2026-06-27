package commands

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opsi-dev/opsi/cli/internal/keychain"
	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
	"google.golang.org/grpc"
)

func TestServiceCreateCommand(t *testing.T) {
	addr, stop := startCommandServiceManagerServer(t)
	defer stop()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "cli.yaml")
	if err := os.WriteFile(configPath, []byte("agent_addr: "+addr+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := NewRootCommand(Options{KeychainFactory: func() (keychain.Store, error) { return keychain.NewFakeStore(), nil }})
	buf := bytes.NewBuffer(nil)
	cmd.SetOut(buf)
	cmd.SetArgs([]string{"--config", configPath, "service", "create", "--project-id", "demo", "--name", "cache", "--type", "redis", "--namespace", "prod", "--set", "max_memory=128mb"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"secret_name":"opsi-svc-cache"`) || !strings.Contains(buf.String(), `"host":"cache.prod.svc.cluster.local"`) {
		t.Fatalf("unexpected output: %q", buf.String())
	}
}

type commandServiceManagerServer struct {
	agentv1.UnimplementedServiceManagerServiceServer
}

func (commandServiceManagerServer) ListCatalog(context.Context, *agentv1.ListCatalogRequest) (*agentv1.ListCatalogResponse, error) {
	return &agentv1.ListCatalogResponse{Services: []agentv1.CatalogService{{Type: "redis", ManagedSupported: true}}}, nil
}

func (commandServiceManagerServer) CreateManagedService(_ context.Context, req *agentv1.CreateManagedServiceRequest) (*agentv1.ManagedServiceResponse, error) {
	if req.ProjectID != "demo" || req.Name != "cache" || req.Type != "redis" || req.Namespace != "prod" || req.Overrides["max_memory"] != "128mb" {
		return &agentv1.ManagedServiceResponse{}, nil
	}
	return &agentv1.ManagedServiceResponse{ProjectID: req.ProjectID, ID: req.Name, Name: req.Name, Type: req.Type, Namespace: req.Namespace, Host: "cache.prod.svc.cluster.local", SecretName: "opsi-svc-cache"}, nil
}

func (commandServiceManagerServer) GetManagedService(_ context.Context, req *agentv1.GetManagedServiceRequest) (*agentv1.ManagedServiceResponse, error) {
	return &agentv1.ManagedServiceResponse{ProjectID: req.ProjectID, ID: req.ID, Name: req.ID, SecretName: "opsi-svc-" + req.ID}, nil
}

func startCommandServiceManagerServer(t *testing.T) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	agentv1.RegisterServiceManagerServiceServer(server, commandServiceManagerServer{})
	go func() { _ = server.Serve(listener) }()
	return listener.Addr().String(), server.Stop
}
