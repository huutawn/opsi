package commands

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/opsi-dev/opsi/cli/internal/keychain"
	"github.com/spf13/cobra"
)

func TestGitHubCommandsUseExistingCloudAPIs(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	cloud := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer command-test-pat" {
			t.Fatalf("unexpected authorization header")
		}
		mu.Lock()
		calls = append(calls, request.Method+" "+request.URL.Path)
		mu.Unlock()
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /v1/projects/proj-1/github/installations":
			_, _ = io.WriteString(response, `{"installations":[{"installation_id":7,"status":"active"}]}`)
		case "GET /v1/projects/proj-1/github/repositories":
			_, _ = io.WriteString(response, `{"repositories":[{"repository_id":9,"installation_id":7,"full_name":"owner/repo","status":"active"}]}`)
		case "POST /v1/projects/proj-1/github/repositories/9/claim":
			_, _ = io.WriteString(response, `{"repository_id":9,"project_id":"proj-1","status":"active"}`)
		case "DELETE /v1/projects/proj-1/github/repositories/9/claim":
			_, _ = io.WriteString(response, `{}`)
		case "GET /v1/projects/proj-1/github/bindings":
			_, _ = io.WriteString(response, `{"bindings":[{"id":"binding-1","project_id":"proj-1","service_id":"svc-1","repository_id":9,"service_key":"api","status":"active"}]}`)
		case "POST /v1/projects/proj-1/github/bindings":
			_, _ = io.WriteString(response, `{"id":"binding-1","project_id":"proj-1","service_id":"svc-1","repository_id":9,"service_key":"api","status":"active"}`)
		case "DELETE /v1/projects/proj-1/github/bindings/binding-1":
			_, _ = io.WriteString(response, `{}`)
		default:
			t.Fatalf("unexpected request %s %s", request.Method, request.URL.Path)
		}
	}))
	defer cloud.Close()

	tests := [][]string{
		{"github", "installation", "list", "--project-id", "proj-1"},
		{"github", "repository", "list", "--project-id", "proj-1"},
		{"github", "repository", "claim", "--project-id", "proj-1", "--repository-id", "9"},
		{"github", "repository", "release", "--project-id", "proj-1", "--repository-id", "9"},
		{"github", "binding", "list", "--project-id", "proj-1"},
		{"github", "binding", "create", "--project-id", "proj-1", "--service-id", "svc-1", "--repository-id", "9", "--service-key", "api"},
		{"github", "binding", "remove", "--project-id", "proj-1", "--binding-id", "binding-1"},
	}
	for _, args := range tests {
		command, output, configPath := newGitHubCommandRoot(t, cloud.URL)
		command.SetArgs(append([]string{"--config", configPath}, args...))
		if err := command.Execute(); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if output.Len() == 0 {
			t.Fatalf("%v: expected JSON output", args)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != len(tests) {
		t.Fatalf("calls=%v", calls)
	}
}

func TestGitHubCommandsValidateMutatingInputsBeforeCloudCall(t *testing.T) {
	command, _, configPath := newGitHubCommandRoot(t, "http://127.0.0.1:1")
	command.SetArgs([]string{"--config", configPath, "github", "repository", "claim", "--project-id", "proj-1"})
	if err := command.Execute(); err == nil {
		t.Fatal("expected repository id validation error")
	}

	command, _, configPath = newGitHubCommandRoot(t, "http://127.0.0.1:1")
	command.SetArgs([]string{"--config", configPath, "github", "binding", "create", "--project-id", "proj-1", "--repository-id", "9"})
	if err := command.Execute(); err == nil {
		t.Fatal("expected binding validation error")
	}
}

func newGitHubCommandRoot(t *testing.T, cloudURL string) (*cobra.Command, *bytes.Buffer, string) {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "cli.yaml")
	if err := os.WriteFile(configPath, []byte("cloud_url: "+cloudURL+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := keychain.NewFakeStore()
	if err := store.SetPAT("command-test-pat"); err != nil {
		t.Fatal(err)
	}
	command := NewRootCommand(Options{KeychainFactory: func() (keychain.Store, error) { return store, nil }})
	output := bytes.NewBuffer(nil)
	command.SetOut(output)
	command.SetErr(output)
	return command, output, configPath
}
