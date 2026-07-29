package commands

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opsi-dev/opsi/cli/internal/config"
	"github.com/opsi-dev/opsi/cli/internal/keychain"
)

func TestTopologyAndPolicyCLIUsePATStrictFilesConfirmationAndIdempotency(t *testing.T) {
	const pat = "placement-pat"
	paths := []string{}
	keys := []string{}
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+pat {
			t.Fatalf("auth=%q", r.Header.Get("Authorization"))
		}
		paths = append(paths, r.URL.Path)
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/topology/plan"):
			io.WriteString(w, `{"draft":{"schema_version":"opsi.topology_plan/v1","project_id":"p1","assignments":[]},"plan_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","state_hash":""}`)
		case strings.HasSuffix(r.URL.Path, "/topology/apply"):
			io.WriteString(w, `{"plan":{"schema_version":"opsi.topology_plan/v1","id":"topo-1","project_id":"p1","revision":1,"state_hash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","plan_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","assignments":[],"created_by":"u1","applied_by":"u1","created_at":"2026-07-19T00:00:00Z","applied_at":"2026-07-19T00:00:00Z"},"reused":false}`)
		case strings.HasSuffix(r.URL.Path, "/deployment-policies/preview"):
			io.WriteString(w, `{"policy":{"schema_version":"opsi.deployment_policy/v1","project_id":"p1","repository_id":7,"service_keys":["api"],"workflow_refs":["wf"],"allowed_events":["push"],"allowed_git_refs":["refs/heads/main"],"environment_id":"e1","allowed_runtime_ids":["r1"],"allowed_oci_repositories":["ghcr.io/o/r/api"],"allowed_platforms":["linux/amd64"],"allowed_config_hashes":["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"],"allowed_build_plan_hashes":["bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"],"allow_unknown_capacity":false,"enabled":true},"policy_hash":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}`)
		case strings.HasSuffix(r.URL.Path, "/deployment-policies/apply"):
			io.WriteString(w, `{"policy":{"schema_version":"opsi.deployment_policy/v1","id":"pol-1","revision":1,"state_hash":"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd","policy_hash":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","policy":{"schema_version":"opsi.deployment_policy/v1","project_id":"p1","repository_id":7,"service_keys":["api"],"workflow_refs":["wf"],"allowed_events":["push"],"allowed_git_refs":["refs/heads/main"],"environment_id":"e1","allowed_runtime_ids":["r1"],"allowed_oci_repositories":["ghcr.io/o/r/api"],"allowed_platforms":["linux/amd64"],"allowed_config_hashes":["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"],"allowed_build_plan_hashes":["bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"],"allow_unknown_capacity":false,"enabled":true},"created_by":"u1","applied_by":"u1","created_at":"2026-07-19T00:00:00Z","applied_at":"2026-07-19T00:00:00Z"},"reused":false}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer cloud.Close()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("cloud_url: "+cloud.URL+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	topologyFile := filepath.Join(dir, "topology.json")
	if err := os.WriteFile(topologyFile, []byte(`{"schema_version":"opsi.topology_plan/v1","project_id":"p1","assignments":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	policyFile := filepath.Join(dir, "policy.json")
	if err := os.WriteFile(policyFile, []byte(`{"schema_version":"opsi.deployment_policy/v1","project_id":"p1","repository_id":7,"service_keys":["api"],"workflow_refs":["wf"],"allowed_events":["push"],"allowed_git_refs":["refs/heads/main"],"environment_id":"e1","allowed_runtime_ids":["r1"],"allowed_oci_repositories":["ghcr.io/o/r/api"],"allowed_platforms":["linux/amd64"],"allowed_config_hashes":["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"],"allowed_build_plan_hashes":["bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"],"allow_unknown_capacity":false,"enabled":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store := keychain.NewFakeStore()
	_ = store.SetPAT(pat)
	options := Options{Version: "test", KeychainFactory: func() (keychain.Store, error) { return store, nil }}
	run := func(args ...string) string {
		command := NewRootCommand(options)
		output := &bytes.Buffer{}
		command.SetOut(output)
		command.SetArgs(append([]string{"--config", configPath}, args...))
		if err := command.Execute(); err != nil {
			t.Fatalf("args=%v err=%v", args, err)
		}
		return output.String()
	}
	if output := run("topology", "plan", "--project-id", "p1", "--file", topologyFile); !strings.Contains(output, "Plan hash") {
		t.Fatalf("output=%s", output)
	}
	command := NewRootCommand(options)
	command.SetArgs([]string{"--config", configPath, "topology", "apply", "--project-id", "p1", "--file", topologyFile, "--idempotency-key", "topology-key"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("confirmation err=%v", err)
	}
	if output := run("topology", "apply", "--project-id", "p1", "--file", topologyFile, "--idempotency-key", "topology-key", "--yes"); !strings.Contains(output, "topo-1 revision 1") {
		t.Fatalf("output=%s", output)
	}
	if output := run("policy", "create", "--project-id", "p1", "--file", policyFile); !strings.Contains(output, "Policy hash") {
		t.Fatalf("output=%s", output)
	}
	if output := run("policy", "apply", "--project-id", "p1", "--file", policyFile, "--idempotency-key", "policy-key", "--yes"); !strings.Contains(output, "pol-1 revision 1") {
		t.Fatalf("output=%s", output)
	}
	if strings.Join(paths, ",") != "/api/projects/p1/topology/plan,/api/projects/p1/topology/apply,/api/projects/p1/deployment-policies/preview,/api/projects/p1/deployment-policies/apply" {
		t.Fatalf("paths=%v", paths)
	}
	if keys[1] != "topology-key" || keys[3] != "policy-key" {
		t.Fatalf("keys=%v", keys)
	}
}

func TestLocalPlacementProxyPreservesHashesAndMutationHeaders(t *testing.T) {
	const pat = "local-placement-pat"
	cloudPaths := []string{}
	cloudKeys := []string{}
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+pat {
			t.Fatalf("auth=%q", r.Header.Get("Authorization"))
		}
		cloudPaths = append(cloudPaths, r.URL.Path)
		cloudKeys = append(cloudKeys, r.Header.Get("Idempotency-Key"))
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/topology/plan") {
			io.WriteString(w, `{"plan_hash":"same-hash","state_hash":""}`)
			return
		}
		io.WriteString(w, `{"plan":{"id":"topo-1","revision":1,"plan_hash":"same-hash","state_hash":"state-hash"},"reused":false}`)
	}))
	defer cloud.Close()
	store := keychain.NewFakeStore()
	_ = store.SetPAT(pat)
	mux := newStartMux(t.TempDir(), "", config.Config{AgentAddr: "127.0.0.1:1", CloudURL: cloud.URL}, func() (keychain.Store, error) { return store, nil })
	server := httptest.NewServer(mux)
	defer server.Close()
	previewBody := `{"draft":{"schema_version":"opsi.topology_plan/v1","project_id":"p1","assignments":[]}}`
	response, err := http.Post(server.URL+"/api/local/projects/p1/topology/plan", "application/json", strings.NewReader(previewBody))
	if err != nil {
		t.Fatal(err)
	}
	var preview map[string]any
	_ = json.NewDecoder(response.Body).Decode(&preview)
	response.Body.Close()
	if response.StatusCode != 200 || preview["plan_hash"] != "same-hash" {
		t.Fatalf("preview=%v status=%d", preview, response.StatusCode)
	}
	sessionResponse, err := http.Get(server.URL + "/api/local/session")
	if err != nil {
		t.Fatal(err)
	}
	var session map[string]any
	_ = json.NewDecoder(sessionResponse.Body).Decode(&session)
	sessionResponse.Body.Close()
	request, err := http.NewRequest(http.MethodPost, server.URL+"/api/local/projects/p1/topology/apply", strings.NewReader(`{"draft":{"schema_version":"opsi.topology_plan/v1","project_id":"p1","assignments":[]},"expected_revision":0,"expected_state_hash":""}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Local-Session", session["local_session"].(string))
	request.Header.Set("Idempotency-Key", "local-topology-key")
	mutation, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(mutation.Body)
	mutation.Body.Close()
	if mutation.StatusCode != 200 || !strings.Contains(string(body), "same-hash") {
		t.Fatalf("status=%d body=%s", mutation.StatusCode, body)
	}
	if strings.Join(cloudPaths, ",") != "/api/projects/p1/topology/plan,/api/projects/p1/topology/apply" || cloudKeys[1] != "local-topology-key" {
		t.Fatalf("paths=%v keys=%v", cloudPaths, cloudKeys)
	}
}
