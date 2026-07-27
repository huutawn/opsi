package commands

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opsi-dev/opsi/cli/internal/keychain"
)

func TestNodeCommandUsesCloudNodeAPIs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"status":"accepted","action":"drain"}`))
			return
		}
		_, _ = w.Write([]byte(`{"nodes":[{"id":"n1","project_id":"p1","name":"node","role":"server","status":"healthy"}]}`))
	}))
	defer server.Close()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "cli.yaml")
	if err := os.WriteFile(configPath, []byte("cloud_url: "+server.URL+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := keychain.NewFakeStore()
	_ = store.SetPAT("node-pat")
	command := NewRootCommand(Options{KeychainFactory: func() (keychain.Store, error) { return store, nil }})
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetArgs([]string{"--config", configPath, "node", "list", "--project-id", "p1", "--json"})
	if err := command.Execute(); err != nil || !strings.Contains(output.String(), "n1") {
		t.Fatalf("err=%v output=%s", err, output.String())
	}
}
