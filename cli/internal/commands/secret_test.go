package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opsi-dev/opsi/cli/internal/keychain"
	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	totpSecretCanary = "totp-secret-canary-r5013"
	totpURICanary    = "otpauth://totp/Opsi:r5013?secret=uri-canary-r5013"
	usernameCanary   = "generated-user-canary-r5013"
	passwordCanary   = "generated-password-canary-r5013"
	otpCanary        = "otp-input-canary-r5013"
	totpCanary       = "totp-input-canary-r5013"
	patCanary        = "pat-canary-r5013"
)

var secretCanaries = []string{totpSecretCanary, totpURICanary, usernameCanary, passwordCanary, otpCanary, totpCanary, patCanary}

func TestSecretCommandsWriteProtectedResponses(t *testing.T) {
	addr, service, stop := startCommandSecretServer(t)
	defer stop()

	dir := t.TempDir()
	configPath := writeSecretConfig(t, dir, addr)
	patPath := writeSecretInput(t, dir, "pat", patCanary)
	otpPath := writeSecretInput(t, dir, "otp", otpCanary)
	totpPath := writeSecretInput(t, dir, "totp", totpCanary)

	tests := []struct {
		name string
		args []string
		want any
	}{
		{
			name: "setup",
			args: []string{"secret", "setup-totp", "--project-id", "proj", "--pat-file", patPath},
			want: &agentv1.SetupTOTPResponse{Secret: totpSecretCanary, URI: totpURICanary},
		},
		{
			name: "create",
			args: []string{"secret", "create", "--project-id", "proj", "--service-id", "svc", "--name", "db", "--pat-file", patPath},
			want: secretResponse("proj", "svc", "db"),
		},
		{
			name: "reveal",
			args: []string{"secret", "reveal", "--project-id", "proj", "--service-id", "svc", "--name", "db", "--pat-file", patPath, "--otp-file", otpPath, "--totp-file", totpPath},
			want: secretResponse("proj", "svc", "db"),
		},
		{
			name: "rotate",
			args: []string{"secret", "rotate", "--project-id", "proj", "--service-id", "svc", "--name", "db", "--pat-file", patPath},
			want: secretResponse("proj", "svc", "db"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outputPath := filepath.Join(dir, tt.name+".json")
			args := append([]string{"--config", configPath}, tt.args...)
			args = append(args, "--output-file", outputPath)
			stdout, stderr, err := executeSecretCommand(args)
			if err != nil {
				t.Fatal(err)
			}
			assertNoSecretCanary(t, stdout, stderr, strings.Join(args, " "))

			var result secretWriteResult
			if err := json.Unmarshal([]byte(stdout), &result); err != nil {
				t.Fatalf("sanitized stdout is not JSON: %v", err)
			}
			if result.Status != "written" || result.ProjectID != "proj" {
				t.Fatalf("sanitized result=%+v", result)
			}
			assertNoSecretCanary(t, result.Status, result.ProjectID, result.ServiceID, result.Name)

			info, err := os.Stat(outputPath)
			if err != nil {
				t.Fatal(err)
			}
			if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
				t.Fatalf("protected output mode=%v", info.Mode())
			}
			got, err := os.ReadFile(outputPath)
			if err != nil {
				t.Fatal(err)
			}
			want, err := json.Marshal(tt.want)
			if err != nil {
				t.Fatal(err)
			}
			want = append(want, '\n')
			if !bytes.Equal(got, want) {
				t.Fatalf("protected response mismatch\n got: %s\nwant: %s", got, want)
			}
		})
	}

	if service.authorization != "Bearer "+patCanary || strings.Contains(service.serializedRequest, patCanary) {
		t.Fatalf("authorization=%q request=%s", service.authorization, service.serializedRequest)
	}
	if !strings.Contains(service.serializedRequest, otpCanary) || !strings.Contains(service.serializedRequest, totpCanary) {
		t.Fatalf("protected codes not delivered to Agent: %s", service.serializedRequest)
	}
}

func TestSecretCommandsRequireOutputFileBeforeAgentCall(t *testing.T) {
	tests := [][]string{
		{"secret", "setup-totp", "--project-id", "proj"},
		{"secret", "create", "--project-id", "proj", "--service-id", "svc", "--name", "db"},
		{"secret", "reveal", "--project-id", "proj", "--service-id", "svc", "--name", "db"},
		{"secret", "rotate", "--project-id", "proj", "--service-id", "svc", "--name", "db"},
	}
	for _, args := range tests {
		stdout, stderr, err := executeSecretCommand(args)
		if err == nil || !strings.Contains(err.Error(), "output-file") {
			t.Fatalf("args=%v err=%v", args, err)
		}
		assertNoSecretCanary(t, stdout, stderr, err.Error(), strings.Join(args, " "))
	}
}

func TestProtectedResponseRefusesUnsafeTargetsAndRemovesFailures(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "existing")
	if err := os.WriteFile(existing, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(dir, "symlink")
	if err := os.Symlink(filepath.Join(dir, "missing-target"), symlink); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(dir, "directory")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{existing, symlink, directory} {
		if err := writeProtectedResponse(path, secretResponse("proj", "svc", "db")); err == nil {
			t.Fatalf("unsafe target accepted: %s", path)
		} else {
			assertNoSecretCanary(t, err.Error())
		}
	}
	data, err := os.ReadFile(existing)
	if err != nil || string(data) != "keep" {
		t.Fatalf("existing file changed: %q err=%v", data, err)
	}

	missingParent := filepath.Join(dir, "missing", "response.json")
	if err := writeProtectedResponse(missingParent, secretResponse("proj", "svc", "db")); err == nil {
		t.Fatal("expected protected write failure")
	}
	if _, err := os.Lstat(missingParent); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed write left output: %v", err)
	}

	oversized := filepath.Join(dir, "oversized")
	if err := writeProtectedResponse(oversized, map[string]string{"value": strings.Repeat("x", maxProtectedSecretBytes)}); err == nil {
		t.Fatal("expected protected response size failure")
	}
	if _, err := os.Lstat(oversized); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("oversized response left output: %v", err)
	}

	partial := filepath.Join(dir, "partial")
	if err := writeProtectedResponseUsing(partial, secretResponse("proj", "svc", "db"), func(*os.File, []byte) error {
		return errors.New("simulated write failure")
	}); err == nil {
		t.Fatal("expected simulated protected write failure")
	}
	if _, err := os.Lstat(partial); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("simulated write failure left output: %v", err)
	}
}

func TestSecretAgentErrorCreatesNoOutputAndLeaksNoCanary(t *testing.T) {
	addr, service, stop := startCommandSecretServer(t)
	defer stop()
	service.err = status.Error(codes.Internal, strings.Join(secretCanaries, " "))

	dir := t.TempDir()
	configPath := writeSecretConfig(t, dir, addr)
	patPath := writeSecretInput(t, dir, "pat", patCanary)
	outputPath := filepath.Join(dir, "must-not-exist")
	args := []string{"--config", configPath, "secret", "create", "--project-id", "proj", "--service-id", "svc", "--name", "db", "--pat-file", patPath, "--output-file", outputPath}
	stdout, stderr, err := executeSecretCommand(args)
	if err == nil {
		t.Fatal("expected Agent error")
	}
	assertNoSecretCanary(t, stdout, stderr, err.Error(), strings.Join(args, " "))
	if _, statErr := os.Lstat(outputPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("Agent error created output: %v", statErr)
	}
}

func TestSecretHelpAndPATErrorsContainNoCanaries(t *testing.T) {
	for _, command := range []string{"setup-totp", "create", "reveal", "rotate"} {
		stdout, stderr, err := executeSecretCommand([]string{"secret", command, "--help"})
		if err != nil {
			t.Fatal(err)
		}
		assertNoSecretCanary(t, stdout, stderr)
		if !strings.Contains(stdout, "--output-file") {
			t.Fatalf("%s help missing output-file: %s", command, stdout)
		}
	}

	err := redactPATError(errors.New("request rejected: "+patCanary), patCanary)
	if err == nil || strings.Contains(err.Error(), patCanary) {
		t.Fatalf("PAT redaction failed: %v", err)
	}
}

func TestSecretHelpDoesNotExposeCallerAuthorityFlags(t *testing.T) {
	stdout, stderr, err := executeSecretCommand([]string{"secret", "reveal", "--help"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout+stderr, "--user-id") || strings.Contains(stdout+stderr, "--role") {
		t.Fatalf("secret help exposes caller authority flags: %s%s", stdout, stderr)
	}
}

func executeSecretCommand(args []string) (string, string, error) {
	cmd := NewRootCommand(Options{KeychainFactory: func() (keychain.Store, error) { return keychain.NewFakeStore(), nil }})
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

func writeSecretConfig(t *testing.T, dir, addr string) string {
	t.Helper()
	path := filepath.Join(dir, "cli.yaml")
	if err := os.WriteFile(path, []byte("agent_addr: "+addr+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeSecretInput(t *testing.T, dir, name, value string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(value+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertNoSecretCanary(t *testing.T, values ...string) {
	t.Helper()
	for _, value := range values {
		for _, canary := range secretCanaries {
			if strings.Contains(value, canary) {
				t.Fatalf("secret canary leaked: %q", canary)
			}
		}
	}
}

func secretResponse(projectID, serviceID, name string) *agentv1.SecretResponse {
	return &agentv1.SecretResponse{ProjectID: projectID, ServiceID: serviceID, Name: name, Username: usernameCanary, Password: passwordCanary}
}

type commandSecretServer struct {
	agentv1.UnimplementedSecretServiceServer
	t                 *testing.T
	authorization     string
	serializedRequest string
	err               error
}

func (s *commandSecretServer) SetupTOTP(context.Context, *agentv1.SetupTOTPRequest) (*agentv1.SetupTOTPResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &agentv1.SetupTOTPResponse{Secret: totpSecretCanary, URI: totpURICanary}, nil
}

func (s *commandSecretServer) CreateSecret(_ context.Context, req *agentv1.SecretRequest) (*agentv1.SecretResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	return secretResponse(req.ProjectID, req.ServiceID, req.Name), nil
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
	if s.err != nil {
		return nil, s.err
	}
	return secretResponse(req.ProjectID, req.ServiceID, req.Name), nil
}

func (s *commandSecretServer) RotateSecret(_ context.Context, req *agentv1.SecretRequest) (*agentv1.SecretResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	return secretResponse(req.ProjectID, req.ServiceID, req.Name), nil
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
