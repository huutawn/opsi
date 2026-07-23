package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opsi-dev/opsi/cli/internal/keychain"
	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func TestSecretRevealCommand(t *testing.T) {
	addr, service, stop := startCommandSecretServer(t)
	defer stop()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "cli.yaml")
	if err := os.WriteFile(configPath, []byte("agent_addr: "+addr+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	patPath := filepath.Join(dir, "pat")
	totpPath := filepath.Join(dir, "totp")
	if err := os.WriteFile(patPath, []byte("pat_test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(totpPath, []byte("123456\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := NewRootCommand(Options{KeychainFactory: func() (keychain.Store, error) { return keychain.NewFakeStore(), nil }})
	buf := bytes.NewBuffer(nil)
	cmd.SetOut(buf)
	cmd.SetArgs([]string{"--config", configPath, "secret", "reveal", "--project-id", "proj", "--service-id", "svc", "--name", "db", "--pat-file", patPath, "--totp-file", totpPath})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"username":"user"`) || !strings.Contains(buf.String(), `"password":"pass"`) {
		t.Fatalf("unexpected output: %q", buf.String())
	}
	if service.authorization != "Bearer pat_test" || strings.Contains(service.serializedRequest, "pat_test") {
		t.Fatalf("authorization=%q request=%s", service.authorization, service.serializedRequest)
	}
	if strings.Contains(buf.String(), "pat_test") {
		t.Fatalf("stdout leaked PAT: %q", buf.String())
	}
}

func TestSecretHelpDoesNotExposeCallerAuthorityFlags(t *testing.T) {
	cmd := NewRootCommand(Options{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"secret", "reveal", "--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "--user-id") || strings.Contains(out.String(), "--role") {
		t.Fatalf("secret help exposes caller authority flags: %s", out.String())
	}
}

type commandSecretServer struct {
	agentv1.UnimplementedSecretServiceServer
	t                 *testing.T
	authorization     string
	serializedRequest string
}

func (commandSecretServer) SetupTOTP(context.Context, *agentv1.SetupTOTPRequest) (*agentv1.SetupTOTPResponse, error) {
	return &agentv1.SetupTOTPResponse{Secret: "secret", URI: "otpauth://totp/Opsi:test"}, nil
}

func (commandSecretServer) CreateSecret(_ context.Context, req *agentv1.SecretRequest) (*agentv1.SecretResponse, error) {
	return &agentv1.SecretResponse{ProjectID: req.ProjectID, ServiceID: req.ServiceID, Name: req.Name, Username: "user"}, nil
}

func (s *commandSecretServer) RevealSecret(ctx context.Context, req *agentv1.SecretRequest) (*agentv1.SecretResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	if values := md.Get("authorization"); len(values) > 0 {
		s.authorization = values[0]
	}
	data, err := json.Marshal(req)
	if err != nil {
		s.t.Fatal(err)
	}
	s.serializedRequest = string(data)
	if req.ProjectID != "proj" || req.ServiceID != "svc" || req.Name != "db" || req.TOTPCode != "123456" {
		return &agentv1.SecretResponse{}, nil
	}
	return &agentv1.SecretResponse{ProjectID: req.ProjectID, ServiceID: req.ServiceID, Name: req.Name, Username: "user", Password: "pass"}, nil
}

func (commandSecretServer) RotateSecret(_ context.Context, req *agentv1.SecretRequest) (*agentv1.SecretResponse, error) {
	return &agentv1.SecretResponse{ProjectID: req.ProjectID, ServiceID: req.ServiceID, Name: req.Name, Username: "user"}, nil
}

func startCommandSecretServer(t *testing.T) (string, *commandSecretServer, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	service := &commandSecretServer{t: t}
	agentv1.RegisterSecretServiceServer(server, service)
	go func() { _ = server.Serve(listener) }()
	return listener.Addr().String(), service, server.Stop
}
