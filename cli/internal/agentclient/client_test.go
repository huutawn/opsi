package agentclient

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cli/internal/config"
	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
	"google.golang.org/grpc"
)

func TestNormalizeFingerprint(t *testing.T) {
	got := normalizeFingerprint("AA:BB:CC")
	if got != "aabbcc" {
		t.Fatalf("unexpected fingerprint %q", got)
	}
}

func TestStatusInsecure(t *testing.T) {
	addr, stop := startStatusServer(t)
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	status, err := New(config.Config{AgentAddr: addr}).Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.NodeID != "fake-node" || status.Health != "ok" {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestTransportCredentialsRejectsMissingCA(t *testing.T) {
	_, err := transportCredentials(config.Config{
		AgentAddr: "127.0.0.1:9443",
		TLS: config.TLSConfig{
			CACertPath: "missing-ca.crt",
		},
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

type fakeStatusServer struct{}

func (fakeStatusServer) Status(context.Context, *agentv1.StatusRequest) (*agentv1.StatusResponse, error) {
	return &agentv1.StatusResponse{NodeID: "fake-node", Health: "ok", Version: "test"}, nil
}

func startStatusServer(t *testing.T) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	agentv1.RegisterStatusServiceServer(server, fakeStatusServer{})
	go func() { _ = server.Serve(listener) }()
	return listener.Addr().String(), server.Stop
}
