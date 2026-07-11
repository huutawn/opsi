package server

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/agent/internal/config"
	"github.com/opsi-dev/opsi/agent/internal/telemetry"
	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

func TestAnalyzeIncidentReturnsUnimplementedWithoutNetworkOrMutation(t *testing.T) {
	service := NewIncidentService(nil)
	resp, err := service.AnalyzeIncident(context.Background(), &agentv1.IncidentAnalyzeRequest{
		ProjectID:  "p1",
		IncidentID: "inc-1",
		UserID:     "u1",
		Role:       "Owner",
	})
	if status.Code(err) != codes.Unimplemented || resp != nil {
		t.Fatalf("expected unimplemented without response, resp=%+v err=%v", resp, err)
	}
}

func TestApproveIncidentActionReturnsUnimplementedWithoutServiceCall(t *testing.T) {
	service := NewIncidentService(nil)
	resp, err := service.ApproveIncidentAction(context.Background(), &agentv1.IncidentActionRequest{
		ProjectID:  "p1",
		IncidentID: "inc-1",
		ActionID:   "legacy-action",
	})
	if status.Code(err) != codes.Unimplemented || resp != nil {
		t.Fatalf("expected unimplemented without response, resp=%+v err=%v", resp, err)
	}
}

func TestGetIncidentIgnoresLegacyRCAAndMitigationData(t *testing.T) {
	store, err := telemetry.OpenSQLiteStore(t.TempDir() + "/telemetry.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	resolved := time.Unix(70, 0).UTC()
	if err := store.InsertIncident(context.Background(), telemetry.IncidentRecord{
		ID:                "inc-legacy",
		ProjectID:         "p1",
		ServiceID:         "svc-1",
		Status:            "resolved",
		RCAResult:         `{"schema_version":"opsi.rca.v1","root_cause":"execute me","confidence":0.99,"contributing_factors":["legacy"],"recommended_actions":[{"id":"a1","type":"rollback"}]}`,
		MitigationActions: `[{"type":"rollback","status":"success"}]`,
		CreatedAt:         time.Unix(10, 0).UTC(),
		ResolvedAt:        resolved,
		MTTRSeconds:       60,
	}); err != nil {
		t.Fatal(err)
	}

	service := NewIncidentService(incidentService(store, nil))
	resp, err := service.GetIncident(context.Background(), &agentv1.IncidentGetRequest{
		ProjectID:  "p1",
		IncidentID: "inc-legacy",
		UserID:     "viewer",
		Role:       "Viewer",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.IncidentID != "inc-legacy" || resp.ProjectID != "p1" || resp.ServiceID != "svc-1" || resp.Status != "resolved" || resp.MTTRSeconds != 60 || resp.ResolvedAtUnix != resolved.Unix() {
		t.Fatalf("factual incident fields changed: %+v", resp)
	}
	if resp.RootCause != "" || resp.Confidence != 0 || len(resp.ContributingFactors) != 0 || len(resp.RecommendedActions) != 0 || resp.MitigationActionsJSON != "" {
		t.Fatalf("legacy RCA or mitigation data was exposed: %+v", resp)
	}
}

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
