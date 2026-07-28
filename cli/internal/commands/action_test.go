package commands

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cli/internal/actionapproval"
	"github.com/opsi-dev/opsi/cli/internal/keychain"
	actionv1 "github.com/opsi-dev/opsi/contracts/go/actionv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func TestActionApprovalRejectsNonTTYAndAutomationFlags(t *testing.T) {
	command := NewRootCommand(Options{IsTerminal: func(io.Reader) bool { return false }})
	command.SetArgs([]string{"action", "approve", "challenge-1", "--project-id", "p1", "--device-id", "device-1"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "interactive TTY") {
		t.Fatalf("non-TTY error=%v", err)
	}
	var output bytes.Buffer
	help := NewRootCommand(Options{})
	help.SetOut(&output)
	help.SetArgs([]string{"action", "--help"})
	if err := help.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"--yes", "--auto-approve", "--grant"} {
		if strings.Contains(output.String(), forbidden) {
			t.Fatalf("unsafe action flag %q in help: %s", forbidden, output.String())
		}
	}
}

func TestActionApproveAndExecuteUseSecureStoreWithoutGrantOutput(t *testing.T) {
	service := &commandActionServer{}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	actionv1.RegisterActionServiceServer(server, service)
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()

	configPath := filepath.Join(t.TempDir(), "cli.yaml")
	if err := os.WriteFile(configPath, []byte("agent_addr: "+listener.Addr().String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := keychain.NewFakeStore()
	if err := store.SetPAT("action-pat-canary"); err != nil {
		t.Fatal(err)
	}
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	secure := actionapproval.Store{Backend: store}
	if err := secure.SavePrivateKey("device-1", privateKey); err != nil {
		t.Fatal(err)
	}
	options := Options{KeychainFactory: func() (keychain.Store, error) { return store, nil }, IsTerminal: func(io.Reader) bool { return true }}

	approve := NewRootCommand(options)
	var approved bytes.Buffer
	approve.SetOut(&approved)
	approve.SetErr(&approved)
	approve.SetIn(strings.NewReader("APPROVE action-1\n"))
	approve.SetArgs([]string{"--config", configPath, "action", "approve", "challenge-1", "--project-id", "p1", "--device-id", "device-1"})
	if err := approve.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(approved.String(), "action-pat-canary") || strings.Contains(approved.String(), "signature") || strings.Contains(approved.String(), string(privateKey)) {
		t.Fatalf("approval leaked protected material: %s", approved.String())
	}

	execute := NewRootCommand(options)
	var executed bytes.Buffer
	execute.SetOut(&executed)
	execute.SetErr(&executed)
	execute.SetArgs([]string{"--config", configPath, "action", "execute", "challenge-1", "--project-id", "p1"})
	if err := execute.Execute(); err != nil {
		t.Fatal(err)
	}
	if service.authorization != "Bearer action-pat-canary" || service.request == nil || len(service.request.Grant.Signature) != ed25519.SignatureSize {
		t.Fatalf("authorization=%q request=%#v", service.authorization, service.request)
	}
	if strings.Contains(executed.String(), "action-pat-canary") || strings.Contains(executed.String(), "signature") || strings.Contains(executed.String(), string(privateKey)) {
		t.Fatalf("execution leaked protected material: %s", executed.String())
	}
	if _, err := secure.PendingGrant("challenge-1"); !errors.Is(err, keychain.ErrActionSecretNotFound) {
		t.Fatalf("pending approval was not deleted: %v", err)
	}
}

type commandActionServer struct {
	actionv1.UnimplementedActionServiceServer
	authorization string
	request       *actionv1.ExecuteRequest
}

func (s *commandActionServer) GetChallenge(ctx context.Context, _ *actionv1.ChallengeRequest) (*actionv1.ApprovalChallenge, error) {
	s.capture(ctx)
	now := time.Now().UTC()
	return &actionv1.ApprovalChallenge{SchemaVersion: actionv1.SchemaVersion, ID: "challenge-1", ActionID: "action-1", ProjectID: "p1", PlanHash: strings.Repeat("a", 64), StateHash: strings.Repeat("b", 64), Nonce: "nonce-1", Risk: actionv1.RiskR2, Target: actionv1.TargetIdentity{ProjectID: "p1", NodeID: "n1", ServiceID: "s1"}, Summary: "restart owned workload", ConfirmationPhrase: "APPROVE action-1", IssuedAt: now, ExpiresAt: now.Add(time.Minute)}, nil
}

func (s *commandActionServer) Execute(ctx context.Context, request *actionv1.ExecuteRequest) (*actionv1.ActionResult, error) {
	s.capture(ctx)
	s.request = request
	return &actionv1.ActionResult{SchemaVersion: actionv1.SchemaVersion, ActionID: "action-1", ChallengeID: "challenge-1", ProjectID: "p1", Status: actionv1.StatusSucceeded, FinishedAt: time.Now().UTC()}, nil
}

func (s *commandActionServer) capture(ctx context.Context) {
	values, _ := metadata.FromIncomingContext(ctx)
	if authorization := values.Get("authorization"); len(authorization) > 0 {
		s.authorization = authorization[0]
	}
}
