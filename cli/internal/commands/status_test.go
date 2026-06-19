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

func TestStatusCommandCallsAgent(t *testing.T) {
	addr, stop := startCommandStatusServer(t)
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
	cmd.SetArgs([]string{"--config", configPath, "status"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "fake-node") {
		t.Fatalf("expected status output, got %q", buf.String())
	}
}

type commandStatusServer struct{}

func (commandStatusServer) Status(context.Context, *agentv1.StatusRequest) (*agentv1.StatusResponse, error) {
	return &agentv1.StatusResponse{NodeID: "fake-node", Health: "ok", Version: "test"}, nil
}

func startCommandStatusServer(t *testing.T) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	agentv1.RegisterStatusServiceServer(server, commandStatusServer{})
	go func() { _ = server.Serve(listener) }()
	return listener.Addr().String(), server.Stop
}
