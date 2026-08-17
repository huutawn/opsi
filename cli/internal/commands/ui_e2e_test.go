package commands

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cli/internal/config"
	"github.com/opsi-dev/opsi/cli/internal/keychain"
	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

const (
	manualParityLocalAddr = "127.0.0.1:19881"
	manualParityCloudAddr = "127.0.0.1:19882"
	manualParityAgentAddr = "127.0.0.1:19883"
)

func TestManualParityServer(t *testing.T) {
	if os.Getenv("OPSI_UI_E2E_SERVER") != "1" {
		t.Skip("Playwright server helper")
	}
	agentDown := &atomic.Bool{}
	cloud := &manualParityCloud{agentDown: agentDown, projects: []map[string]any{manualProject("proj-1", "Parity Project", "parity")}}
	cloudServer := &http.Server{Addr: manualParityCloudAddr, Handler: cloud}
	cloudListener, err := net.Listen("tcp", manualParityCloudAddr)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = cloudServer.Serve(cloudListener) }()
	t.Cleanup(func() { _ = cloudServer.Close() })

	pin, stopAgent := startManualParityTLSAgent(t, agentDown)
	defer stopAgent()
	configPath := filepath.Join(t.TempDir(), "cli.yaml")
	if err := config.Save(configPath, config.Config{
		AgentAddr: manualParityAgentAddr,
		CloudURL:  "http://" + manualParityCloudAddr,
		TLS:       config.TLSConfig{PinnedServerCertSHA256: pin, ServerName: "127.0.0.1"},
	}); err != nil {
		t.Fatal(err)
	}
	store := keychain.NewFakeStore()
	if err := store.SetPAT("e2e-pat"); err != nil {
		t.Fatal(err)
	}
	uiDir, err := filepath.Abs("../../ui/out")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(uiDir, "index.html")); err != nil {
		t.Fatalf("build the UI before Playwright: %v", err)
	}
	t.Setenv("OPSI_UI_DIR", uiDir)
	path := configPath
	command := newStartCommand(&path, func() (keychain.Store, error) { return store, nil })
	command.SetArgs([]string{"--addr", manualParityLocalAddr})
	command.SetOut(os.Stdout)
	command.SetErr(os.Stderr)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
}

type manualParityCloud struct {
	mu        sync.Mutex
	mode      string
	projects  []map[string]any
	agentDown *atomic.Bool
}

func (s *manualParityCloud) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/__control" {
		s.mu.Lock()
		s.mode = r.URL.Query().Get("mode")
		s.agentDown.Store(r.URL.Query().Get("agent") == "down")
		s.mu.Unlock()
		writeManualJSON(w, map[string]any{"ok": true})
		return
	}
	s.mu.Lock()
	mode := s.mode
	s.mu.Unlock()
	if mode == "cloud-outage" {
		w.WriteHeader(http.StatusServiceUnavailable)
		writeManualJSON(w, map[string]any{"error": map[string]any{"code": "CLOUD_UNAVAILABLE", "message": "fake Cloud outage"}})
		return
	}
	if mode == "session-expired" {
		w.WriteHeader(http.StatusUnauthorized)
		writeManualJSON(w, map[string]any{"error": map[string]any{"code": "PAT_EXPIRED", "message": "fake session expiry"}})
		return
	}
	if r.Header.Get("Authorization") != "Bearer e2e-pat" {
		w.WriteHeader(http.StatusUnauthorized)
		writeManualJSON(w, map[string]any{"error": map[string]any{"code": "PAT_REQUIRED", "message": "missing test PAT"}})
		return
	}

	switch {
	case r.URL.Path == "/v1/auth/pat/verify":
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		projectID := body["project_id"]
		if projectID == "" {
			projectID = "proj-1"
		}
		writeManualJSON(w, map[string]any{"user_id": "user-1", "org_id": "org-1", "project_id": projectID, "role": "owner"})
	case r.URL.Path == "/api/orgs/org-1/projects" && r.Method == http.MethodGet:
		s.mu.Lock()
		projects := append([]map[string]any(nil), s.projects...)
		s.mu.Unlock()
		writeManualJSON(w, map[string]any{"projects": projects})
	case r.URL.Path == "/api/orgs/org-1/projects" && r.Method == http.MethodPost:
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		project := manualProject("proj-2", stringValue(body["name"], "Created Project"), stringValue(body["slug"], "created"))
		s.mu.Lock()
		if len(s.projects) == 1 {
			s.projects = append(s.projects, project)
		}
		s.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		writeManualJSON(w, project)
	case strings.HasSuffix(r.URL.Path, "/readiness"):
		writeManualJSON(w, map[string]any{"project_id": manualProjectID(r.URL.Path), "status": "ready", "can_deploy": true, "next_action": "Review a BuildRecord deployment."})
	case strings.HasSuffix(r.URL.Path, "/nodes") && r.Method == http.MethodGet:
		writeManualJSON(w, map[string]any{"nodes": []map[string]any{{"id": "node-1", "name": "agent-node", "role": "server", "status": "healthy", "cpu_cores": 4, "memory_mb": 8192, "agent_id": "agent-1", "agent_version": "test", "last_seen_at": time.Now().UTC()}}})
	case strings.Contains(r.URL.Path, "/nodes/node-1/") && r.Method == http.MethodPost:
		action := filepath.Base(r.URL.Path)
		writeManualJSON(w, map[string]any{"id": "node-1", "name": "agent-node", "role": "server", "status": action})
	case strings.HasSuffix(r.URL.Path, "/nodes/node-1"):
		writeManualJSON(w, map[string]any{"id": "node-1", "name": "agent-node", "role": "server", "status": "healthy", "runtime_id": "runtime-1", "checks": []any{}})
	case strings.HasSuffix(r.URL.Path, "/services") && r.Method == http.MethodGet:
		writeManualJSON(w, map[string]any{"services": []map[string]any{{"id": "svc-1", "name": "api", "type": "application", "status": "ready", "source_type": "image", "container_port": 8080, "replicas": 1}}})
	case strings.HasSuffix(r.URL.Path, "/services") && r.Method == http.MethodPost:
		w.WriteHeader(http.StatusCreated)
		writeManualJSON(w, map[string]any{"id": "svc-created", "name": "created", "type": "application", "status": "ready", "source_type": "image"})
	case strings.HasSuffix(r.URL.Path, "/bootstrap-sessions") && r.Method == http.MethodGet:
		writeManualJSON(w, map[string]any{"sessions": []any{}})
	case strings.HasSuffix(r.URL.Path, "/bootstrap-sessions") && r.Method == http.MethodPost:
		w.WriteHeader(http.StatusAccepted)
		writeManualJSON(w, map[string]any{"id": "boot-1", "status": "created", "role": "server", "created_at": time.Now().UTC()})
	case strings.HasSuffix(r.URL.Path, "/deployments") && r.Method == http.MethodGet:
		writeManualJSON(w, map[string]any{"deployments": []any{}})
	case strings.HasSuffix(r.URL.Path, "/exposures") && r.Method == http.MethodGet:
		writeManualJSON(w, map[string]any{"exposures": []any{}})
	case strings.HasSuffix(r.URL.Path, "/audit"):
		writeManualJSON(w, map[string]any{"events": []map[string]any{{"id": "audit-1", "actor_user_id": "user-1", "actor_type": "user", "action": "PROJECT_VIEWED", "resource_type": "project", "resource_id": manualProjectID(r.URL.Path), "result": "success", "metadata_redacted": map[string]any{"request_id": "req-e2e-1"}, "created_at": time.Now().UTC()}}})
	case strings.HasSuffix(r.URL.Path, "/support"):
		writeManualJSON(w, manualSupport(manualProjectID(r.URL.Path)))
	case strings.HasSuffix(r.URL.Path, "/topology/facts"):
		writeManualJSON(w, manualFacts(manualProjectID(r.URL.Path)))
	case strings.HasSuffix(r.URL.Path, "/topology"):
		writeManualJSON(w, map[string]any{"schema_version": "opsi.topology_plan/v1", "project_id": manualProjectID(r.URL.Path), "id": "topology-1", "revision": 1, "state_hash": "state-1", "plan_hash": "plan-1", "assignments": []any{}, "created_by": "user-1", "applied_by": "user-1", "created_at": time.Now().UTC(), "applied_at": time.Now().UTC()})
	case strings.HasSuffix(r.URL.Path, "/deployment-policies"):
		writeManualJSON(w, map[string]any{"policies": []any{}})
	case strings.HasSuffix(r.URL.Path, "/github/installations"):
		writeManualJSON(w, map[string]any{"installations": []map[string]any{{"installation_id": 42, "account_login": "opsi-test", "account_type": "Organization", "status": "active"}}})
	case strings.HasSuffix(r.URL.Path, "/github/repositories"):
		writeManualJSON(w, map[string]any{"repositories": []map[string]any{{"repository_id": 101, "installation_id": 42, "full_name": "opsi-test/api", "default_branch": "main", "status": "active", "claim_status": "active", "archived": false, "disabled": false}}})
	case strings.HasSuffix(r.URL.Path, "/github/bindings"):
		writeManualJSON(w, map[string]any{"bindings": []map[string]any{{"id": "binding-1", "project_id": manualProjectID(r.URL.Path), "service_id": "svc-1", "repository_id": 101, "service_key": "api", "config_path": ".opsi/opsi-cd.yaml", "status": "active"}}})
	case strings.Contains(r.URL.Path, "/build-records/"):
		writeManualJSON(w, manualBuildRecord(manualProjectID(r.URL.Path)))
	case strings.HasSuffix(r.URL.Path, "/build-records"):
		writeManualJSON(w, map[string]any{"records": []any{manualBuildRecord(manualProjectID(r.URL.Path))}})
	case strings.Contains(r.URL.Path, "/build-jobs"):
		writeManualJSON(w, map[string]any{"jobs": []any{}})
	case strings.HasSuffix(r.URL.Path, "/resources"):
		writeManualJSON(w, map[string]any{"resources": []any{}})
	case strings.HasSuffix(r.URL.Path, "/resource-bindings"):
		writeManualJSON(w, map[string]any{"bindings": []any{}})
	case strings.HasSuffix(r.URL.Path, "/retained-storages"):
		writeManualJSON(w, map[string]any{"retained_storages": []any{}})
	case strings.HasSuffix(r.URL.Path, "/backups"):
		writeManualJSON(w, map[string]any{"backups": []any{}})
	case strings.HasSuffix(r.URL.Path, "/restores"):
		writeManualJSON(w, map[string]any{"restores": []any{}})
	case strings.HasSuffix(r.URL.Path, "/cutover-reviews"):
		writeManualJSON(w, map[string]any{"reviews": []any{}})
	case strings.HasSuffix(r.URL.Path, "/cutovers"):
		writeManualJSON(w, map[string]any{"cutovers": []any{}})
	case strings.HasSuffix(r.URL.Path, "/cutover-rollbacks"):
		writeManualJSON(w, map[string]any{"rollbacks": []any{}})
	case strings.HasSuffix(r.URL.Path, "/cutover-finalizations"):
		writeManualJSON(w, map[string]any{"finalizations": []any{}})
	default:
		w.WriteHeader(http.StatusNotFound)
		writeManualJSON(w, map[string]any{"error": map[string]any{"code": "TEST_ROUTE_MISSING", "message": r.Method + " " + r.URL.Path}})
	}
}

func manualProject(id, name, slug string) map[string]any {
	return map[string]any{"id": id, "org_id": "org-1", "name": name, "slug": slug, "status": "ready", "created_by": "user-1"}
}

func manualProjectID(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i, part := range parts {
		if part == "projects" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return "proj-1"
}

func manualFacts(projectID string) map[string]any {
	return map[string]any{
		"project_id":   projectID,
		"environments": []map[string]any{{"id": "env-1", "project_id": projectID, "name": "Production", "type": "prod", "status": "active"}},
		"runtimes":     []map[string]any{{"id": "runtime-1", "project_id": projectID, "environment_id": "env-1", "name": "Primary", "type": "k3s", "status": "ready"}},
		"nodes":        []map[string]any{{"id": "node-1", "project_id": projectID, "runtime_id": "runtime-1", "status": "healthy", "cpu_cores": 4, "memory_mb": 8192, "last_seen_at": time.Now().UTC()}},
		"agents":       []map[string]any{{"id": "agent-1", "project_id": projectID, "runtime_id": "runtime-1", "node_id": "node-1", "status": "active", "capabilities": map[string]any{"deploy": true}, "last_seen_at": time.Now().UTC()}},
		"services":     []map[string]any{{"id": "svc-1", "project_id": projectID, "key": "api"}},
	}
}

func manualBuildRecord(projectID string) map[string]any {
	return map[string]any{
		"schema_version": "opsi.build_record/v1", "id": "build-1", "project_id": projectID, "repository_id": 101, "repository_owner_id": 42, "active_binding_id": "binding-1", "service_id": "svc-1", "service_key": "api", "created_at": time.Now().UTC(),
		"workload": map[string]any{"issuer": "https://token.actions.githubusercontent.com", "subject": "repo:opsi-test/api:ref:refs/heads/main", "repository_id": 101, "repository_owner_id": 42, "ref": "refs/heads/main", "sha": strings.Repeat("a", 40), "event_name": "push", "workflow": "build", "workflow_ref": "opsi-test/api/.github/workflows/build.yml@refs/heads/main", "run_id": 7, "run_attempt": 1},
		"build":    map[string]any{"config_hash": "config-1", "plan_hash": "plan-1", "platform": "linux/amd64", "oci_repository": "registry.example.test/opsi/api", "oci_digest": "sha256:" + strings.Repeat("b", 64), "status": "succeeded"},
	}
}

func manualSupport(projectID string) map[string]any {
	return map[string]any{
		"generated_at": time.Now().UTC(), "readiness": map[string]any{"project_id": projectID, "status": "ready", "can_deploy": true},
		"counts":    map[string]any{"nodes": 1, "healthy_nodes": 1, "services": 1, "deployment_jobs": 0, "failed_deployments": 0, "bootstrap_sessions": 0, "open_bootstrap_jobs": 0, "audit_events": 1},
		"dashboard": map[string]any{"title": "Opsi", "datasource": "local", "refresh": "30s", "panels": []any{}},
		"signals":   []any{}, "active_alerts": []any{}, "configured_alerts": []any{}, "production_gates": []any{}, "runbooks": []any{}, "recent_request_ids": []string{"req-e2e-1"},
		"break_glass_policy": map[string]any{"time_limited": true, "approval_required": true, "reason_required": true, "audited": true, "secret_reveal_by_default": false, "owner_notification": "required"},
	}
}

func stringValue(value any, fallback string) string {
	if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
		return text
	}
	return fallback
}

func writeManualJSON(w http.ResponseWriter, value any) {
	w.Header().Set("content-type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

type manualParityAgent struct {
	*localFacadeAgent
	unavailable *atomic.Bool
}

func (s *manualParityAgent) Status(ctx context.Context, req *agentv1.StatusRequest) (*agentv1.StatusResponse, error) {
	if s.unavailable.Load() {
		return nil, status.Error(codes.Unavailable, "fake Agent unavailable")
	}
	return s.localFacadeAgent.Status(ctx, req)
}

func (s *manualParityAgent) SetupTOTP(context.Context, *agentv1.SetupTOTPRequest) (*agentv1.SetupTOTPResponse, error) {
	return &agentv1.SetupTOTPResponse{Secret: "JBSWY3DPEHPK3PXP", URI: "otpauth://totp/Opsi:e2e"}, nil
}

func startManualParityTLSAgent(t *testing.T, unavailable *atomic.Bool) (string, func()) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "agent.test"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, BasicConstraintsValid: true,
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", manualParityAgentAddr)
	if err != nil {
		t.Fatal(err)
	}
	certificate := tls.Certificate{Certificate: [][]byte{certificateDER}, PrivateKey: privateKey}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS13})))
	agent := &manualParityAgent{localFacadeAgent: &localFacadeAgent{id: "node-1"}, unavailable: unavailable}
	agentv1.RegisterStatusServiceServer(server, agent)
	agentv1.RegisterSecretServiceServer(server, agent)
	agentv1.RegisterTelemetryServiceServer(server, agent)
	agentv1.RegisterIncidentServiceServer(server, agent)
	go func() { _ = server.Serve(listener) }()
	fingerprint := sha256.Sum256(certificateDER)
	return hex.EncodeToString(fingerprint[:]), server.Stop
}
