package commands

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestGitHubInstallationClaimCommandUsesOneTimeLocalCallback(t *testing.T) {
	callbackDone := make(chan struct{})
	cloud := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer command-test-pat" {
			t.Fatalf("unexpected authorization header")
		}
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/v1/projects/proj-1/github/installations/77/claim/start":
			var body struct {
				LocalCallback string `json:"local_callback"`
				LocalState    string `json:"local_state"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			_, _ = io.WriteString(response, `{"authorization_url":"https://github.com/login/oauth/authorize?client_id=test"}`)
			go func() {
				defer close(callbackDone)
				result, err := http.Get(body.LocalCallback + "?grant=one-time-grant&state=" + url.QueryEscape(body.LocalState))
				if err != nil {
					t.Errorf("callback: %v", err)
					return
				}
				_ = result.Body.Close()
			}()
		case "/v1/github/installations/claim/redeem":
			var body struct {
				Grant string `json:"grant"`
				State string `json:"state"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Grant != "one-time-grant" || body.State == "" {
				t.Fatalf("redeem=%+v", body)
			}
			_, _ = io.WriteString(response, `{"installation":{"installation_id":77,"status":"active"},"repositories_synced":1}`)
		default:
			t.Fatalf("unexpected request %s %s", request.Method, request.URL.Path)
		}
	}))
	defer cloud.Close()

	command, output, configPath := newGitHubCommandRoot(t, cloud.URL)
	command.SetArgs([]string{"--config", configPath, "github", "installation", "claim", "--project-id", "proj-1", "--installation-id", "77", "--no-browser", "--timeout", "5s"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	<-callbackDone
	if !bytes.Contains(output.Bytes(), []byte("https://github.com/login/oauth/authorize")) || bytes.Contains(output.Bytes(), []byte("one-time-grant")) || bytes.Contains(output.Bytes(), []byte("command-test-pat")) {
		t.Fatalf("unsafe or incomplete output: %s", output.String())
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

	command, _, configPath = newGitHubCommandRoot(t, "http://127.0.0.1:1")
	command.SetArgs([]string{"--config", configPath, "github", "installation", "claim", "--project-id", "proj-1"})
	if err := command.Execute(); err == nil {
		t.Fatal("expected installation id validation error")
	}

	command, _, configPath = newGitHubCommandRoot(t, "http://127.0.0.1:1")
	command.SetArgs([]string{"--config", configPath, "github", "binding", "create", "--project-id", "proj-1", "--service-id", "svc-1", "--repository-id", "9", "--service-key", "Invalid"})
	if err := command.Execute(); err == nil {
		t.Fatal("expected service key validation error")
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
