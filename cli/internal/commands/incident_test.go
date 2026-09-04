package commands

import (
	"bytes"
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
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cli/internal/keychain"
	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
)
func TestIncidentHelpContainsOnlyActiveCommands(t *testing.T) {
	if incidentEvidenceOperationTimeout != 30*time.Second {
		t.Fatal("incident evidence operation timeout changed")
	}
	cmd := NewRootCommand(Options{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"incident", "--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	help := out.String()
	for _, command := range []string{"list", "get", "evidence", "resolve"} {
		if !strings.Contains(help, command) {
			t.Fatalf("incident help missing %q: %s", command, help)
		}
	}
	for _, removed := range []string{"analyze", "approve", "RCA", "recommended action"} {
		if strings.Contains(help, removed) {
			t.Fatalf("incident help contains removed surface %q: %s", removed, help)
		}
	}
}

func TestIncidentEvidenceCommandUsesSelectedAgentAndDoesNotLeakPAT(t *testing.T) {
	service := &commandIncidentServer{t: t}
	agentAddr, pin, stop := startTLSIncidentAgent(t, service)
	defer stop()
	host, portStr, _ := net.SplitHostPort(agentAddr)
	port, _ := strconv.Atoi(portStr)

	nodeCount := 1
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		nodes := []map[string]any{
			{
				"id":                    "node-1",
				"agent_id":              "agent-1",
				"agent_endpoint":        host,
				"agent_port":            port,
				"agent_tls_server_name": "127.0.0.1",
				"agent_cert_sha256":     pin,
				"status":                "ready",
			},
		}
		if nodeCount > 1 {
			nodes = append(nodes, map[string]any{
				"id":                    "node-2",
				"agent_id":              "agent-2",
				"agent_endpoint":        host,
				"agent_port":            port,
				"agent_tls_server_name": "127.0.0.1",
				"agent_cert_sha256":     pin,
				"status":                "ready",
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"nodes": nodes})
	}))
	defer cloud.Close()

	configPath := filepath.Join(t.TempDir(), "cli.yaml")
	// Stale local AgentAddr and stale pin in config to prove Cloud discovery authority
	staleConfig := fmt.Sprintf("agent_addr: 127.0.0.1:9\ncloud_url: %s\ntls:\n  pinned_server_cert_sha256: %s\n", cloud.URL, strings.Repeat("0", 64))
	if err := os.WriteFile(configPath, []byte(staleConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	store := keychain.NewFakeStore()
	if err := store.SetPAT("evidence-pat-canary"); err != nil {
		t.Fatal(err)
	}
	cmd := NewRootCommand(Options{KeychainFactory: func() (keychain.Store, error) { return store, nil }})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--config", configPath, "incident", "evidence", "--project-id", "project-1", "--incident-id", "inc-1", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if service.authorization != "Bearer evidence-pat-canary" || service.evidenceRequest.ProjectID != "project-1" || service.evidenceRequest.IncidentID != "inc-1" || strings.Contains(out.String(), "evidence-pat-canary") {
		t.Fatalf("authorization=%q request=%+v output=%q", service.authorization, service.evidenceRequest, out.String())
	}

	// Ambiguous target rejection when multiple agents exist without --node-id
	nodeCount = 2
	ambigCmd := NewRootCommand(Options{KeychainFactory: func() (keychain.Store, error) { return store, nil }})
	ambigCmd.SetOut(&out)
	ambigCmd.SetErr(&out)
	ambigCmd.SetArgs([]string{"--config", configPath, "incident", "evidence", "--project-id", "project-1", "--incident-id", "inc-1"})
	if err := ambigCmd.Execute(); err == nil || !strings.Contains(err.Error(), "node-id is required") {
		t.Fatalf("expected node-id required error on multiple agents, got: %v", err)
	}

	// Succeeds when --node-id is specified
	scopedCmd := NewRootCommand(Options{KeychainFactory: func() (keychain.Store, error) { return store, nil }})
	out.Reset()
	scopedCmd.SetOut(&out)
	scopedCmd.SetErr(&out)
	scopedCmd.SetArgs([]string{"--config", configPath, "incident", "evidence", "--project-id", "project-1", "--incident-id", "inc-1", "--node-id", "node-1", "--json"})
	if err := scopedCmd.Execute(); err != nil {
		t.Fatalf("scoped evidence failed: %v", err)
	}

	missing := NewRootCommand(Options{KeychainFactory: func() (keychain.Store, error) { return store, nil }})
	missing.SetOut(&out)
	missing.SetErr(&out)
	missing.SetArgs([]string{"incident", "evidence", "--project-id", "project-1", "--incident-id", "inc-1"})
	if err := missing.Execute(); err == nil || !strings.Contains(err.Error(), "selected CLI config") {
		t.Fatalf("missing selected config error=%v", err)
	}

	err := incidentEvidenceCLIError(errors.New("connect agent 127.0.0.1:1 pin-canary evidence-pat-canary raw-kubernetes-canary"))
	if err == nil || !strings.Contains(err.Error(), "INCIDENT_EVIDENCE_AGENT_UNAVAILABLE") || strings.Contains(err.Error(), "127.0.0.1:1") || strings.Contains(err.Error(), "pin-canary") || strings.Contains(err.Error(), "evidence-pat-canary") || strings.Contains(err.Error(), "raw-kubernetes-canary") {
		t.Fatalf("unsanitized unavailable Agent error=%v", err)
	}
}

func TestIncidentCommandsUseBearerMetadataWithoutCallerAuthority(t *testing.T) {
	service := &commandIncidentServer{t: t}
	agentAddr, pin, stop := startTLSIncidentAgent(t, service)
	defer stop()
	host, portStr, _ := net.SplitHostPort(agentAddr)
	port, _ := strconv.Atoi(portStr)

	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"nodes": []map[string]any{
				{
					"id":                    "node-1",
					"agent_id":              "agent-1",
					"agent_endpoint":        host,
					"agent_port":            port,
					"agent_tls_server_name": "127.0.0.1",
					"agent_cert_sha256":     pin,
					"status":                "ready",
				},
			},
		})
	}))
	defer cloud.Close()

	configPath := filepath.Join(t.TempDir(), "cli.yaml")
	if err := os.WriteFile(configPath, []byte("cloud_url: "+cloud.URL+"\nagent_addr: 127.0.0.1:9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := keychain.NewFakeStore()
	if err := store.SetPAT("incident-pat-canary"); err != nil {
		t.Fatal(err)
	}
	cmd := NewRootCommand(Options{KeychainFactory: func() (keychain.Store, error) { return store, nil }})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--config", configPath, "incident", "list", "--project-id", "project-1"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if service.authorization != "Bearer incident-pat-canary" || strings.Contains(service.serializedRequest, "incident-pat-canary") || strings.Contains(out.String(), "incident-pat-canary") {
		t.Fatalf("authorization=%q request=%s output=%q", service.authorization, service.serializedRequest, out.String())
	}
	help := NewRootCommand(Options{})
	out.Reset()
	help.SetOut(&out)
	help.SetErr(&out)
	help.SetArgs([]string{"incident", "list", "--help"})
	if err := help.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "--user-id") || strings.Contains(out.String(), "--role") {
		t.Fatalf("incident help exposes caller authority flags: %s", out.String())
	}
}

type commandIncidentServer struct {
	agentv1.UnimplementedIncidentServiceServer
	t                 *testing.T
	authorization     string
	serializedRequest string
	evidenceRequest   agentv1.IncidentGetRequest
}

func (s *commandIncidentServer) GetIncidentEvidence(ctx context.Context, req *agentv1.IncidentGetRequest) (*agentv1.IncidentEvidence, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	if values := md.Get("authorization"); len(values) > 0 {
		s.authorization = values[0]
	}
	s.evidenceRequest = *req
	return &agentv1.IncidentEvidence{SchemaVersion: "opsi.incident_evidence.v1", Identity: agentv1.IncidentEvidenceIdentity{IncidentID: req.IncidentID, ProjectID: req.ProjectID}, ContentSHA256: strings.Repeat("a", 64)}, nil
}

func (s *commandIncidentServer) ListIncidents(ctx context.Context, req *agentv1.IncidentListRequest) (*agentv1.IncidentListResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	if values := md.Get("authorization"); len(values) > 0 {
		s.authorization = values[0]
	}
	data, err := json.Marshal(req)
	if err != nil {
		s.t.Fatal(err)
	}
	s.serializedRequest = string(data)
	return &agentv1.IncidentListResponse{}, nil
}

func startTLSIncidentAgent(t *testing.T, service agentv1.IncidentServiceServer) (string, string, func()) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "agent.test"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Minute),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate := tls.Certificate{Certificate: [][]byte{certificateDER}, PrivateKey: privateKey}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS13})))
	agentv1.RegisterIncidentServiceServer(server, service)
	go func() { _ = server.Serve(listener) }()
	fingerprint := sha256.Sum256(certificateDER)
	return listener.Addr().String(), hex.EncodeToString(fingerprint[:]), server.Stop
}
