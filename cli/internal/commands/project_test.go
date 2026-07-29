package commands

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/opsi-dev/opsi/cli/internal/keychain"
)

func TestProjectCommandsUseSelectedCloudConfigAndPAT(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer project-pat" {
			t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"id":"p1","org_id":"o1","name":"Demo","slug":"demo","status":"active"}`))
			return
		}
		_, _ = w.Write([]byte(`{"projects":[{"id":"p1","org_id":"o1","name":"Demo","slug":"demo","status":"active"}]}`))
	}))
	defer server.Close()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "cli.yaml")
	if err := os.WriteFile(configPath, []byte("cloud_url: "+server.URL+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := keychain.NewFakeStore()
	if err := store.SetPAT("project-pat"); err != nil {
		t.Fatal(err)
	}
	command := NewRootCommand(Options{KeychainFactory: func() (keychain.Store, error) { return store, nil }})
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetArgs([]string{"project", "list", "--org-id", "o1", "--config", configPath, "--json"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	var listed map[string]any
	if err := json.Unmarshal(output.Bytes(), &listed); err != nil || len(listed["projects"].([]any)) != 1 {
		t.Fatalf("output=%s err=%v", output.String(), err)
	}
	output.Reset()
	command = NewRootCommand(Options{KeychainFactory: func() (keychain.Store, error) { return store, nil }})
	command.SetOut(&output)
	command.SetArgs([]string{"--config", configPath, "project", "create", "--org-id", "o1", "--name", "Demo", "--slug", "demo", "--idempotency-key", "project-key"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || output.Len() == 0 {
		t.Fatalf("paths=%v output=%q", paths, output.String())
	}
}

func TestProjectMalformedConfigMakesZeroNetworkCalls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cli.yaml")
	if err := os.WriteFile(path, []byte("cloud_url: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	command := NewRootCommand(Options{HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, nil
	})}})
	command.SetArgs([]string{"project", "list", "--org-id", "o1", "--config", path})
	if err := command.Execute(); err == nil {
		t.Fatal("malformed config was accepted")
	}
	if calls.Load() != 0 {
		t.Fatalf("network calls=%d", calls.Load())
	}
}
