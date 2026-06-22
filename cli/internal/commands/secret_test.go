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

func TestSecretRevealCommand(t *testing.T) {
	addr, stop := startCommandSecretServer(t)
	defer stop()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "cli.yaml")
	if err := os.WriteFile(configPath, []byte("agent_addr: "+addr+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := NewRootCommand(Options{KeychainFactory: func() (keychain.Store, error) { return keychain.NewFakeStore(), nil }})
	buf := bytes.NewBuffer(nil)
	cmd.SetOut(buf)
	cmd.SetArgs([]string{"--config", configPath, "secret", "reveal", "--project-id", "proj", "--service-id", "svc", "--name", "db", "--user-id", "owner", "--role", "Owner", "--pat", "pat_test", "--totp", "123456"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"username":"user"`) || !strings.Contains(buf.String(), `"password":"pass"`) {
		t.Fatalf("unexpected output: %q", buf.String())
	}
}

type commandSecretServer struct {
	agentv1.UnimplementedSecretServiceServer
}

func (commandSecretServer) SetupTOTP(context.Context, *agentv1.SetupTOTPRequest) (*agentv1.SetupTOTPResponse, error) {
	return &agentv1.SetupTOTPResponse{Secret: "secret", URI: "otpauth://totp/Opsi:test"}, nil
}

func (commandSecretServer) CreateSecret(_ context.Context, req *agentv1.SecretRequest) (*agentv1.SecretResponse, error) {
	return &agentv1.SecretResponse{ProjectID: req.ProjectID, ServiceID: req.ServiceID, Name: req.Name, Username: "user"}, nil
}

func (commandSecretServer) RevealSecret(_ context.Context, req *agentv1.SecretRequest) (*agentv1.SecretResponse, error) {
	if req.ProjectID != "proj" || req.ServiceID != "svc" || req.Name != "db" || req.UserID != "owner" || req.PAT != "pat_test" || req.TOTPCode != "123456" {
		return &agentv1.SecretResponse{}, nil
	}
	return &agentv1.SecretResponse{ProjectID: req.ProjectID, ServiceID: req.ServiceID, Name: req.Name, Username: "user", Password: "pass"}, nil
}

func (commandSecretServer) RotateSecret(_ context.Context, req *agentv1.SecretRequest) (*agentv1.SecretResponse, error) {
	return &agentv1.SecretResponse{ProjectID: req.ProjectID, ServiceID: req.ServiceID, Name: req.Name, Username: "user"}, nil
}

func startCommandSecretServer(t *testing.T) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	agentv1.RegisterSecretServiceServer(server, commandSecretServer{})
	go func() { _ = server.Serve(listener) }()
	return listener.Addr().String(), server.Stop
}
