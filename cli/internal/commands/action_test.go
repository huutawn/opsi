package commands

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"

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

type cleanupFailStore struct {
	*keychain.FakeStore
	fail        bool
	failPrivate bool
}

func (s *cleanupFailStore) DeletePendingApproval(id string) error {
	if s.fail {
		return errors.New("delete failed")
	}
	return s.FakeStore.DeletePendingApproval(id)
}

func (s *cleanupFailStore) DeleteActionPrivateKey(id string) error {
	if s.failPrivate {
		return errors.New("backend path secret-marker")
	}
	return s.FakeStore.DeleteActionPrivateKey(id)
}

func TestActionDeviceRevokeReportsLocalCleanupAndRetries(t *testing.T) {
	var remoteCalls int
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remoteCalls++
		if r.URL.Path != "/api/projects/p1/action-devices/device-1/revoke" || r.Header.Get("Authorization") != "Bearer pat-canary" {
			t.Fatalf("request path=%s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"device":{"id":"device-1","status":"revoked"}}`)
	}))
	defer cloud.Close()
	configPath := filepath.Join(t.TempDir(), "cli.yaml")
	if err := os.WriteFile(configPath, []byte("agent_addr: 127.0.0.1:9443\ncloud_url: "+cloud.URL+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &cleanupFailStore{FakeStore: keychain.NewFakeStore(), failPrivate: true}
	if err := store.SetPAT("pat-canary"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetActionPrivateKey("device-1", []byte("private-key-canary")); err != nil {
		t.Fatal(err)
	}
	options := Options{KeychainFactory: func() (keychain.Store, error) { return store, nil }}
	run := func() (string, string, error) {
		command := NewRootCommand(options)
		var stdout, stderr bytes.Buffer
		command.SetOut(&stdout)
		command.SetErr(&stderr)
		command.SetArgs([]string{"--config", configPath, "action", "device", "revoke", "device-1", "--project-id", "p1"})
		err := command.Execute()
		return stdout.String(), stderr.String(), err
	}
	stdout, stderr, err := run()
	if !errors.Is(err, ErrActionSecureCleanupRequired) {
		t.Fatalf("error=%v stdout=%s stderr=%s", err, stdout, stderr)
	}
	var receipt map[string]any
	if json.Unmarshal([]byte(stdout), &receipt) != nil || len(receipt) != 3 || receipt["device_id"] != "device-1" || receipt["status"] != "revoked" || receipt["local_cleanup_required"] != true {
		t.Fatalf("receipt=%v stdout=%s", receipt, stdout)
	}
	for _, canary := range []string{"private-key-canary", "backend path", "secret-marker"} {
		if strings.Contains(stdout+stderr, canary) {
			t.Fatalf("cleanup leaked %q: stdout=%s stderr=%s", canary, stdout, stderr)
		}
	}
	if _, err := store.GetActionPrivateKey("device-1"); err != nil {
		t.Fatalf("failed cleanup removed key: %v", err)
	}
	store.failPrivate = false
	stdout, stderr, err = run()
	if err != nil || strings.Contains(stdout, "local_cleanup_required") || stderr != "" || remoteCalls != 2 {
		t.Fatalf("retry error=%v calls=%d stdout=%s stderr=%s", err, remoteCalls, stdout, stderr)
	}
	if _, err := store.GetActionPrivateKey("device-1"); !errors.Is(err, keychain.ErrActionSecretNotFound) {
		t.Fatalf("retry did not remove key: %v", err)
	}
}

func TestApprovalTargetDisplayIsLabeledBoundedAndControlFree(t *testing.T) {
	target := actionv1.TargetIdentity{ProjectID: "p1\x00hidden", EnvironmentID: "prod\nnext", RuntimeID: strings.Repeat("r", 200), NodeID: "node\t1", ServiceID: "svc\r1"}
	output := formatApprovalTarget(target)
	if strings.IndexFunc(output, unicode.IsControl) >= 0 {
		t.Fatalf("control character in target output: %q", output)
	}
	for _, field := range []string{"project=", "environment=", "runtime=", "node=", "service="} {
		if !strings.Contains(output, field) {
			t.Fatalf("missing %s in %q", field, output)
		}
	}
	if strings.Contains(output, "\x00") || len([]rune(strings.Split(output, "runtime=")[1])) > 180 {
		t.Fatalf("target output is unsafe or unbounded: %q", output)
	}
}

func TestActionExecuteReportsFactualResultWhenSecureCleanupFailsAndRetries(t *testing.T) {
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
	store := &cleanupFailStore{FakeStore: keychain.NewFakeStore(), fail: true}
	if err := store.SetPAT("pat"); err != nil {
		t.Fatal(err)
	}
	grant := actionv1.ApprovalGrant{SchemaVersion: actionv1.SchemaVersion, ChallengeID: "challenge-1", ActionID: "action-1", ProjectID: "p1", DeviceID: "device-1"}
	body, err := json.Marshal(grant)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetPendingApproval("challenge-1", body); err != nil {
		t.Fatal(err)
	}
	options := Options{KeychainFactory: func() (keychain.Store, error) { return store, nil }}

	first := NewRootCommand(options)
	var output bytes.Buffer
	first.SetOut(&output)
	first.SetErr(&output)
	first.SetArgs([]string{"--config", configPath, "action", "execute", "challenge-1", "--project-id", "p1"})
	if err := first.Execute(); !errors.Is(err, ErrActionSecureCleanupRequired) {
		t.Fatalf("cleanup error=%v output=%s", err, output.String())
	}
	var receipt map[string]any
	if err := json.Unmarshal(output.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if len(receipt) != 4 || receipt["status"] != string(actionv1.StatusSucceeded) || receipt["action_id"] != "action-1" || receipt["challenge_id"] != "challenge-1" || receipt["secure_cleanup_required"] != true {
		t.Fatalf("cleanup receipt=%v", receipt)
	}
	if strings.Contains(output.String(), "signature") || strings.Contains(output.String(), "device_id") || strings.Contains(output.String(), "project_id") {
		t.Fatalf("cleanup receipt leaked grant fields: %s", output.String())
	}

	store.fail = false
	second := NewRootCommand(options)
	output.Reset()
	second.SetOut(&output)
	second.SetErr(&output)
	second.SetArgs([]string{"--config", configPath, "action", "execute", "challenge-1", "--project-id", "p1"})
	if err := second.Execute(); err != nil {
		t.Fatal(err)
	}
	if service.executeRPCs != 2 || service.executorCalls != 1 {
		t.Fatalf("rpc=%d executor=%d", service.executeRPCs, service.executorCalls)
	}
	if _, err := store.GetPendingApproval("challenge-1"); !errors.Is(err, keychain.ErrActionSecretNotFound) {
		t.Fatalf("pending grant remains: %v", err)
	}
}

type commandActionServer struct {
	actionv1.UnimplementedActionServiceServer
	authorization string
	request       *actionv1.ExecuteRequest
	executeRPCs   int
	executorCalls int
}

func (s *commandActionServer) GetChallenge(ctx context.Context, _ *actionv1.ChallengeRequest) (*actionv1.ApprovalChallenge, error) {
	s.capture(ctx)
	now := time.Now().UTC()
	return &actionv1.ApprovalChallenge{SchemaVersion: actionv1.SchemaVersion, ID: "challenge-1", ActionID: "action-1", ProjectID: "p1", PlanHash: strings.Repeat("a", 64), StateHash: strings.Repeat("b", 64), Nonce: "nonce-1", Risk: actionv1.RiskR2, Target: actionv1.TargetIdentity{ProjectID: "p1", NodeID: "n1", ServiceID: "s1"}, Summary: "restart owned workload", ConfirmationPhrase: "APPROVE action-1", IssuedAt: now, ExpiresAt: now.Add(time.Minute)}, nil
}

func (s *commandActionServer) Execute(ctx context.Context, request *actionv1.ExecuteRequest) (*actionv1.ActionResult, error) {
	s.capture(ctx)
	s.request = request
	s.executeRPCs++
	if s.executeRPCs == 1 {
		s.executorCalls++
	}
	return &actionv1.ActionResult{SchemaVersion: actionv1.SchemaVersion, ActionID: "action-1", ChallengeID: "challenge-1", ProjectID: "p1", Status: actionv1.StatusSucceeded, FinishedAt: time.Now().UTC()}, nil
}

func (s *commandActionServer) capture(ctx context.Context) {
	values, _ := metadata.FromIncomingContext(ctx)
	if authorization := values.Get("authorization"); len(authorization) > 0 {
		s.authorization = authorization[0]
	}
}
