package commands

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/opsi-dev/opsi/cli/internal/keychain"
)

func TestRuntimeCommandUsesPlacementFacts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"project_id":"p1","environments":[],"runtimes":[{"id":"rt1","project_id":"p1","name":"prod","type":"k3s","status":"healthy"}],"nodes":[],"agents":[],"services":[]}`))
	}))
	defer server.Close()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "cli.yaml")
	if err := os.WriteFile(configPath, []byte("cloud_url: "+server.URL+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := keychain.NewFakeStore()
	_ = store.SetPAT("runtime-pat")
	command := NewRootCommand(Options{KeychainFactory: func() (keychain.Store, error) { return store, nil }})
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetArgs([]string{"runtime", "get", "--project-id", "p1", "--runtime-id", "rt1", "--config", configPath, "--json"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if output.Len() == 0 {
		t.Fatal("runtime output is empty")
	}
}
