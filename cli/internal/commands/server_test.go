package commands

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/opsi-dev/opsi/cli/internal/config"
	"github.com/opsi-dev/opsi/cli/internal/keychain"
)

func TestServerBootstrapCLIAndLocalUIUseSameCloudFlow(t *testing.T) {
	type capturedRequest struct {
		Path           string
		Authorization  string
		IdempotencyKey string
		Body           map[string]any
	}
	var mu sync.Mutex
	var captured []capturedRequest
	cloud := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/api/projects/proj-1/bootstrap-sessions" {
			t.Fatalf("unexpected Cloud request: %s %s", request.Method, request.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		captured = append(captured, capturedRequest{
			Path:           request.URL.Path,
			Authorization:  request.Header.Get("Authorization"),
			IdempotencyKey: request.Header.Get("Idempotency-Key"),
			Body:           body,
		})
		mu.Unlock()
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(response, `{"id":"boot-1","status":"created","role":"first_server"}`)
	}))
	defer cloud.Close()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "cli.yaml")
	if err := os.WriteFile(configPath, []byte("agent_addr: 127.0.0.1:1\ncloud_url: "+cloud.URL+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	credentialPath := filepath.Join(dir, "bootstrap-key")
	credential := "-----BEGIN OPENSSH PRIVATE KEY-----\ntest-only\n-----END OPENSSH PRIVATE KEY-----\n"
	if err := os.WriteFile(credentialPath, []byte(credential), 0o600); err != nil {
		t.Fatal(err)
	}
	store := keychain.NewFakeStore()
	if err := store.SetPAT("test-pat"); err != nil {
		t.Fatal(err)
	}
	factory := func() (keychain.Store, error) { return store, nil }

	command := NewRootCommand(Options{Version: "test", KeychainFactory: factory, HTTPClient: cloud.Client()})
	commandOutput := bytes.NewBuffer(nil)
	command.SetOut(commandOutput)
	command.SetArgs([]string{
		"--config", configPath,
		"server", "bootstrap",
		"--project-id", "proj-1",
		"--public-host", "203.0.113.10",
		"--ssh-username", "ubuntu",
		"--credential-file", credentialPath,
		"--idempotency-key", "bootstrap-cli",
	})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(commandOutput.String(), "PRIVATE KEY") || strings.Contains(commandOutput.String(), "test-only") {
		t.Fatal("CLI output exposed bootstrap credential")
	}

	local := httptest.NewServer(newStartMux(dir, "", config.Config{AgentAddr: "127.0.0.1:1", CloudURL: cloud.URL}, factory))
	defer local.Close()
	localSession := localTestSession(t, local.URL)
	body, err := json.Marshal(map[string]any{
		"role":            "first_server",
		"public_host":     "203.0.113.10",
		"ssh_port":        22,
		"ssh_username":    "ubuntu",
		"auth_method":     "private_key",
		"ssh_private_key": credential,
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, local.URL+"/api/local/projects/proj-1/bootstrap-sessions", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Local-Session", localSession)
	request.Header.Set("Idempotency-Key", "bootstrap-ui")
	result, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer result.Body.Close()
	if result.StatusCode != http.StatusCreated {
		responseBody, _ := io.ReadAll(result.Body)
		t.Fatalf("local bootstrap status=%d body=%s", result.StatusCode, responseBody)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(captured) != 2 {
		t.Fatalf("captured requests=%d", len(captured))
	}
	if captured[0].Path != captured[1].Path || !reflect.DeepEqual(captured[0].Body, captured[1].Body) {
		t.Fatalf("CLI and Local UI flows diverged: cli=%+v ui=%+v", captured[0], captured[1])
	}
	for _, request := range captured {
		if request.Authorization != "Bearer test-pat" || request.IdempotencyKey == "" {
			t.Fatalf("missing protected Cloud headers: %+v", request)
		}
	}
}

func TestServerBootstrapRejectsUnsafeCredentialInput(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "cli.yaml")
	if err := os.WriteFile(configPath, []byte("agent_addr: 127.0.0.1:1\ncloud_url: http://127.0.0.1:9800\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	credentialPath := filepath.Join(dir, "bootstrap-key")
	if err := os.WriteFile(credentialPath, []byte("-----BEGIN PRIVATE KEY-----\ntest\n-----END PRIVATE KEY-----\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	command := NewRootCommand(Options{KeychainFactory: func() (keychain.Store, error) { return keychain.NewFakeStore(), nil }})
	command.SetArgs([]string{
		"--config", configPath,
		"server", "bootstrap",
		"--project-id", "proj-1",
		"--public-host", "203.0.113.10",
		"--credential-file", credentialPath,
	})
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "must not be group or world accessible") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReadProtectedSecretRejectsUnsafeFiles(t *testing.T) {
	dir := t.TempDir()
	validPath := filepath.Join(dir, "valid")
	if err := os.WriteFile(validPath, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlinkPath := filepath.Join(dir, "symlink")
	if err := os.Symlink(validPath, symlinkPath); err != nil {
		t.Fatal(err)
	}
	worldReadablePath := filepath.Join(dir, "world-readable")
	if err := os.WriteFile(worldReadablePath, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	emptyPath := filepath.Join(dir, "empty")
	if err := os.WriteFile(emptyPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	oversizedPath := filepath.Join(dir, "oversized")
	if err := os.WriteFile(oversizedPath, bytes.Repeat([]byte("x"), maxProtectedSecretBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		path    string
		message string
	}{
		{name: "symlink", path: symlinkPath, message: "must not be a symlink"},
		{name: "directory", path: dir, message: "must be a regular file"},
		{name: "group or world mode", path: worldReadablePath, message: "must not be group or world accessible"},
		{name: "empty", path: emptyPath, message: "is empty"},
		{name: "oversized", path: oversizedPath, message: "exceeds 1 MiB"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value, err := readProtectedSecret(test.path, "test secret")
			clearBytes(value)
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestReadProtectedSecretSupportsDevStdin(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previousStdin := os.Stdin
	previousStdinFD, err := syscall.Dup(int(os.Stdin.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Dup2(int(reader.Fd()), int(os.Stdin.Fd())); err != nil {
		t.Fatal(err)
	}
	os.Stdin = reader
	t.Cleanup(func() {
		_ = syscall.Dup2(previousStdinFD, int(previousStdin.Fd()))
		_ = syscall.Close(previousStdinFD)
		os.Stdin = previousStdin
		_ = reader.Close()
	})
	if _, err := writer.WriteString("stdin-secret"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	value, err := readProtectedSecret("/dev/stdin", "test secret")
	if err != nil {
		t.Fatal(err)
	}
	defer clearBytes(value)
	if string(value) != "stdin-secret" {
		t.Fatalf("value=%q", value)
	}
}
