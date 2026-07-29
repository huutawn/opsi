package commands

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opsi-dev/opsi/cli/internal/keychain"
)

func TestBuildRecordCLIHumanJSONAndDetailUsePATReadOnly(t *testing.T) {
	const pat = "build-record-pat"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+pat {
			t.Fatalf("auth=%q", r.Header.Get("Authorization"))
		}
		if r.Method != "GET" {
			t.Fatalf("method=%s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		record := `{"schema_version":"opsi.build_record/v1","id":"br-1","project_id":"project-1","repository_id":7,"repository_owner_id":8,"active_binding_id":"binding-1","service_id":"service-1","service_key":"api","created_at":"2026-07-19T00:00:00Z","workload":{"issuer":"https://token.actions.githubusercontent.com","subject":"repo:huutawn/opsi","repository_id":7,"repository_owner_id":8,"ref":"refs/heads/developer","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","event_name":"push","workflow":"opsi-cd","workflow_ref":"huutawn/opsi/.github/workflows/opsi-cd.yaml@refs/heads/developer","run_id":99,"run_attempt":1},"build":{"config_hash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","platform":"linux/amd64","oci_repository":"ghcr.io/huutawn/opsi/api","oci_digest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","status":"succeeded"}}`
		if strings.HasSuffix(r.URL.Path, "/build-records/br-1") {
			_, _ = w.Write([]byte(record))
			return
		}
		_, _ = w.Write([]byte(`{"records":[` + record + `],"next_cursor":"next"}`))
	}))
	defer server.Close()
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("cloud_url: "+server.URL+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := keychain.NewFakeStore()
	if err := store.SetPAT(pat); err != nil {
		t.Fatal(err)
	}
	options := Options{Version: "test", KeychainFactory: func() (keychain.Store, error) { return store, nil }}
	human := &bytes.Buffer{}
	command := NewRootCommand(options)
	command.SetOut(human)
	command.SetArgs([]string{"--config", configPath, "build-record", "list", "--project-id", "project-1"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(human.String(), "api") || !strings.Contains(human.String(), "sha256:cccc") || !strings.Contains(human.String(), "Next cursor") {
		t.Fatalf("human=%s", human.String())
	}
	jsonOut := &bytes.Buffer{}
	command = NewRootCommand(options)
	command.SetOut(jsonOut)
	command.SetArgs([]string{"--config", configPath, "build-record", "get", "--project-id", "project-1", "--record-id", "br-1", "--json"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(jsonOut.Bytes(), &value); err != nil || value["id"] != "br-1" {
		t.Fatalf("json=%s err=%v", jsonOut.String(), err)
	}
}
