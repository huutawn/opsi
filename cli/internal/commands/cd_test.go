package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/opsi-dev/opsi/cli/internal/keychain"
	"github.com/opsi-dev/opsi/cli/internal/repository"
	"github.com/spf13/cobra"
)

type cdCommandRunner struct {
	root string
	step int
}

func (r *cdCommandRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	r.step++
	switch r.step {
	case 1:
		return []byte(r.root + "\n"), nil
	case 2:
		return []byte("false\n"), nil
	case 3, 4:
		return nil, nil
	case 5:
		return []byte("M\x00Dockerfile\x00"), nil
	default:
		return nil, errors.New("unexpected git command")
	}
}

func TestCDPlanHumanAndJSONParity(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	configBytes, err := repository.RenderConfig(repository.ConfigOptions{ServiceKey: "api", Context: ".", Dockerfile: "Dockerfile", Platform: "linux/amd64", Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".opsi"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, defaultConfigPath), configBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	base, head := strings.Repeat("a", 40), strings.Repeat("b", 40)
	jsonOutput := &bytes.Buffer{}
	jsonCommand := NewRootCommand(Options{Version: "test", GitRunner: &cdCommandRunner{root: root}})
	jsonCommand.SetOut(jsonOutput)
	missingCLIConfig := filepath.Join(root, "missing-cli.yaml")
	jsonCommand.SetArgs([]string{"--config", missingCLIConfig, "cd", "plan", "--repo-dir", root, "--base", base, "--head", head, "--json"})
	if err := jsonCommand.Execute(); err != nil {
		t.Fatal(err)
	}
	var plan repository.ChangedServicePlan
	if err := json.Unmarshal(jsonOutput.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	humanOutput := &bytes.Buffer{}
	humanCommand := NewRootCommand(Options{Version: "test", GitRunner: &cdCommandRunner{root: root}})
	humanCommand.SetOut(humanOutput)
	humanCommand.SetArgs([]string{"cd", "plan", "--repo-dir", root, "--base", base, "--head", head, "--config", missingCLIConfig})
	if err := humanCommand.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(humanOutput.String(), plan.PlanHash) || !strings.Contains(humanOutput.String(), "api [service_path_changed]") {
		t.Fatalf("human=%s plan=%+v", humanOutput.String(), plan)
	}
}

func TestCDCloudCommandsUseGlobalConfigAndPAT(t *testing.T) {
	const pat = "cd-test-pat"
	var pathsMu sync.Mutex
	var paths []string
	var loopbackCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+pat {
			t.Errorf("authorization=%q", r.Header.Get("Authorization"))
		}
		pathsMu.Lock()
		paths = append(paths, r.URL.Path)
		pathsMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/repository-export/preview"):
			_, _ = io.WriteString(w, `{"run_id":"run-1","run_revision":4,"plan_hash":"plan-hash","source_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","repository_id":7,"target_branch":"main","path":".opsi/opsi-cd.yaml","yaml":"version: 2\n","diff":"+version: 2\n","preview_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","export_enabled":true}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/repository-export"):
			if !strings.HasPrefix(r.Header.Get("Idempotency-Key"), "export:run-1:") {
				t.Errorf("export idempotency=%q", r.Header.Get("Idempotency-Key"))
			}
			_, _ = io.WriteString(w, `{"repository_export":{"branch":"opsi/export-run","commit_sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","pull_request_number":9,"pull_request_url":"https://github.test/pr/9","reused":false}}`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/deployments"):
			_, _ = io.WriteString(w, `{"deployments":[{"id":"main-1","project_id":"proj-1","status":"succeeded","desired_digest":"sha256:main","snapshot":{}},{"id":"preview-1","project_id":"proj-1","status":"queued","desired_digest":"sha256:preview","snapshot":{"preview":{"head_sha":"abc123","expires_at":"2026-07-27T12:00:00Z"}}}]}`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/deployments/preview-1"):
			_, _ = io.WriteString(w, `{"id":"preview-1","project_id":"proj-1","status":"queued","desired_digest":"sha256:preview","snapshot":{"preview":{"head_sha":"abc123","expires_at":"2026-07-27T12:00:00Z"}}}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/cleanup"):
			_, _ = io.WriteString(w, `{"id":"preview-1","project_id":"proj-1","status":"cleanup_requested","desired_digest":"sha256:preview","snapshot":{"preview":{"head_sha":"abc123","expires_at":"2026-07-27T12:00:00Z"}}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	configPath := filepath.Join(t.TempDir(), "staging.yaml")
	if err := os.WriteFile(configPath, []byte("cloud_url: "+server.URL+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := keychain.NewFakeStore()
	if err := store.SetPAT(pat); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host == "127.0.0.1:9800" {
			loopbackCalls.Add(1)
			return nil, errors.New("default loopback trap received request")
		}
		return http.DefaultTransport.RoundTrip(request)
	})}
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "status before", args: []string{"--config", configPath, "cd", "status", "--project-id", "proj-1"}, want: "main-1\tsucceeded"},
		{name: "history after", args: []string{"cd", "history", "--config", configPath, "--project-id", "proj-1", "--json"}, want: `"project_id":"proj-1"`},
		{name: "preview list", args: []string{"--config", configPath, "cd", "preview", "list", "--project-id", "proj-1"}, want: "preview-1\tqueued"},
		{name: "preview detail", args: []string{"cd", "preview", "detail", "--config", configPath, "--project-id", "proj-1", "--deployment-id", "preview-1", "--json"}, want: `"head_sha":"abc123"`},
		{name: "preview cleanup", args: []string{"--config", configPath, "cd", "preview", "cleanup", "--project-id", "proj-1", "--deployment-id", "preview-1", "--idempotency-key", "cleanup-1", "--json"}, want: `"status":"cleanup_requested"`},
		{name: "export preview", args: []string{"--config", configPath, "cd", "export", "--project-id", "proj-1", "--run-id", "run-1", "--preview"}, want: "+version: 2"},
		{name: "export pull request", args: []string{"--config", configPath, "cd", "export", "--project-id", "proj-1", "--run-id", "run-1"}, want: "https://github.test/pr/9"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output := &bytes.Buffer{}
			command := NewRootCommand(Options{Version: "test", KeychainFactory: func() (keychain.Store, error) { return store, nil }, HTTPClient: client})
			command.SetOut(output)
			command.SetArgs(test.args)
			if err := command.Execute(); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), test.want) || strings.Contains(output.String(), pat) || strings.Contains(output.String(), configPath) || strings.Contains(output.String(), server.URL) {
				t.Fatalf("output=%s", output.String())
			}
		})
	}
	pathsMu.Lock()
	gotPaths := append([]string(nil), paths...)
	pathsMu.Unlock()
	if loopbackCalls.Load() != 0 || len(gotPaths) != 9 {
		t.Fatalf("paths=%v loopback calls=%d", gotPaths, loopbackCalls.Load())
	}
}

func TestCDSelectedConfigFailsClosed(t *testing.T) {
	var calls atomic.Int64
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("unexpected Cloud request")
	})}
	for _, test := range []struct {
		name    string
		content string
	}{
		{name: "missing", content: ""},
		{name: "unreadable", content: ""},
		{name: "malformed", content: "cloud_url: ["},
		{name: "invalid", content: "cloud_url: http://"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			configPath := filepath.Join(dir, "invalid.yaml")
			if test.name == "unreadable" {
				configPath = dir
			} else if test.name != "missing" {
				if err := os.WriteFile(configPath, []byte(test.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			command := NewRootCommand(Options{Version: "test", HTTPClient: client})
			command.SetArgs([]string{"--config", configPath, "cd", "status", "--project-id", "proj-1"})
			if err := command.Execute(); err == nil {
				t.Fatal("expected selected config failure")
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("Cloud requests=%d", calls.Load())
	}
}

func TestCDCommandConstructionKeepsConfigIndependent(t *testing.T) {
	type authority struct {
		server *httptest.Server
		path   string
		calls  *atomic.Int64
	}
	authorities := make([]authority, 2)
	for i := range authorities {
		name := "one"
		if i == 1 {
			name = "two"
		}
		calls := &atomic.Int64{}
		authorities[i].calls = calls
		authorities[i].server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			_, _ = io.WriteString(w, `{"deployments":[]}`)
		}))
		authorities[i].path = filepath.Join(t.TempDir(), name+".yaml")
		if err := os.WriteFile(authorities[i].path, []byte("cloud_url: "+authorities[i].server.URL+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		defer authorities[i].server.Close()
	}
	var wait sync.WaitGroup
	errs := make(chan error, len(authorities))
	for _, authority := range authorities {
		command := NewRootCommand(Options{Version: "test", HTTPClient: authority.server.Client()})
		command.SetArgs([]string{"--config", authority.path, "cd", "status", "--project-id", "proj-1"})
		wait.Add(1)
		go func(command *cobra.Command) {
			defer wait.Done()
			errs <- command.Execute()
		}(command)
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, authority := range authorities {
		if authority.calls.Load() != 1 {
			t.Fatalf("authority calls=%d", authority.calls.Load())
		}
	}
}
