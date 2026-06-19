package server

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/agent/internal/config"
	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestRunServesHealthAndStatus(t *testing.T) {
	cfg := config.Default()
	cfg.ListenAddr = freeAddr(t)
	cfg.HealthAddr = freeAddr(t)
	cfg.NodeID = "test-node"
	cfg.SQLitePath = t.TempDir() + "/agent.sqlite"
	cfg.Deployment.DryRun = true

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(ctx, cfg, "test-version", slog.Default())
	}()

	waitForHealth(t, "http://"+cfg.HealthAddr+"/health")

	dialCtx, dialCancel := context.WithTimeout(context.Background(), time.Second)
	defer dialCancel()
	conn, err := grpc.DialContext(dialCtx, cfg.ListenAddr, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	resp, err := agentv1.NewStatusServiceClient(conn).Status(context.Background(), &agentv1.StatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.NodeID != "test-node" || resp.Health != "ok" || resp.Version != "test-version" {
		t.Fatalf("unexpected status: %+v", resp)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not stop")
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

func waitForHealth(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("health endpoint did not become ready: %s", url)
}
