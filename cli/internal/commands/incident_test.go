package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cli/internal/keychain"
	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
	"google.golang.org/grpc"
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
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	agentv1.RegisterIncidentServiceServer(server, service)
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()

	configPath := filepath.Join(t.TempDir(), "cli.yaml")
	if err := os.WriteFile(configPath, []byte("agent_addr: "+listener.Addr().String()+"\ncloud_url: http://unused.invalid\n"), 0o600); err != nil {
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

	missing := NewRootCommand(Options{KeychainFactory: func() (keychain.Store, error) { return store, nil }})
	missing.SetOut(&out)
	missing.SetErr(&out)
	missing.SetArgs([]string{"incident", "evidence", "--project-id", "project-1", "--incident-id", "inc-1"})
	if err := missing.Execute(); err == nil || !strings.Contains(err.Error(), "selected CLI config") {
		t.Fatalf("missing selected config error=%v", err)
	}
	loopbackFallbackPath := filepath.Join(t.TempDir(), "missing-agent.yaml")
	if err := os.WriteFile(loopbackFallbackPath, []byte("cloud_url: http://unused.invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fallback := NewRootCommand(Options{KeychainFactory: func() (keychain.Store, error) { return store, nil }})
	fallback.SetOut(&out)
	fallback.SetErr(&out)
	fallback.SetArgs([]string{"--config", loopbackFallbackPath, "incident", "evidence", "--project-id", "project-1", "--incident-id", "inc-1"})
	if err := fallback.Execute(); err == nil || !strings.Contains(err.Error(), "explicitly set agent_addr") {
		t.Fatalf("implicit loopback fallback error=%v", err)
	}

	err = incidentEvidenceCLIError(errors.New("connect agent 127.0.0.1:1 pin-canary evidence-pat-canary raw-kubernetes-canary"))
	if err == nil || !strings.Contains(err.Error(), "INCIDENT_EVIDENCE_AGENT_UNAVAILABLE") || strings.Contains(err.Error(), "127.0.0.1:1") || strings.Contains(err.Error(), "pin-canary") || strings.Contains(err.Error(), "evidence-pat-canary") || strings.Contains(err.Error(), "raw-kubernetes-canary") {
		t.Fatalf("unsanitized unavailable Agent error=%v", err)
	}
}

func TestIncidentCommandsUseBearerMetadataWithoutCallerAuthority(t *testing.T) {
	service := &commandIncidentServer{t: t}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	agentv1.RegisterIncidentServiceServer(server, service)
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()

	configPath := filepath.Join(t.TempDir(), "cli.yaml")
	if err := os.WriteFile(configPath, []byte("agent_addr: "+listener.Addr().String()+"\n"), 0o600); err != nil {
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
