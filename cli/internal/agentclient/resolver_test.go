package agentclient

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cli/internal/cloudclient"
	"github.com/opsi-dev/opsi/cli/internal/config"
	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

type mockNodeLister struct {
	nodes []cloudclient.Node
	err   error
}

func (m *mockNodeLister) ListNodes(_ context.Context, _ string) ([]cloudclient.Node, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.nodes, nil
}

type mockTelemetryClient struct {
	statusFunc func(ctx context.Context) (*agentv1.StatusResponse, error)
	queryFunc  func(ctx context.Context, req *agentv1.TelemetryQueryRequest) (*agentv1.TelemetryQueryResponse, error)
}

func (m *mockTelemetryClient) Status(ctx context.Context) (*agentv1.StatusResponse, error) {
	if m.statusFunc != nil {
		return m.statusFunc(ctx)
	}
	return &agentv1.StatusResponse{Version: "v1.0.0"}, nil
}

func (m *mockTelemetryClient) QueryTelemetry(ctx context.Context, req *agentv1.TelemetryQueryRequest) (*agentv1.TelemetryQueryResponse, error) {
	if m.queryFunc != nil {
		return m.queryFunc(ctx, req)
	}
	return &agentv1.TelemetryQueryResponse{
		ProjectID: req.ProjectID,
		Source:    "agent",
		Summary: &agentv1.TelemetryRuntimeSummary{
			MetricCount: 1,
			LogCount:    1,
			Health:      "healthy",
		},
	}, nil
}

func TestResolverDiscoveryFiltersInvalidNodesAndBuildsTLSPinning(t *testing.T) {
	const validPin = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	nodes := []cloudclient.Node{
		{
			ID:                 "node-valid-1",
			AgentID:            "agent-1",
			AgentEndpoint:      "203.0.113.10",
			AgentPort:          9443,
			AgentTLSServerName: "node1.test",
			AgentCertSHA256:    validPin,
			Status:             "healthy",
		},
		{
			ID:                 "node-valid-2",
			AgentID:            "agent-2",
			AgentEndpoint:      "203.0.113.11",
			AgentPort:          9443,
			AgentTLSServerName: "node2.test",
			AgentCertSHA256:    validPin,
			Status:             "ready",
		},
		{
			ID:              "node-invalid-status",
			AgentID:         "agent-3",
			AgentEndpoint:   "203.0.113.12",
			AgentPort:       9443,
			AgentCertSHA256: validPin,
			Status:          "removed",
		},
		{
			ID:                 "node-invalid-endpoint",
			AgentID:            "agent-4",
			AgentEndpoint:      "",
			AgentPort:          9443,
			AgentTLSServerName: "node4.test",
			AgentCertSHA256:    validPin,
			Status:             "healthy",
		},
		{
			ID:                 "node-invalid-pin",
			AgentID:            "agent-5",
			AgentEndpoint:      "203.0.113.13",
			AgentPort:          9443,
			AgentTLSServerName: "node5.test",
			AgentCertSHA256:    "short-pin",
			Status:             "healthy",
		},
	}

	baseCfg := config.Config{
		AgentAddr: "127.0.0.1:9443", // Must NOT be used as fallback
		CloudURL:  "https://cloud.test",
	}

	resolver := NewAgentTargetResolver(AgentTargetResolverOptions{
		CloudClient: &mockNodeLister{nodes: nodes},
		BaseConfig:  baseCfg,
	})

	targets, preDialErrors, err := resolver.ResolveTargets(context.Background(), "project-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(preDialErrors) != 0 {
		t.Fatalf("unexpected preDialErrors: %+v", preDialErrors)
	}
	if len(targets) != 2 {
		t.Fatalf("expected 2 valid targets, got %d", len(targets))
	}

	if targets[0].Config.AgentAddr != "203.0.113.10:9443" || targets[0].Config.TLS.PinnedServerCertSHA256 != validPin || targets[0].Config.TLS.ServerName != "node1.test" {
		t.Fatalf("target 0 TLS config invalid: %+v", targets[0].Config)
	}
	if targets[1].Config.AgentAddr != "203.0.113.11:9443" || targets[1].Config.TLS.PinnedServerCertSHA256 != validPin || targets[1].Config.TLS.ServerName != "node2.test" {
		t.Fatalf("target 1 TLS config invalid: %+v", targets[1].Config)
	}
}

func TestResolverNoLoopbackFallbackWhenNoObservableAgents(t *testing.T) {
	baseCfg := config.Config{
		AgentAddr: "127.0.0.1:9443", // Loopback configured in cli.yaml
		CloudURL:  "https://cloud.test",
	}

	resolver := NewAgentTargetResolver(AgentTargetResolverOptions{
		CloudClient: &mockNodeLister{nodes: []cloudclient.Node{}}, // No nodes in Cloud
		BaseConfig:  baseCfg,
	})

	targets, _, err := resolver.ResolveTargets(context.Background(), "project-1")
	if err != nil {
		t.Fatalf("unexpected resolve error: %v", err)
	}
	if len(targets) != 0 {
		t.Fatalf("expected 0 targets (no loopback fallback), got %d: %+v", len(targets), targets)
	}

	conn, err := resolver.CheckConnection(context.Background(), "project-1")
	if err != nil {
		t.Fatalf("unexpected check connection error: %v", err)
	}
	if conn.Status != CoverageUnavailable || conn.ExpectedAgents != 0 || len(conn.Errors) == 0 || conn.Errors[0].Code != DiagNoObservableAgents {
		t.Fatalf("expected unavailable with NO_OBSERVABLE_AGENTS, got: %+v", conn)
	}

	_, cov, err := resolver.QueryTelemetry(context.Background(), &agentv1.TelemetryQueryRequest{ProjectID: "project-1"})
	if err == nil {
		t.Fatal("expected error for query telemetry with 0 agents")
	}
	var unavail *TelemetryUnavailableError
	if !errors.As(err, &unavail) {
		t.Fatalf("expected typed TelemetryUnavailableError, got: %T (%v)", err, err)
	}
	if cov.Status != CoverageUnavailable {
		t.Fatalf("expected CoverageUnavailable, got %v", cov.Status)
	}
}

func TestResolverCloudPATAndUnavailableErrors(t *testing.T) {
	baseCfg := config.Config{CloudURL: "https://cloud.test"}

	// 1. Auth error (401)
	authResolver := NewAgentTargetResolver(AgentTargetResolverOptions{
		CloudClient: &mockNodeLister{err: &cloudclient.APIError{Status: 401, Code: "UNAUTHENTICATED", Message: "invalid token"}},
		BaseConfig:  baseCfg,
	})
	conn, err := authResolver.CheckConnection(context.Background(), "project-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conn.Status != CoverageUnavailable || len(conn.Errors) == 0 || conn.Errors[0].Code != DiagCloudAuthRequired {
		t.Fatalf("expected CLOUD_AUTH_REQUIRED, got: %+v", conn)
	}

	// 2. Cloud unavailable
	unavailResolver := NewAgentTargetResolver(AgentTargetResolverOptions{
		CloudClient: &mockNodeLister{err: errors.New("connection refused to cloud")},
		BaseConfig:  baseCfg,
	})
	conn, err = unavailResolver.CheckConnection(context.Background(), "project-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conn.Status != CoverageUnavailable || len(conn.Errors) == 0 || conn.Errors[0].Code != DiagCloudUnavailable {
		t.Fatalf("expected CLOUD_UNAVAILABLE, got: %+v", conn)
	}
}

func TestResolverPartialSuccessAndAllAgentFailure(t *testing.T) {
	const validPin = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	nodes := []cloudclient.Node{
		{
			ID:                 "node-1",
			AgentID:            "agent-1",
			AgentEndpoint:      "203.0.113.10",
			AgentPort:          9443,
			AgentTLSServerName: "node1.test",
			AgentCertSHA256:    validPin,
			Status:             "healthy",
		},
		{
			ID:                 "node-2",
			AgentID:            "agent-2",
			AgentEndpoint:      "203.0.113.11",
			AgentPort:          9443,
			AgentTLSServerName: "node2.test",
			AgentCertSHA256:    validPin,
			Status:             "healthy",
		},
	}

	var callsNode1, callsNode2 atomic.Int32
	clientFactory := func(cfg config.Config) TelemetryClient {
		if strings.HasPrefix(cfg.AgentAddr, "203.0.113.10") {
			return &mockTelemetryClient{
				queryFunc: func(_ context.Context, req *agentv1.TelemetryQueryRequest) (*agentv1.TelemetryQueryResponse, error) {
					callsNode1.Add(1)
					return &agentv1.TelemetryQueryResponse{
						ProjectID: req.ProjectID,
						Source:    "agent",
						Summary: &agentv1.TelemetryRuntimeSummary{
							MetricCount: 5,
							LogCount:    2,
							Health:      "healthy",
						},
						Services: []agentv1.TelemetryServiceStatus{
							{ServiceID: "svc-web", PodCount: 2, ReadyPods: 2, Health: "healthy"},
						},
					}, nil
				},
			}
		}
		return &mockTelemetryClient{
			queryFunc: func(_ context.Context, _ *agentv1.TelemetryQueryRequest) (*agentv1.TelemetryQueryResponse, error) {
				callsNode2.Add(1)
				return nil, grpcstatus.Error(codes.DeadlineExceeded, "agent query deadline exceeded bearer secret-token-canary-1234567890abcdef")
			},
		}
	}

	resolver := NewAgentTargetResolver(AgentTargetResolverOptions{
		CloudClient:   &mockNodeLister{nodes: nodes},
		ClientFactory: clientFactory,
	})

	// 1. Partial success
	resp, cov, err := resolver.QueryTelemetry(context.Background(), &agentv1.TelemetryQueryRequest{
		ProjectID:      "project-1",
		IncludeSummary: true,
	})
	if err != nil {
		t.Fatalf("partial success should not return error: %v", err)
	}
	if cov.Status != CoveragePartial || cov.ExpectedAgents != 2 || cov.SuccessfulAgents != 1 || cov.FailedAgents != 1 {
		t.Fatalf("unexpected coverage: %+v", cov)
	}
	if len(cov.Errors) != 1 || cov.Errors[0].Code != DiagAgentTimeout {
		t.Fatalf("unexpected error diagnostic: %+v", cov.Errors)
	}
	// Check secret redaction
	if strings.Contains(cov.Errors[0].MessageRedacted, "secret-token-canary") {
		t.Fatalf("error leaked secret: %s", cov.Errors[0].MessageRedacted)
	}
	if resp == nil || resp.Summary == nil || resp.Summary.MetricCount != 5 {
		t.Fatalf("unexpected merged response: %+v", resp)
	}

	// 2. All-agent failure
	allFailFactory := func(_ config.Config) TelemetryClient {
		return &mockTelemetryClient{
			queryFunc: func(_ context.Context, _ *agentv1.TelemetryQueryRequest) (*agentv1.TelemetryQueryResponse, error) {
				return nil, grpcstatus.Error(codes.Unavailable, "connection refused")
			},
		}
	}
	allFailResolver := NewAgentTargetResolver(AgentTargetResolverOptions{
		CloudClient:   &mockNodeLister{nodes: nodes},
		ClientFactory: allFailFactory,
	})
	_, allFailCov, err := allFailResolver.QueryTelemetry(context.Background(), &agentv1.TelemetryQueryRequest{ProjectID: "project-1"})
	if err == nil {
		t.Fatal("expected error when all agents fail")
	}
	var unavail *TelemetryUnavailableError
	if !errors.As(err, &unavail) {
		t.Fatalf("expected TelemetryUnavailableError, got %T (%v)", err, err)
	}
	if allFailCov.Status != CoverageUnavailable || allFailCov.SuccessfulAgents != 0 || allFailCov.FailedAgents != 2 {
		t.Fatalf("unexpected coverage on all-agent failure: %+v", allFailCov)
	}
	if len(allFailCov.Errors) != 2 || allFailCov.Errors[0].Code != DiagAgentUnreachable {
		t.Fatalf("unexpected error diagnostics: %+v", allFailCov.Errors)
	}
}

func TestResolverFailsClosedForInvalidConfiguredClientMTLS(t *testing.T) {
	const validPin = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	resolver := NewAgentTargetResolver(AgentTargetResolverOptions{
		CloudClient: &mockNodeLister{nodes: []cloudclient.Node{{
			ID: "node-1", AgentID: "agent-1", AgentEndpoint: "203.0.113.10", AgentPort: 9443,
			AgentCertSHA256: validPin, Status: "ready",
		}}},
		BaseConfig: config.Config{TLS: config.TLSConfig{ClientCertPath: "/missing/client.crt"}},
		ClientFactory: func(config.Config) TelemetryClient {
			return &mockTelemetryClient{}
		},
	})

	connection, err := resolver.CheckConnection(context.Background(), "project-1")
	if err != nil {
		t.Fatalf("unexpected connection error: %v", err)
	}
	if connection.Status != CoverageUnavailable || connection.SuccessfulAgents != 0 || connection.FailedAgents != 1 || len(connection.Errors) != 1 || connection.Errors[0].Code != DiagClientMTLSMissing {
		t.Fatalf("invalid mTLS must fail closed, got %+v", connection)
	}
}

func TestResolverHonorsParentCancellationWhileTargetsAreQueued(t *testing.T) {
	const validPin = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	nodes := make([]cloudclient.Node, 12)
	for i := range nodes {
		nodes[i] = cloudclient.Node{
			ID: "node-" + strconv.Itoa(i), AgentID: "agent-" + strconv.Itoa(i), AgentEndpoint: "203.0.113." + strconv.Itoa(i+1),
			AgentPort: 9443, AgentCertSHA256: validPin, Status: "ready",
		}
	}
	resolver := NewAgentTargetResolver(AgentTargetResolverOptions{
		CloudClient:  &mockNodeLister{nodes: nodes},
		Concurrency:  1,
		QueryTimeout: time.Second,
		ClientFactory: func(config.Config) TelemetryClient {
			return &mockTelemetryClient{queryFunc: func(ctx context.Context, _ *agentv1.TelemetryQueryRequest) (*agentv1.TelemetryQueryResponse, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			}}
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, coverage, err := resolver.QueryTelemetry(ctx, &agentv1.TelemetryQueryRequest{ProjectID: "project-1"})
	if err == nil {
		t.Fatal("expected unavailable telemetry after cancellation")
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("queued targets ignored cancellation for %s", elapsed)
	}
	if coverage == nil || coverage.Status != CoverageUnavailable || coverage.FailedAgents != len(nodes) {
		t.Fatalf("unexpected cancelled coverage: %+v", coverage)
	}
}

func TestResolverDiagnosticsRedactSensitiveAssignments(t *testing.T) {
	redacted := redactSecrets("agent error password=super-secret token:abc123 api_key=xyz")
	for _, secret := range []string{"super-secret", "abc123", "xyz"} {
		if strings.Contains(redacted, secret) {
			t.Fatalf("diagnostic leaked %q: %s", secret, redacted)
		}
	}
}

func TestResolverBoundedFanOut(t *testing.T) {
	const validPin = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	nodes := make([]cloudclient.Node, 10)
	for i := 0; i < 10; i++ {
		nodes[i] = cloudclient.Node{
			ID:                 "node-" + string(rune('0'+i)),
			AgentID:            "agent-" + string(rune('0'+i)),
			AgentEndpoint:      "203.0.113." + string(rune('1'+i)),
			AgentPort:          9443,
			AgentTLSServerName: "node.test",
			AgentCertSHA256:    validPin,
			Status:             "healthy",
		}
	}

	var active, maxActive atomic.Int32
	clientFactory := func(_ config.Config) TelemetryClient {
		return &mockTelemetryClient{
			statusFunc: func(_ context.Context) (*agentv1.StatusResponse, error) {
				current := active.Add(1)
				for {
					max := maxActive.Load()
					if current <= max || maxActive.CompareAndSwap(max, current) {
						break
					}
				}
				time.Sleep(20 * time.Millisecond)
				active.Add(-1)
				return &agentv1.StatusResponse{Version: "v1.0.0"}, nil
			},
		}
	}

	resolver := NewAgentTargetResolver(AgentTargetResolverOptions{
		CloudClient:   &mockNodeLister{nodes: nodes},
		ClientFactory: clientFactory,
		Concurrency:   3, // Limit to 3 concurrent
	})

	conn, err := resolver.CheckConnection(context.Background(), "project-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conn.Status != CoverageConnected || conn.SuccessfulAgents != 10 {
		t.Fatalf("unexpected conn status: %+v", conn)
	}
	if maxActive.Load() > 3 {
		t.Fatalf("concurrency exceeded limit 3: max was %d", maxActive.Load())
	}
}
