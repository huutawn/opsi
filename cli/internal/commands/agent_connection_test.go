package commands

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/opsi-dev/opsi/cli/internal/agentclient"
	"github.com/opsi-dev/opsi/cli/internal/config"
)

func TestProjectAgentConnectionEndpoint(t *testing.T) {
	agent1 := &localTelemetryServer{}
	addr1, pin1, stop1 := startLocalTLSTelemetryServer(t, agent1)
	defer stop1()

	agent2 := &localTelemetryServer{}
	addr2, pin2, stop2 := startLocalTLSTelemetryServer(t, agent2)
	defer stop2()

	host1, portStr1, _ := net.SplitHostPort(addr1)
	port1, _ := strconv.Atoi(portStr1)
	host2, portStr2, _ := net.SplitHostPort(addr2)
	port2, _ := strconv.Atoi(portStr2)

	var agent2Online atomic.Bool
	agent2Online.Store(true)

	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		nodes := []map[string]any{
			{
				"id":                    "node-1",
				"agent_id":              "agent-1",
				"agent_endpoint":        host1,
				"agent_port":            port1,
				"agent_tls_server_name": "127.0.0.1",
				"agent_cert_sha256":     pin1,
				"status":                "ready",
			},
		}
		if agent2Online.Load() {
			nodes = append(nodes, map[string]any{
				"id":                    "node-2",
				"agent_id":              "agent-2",
				"agent_endpoint":        host2,
				"agent_port":            port2,
				"agent_tls_server_name": "127.0.0.1",
				"agent_cert_sha256":     pin2,
				"status":                "ready",
			})
		} else {
			// Report node-2 pointing to an unreachable port to simulate timeout/connection failure
			nodes = append(nodes, map[string]any{
				"id":                    "node-2",
				"agent_id":              "agent-2",
				"agent_endpoint":        "127.0.0.1",
				"agent_port":            1, // Unreachable port
				"agent_tls_server_name": "127.0.0.1",
				"agent_cert_sha256":     pin2,
				"status":                "ready",
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"nodes": nodes})
	}))
	defer cloud.Close()

	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "cli.yaml")
	initialConfigBytes := []byte("cloud_url: " + cloud.URL + "\n")
	if err := os.WriteFile(configPath, initialConfigBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{CloudURL: cloud.URL}
	server := httptest.NewServer(newStartMux(t.TempDir(), "", cfg, nil, configPath))
	defer server.Close()

	// 1. Both agents connected
	t.Run("both agents connected", func(t *testing.T) {
		res, err := http.Get(server.URL + "/api/local/projects/proj-1/agent-connection")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(res.Body)
			t.Fatalf("status=%d body=%s", res.StatusCode, body)
		}
		var conn agentclient.ProjectAgentConnectionResponse
		if err := json.NewDecoder(res.Body).Decode(&conn); err != nil {
			t.Fatal(err)
		}
		if conn.Status != agentclient.CoverageConnected || conn.ExpectedAgents != 2 || conn.SuccessfulAgents != 2 || conn.FailedAgents != 0 {
			t.Fatalf("unexpected connection status: %+v", conn)
		}
		if len(conn.Errors) != 0 {
			t.Fatalf("expected 0 errors, got: %+v", conn.Errors)
		}

		// Verify config file was NOT modified by opening dashboard / querying agent-connection
		currentConfigBytes, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(currentConfigBytes) != string(initialConfigBytes) {
			t.Fatalf("config file was mutated: before=%q after=%q", initialConfigBytes, currentConfigBytes)
		}
	})

	// 2. Partial connection (agent 2 fails)
	t.Run("partial connection one agent failing", func(t *testing.T) {
		agent2Online.Store(false)
		res, err := http.Get(server.URL + "/api/local/projects/proj-1/agent-connection")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(res.Body)
			t.Fatalf("status=%d body=%s", res.StatusCode, body)
		}
		var conn agentclient.ProjectAgentConnectionResponse
		if err := json.NewDecoder(res.Body).Decode(&conn); err != nil {
			t.Fatal(err)
		}
		if conn.Status != agentclient.CoveragePartial || conn.ExpectedAgents != 2 || conn.SuccessfulAgents != 1 || conn.FailedAgents != 1 {
			t.Fatalf("unexpected partial connection status: %+v", conn)
		}
		if len(conn.Errors) != 1 || conn.Errors[0].NodeID != "node-2" || conn.Errors[0].Code == "" {
			t.Fatalf("expected node-2 error diagnostic, got: %+v", conn.Errors)
		}

		// Querying telemetry summary should succeed with partial coverage
		summaryRes, err := http.Get(server.URL + "/api/local/projects/proj-1/telemetry/summary?since_unix=0")
		if err != nil {
			t.Fatal(err)
		}
		defer summaryRes.Body.Close()
		if summaryRes.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(summaryRes.Body)
			t.Fatalf("expected 200 OK for partial summary, got status=%d body=%s", summaryRes.StatusCode, body)
		}
		var sum struct {
			ProjectID string                         `json:"project_id"`
			Coverage  *agentclient.TelemetryCoverage `json:"coverage"`
		}
		if err := json.NewDecoder(summaryRes.Body).Decode(&sum); err != nil {
			t.Fatal(err)
		}
		if sum.Coverage == nil || sum.Coverage.Status != agentclient.CoveragePartial || sum.Coverage.SuccessfulAgents != 1 || sum.Coverage.FailedAgents != 1 {
			t.Fatalf("expected partial coverage in summary, got: %+v", sum.Coverage)
		}
	})

	// 3. All agents unavailable
	t.Run("all agents unavailable returns typed AGENT_TELEMETRY_UNAVAILABLE", func(t *testing.T) {
		allUnreachableCloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"nodes": []map[string]any{
					{
						"id":                    "node-down-1",
						"agent_id":              "agent-down-1",
						"agent_endpoint":        "127.0.0.1",
						"agent_port":            1,
						"agent_tls_server_name": "127.0.0.1",
						"agent_cert_sha256":     pin1,
						"status":                "ready",
					},
				},
			})
		}))
		defer allUnreachableCloud.Close()

		unreachServer := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{CloudURL: allUnreachableCloud.URL}, nil))
		defer unreachServer.Close()

		res, err := http.Get(unreachServer.URL + "/api/local/projects/proj-1/agent-connection")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var conn agentclient.ProjectAgentConnectionResponse
		if err := json.NewDecoder(res.Body).Decode(&conn); err != nil {
			t.Fatal(err)
		}
		if conn.Status != agentclient.CoverageUnavailable || conn.ExpectedAgents != 1 || conn.SuccessfulAgents != 0 {
			t.Fatalf("expected unavailable status, got: %+v", conn)
		}

		// Summary should fail with 502 AGENT_TELEMETRY_UNAVAILABLE and coverage
		summaryRes, err := http.Get(unreachServer.URL + "/api/local/projects/proj-1/telemetry/summary")
		if err != nil {
			t.Fatal(err)
		}
		defer summaryRes.Body.Close()
		if summaryRes.StatusCode != http.StatusBadGateway {
			body, _ := io.ReadAll(summaryRes.Body)
			t.Fatalf("expected 502 Bad Gateway, got status=%d body=%s", summaryRes.StatusCode, body)
		}
		var errResp struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
			Coverage *agentclient.TelemetryCoverage `json:"coverage"`
		}
		if err := json.NewDecoder(summaryRes.Body).Decode(&errResp); err != nil {
			t.Fatal(err)
		}
		if errResp.Error.Code != "AGENT_TELEMETRY_UNAVAILABLE" {
			t.Fatalf("expected AGENT_TELEMETRY_UNAVAILABLE, got: %q", errResp.Error.Code)
		}
		if errResp.Coverage == nil || errResp.Coverage.Status != agentclient.CoverageUnavailable {
			t.Fatalf("expected unavailable coverage metadata, got: %+v", errResp.Coverage)
		}
	})

	// 4. Cloud PAT rejected (401)
	t.Run("cloud PAT rejected reports CLOUD_AUTH_REQUIRED", func(t *testing.T) {
		authFailCloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":"UNAUTHENTICATED","message":"invalid PAT"}`))
		}))
		defer authFailCloud.Close()

		authFailServer := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{CloudURL: authFailCloud.URL}, nil))
		defer authFailServer.Close()

		res, err := http.Get(authFailServer.URL + "/api/local/projects/proj-1/agent-connection")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var conn agentclient.ProjectAgentConnectionResponse
		if err := json.NewDecoder(res.Body).Decode(&conn); err != nil {
			t.Fatal(err)
		}
		if conn.Status != agentclient.CoverageUnavailable || len(conn.Errors) == 0 || conn.Errors[0].Code != agentclient.DiagCloudAuthRequired {
			t.Fatalf("expected CLOUD_AUTH_REQUIRED, got: %+v", conn)
		}
	})

	// 5. No observable agents in project
	t.Run("no observable agents in project reports NO_OBSERVABLE_AGENTS", func(t *testing.T) {
		emptyCloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"nodes": []any{}})
		}))
		defer emptyCloud.Close()

		emptyServer := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{CloudURL: emptyCloud.URL}, nil))
		defer emptyServer.Close()

		res, err := http.Get(emptyServer.URL + "/api/local/projects/proj-1/agent-connection")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var conn agentclient.ProjectAgentConnectionResponse
		if err := json.NewDecoder(res.Body).Decode(&conn); err != nil {
			t.Fatal(err)
		}
		if conn.Status != agentclient.CoverageUnavailable || conn.ExpectedAgents != 0 || len(conn.Errors) == 0 || conn.Errors[0].Code != agentclient.DiagNoObservableAgents {
			t.Fatalf("expected NO_OBSERVABLE_AGENTS, got: %+v", conn)
		}
	})
}
