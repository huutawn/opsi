package commands

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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
	privateKeyMarker := "OPENSSH " + "PRIVATE KEY"
	credential := "-----BEGIN " + privateKeyMarker + "-----\ntest-only\n-----END " + privateKeyMarker + "-----\n"
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

func TestAgentUpgradeLifecycleInputsAndScriptAreFailClosed(t *testing.T) {
	dir := t.TempDir()
	identity := filepath.Join(dir, "identity")
	knownHosts := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(identity, []byte("test-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(knownHosts, []byte("host ssh-ed25519 test"), 0o644); err != nil {
		t.Fatal(err)
	}
	valid := agentUpgradeFlags{projectID: "proj-1", nodeID: "node-1", artifactURL: "https://github.com/huutawn/opsi/releases/download/r5-010/opsi-agent-linux-amd64", artifactSHA256: strings.Repeat("a", 64), expectedVersion: "0.0.0-r5.010.test", sshUsername: "ubuntu", sshPort: 22, identityFile: identity, knownHostsFile: knownHosts}
	if err := valid.validate(); err != nil {
		t.Fatal(err)
	}
	invalid := valid
	invalid.artifactURL = "https://user:secret@example.test/agent"
	if invalid.validate() == nil {
		t.Fatal("accepted credential-bearing artifact URL")
	}
	invalid = valid
	invalid.artifactSHA256 = strings.Repeat("A", 64)
	if invalid.validate() == nil {
		t.Fatal("accepted non-canonical Agent checksum")
	}
	for _, required := range []string{"/opt/opsi/agent/releases", "/opt/opsi/agent/current", "/opt/opsi/agent/previous", "sha256sum", "systemctl restart opsi-agent", "wait_health", "atomic_link"} {
		if !strings.Contains(atomicAgentUpgradeScript, required) {
			t.Fatalf("Agent upgrade lifecycle omitted %q", required)
		}
	}
	if strings.Contains(atomicAgentUpgradeScript, "k3s") || strings.Contains(atomicAgentUpgradeScript, "scp ") {
		t.Fatal("Agent upgrade lifecycle mutates K3s or uses scp replacement")
	}
	syntax := exec.Command("sh", "-n")
	syntax.Stdin = strings.NewReader(atomicAgentUpgradeScript)
	if output, err := syntax.CombinedOutput(); err != nil {
		t.Fatalf("atomic Agent upgrade script is not valid POSIX shell: %v: %s", err, output)
	}
}

func TestServerConnectSavesOnlyCompletePinnedAgentMetadata(t *testing.T) {
	const pat = "connect-test-pat"
	const certPin = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	cloud := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/api/projects/project-1/nodes" || request.Header.Get("Authorization") != "Bearer "+pat {
			t.Fatalf("unexpected Cloud request: %s %s headers=%+v", request.Method, request.URL.Path, request.Header)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(response, `{"nodes":[{"id":"node-1","agent_id":"agent-1","agent_endpoint":"52.77.226.123","agent_port":9443,"agent_tls_server_name":"52.77.226.123","agent_cert_sha256":"`+certPin+`"}]}`)
	}))
	defer cloud.Close()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "cli.yaml")
	if err := os.WriteFile(configPath, []byte("agent_addr: 127.0.0.1:9443\ncloud_url: "+cloud.URL+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := keychain.NewFakeStore()
	if err := store.SetPAT(pat); err != nil {
		t.Fatal(err)
	}
	command := NewRootCommand(Options{Version: "test", KeychainFactory: func() (keychain.Store, error) { return store, nil }, HTTPClient: cloud.Client()})
	output := bytes.NewBuffer(nil)
	command.SetOut(output)
	command.SetArgs([]string{"--config", configPath, "server", "connect", "--project-id", "project-1", "--node-id", "node-1"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"node_id":"node-1"`) || strings.Contains(output.String(), pat) {
		t.Fatalf("unexpected command output: %s", output.String())
	}
	info, err := os.Lstat(configPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("config file info=%v err=%v", info, err)
	}
	contents, err := os.ReadFile(configPath)
	if err != nil || strings.Contains(string(contents), pat) {
		t.Fatalf("config contents=%q err=%v", contents, err)
	}
	cfg, err := config.Load(configPath)
	if err != nil || cfg.AgentAddr != "52.77.226.123:9443" || cfg.TLS.ServerName != "52.77.226.123" || cfg.TLS.PinnedServerCertSHA256 != certPin {
		t.Fatalf("config=%+v err=%v", cfg, err)
	}
}

func TestServerConnectFailurePreservesExistingConfig(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "missing direct TLS metadata", body: `{"nodes":[{"id":"node-1","agent_id":"agent-1"}]}`, want: "no complete direct TLS Agent metadata"},
		{name: "node outside project response", body: `{"nodes":[{"id":"other-node","agent_id":"agent-1","agent_endpoint":"52.77.226.123","agent_port":9443,"agent_tls_server_name":"52.77.226.123","agent_cert_sha256":"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"}]}`, want: "not found in the requested project"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cloud := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(response, test.body)
			}))
			defer cloud.Close()
			dir := t.TempDir()
			configPath := filepath.Join(dir, "cli.yaml")
			original := []byte("agent_addr: 127.0.0.1:9443\ncloud_url: " + cloud.URL + "\n")
			if err := os.WriteFile(configPath, original, 0o600); err != nil {
				t.Fatal(err)
			}
			store := keychain.NewFakeStore()
			if err := store.SetPAT("pat"); err != nil {
				t.Fatal(err)
			}
			command := NewRootCommand(Options{KeychainFactory: func() (keychain.Store, error) { return store, nil }, HTTPClient: cloud.Client()})
			command.SetArgs([]string{"--config", configPath, "server", "connect", "--project-id", "project-1", "--node-id", "node-1"})
			err := command.Execute()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v", err)
			}
			contents, readErr := os.ReadFile(configPath)
			if readErr != nil || !bytes.Equal(contents, original) {
				t.Fatalf("config changed=%q err=%v", contents, readErr)
			}
		})
	}
}

func TestServerBootstrapRejectsUnsafeCredentialInput(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "cli.yaml")
	if err := os.WriteFile(configPath, []byte("agent_addr: 127.0.0.1:1\ncloud_url: http://127.0.0.1:9800\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	credentialPath := filepath.Join(dir, "bootstrap-key")
	privateKeyMarker := "PRIVATE " + "KEY"
	if err := os.WriteFile(credentialPath, []byte("-----BEGIN "+privateKeyMarker+"-----\ntest\n-----END "+privateKeyMarker+"-----\n"), 0o644); err != nil {
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
