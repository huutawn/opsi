package commands

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/opsi-dev/opsi/cli/internal/keychain"
	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
	"google.golang.org/grpc"
)

func TestSyncCommandPersistsAndUsesState(t *testing.T) {
	server := &commandTelemetryServer{}
	addr, stop := startCommandTelemetryServer(t, server)
	defer stop()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "cli.yaml")
	statePath := filepath.Join(dir, "sync-state.json")
	if err := os.WriteFile(configPath, []byte("agent_addr: "+addr+"\nsync_state_path: "+statePath+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := NewRootCommand(Options{KeychainFactory: func() (keychain.Store, error) {
		return keychain.NewFakeStore(), nil
	}})
	cmd.SetOut(bytes.NewBuffer(nil))
	cmd.SetArgs([]string{"--config", configPath, "sync", "--project-id", "proj-dev"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	stored, err := readSyncState(statePath, "proj-dev")
	if err != nil {
		t.Fatal(err)
	}
	if stored != 1234 {
		t.Fatalf("expected stored timestamp 1234, got %d", stored)
	}

	cmd = NewRootCommand(Options{KeychainFactory: func() (keychain.Store, error) {
		return keychain.NewFakeStore(), nil
	}})
	cmd.SetOut(bytes.NewBuffer(nil))
	cmd.SetArgs([]string{"--config", configPath, "sync", "--project-id", "proj-dev"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if server.lastReceivedUnix != 1234 {
		t.Fatalf("expected sync to resume from state, got %d", server.lastReceivedUnix)
	}

	cmd = NewRootCommand(Options{KeychainFactory: func() (keychain.Store, error) {
		return keychain.NewFakeStore(), nil
	}})
	cmd.SetOut(bytes.NewBuffer(nil))
	cmd.SetArgs([]string{"--config", configPath, "sync", "--project-id", "proj-dev", "--since-unix", "0"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if server.lastReceivedUnix != 0 {
		t.Fatalf("expected explicit since-unix to override state, got %d", server.lastReceivedUnix)
	}
}

type commandTelemetryServer struct {
	agentv1.UnimplementedTelemetryServiceServer
	lastReceivedUnix int64
}

func (s *commandTelemetryServer) Sync(req *agentv1.SyncRequest, stream agentv1.TelemetryService_SyncServer) error {
	s.lastReceivedUnix = req.LastReceivedUnix
	return stream.Send(&agentv1.SyncChunk{ProjectID: req.ProjectID, StartUnix: req.LastReceivedUnix, EndUnix: 1234, Compression: "zstd", Done: true})
}

func startCommandTelemetryServer(t *testing.T, service agentv1.TelemetryServiceServer) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	agentv1.RegisterTelemetryServiceServer(server, service)
	go func() { _ = server.Serve(listener) }()
	return listener.Addr().String(), server.Stop
}

func TestReadSyncStateMissingFile(t *testing.T) {
	lastReceived, err := readSyncState(filepath.Join(t.TempDir(), "missing.json"), "proj-dev")
	if err != nil {
		t.Fatal(err)
	}
	if lastReceived != 0 {
		t.Fatalf("expected zero state, got %d", lastReceived)
	}
}

func TestWriteSyncStateKeepsOtherProjects(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync-state.json")
	if err := writeSyncState(path, "proj-a", 10); err != nil {
		t.Fatal(err)
	}
	if err := writeSyncState(path, "proj-b", 20); err != nil {
		t.Fatal(err)
	}
	projA, err := readSyncState(path, "proj-a")
	if err != nil {
		t.Fatal(err)
	}
	projB, err := readSyncState(path, "proj-b")
	if err != nil {
		t.Fatal(err)
	}
	if projA != 10 || projB != 20 {
		t.Fatalf("unexpected states: proj-a=%d proj-b=%d", projA, projB)
	}
}
