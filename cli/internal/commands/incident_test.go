package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opsi-dev/opsi/cli/internal/keychain"
	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func TestIncidentHelpContainsOnlyActiveCommands(t *testing.T) {
	cmd := NewRootCommand(Options{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"incident", "--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	help := out.String()
	for _, command := range []string{"list", "get", "resolve"} {
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
