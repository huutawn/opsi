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

func TestAuditCommandPrintsRedactedEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/projects/p1/audit" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"events":[{"action":"PROJECT_CREATED","result":"success"}]}`))
	}))
	defer server.Close()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "cli.yaml")
	if err := os.WriteFile(configPath, []byte("cloud_url: "+server.URL+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := keychain.NewFakeStore()
	_ = store.SetPAT("audit-pat")
	command := NewRootCommand(Options{KeychainFactory: func() (keychain.Store, error) { return store, nil }})
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetArgs([]string{"audit", "list", "--project-id", "p1", "--config", configPath})
	if err := command.Execute(); err != nil || !strings.Contains(output.String(), "PROJECT_CREATED") {
		t.Fatalf("err=%v output=%s", err, output.String())
	}
}
