package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/agent/internal/cloudrunner"
	"github.com/opsi-dev/opsi/agent/internal/config"
	"github.com/opsi-dev/opsi/agent/internal/incident"
	"github.com/opsi-dev/opsi/agent/internal/secret"
	"github.com/opsi-dev/opsi/agent/internal/telemetry"
	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	grpcstatus "google.golang.org/grpc/status"
)

func TestIncidentServiceDescriptorContainsOnlyActiveIncidentRPCs(t *testing.T) {
	want := map[string]bool{"ListIncidents": true, "GetIncident": true, "ResolveIncident": true}
	if len(agentv1.IncidentService_ServiceDesc.Methods) != len(want) {
		t.Fatalf("unexpected incident RPC count: %+v", agentv1.IncidentService_ServiceDesc.Methods)
	}
	for _, method := range agentv1.IncidentService_ServiceDesc.Methods {
		if !want[method.MethodName] {
			t.Fatalf("unexpected incident RPC %q", method.MethodName)
		}
		delete(want, method.MethodName)
	}
	if len(want) != 0 {
		t.Fatalf("missing active incident RPCs: %v", want)
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
		RCAResult:         `{"legacy":"ignored"}`,
		MitigationActions: `[{"type":"rollback","status":"success"}]`,
		CreatedAt:         time.Unix(10, 0).UTC(),
		ResolvedAt:        resolved,
		MTTRSeconds:       60,
	}); err != nil {
		t.Fatal(err)
	}

	service := NewIncidentService(incidentService(store), &fixedAuthVerifier{auth: secret.AuthContext{ProjectID: "p1", UserID: "viewer", Role: secret.RoleViewer}})
	resp, err := service.GetIncident(incomingBearer("pat-viewer"), &agentv1.IncidentGetRequest{
		ProjectID:  "p1",
		IncidentID: "inc-legacy",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.IncidentID != "inc-legacy" || resp.ProjectID != "p1" || resp.ServiceID != "svc-1" || resp.Status != "resolved" || resp.MTTRSeconds != 60 || resp.ResolvedAtUnix != resolved.Unix() {
		t.Fatalf("factual incident fields changed: %+v", resp)
	}
}

func TestRunServesHealthAndStatus(t *testing.T) {
	cfg := config.Default()
	cfg.ListenAddr = freeAddr(t)
	cfg.HealthAddr = freeAddr(t)
	cfg.NodeID = "test-node"
	cfg.SQLitePath = t.TempDir() + "/agent.sqlite"
	cfg.Deployment.DryRun = true
	cfg.Telemetry.KubectlPath = t.TempDir() + "/missing-kubectl"

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
	if resp.NodeID != "test-node" || resp.Health != "unavailable" || resp.Version != "test-version" {
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

func TestStatusAndHealthExposeCloudConnectionState(t *testing.T) {
	connected := true
	cfg := config.Default()
	cfg.NodeID = "node-1"
	status := NewStatusService("v1", time.Unix(10, 0), cfg, func() bool { return connected }, staticServerHealthProbe{health: cloudrunner.RuntimeHealth{NodeReady: true, K3SStatus: cloudrunner.K3SStatusReady}})
	response, err := status.Status(context.Background(), &agentv1.StatusRequest{})
	if err != nil || !response.CloudConnected {
		t.Fatalf("status=%+v err=%v", response, err)
	}
	httpResponse := httptest.NewRecorder()
	healthHandler("v1", time.Unix(10, 0), cfg, func() bool { return connected }).ServeHTTP(httpResponse, httptest.NewRequest(http.MethodGet, "/health", nil))
	var body map[string]any
	if err := json.Unmarshal(httpResponse.Body.Bytes(), &body); err != nil || body["cloud_connected"] != true {
		t.Fatalf("health body=%s err=%v", httpResponse.Body.String(), err)
	}
	connected = false
	response, _ = status.Status(context.Background(), &agentv1.StatusRequest{})
	if response.CloudConnected {
		t.Fatal("status did not reflect disconnected Cloud state")
	}
}

func TestStatusReflectsRuntimeProbe(t *testing.T) {
	cfg := config.Default()
	tests := []struct {
		name   string
		probe  cloudrunner.HealthProbe
		health string
	}{
		{name: "ready", probe: staticServerHealthProbe{health: cloudrunner.RuntimeHealth{NodeReady: true, K3SStatus: cloudrunner.K3SStatusReady}}, health: "ok"},
		{name: "not ready", probe: staticServerHealthProbe{health: cloudrunner.RuntimeHealth{K3SStatus: cloudrunner.K3SStatusNotReady}}, health: "degraded"},
		{name: "unavailable", probe: staticServerHealthProbe{err: errors.New("unavailable")}, health: "unavailable"},
		{name: "missing", health: "unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response, err := NewStatusService("v1", time.Now(), cfg, func() bool { return true }, tt.probe).Status(context.Background(), &agentv1.StatusRequest{})
			if err != nil || response.Health != tt.health || !response.CloudConnected {
				t.Fatalf("response=%+v err=%v", response, err)
			}
		})
	}
}

func TestIncidentServerUsesVerifiedIdentityAndRejectsCallerEscalation(t *testing.T) {
	store := &recordingIncidentStore{record: telemetry.IncidentRecord{ID: "inc-1", ProjectID: "project-1", Status: "open"}}
	audit := &recordingAuditSink{}
	verifier := &fixedAuthVerifier{auth: secret.AuthContext{ProjectID: "project-1", UserID: "verified-viewer", Role: secret.RoleViewer}}
	service := NewIncidentService(&incident.Service{Store: store, Audit: audit}, verifier)

	_, err := service.ResolveIncident(incomingBearer("pat-viewer"), &agentv1.IncidentResolveRequest{ProjectID: "project-1", IncidentID: "inc-1"})
	if grpcstatus.Code(err) != codes.PermissionDenied || store.resolveCalls != 0 {
		t.Fatalf("viewer escalation err=%v resolve_calls=%d", err, store.resolveCalls)
	}

	verifier.auth = secret.AuthContext{ProjectID: "project-1", UserID: "verified-owner", Role: secret.RoleOwner}
	response, err := service.ResolveIncident(incomingBearer("pat-owner"), &agentv1.IncidentResolveRequest{ProjectID: "project-1", IncidentID: "inc-1"})
	if err != nil || response.Status != incident.StatusResolved || store.resolveCalls != 1 {
		t.Fatalf("response=%+v err=%v resolve_calls=%d", response, err, store.resolveCalls)
	}
	if audit.last.Actor != "verified-owner" || verifier.last.PAT != "pat-owner" {
		t.Fatalf("audit=%+v verifier_input=%+v", audit.last, verifier.last)
	}
}

func TestSecretAndIncidentRPCsRequireAuthorizationBearer(t *testing.T) {
	verifier := &fixedAuthVerifier{auth: secret.AuthContext{ProjectID: "project-1", UserID: "owner", Role: secret.RoleOwner}}
	secretService := NewSecretService(config.Default(), &secret.Service{}, verifier)
	incidentService := NewIncidentService(&incident.Service{Store: &recordingIncidentStore{}}, verifier)

	if _, err := secretService.SetupTOTP(context.Background(), &agentv1.SetupTOTPRequest{ProjectID: "project-1"}); grpcstatus.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing secret Bearer err=%v", err)
	}
	legacy := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-opsi-pat", "legacy-pat"))
	if _, err := incidentService.ListIncidents(legacy, &agentv1.IncidentListRequest{ProjectID: "project-1"}); grpcstatus.Code(err) != codes.Unauthenticated {
		t.Fatalf("legacy metadata err=%v", err)
	}
	invalid := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Basic caller"))
	if _, err := incidentService.ListIncidents(invalid, &agentv1.IncidentListRequest{ProjectID: "project-1"}); grpcstatus.Code(err) != codes.Unauthenticated {
		t.Fatalf("invalid authorization err=%v", err)
	}
}

type fixedAuthVerifier struct {
	auth secret.AuthContext
	err  error
	last secret.AuthContext
}

func (v *fixedAuthVerifier) VerifyAuth(_ context.Context, auth secret.AuthContext) (secret.AuthContext, error) {
	v.last = auth
	if v.err != nil {
		return secret.AuthContext{}, v.err
	}
	verified := v.auth
	verified.PAT = auth.PAT
	return verified, nil
}

type staticServerHealthProbe struct {
	health cloudrunner.RuntimeHealth
	err    error
}

func (p staticServerHealthProbe) Probe(context.Context) (cloudrunner.RuntimeHealth, error) {
	return p.health, p.err
}

type recordingIncidentStore struct {
	record       telemetry.IncidentRecord
	resolveCalls int
}

func (s *recordingIncidentStore) GetIncident(context.Context, string, string) (*telemetry.IncidentRecord, error) {
	copy := s.record
	return &copy, nil
}

func (s *recordingIncidentStore) ListIncidents(context.Context, string, string, int) ([]telemetry.IncidentRecord, error) {
	return []telemetry.IncidentRecord{s.record}, nil
}

func (s *recordingIncidentStore) ResolveIncident(_ context.Context, projectID, incidentID string, resolved time.Time) (*telemetry.IncidentRecord, error) {
	s.resolveCalls++
	s.record.ProjectID = projectID
	s.record.ID = incidentID
	s.record.Status = incident.StatusResolved
	s.record.ResolvedAt = resolved
	copy := s.record
	return &copy, nil
}

type recordingAuditSink struct {
	last secret.AuditRecord
}

func (s *recordingAuditSink) InsertAudit(_ context.Context, record secret.AuditRecord) error {
	s.last = record
	return nil
}

func incomingBearer(token string) context.Context {
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token))
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
