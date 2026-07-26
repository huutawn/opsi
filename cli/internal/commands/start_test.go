package commands

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cli/internal/config"
	"github.com/opsi-dev/opsi/cli/internal/keychain"
	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestRunStartKeepsSessionAndReconnectsAfterConfigSave(t *testing.T) {
	addrA, callsA, stopA := startCommandIdentifiedStatusServer(t, "agent-a")
	defer stopA()
	addrB, callsB, stopB := startCommandIdentifiedStatusServer(t, "agent-b")
	defer stopB()

	configPath := filepath.Join(t.TempDir(), "cli.yaml")
	initial := config.Config{AgentAddr: addrA, CloudURL: "http://cloud-startup"}
	if err := config.Save(configPath, initial); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	outReader, outWriter := io.Pipe()
	runErr := make(chan error, 1)
	go func() { runErr <- runStart(ctx, "127.0.0.1:0", "", configPath, outWriter, nil) }()
	line, err := bufio.NewReader(outReader).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	localURL := strings.TrimSpace(strings.TrimPrefix(line, "Local Web UI listening on "))
	statusURL := fmt.Sprintf("%s/api/local/status", localURL)
	session := localTestSession(t, localURL)

	var first agentv1.StatusResponse
	getStatus := func() agentv1.StatusResponse {
		res, err := http.Get(statusURL)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(res.Body)
			t.Fatalf("status=%d body=%s", res.StatusCode, body)
		}
		var got agentv1.StatusResponse
		if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	first = getStatus()
	if first.NodeID != "agent-a" {
		t.Fatalf("initial status node=%q", first.NodeID)
	}
	callsBeforeSwitch := callsA.Load()
	if err := config.Save(configPath, config.Config{AgentAddr: addrB, CloudURL: "http://cloud-changed"}); err != nil {
		t.Fatal(err)
	}
	second := getStatus()
	if second.NodeID != "agent-b" {
		t.Fatalf("reloaded status node=%q", second.NodeID)
	}
	if reloadedSession := localTestSession(t, localURL); reloadedSession != session {
		t.Fatalf("local session changed after Agent reconnect")
	}
	if callsA.Load() != callsBeforeSwitch || callsB.Load() < 2 {
		t.Fatalf("post-switch calls used old Agent: a=%d before=%d b=%d", callsA.Load(), callsBeforeSwitch, callsB.Load())
	}
	if err := cancelAndWait(cancel, runErr); err != nil {
		t.Fatal(err)
	}
}

func cancelAndWait(cancel context.CancelFunc, runErr <-chan error) error {
	cancel()
	select {
	case err := <-runErr:
		return err
	case <-time.After(5 * time.Second):
		return fmt.Errorf("local server did not stop")
	}
}

type commandIdentifiedStatusServer struct {
	agentv1.UnimplementedStatusServiceServer
	nodeID string
	calls  *atomic.Int64
}

func (s commandIdentifiedStatusServer) Status(context.Context, *agentv1.StatusRequest) (*agentv1.StatusResponse, error) {
	s.calls.Add(1)
	return &agentv1.StatusResponse{NodeID: s.nodeID, Health: "ok", Version: "test"}, nil
}

func startCommandIdentifiedStatusServer(t *testing.T, nodeID string) (string, *atomic.Int64, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	calls := &atomic.Int64{}
	agentv1.RegisterStatusServiceServer(server, commandIdentifiedStatusServer{nodeID: nodeID, calls: calls})
	go func() { _ = server.Serve(listener) }()
	return listener.Addr().String(), calls, server.Stop
}

func TestLocalAgentFacadesReconnectTogether(t *testing.T) {
	addrA, agentA, stopA := startLocalFacadeAgent(t, "agent-a")
	defer stopA()
	addrB, agentB, stopB := startLocalFacadeAgent(t, "agent-b")
	defer stopB()
	configPath := filepath.Join(t.TempDir(), "cli.yaml")
	if err := config.Save(configPath, config.Config{AgentAddr: addrA, CloudURL: "http://cloud-a"}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: addrA, CloudURL: "http://cloud-a"}, nil, configPath))
	defer server.Close()
	session := localTestSession(t, server.URL)

	get := func(path string) {
		res, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(res.Body)
			t.Fatalf("GET %s status=%d body=%s", path, res.StatusCode, body)
		}
	}
	post := func(path, body, key string) {
		req, err := http.NewRequest(http.MethodPost, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Local-Session", session)
		req.Header.Set("Idempotency-Key", key)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			response, _ := io.ReadAll(res.Body)
			t.Fatalf("POST %s status=%d body=%s", path, res.StatusCode, response)
		}
	}

	get("/api/local/status")
	statusCallsBeforeSwitch := agentA.statusCalls.Load()
	if err := config.Save(configPath, config.Config{AgentAddr: addrB, CloudURL: "http://cloud-b"}); err != nil {
		t.Fatal(err)
	}
	get("/api/local/status")
	post("/api/local/projects/proj-1/secrets", `{"service_id":"svc-1","name":"db","namespace":"app"}`, "facade-secret")
	get("/api/local/projects/proj-1/telemetry/summary?since_unix=1")
	get("/api/local/projects/proj-1/logs?service_id=svc-1")
	get("/api/local/projects/proj-1/incidents")
	get("/api/local/projects/proj-1/incidents/inc-1")
	post("/api/local/projects/proj-1/incidents/inc-1/resolve", `{}`, "facade-incident")

	if agentA.statusCalls.Load() != statusCallsBeforeSwitch || agentA.secretCalls.Load() != 0 || agentA.telemetryCalls.Load() != 0 || agentA.incidentListCalls.Load() != 0 || agentA.incidentGetCalls.Load() != 0 || agentA.incidentResolveCalls.Load() != 0 {
		t.Fatalf("old Agent received post-switch calls: %+v", agentA)
	}
	if agentB.statusCalls.Load() != 1 || agentB.secretCalls.Load() != 1 || agentB.telemetryCalls.Load() != 2 || agentB.incidentListCalls.Load() != 1 || agentB.incidentGetCalls.Load() != 1 || agentB.incidentResolveCalls.Load() != 1 {
		t.Fatalf("new Agent facade calls incomplete: %+v", agentB)
	}
}

func TestAgentConfigReloadFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{name: "malformed", mutate: func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("agent_addr: [private-certificate-material"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "missing address", mutate: func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("agent_addr: \"\"\ncloud_url: http://cloud.invalid\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "remote missing pin", mutate: func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("agent_addr: 203.0.113.10:9443\ncloud_url: http://cloud.invalid\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "missing file", mutate: func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agentAddr, agentCalls, stopAgent := startCommandIdentifiedStatusServer(t, "agent-a")
			defer stopAgent()
			cloudCalls := &atomic.Int64{}
			cloud := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { cloudCalls.Add(1) }))
			defer cloud.Close()
			configPath := filepath.Join(t.TempDir(), "cli.yaml")
			startup := config.Config{AgentAddr: agentAddr, CloudURL: cloud.URL}
			if err := config.Save(configPath, startup); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(newStartMux(t.TempDir(), "", startup, nil, configPath))
			defer server.Close()
			res, err := http.Get(server.URL + "/api/local/status")
			if err != nil {
				t.Fatal(err)
			}
			_ = res.Body.Close()
			if res.StatusCode != http.StatusOK {
				t.Fatalf("initial status=%d", res.StatusCode)
			}
			callsBeforeReload := agentCalls.Load()
			tt.mutate(t, configPath)

			res, err = http.Get(server.URL + "/api/local/status")
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(res.Body)
			_ = res.Body.Close()
			if res.StatusCode != http.StatusBadGateway || !strings.Contains(string(body), "AGENT_CONFIG_RELOAD_FAILED") {
				t.Fatalf("reload status=%d body=%s", res.StatusCode, body)
			}
			for _, secret := range []string{configPath, agentAddr, "203.0.113.10", "private-certificate-material"} {
				if strings.Contains(string(body), secret) {
					t.Fatalf("reload error leaked config material %q: %s", secret, body)
				}
			}
			if agentCalls.Load() != callsBeforeReload || cloudCalls.Load() != 0 {
				t.Fatalf("reload failure fell back: agent=%d before=%d cloud=%d", agentCalls.Load(), callsBeforeReload, cloudCalls.Load())
			}
		})
	}
}

func TestAgentReloadDoesNotChangeCloudAuthority(t *testing.T) {
	addrA, _, stopA := startCommandIdentifiedStatusServer(t, "agent-a")
	defer stopA()
	addrB, _, stopB := startCommandIdentifiedStatusServer(t, "agent-b")
	defer stopB()
	cloudACalls, cloudBCalls := &atomic.Int64{}, &atomic.Int64{}
	cloudA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cloudACalls.Add(1)
		_, _ = w.Write([]byte(`{"projects":[]}`))
	}))
	defer cloudA.Close()
	cloudB := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { cloudBCalls.Add(1) }))
	defer cloudB.Close()
	configPath := filepath.Join(t.TempDir(), "cli.yaml")
	startup := config.Config{AgentAddr: addrA, CloudURL: cloudA.URL}
	if err := config.Save(configPath, startup); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(newStartMux(t.TempDir(), "", startup, nil, configPath))
	defer server.Close()
	assertStatusNode(t, server.URL, "agent-a")
	if err := config.Save(configPath, config.Config{AgentAddr: addrB, CloudURL: cloudB.URL}); err != nil {
		t.Fatal(err)
	}
	assertStatusNode(t, server.URL, "agent-b")
	res, err := http.Get(server.URL + "/api/local/projects?org_id=org-1")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || cloudACalls.Load() != 1 || cloudBCalls.Load() != 0 {
		t.Fatalf("Cloud authority changed: status=%d a=%d b=%d", res.StatusCode, cloudACalls.Load(), cloudBCalls.Load())
	}
}

func TestAgentConfigReloadConcurrentRequestsUseWholeSnapshots(t *testing.T) {
	addrA, _, stopA := startCommandIdentifiedStatusServer(t, "agent-a")
	defer stopA()
	addrB, _, stopB := startCommandIdentifiedStatusServer(t, "agent-b")
	defer stopB()
	configPath := filepath.Join(t.TempDir(), "cli.yaml")
	startup := config.Config{AgentAddr: addrA, CloudURL: "http://cloud-a"}
	if err := config.Save(configPath, startup); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(newStartMux(t.TempDir(), "", startup, nil, configPath))
	defer server.Close()

	start := make(chan struct{})
	results := make(chan string, 32)
	var wait sync.WaitGroup
	for range 32 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			nodeID, err := getStatusNode(server.URL)
			if err != nil {
				results <- "error: " + err.Error()
				return
			}
			results <- nodeID
		}()
	}
	close(start)
	if err := config.Save(configPath, config.Config{AgentAddr: addrB, CloudURL: "http://cloud-b"}); err != nil {
		t.Fatal(err)
	}
	wait.Wait()
	close(results)
	for nodeID := range results {
		if nodeID != "agent-a" && nodeID != "agent-b" {
			t.Fatalf("request observed partial config: %q", nodeID)
		}
	}
	for range 8 {
		assertStatusNode(t, server.URL, "agent-b")
	}
}

func TestAgentConfigReloadRotatesTLSIdentity(t *testing.T) {
	addrA, pinA, stopA := startLocalTLSStatusAgent(t, "agent-a")
	defer stopA()
	addrB, pinB, stopB := startLocalTLSStatusAgent(t, "agent-b")
	defer stopB()
	configPath := filepath.Join(t.TempDir(), "cli.yaml")
	startup := config.Config{AgentAddr: addrA, CloudURL: "http://cloud-a", TLS: config.TLSConfig{PinnedServerCertSHA256: pinA, ServerName: "127.0.0.1"}}
	if err := config.Save(configPath, startup); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(newStartMux(t.TempDir(), "", startup, nil, configPath))
	defer server.Close()
	assertStatusNode(t, server.URL, "agent-a")

	wrongPin := config.Config{AgentAddr: addrB, CloudURL: "http://cloud-b", TLS: config.TLSConfig{PinnedServerCertSHA256: pinA, ServerName: "127.0.0.1"}}
	if err := config.Save(configPath, wrongPin); err != nil {
		t.Fatal(err)
	}
	assertSanitizedAgentFailure(t, server.URL, pinA, pinB, addrB)

	valid := wrongPin
	valid.TLS.PinnedServerCertSHA256 = pinB
	if err := config.Save(configPath, valid); err != nil {
		t.Fatal(err)
	}
	assertStatusNode(t, server.URL, "agent-b")

	wrongName := valid
	wrongName.TLS.ServerName = "wrong-agent-name.invalid"
	if err := config.Save(configPath, wrongName); err != nil {
		t.Fatal(err)
	}
	assertSanitizedAgentFailure(t, server.URL, pinA, pinB, addrB, wrongName.TLS.ServerName)
}

func assertSanitizedAgentFailure(t *testing.T, localURL string, forbidden ...string) {
	t.Helper()
	res, err := http.Get(localURL + "/api/local/status")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadGateway || !strings.Contains(string(body), "AGENT_UNAVAILABLE") {
		t.Fatalf("Agent TLS failure status=%d body=%s", res.StatusCode, body)
	}
	for _, value := range forbidden {
		if strings.Contains(string(body), value) {
			t.Fatalf("Agent TLS failure leaked %q: %s", value, body)
		}
	}
}

func startLocalTLSStatusAgent(t *testing.T, nodeID string) (string, string, func()) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "agent.test"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Minute),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		BasicConstraintsValid: true,
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate := tls.Certificate{Certificate: [][]byte{certificateDER}, PrivateKey: privateKey}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS13})))
	calls := &atomic.Int64{}
	agentv1.RegisterStatusServiceServer(server, commandIdentifiedStatusServer{nodeID: nodeID, calls: calls})
	go func() { _ = server.Serve(listener) }()
	fingerprint := sha256.Sum256(certificateDER)
	return listener.Addr().String(), hex.EncodeToString(fingerprint[:]), server.Stop
}

func assertStatusNode(t *testing.T, localURL, want string) {
	t.Helper()
	got, err := getStatusNode(localURL)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("status node=%q want=%q", got, want)
	}
}

func getStatusNode(localURL string) (string, error) {
	res, err := http.Get(localURL + "/api/local/status")
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		return "", fmt.Errorf("status=%d body=%s", res.StatusCode, body)
	}
	var status agentv1.StatusResponse
	if err := json.NewDecoder(res.Body).Decode(&status); err != nil {
		return "", err
	}
	return status.NodeID, nil
}

type localFacadeAgent struct {
	agentv1.UnimplementedStatusServiceServer
	agentv1.UnimplementedSecretServiceServer
	agentv1.UnimplementedTelemetryServiceServer
	agentv1.UnimplementedIncidentServiceServer
	id                   string
	statusCalls          atomic.Int64
	secretCalls          atomic.Int64
	telemetryCalls       atomic.Int64
	incidentListCalls    atomic.Int64
	incidentGetCalls     atomic.Int64
	incidentResolveCalls atomic.Int64
}

func (s *localFacadeAgent) Status(context.Context, *agentv1.StatusRequest) (*agentv1.StatusResponse, error) {
	s.statusCalls.Add(1)
	return &agentv1.StatusResponse{NodeID: s.id, Health: "ok", Version: "test"}, nil
}

func (s *localFacadeAgent) CreateSecret(_ context.Context, req *agentv1.SecretRequest) (*agentv1.SecretResponse, error) {
	s.secretCalls.Add(1)
	return &agentv1.SecretResponse{ProjectID: req.ProjectID, ServiceID: req.ServiceID, Name: req.Name, Namespace: req.Namespace, Username: s.id, Password: "not-for-create"}, nil
}

func (s *localFacadeAgent) RevealSecret(_ context.Context, req *agentv1.SecretRequest) (*agentv1.SecretResponse, error) {
	s.secretCalls.Add(1)
	return &agentv1.SecretResponse{ProjectID: req.ProjectID, ServiceID: req.ServiceID, Name: req.Name, Namespace: req.Namespace, Username: s.id, Password: "secret"}, nil
}

func (s *localFacadeAgent) RotateSecret(_ context.Context, req *agentv1.SecretRequest) (*agentv1.SecretResponse, error) {
	s.secretCalls.Add(1)
	return &agentv1.SecretResponse{ProjectID: req.ProjectID, ServiceID: req.ServiceID, Name: req.Name, Namespace: req.Namespace, Username: s.id}, nil
}

func (s *localFacadeAgent) QueryTelemetry(_ context.Context, req *agentv1.TelemetryQueryRequest) (*agentv1.TelemetryQueryResponse, error) {
	s.telemetryCalls.Add(1)
	return &agentv1.TelemetryQueryResponse{ProjectID: req.ProjectID, Source: "agent", Summary: &agentv1.TelemetryRuntimeSummary{SinceUnix: req.SinceUnix, EndUnix: 2, MetricCount: 1, LogCount: 1}}, nil
}

func (s *localFacadeAgent) Sync(_ *agentv1.SyncRequest, stream agentv1.TelemetryService_SyncServer) error {
	return stream.Send(&agentv1.SyncChunk{Done: true})
}

func (s *localFacadeAgent) ListIncidents(_ context.Context, req *agentv1.IncidentListRequest) (*agentv1.IncidentListResponse, error) {
	s.incidentListCalls.Add(1)
	return &agentv1.IncidentListResponse{Incidents: []agentv1.IncidentResponse{*localIncidentResponse(req.ProjectID, s.id)}}, nil
}

func (s *localFacadeAgent) GetIncident(_ context.Context, req *agentv1.IncidentGetRequest) (*agentv1.IncidentResponse, error) {
	s.incidentGetCalls.Add(1)
	return localIncidentResponse(req.ProjectID, req.IncidentID), nil
}

func (s *localFacadeAgent) ResolveIncident(_ context.Context, req *agentv1.IncidentResolveRequest) (*agentv1.IncidentResponse, error) {
	s.incidentResolveCalls.Add(1)
	response := localIncidentResponse(req.ProjectID, req.IncidentID)
	response.Status = "resolved"
	return response, nil
}

func startLocalFacadeAgent(t *testing.T, id string) (string, *localFacadeAgent, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	agent := &localFacadeAgent{id: id}
	server := grpc.NewServer()
	agentv1.RegisterStatusServiceServer(server, agent)
	agentv1.RegisterSecretServiceServer(server, agent)
	agentv1.RegisterTelemetryServiceServer(server, agent)
	agentv1.RegisterIncidentServiceServer(server, agent)
	go func() { _ = server.Serve(listener) }()
	return listener.Addr().String(), agent, server.Stop
}

func TestStartMuxServesHealthAndBuiltUI(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html><title>Opsi Console</title>"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(newStartMux(dir, "", config.Default(), nil))
	defer server.Close()

	res, err := http.Get(server.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d", res.StatusCode)
	}
	_ = res.Body.Close()

	res, err = http.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("ui status = %d", res.StatusCode)
	}
}

func TestStartMuxReportsMissingUIBuild(t *testing.T) {
	server := httptest.NewServer(newStartMux(filepath.Join(t.TempDir(), "missing"), "", config.Default(), nil))
	defer server.Close()

	res, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", res.StatusCode)
	}
}

func TestStartMuxLocalStatusReportsAgentUnavailable(t *testing.T) {
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: "127.0.0.1:1", CloudURL: "http://127.0.0.1:9800"}, nil))
	defer server.Close()
	res, err := http.Get(server.URL + "/api/local/status")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d", res.StatusCode)
	}
}

func TestStartMuxProxiesDevUI(t *testing.T) {
	dev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("dev-ui"))
	}))
	defer dev.Close()
	server := httptest.NewServer(newStartMux(t.TempDir(), dev.URL, config.Default(), nil))
	defer server.Close()
	res, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if string(body) != "dev-ui" {
		t.Fatalf("body = %q", body)
	}
}

func TestLocalRegistryProxyUsesKeychainPAT(t *testing.T) {
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/orgs/org-1/projects" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer keychain-pat" {
			t.Fatalf("authorization = %q", got)
		}
		_, _ = w.Write([]byte(`{"projects":[]}`))
	}))
	defer cloud.Close()
	store := keychain.NewFakeStore()
	if err := store.SetPAT("keychain-pat"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: "127.0.0.1:1", CloudURL: cloud.URL}, func() (keychain.Store, error) {
		return store, nil
	}))
	defer server.Close()

	req, err := http.NewRequest(http.MethodGet, server.URL+"/api/local/projects?org_id=org-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer browser-pat")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
}

func TestLocalMutationRequiresSession(t *testing.T) {
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("Cloud should not receive unauthenticated local mutation")
	}))
	defer cloud.Close()
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: "127.0.0.1:1", CloudURL: cloud.URL}, nil))
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/local/projects?org_id=org-1", bytes.NewReader([]byte(`{"name":"Demo"}`)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Idempotency-Key", "project-1")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", res.StatusCode)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "LOCAL_SESSION_REQUIRED" {
		t.Fatalf("code = %q", body.Error.Code)
	}
}

func TestLocalMutationWithSessionProxies(t *testing.T) {
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Idempotency-Key"); got != "project-1" {
			t.Fatalf("idempotency = %q", got)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"proj_1"}`))
	}))
	defer cloud.Close()
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: "127.0.0.1:1", CloudURL: cloud.URL}, nil))
	defer server.Close()

	var session struct {
		LocalSession string `json:"local_session"`
	}
	res, err := http.Get(server.URL + "/api/local/session")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewDecoder(res.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if session.LocalSession == "" {
		t.Fatal("empty local session")
	}

	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/local/projects?org_id=org-1", bytes.NewReader([]byte(`{"name":"Demo"}`)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Idempotency-Key", "project-1")
	req.Header.Set("X-Local-Session", session.LocalSession)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d", res.StatusCode)
	}
}

func TestLocalGitHubInventoryUsesV1CloudPathAndKeychainPAT(t *testing.T) {
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/projects/proj-1/github/repositories" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer keychain-pat" {
			t.Fatalf("authorization = %q", got)
		}
		_, _ = w.Write([]byte(`{"repositories":[]}`))
	}))
	defer cloud.Close()
	store := keychain.NewFakeStore()
	if err := store.SetPAT("keychain-pat"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: "127.0.0.1:1", CloudURL: cloud.URL}, func() (keychain.Store, error) {
		return store, nil
	}))
	defer server.Close()

	res, err := http.Get(server.URL + "/api/local/projects/proj-1/github/repositories")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
}

func TestLocalBuildRecordReadUsesProjectScopedCloudPathAndKeychainPAT(t *testing.T) {
	store := keychain.NewFakeStore()
	if err := store.SetPAT("build-record-pat"); err != nil {
		t.Fatal(err)
	}
	paths := []string{}
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer build-record-pat" {
			t.Fatalf("method=%s auth=%q", r.Method, r.Header.Get("Authorization"))
		}
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/br-1") {
			_, _ = w.Write([]byte(`{"id":"br-1"}`))
			return
		}
		_, _ = w.Write([]byte(`{"records":[{"id":"br-1"}]}`))
	}))
	defer cloud.Close()
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: "127.0.0.1:1", CloudURL: cloud.URL}, func() (keychain.Store, error) { return store, nil }))
	defer server.Close()
	for _, path := range []string{"/api/local/projects/project-1/build-records?limit=50", "/api/local/projects/project-1/build-records/br-1"} {
		response, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			t.Fatalf("path=%s status=%d body=%s", path, response.StatusCode, body)
		}
		_ = response.Body.Close()
	}
	if strings.Join(paths, ",") != "/api/projects/project-1/build-records,/api/projects/project-1/build-records/br-1" {
		t.Fatalf("Cloud paths=%v", paths)
	}
}

func TestLocalGitHubInstallationClaimRedeemsOnceWithoutBrowserCredential(t *testing.T) {
	var callbackURL, localState string
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer keychain-pat" {
			t.Fatalf("authorization = %q", got)
		}
		switch r.URL.Path {
		case "/v1/projects/proj-1/github/installations/77/claim/start":
			var body struct {
				LocalCallback string `json:"local_callback"`
				LocalState    string `json:"local_state"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			callbackURL, localState = body.LocalCallback, body.LocalState
			_, _ = w.Write([]byte(`{"authorization_url":"https://github.com/login/oauth/authorize?client_id=test"}`))
		case "/v1/github/installations/claim/redeem":
			var body struct {
				Grant string `json:"grant"`
				State string `json:"state"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Grant != "one-time-grant" || body.State != localState {
				t.Fatalf("redeem = %+v", body)
			}
			_, _ = w.Write([]byte(`{"installation":{"installation_id":77,"status":"active"},"repositories_synced":1}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer cloud.Close()
	store := keychain.NewFakeStore()
	if err := store.SetPAT("keychain-pat"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: "127.0.0.1:1", CloudURL: cloud.URL}, func() (keychain.Store, error) {
		return store, nil
	}))
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/local/projects/proj-1/github/installations/77/claim/start", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Idempotency-Key", "claim-start")
	req.Header.Set("X-Local-Session", localTestSession(t, server.URL))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var started map[string]any
	if err := json.NewDecoder(res.Body).Decode(&started); err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || callbackURL == "" || localState == "" {
		t.Fatalf("status=%d callback=%q state=%q", res.StatusCode, callbackURL, localState)
	}

	callback := callbackURL + "?grant=one-time-grant&state=" + url.QueryEscape(localState)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err = client.Get(callback)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusFound || strings.Contains(res.Header.Get("Location"), "grant") {
		t.Fatalf("callback status=%d location=%q", res.StatusCode, res.Header.Get("Location"))
	}
	res, err = client.Get(callback)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("reused callback status=%d", res.StatusCode)
	}
}

func TestLegacyLocalServiceDeploymentEndpointReturnsNotFound(t *testing.T) {
	cloudCalled := false
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cloudCalled = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer cloud.Close()
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: "127.0.0.1:1", CloudURL: cloud.URL}, nil))
	defer server.Close()

	var session struct {
		LocalSession string `json:"local_session"`
	}
	res, err := http.Get(server.URL + "/api/local/session")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewDecoder(res.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/local/projects/proj-1/services/svc-1/deployments", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Idempotency-Key", "dep-1")
	req.Header.Set("X-Local-Session", session.LocalSession)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if cloudCalled {
		t.Fatal("legacy local deployment route reached Cloud")
	}
}

func TestLocalDisabledAgentEndpointIsTyped(t *testing.T) {
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Default(), nil))
	defer server.Close()
	res, err := http.Get(server.URL + "/api/local/projects/proj_1/secrets")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d", res.StatusCode)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "SECRETS_OPERATION_UNSUPPORTED" {
		t.Fatalf("code = %q", body.Error.Code)
	}
}

func TestLocalSecretCreateUsesAgentNotCloudAndRedactsValue(t *testing.T) {
	agent := &localSecretServer{}
	agentAddr, stop := startLocalSecretServer(t, agent)
	defer stop()
	cloudCalled := false
	cloud := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		cloudCalled = true
	}))
	defer cloud.Close()
	store := keychain.NewFakeStore()
	if err := store.SetPAT("keychain-pat"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: agentAddr, CloudURL: cloud.URL}, func() (keychain.Store, error) {
		return store, nil
	}))
	defer server.Close()
	session := localTestSession(t, server.URL)

	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/local/projects/proj-1/secrets", strings.NewReader(`{"service_id":"svc-1","name":"db","namespace":"app"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Local-Session", session)
	req.Header.Set("Idempotency-Key", "secret-create-1")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.StatusCode, body)
	}
	if cloudCalled {
		t.Fatal("secret create must not call Cloud")
	}
	if agent.createCalls != 1 || agent.lastReq.ProjectID != "proj-1" || agent.lastAuth != "Bearer keychain-pat" {
		t.Fatalf("agent request not used: calls=%d auth=%q req=%+v", agent.createCalls, agent.lastAuth, agent.lastReq)
	}
	if strings.Contains(string(body), "agent-secret-password") {
		t.Fatalf("create response leaked secret value: %s", body)
	}
}

func TestLocalSecretRejectsBrowserSecretValuesBeforeAgentOrCloud(t *testing.T) {
	agent := &localSecretServer{}
	agentAddr, stop := startLocalSecretServer(t, agent)
	defer stop()
	cloudCalled := false
	cloud := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		cloudCalled = true
	}))
	defer cloud.Close()
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: agentAddr, CloudURL: cloud.URL}, nil))
	defer server.Close()
	session := localTestSession(t, server.URL)

	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/local/projects/proj-1/secrets", strings.NewReader(`{"service_id":"svc-1","name":"db","user_id":"owner","role":"Owner","password":"browser-secret"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Local-Session", session)
	req.Header.Set("Idempotency-Key", "secret-create-raw")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", res.StatusCode, body)
	}
	if cloudCalled || agent.createCalls != 0 {
		t.Fatalf("raw secret reached cloud=%v agent_calls=%d", cloudCalled, agent.createCalls)
	}
	if strings.Contains(string(body), "browser-secret") {
		t.Fatalf("error leaked browser secret: %s", body)
	}
}

func TestLocalSecretRevealRequiresExplicitIntent(t *testing.T) {
	agent := &localSecretServer{}
	agentAddr, stop := startLocalSecretServer(t, agent)
	defer stop()
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: agentAddr, CloudURL: "http://127.0.0.1:1"}, nil))
	defer server.Close()
	session := localTestSession(t, server.URL)

	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/local/projects/proj-1/secrets/db/reveal", strings.NewReader(`{"service_id":"svc-1","totp_code":"123456"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Local-Session", session)
	req.Header.Set("Idempotency-Key", "secret-reveal-1")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if agent.revealCalls != 0 {
		t.Fatalf("reveal reached Agent without explicit intent")
	}
}

func TestLocalSecretRevealUsesAgentWithNoStorePolicy(t *testing.T) {
	agent := &localSecretServer{}
	agentAddr, stop := startLocalSecretServer(t, agent)
	defer stop()
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: agentAddr, CloudURL: "http://127.0.0.1:1"}, nil))
	defer server.Close()
	session := localTestSession(t, server.URL)

	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/local/projects/proj-1/secrets/db/reveal", strings.NewReader(`{"service_id":"svc-1","totp_code":"123456","reveal":true}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Local-Session", session)
	req.Header.Set("Idempotency-Key", "secret-reveal-2")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.StatusCode, body)
	}
	if got := res.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache-control = %q", got)
	}
	if agent.revealCalls != 1 || !strings.Contains(string(body), "agent-secret-password") || !strings.Contains(string(body), `"ttl_seconds":60`) {
		t.Fatalf("unexpected reveal calls=%d body=%s", agent.revealCalls, body)
	}
}

func TestLocalSecretAgentErrorIsRedacted(t *testing.T) {
	agent := &localSecretServer{err: status.Error(codes.Internal, "backend saw agent-secret-password")}
	agentAddr, stop := startLocalSecretServer(t, agent)
	defer stop()
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: agentAddr, CloudURL: "http://127.0.0.1:1"}, nil))
	defer server.Close()
	session := localTestSession(t, server.URL)

	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/local/projects/proj-1/secrets/db/rotate", strings.NewReader(`{"service_id":"svc-1","totp_code":"123456"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Local-Session", session)
	req.Header.Set("Idempotency-Key", "secret-rotate-1")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", res.StatusCode, body)
	}
	if strings.Contains(string(body), "agent-secret-password") {
		t.Fatalf("agent error leaked secret value: %s", body)
	}
}

func TestLocalTelemetrySummaryUsesAgentAndHidesRawPayload(t *testing.T) {
	agent := &localTelemetryServer{}
	agentAddr, stop := startCommandTelemetryServer(t, agent)
	defer stop()
	cloudCalled := false
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		cloudCalled = true
		w.WriteHeader(http.StatusTeapot)
	}))
	defer cloud.Close()
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: agentAddr, CloudURL: cloud.URL}, nil))
	defer server.Close()

	res, err := http.Get(server.URL + "/api/local/projects/proj-1/telemetry/summary?since_unix=41")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.StatusCode, body)
	}
	if cloudCalled {
		t.Fatal("telemetry summary must not call Cloud")
	}
	if strings.Contains(string(body), "raw-metric-password") {
		t.Fatalf("response leaked raw payload: %s", body)
	}
	var summary struct {
		ProjectID   string `json:"project_id"`
		SinceUnix   int64  `json:"since_unix"`
		ChunkCount  int    `json:"chunk_count"`
		RecordCount int    `json:"record_count"`
		Source      string `json:"source"`
	}
	if err := json.Unmarshal(body, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.ProjectID != "proj-1" || summary.SinceUnix != 41 || summary.RecordCount != 3 || summary.Source != "agent" {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	if agent.lastReceivedUnix != 41 {
		t.Fatalf("agent since_unix = %d", agent.lastReceivedUnix)
	}
}

func TestLocalLogsUseAgentNotCloudAndRedact(t *testing.T) {
	agent := &localTelemetryServer{}
	agentAddr, stop := startCommandTelemetryServer(t, agent)
	defer stop()
	cloudCalled := false
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		cloudCalled = true
		w.WriteHeader(http.StatusTeapot)
	}))
	defer cloud.Close()
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: agentAddr, CloudURL: cloud.URL}, nil))
	defer server.Close()

	res, err := http.Get(server.URL + "/api/local/projects/proj-1/logs?service_id=svc-1&limit=1")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.StatusCode, body)
	}
	if cloudCalled {
		t.Fatal("logs must not call Cloud")
	}
	if !agent.includeLogs || agent.serviceID != "svc-1" {
		t.Fatalf("agent logs request not used: include=%v service=%q", agent.includeLogs, agent.serviceID)
	}
	if strings.Contains(string(body), "super-secret") || strings.Contains(string(body), "browser-pat") {
		t.Fatalf("log response leaked secret-like value: %s", body)
	}
	if !strings.Contains(string(body), "[REDACTED]") {
		t.Fatalf("log response was not redacted: %s", body)
	}
}

func TestLocalTelemetryInvalidInputFailsClosed(t *testing.T) {
	agent := &localTelemetryServer{}
	agentAddr, stop := startCommandTelemetryServer(t, agent)
	defer stop()
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: agentAddr, CloudURL: "http://127.0.0.1:1"}, nil))
	defer server.Close()

	res, err := http.Get(server.URL + "/api/local/projects/proj-1/logs?cursor=not-a-time")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("status=%d body=%s", res.StatusCode, body)
	}
}

func TestLocalIncidentListUsesAgentNotCloud(t *testing.T) {
	agent := &localIncidentServer{}
	agentAddr, stop := startLocalIncidentServer(t, agent)
	defer stop()
	cloudCalled := false
	cloud := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		cloudCalled = true
	}))
	defer cloud.Close()
	store := keychain.NewFakeStore()
	if err := store.SetPAT("keychain-pat"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: agentAddr, CloudURL: cloud.URL}, func() (keychain.Store, error) { return store, nil }))
	defer server.Close()

	res, err := http.Get(server.URL + "/api/local/projects/proj-1/incidents")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || cloudCalled || agent.listCalls != 1 || agent.lastAuth != "Bearer keychain-pat" || !strings.Contains(string(body), `"incidents"`) {
		t.Fatalf("status=%d cloud=%v calls=%d body=%s", res.StatusCode, cloudCalled, agent.listCalls, body)
	}
}

func TestLocalIncidentDetailUsesAgentAndReturnsFactsOnly(t *testing.T) {
	agent := &localIncidentServer{}
	agentAddr, stop := startLocalIncidentServer(t, agent)
	defer stop()
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: agentAddr, CloudURL: "http://127.0.0.1:1"}, nil))
	defer server.Close()

	res, err := http.Get(server.URL + "/api/local/projects/proj-1/incidents/inc-1")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || agent.getCalls != 1 {
		t.Fatalf("status=%d body=%s", res.StatusCode, body)
	}
	var payload struct {
		Incident map[string]any `json:"incident"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{
		"incident_id": true, "project_id": true, "node_id": true, "service_id": true, "pod_id": true,
		"status": true, "severity": true, "anomaly_type": true, "created_at_unix": true,
		"resolved_at_unix": true, "mttr_seconds": true,
	}
	for field := range payload.Incident {
		if !allowed[field] {
			t.Fatalf("detail response contains non-factual field %q: %s", field, body)
		}
	}
}

func TestRemovedLocalIncidentRoutesReturnNotFound(t *testing.T) {
	agent := &localIncidentServer{}
	agentAddr, stop := startLocalIncidentServer(t, agent)
	defer stop()
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: agentAddr, CloudURL: "http://127.0.0.1:1"}, nil))
	defer server.Close()
	session := localTestSession(t, server.URL)

	for _, path := range []string{
		"/api/local/projects/proj-1/incidents/inc-1/analyze",
		"/api/local/projects/proj-1/incidents/inc-1/actions/scale/approve",
	} {
		req, err := http.NewRequest(http.MethodPost, server.URL+path, strings.NewReader(`{"user_id":"dev","role":"Developer"}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Local-Session", session)
		req.Header.Set("Idempotency-Key", "removed-route")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("removed route %s status=%d", path, res.StatusCode)
		}
	}
	if agent.resolveCalls != 0 {
		t.Fatalf("removed routes reached Agent: resolve_calls=%d", agent.resolveCalls)
	}
}

func TestLocalIncidentResolveRequiresSessionAndIdempotency(t *testing.T) {
	agent := &localIncidentServer{}
	agentAddr, stop := startLocalIncidentServer(t, agent)
	defer stop()
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: agentAddr, CloudURL: "http://127.0.0.1:1"}, nil))
	defer server.Close()

	newRequest := func() *http.Request {
		req, err := http.NewRequest(http.MethodPost, server.URL+"/api/local/projects/proj-1/incidents/inc-1/resolve", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		return req
	}
	res, err := http.DefaultClient.Do(newRequest())
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("resolve without session status=%d", res.StatusCode)
	}

	session := localTestSession(t, server.URL)
	req := newRequest()
	req.Header.Set("X-Local-Session", session)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("resolve without idempotency status=%d", res.StatusCode)
	}

	req = newRequest()
	req.Header.Set("X-Local-Session", session)
	req.Header.Set("Idempotency-Key", "incident-resolve-1")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK || agent.resolveCalls != 1 || agent.lastResolve.ProjectID != "proj-1" || agent.lastResolve.IncidentID != "inc-1" {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("status=%d calls=%d req=%+v body=%s", res.StatusCode, agent.resolveCalls, agent.lastResolve, body)
	}
}

func TestLocalSecretAndIncidentRejectBrowserAuthority(t *testing.T) {
	secretAgent := &localSecretServer{}
	secretAddr, stopSecret := startLocalSecretServer(t, secretAgent)
	defer stopSecret()
	incidentAgent := &localIncidentServer{}
	incidentAddr, stopIncident := startLocalIncidentServer(t, incidentAgent)
	defer stopIncident()
	store := keychain.NewFakeStore()
	if err := store.SetPAT("keychain-pat"); err != nil {
		t.Fatal(err)
	}
	factory := func() (keychain.Store, error) { return store, nil }

	tests := []struct {
		name      string
		agentAddr string
		method    string
		path      string
		body      string
	}{
		{name: "secret user", agentAddr: secretAddr, method: http.MethodPost, path: "/api/local/projects/proj-1/secrets", body: `{"service_id":"svc-1","name":"db","user_id":"owner"}`},
		{name: "secret role", agentAddr: secretAddr, method: http.MethodPost, path: "/api/local/projects/proj-1/secrets", body: `{"service_id":"svc-1","name":"db","role":"Owner"}`},
		{name: "secret pat", agentAddr: secretAddr, method: http.MethodPost, path: "/api/local/projects/proj-1/secrets", body: `{"service_id":"svc-1","name":"db","pat":"browser-pat"}`},
		{name: "secret query", agentAddr: secretAddr, method: http.MethodPost, path: "/api/local/projects/proj-1/secrets?role=Owner", body: `{"service_id":"svc-1","name":"db"}`},
		{name: "incident query", agentAddr: incidentAddr, method: http.MethodGet, path: "/api/local/projects/proj-1/incidents?role=Owner", body: ""},
		{name: "incident query mixed case", agentAddr: incidentAddr, method: http.MethodGet, path: "/api/local/projects/proj-1/incidents?User_ID=owner", body: ""},
		{name: "incident body", agentAddr: incidentAddr, method: http.MethodPost, path: "/api/local/projects/proj-1/incidents/inc-1/resolve", body: `{"pat":"browser-pat"}`},
		{name: "incident resolve query", agentAddr: incidentAddr, method: http.MethodPost, path: "/api/local/projects/proj-1/incidents/inc-1/resolve?pat=browser-pat", body: `{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: tt.agentAddr}, factory))
			defer server.Close()
			var body io.Reader
			if tt.body != "" {
				body = strings.NewReader(tt.body)
			}
			req, err := http.NewRequest(tt.method, server.URL+tt.path, body)
			if err != nil {
				t.Fatal(err)
			}
			if tt.method == http.MethodPost {
				req.Header.Set("X-Local-Session", localTestSession(t, server.URL))
				req.Header.Set("Idempotency-Key", "authority-reject")
			}
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			data, _ := io.ReadAll(res.Body)
			if res.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "CALLER_AUTHORITY_FORBIDDEN") || strings.Contains(string(data), "browser-pat") {
				t.Fatalf("status=%d body=%s", res.StatusCode, data)
			}
		})
	}
	if secretAgent.createCalls != 0 || incidentAgent.listCalls != 0 || incidentAgent.resolveCalls != 0 {
		t.Fatalf("authority payload reached Agent: secret=%d incident_list=%d incident_resolve=%d", secretAgent.createCalls, incidentAgent.listCalls, incidentAgent.resolveCalls)
	}
}

type localTelemetryServer struct {
	agentv1.UnimplementedTelemetryServiceServer
	lastReceivedUnix int64
	includeLogs      bool
	serviceID        string
}

func (s *localTelemetryServer) Sync(req *agentv1.SyncRequest, stream agentv1.TelemetryService_SyncServer) error {
	s.lastReceivedUnix = req.LastReceivedUnix
	return stream.Send(&agentv1.SyncChunk{
		ProjectID:   req.ProjectID,
		StartUnix:   req.LastReceivedUnix,
		EndUnix:     99,
		RecordCount: 3,
		Compression: "zstd",
		Payload:     []byte("raw-metric-password=secret"),
		Done:        true,
	})
}

func (s *localTelemetryServer) QueryTelemetry(_ context.Context, req *agentv1.TelemetryQueryRequest) (*agentv1.TelemetryQueryResponse, error) {
	s.lastReceivedUnix = req.SinceUnix
	s.includeLogs = req.IncludeLogs
	s.serviceID = req.ServiceID
	if req.Cursor != "" {
		return nil, status.Error(codes.InvalidArgument, "cursor is invalid")
	}
	resp := &agentv1.TelemetryQueryResponse{
		ProjectID:     req.ProjectID,
		Source:        "agent",
		PayloadPolicy: "raw telemetry payload remains local and is not returned to the browser",
	}
	if req.IncludeSummary {
		resp.Summary = &agentv1.TelemetryRuntimeSummary{SinceUnix: req.SinceUnix, EndUnix: 99, MetricCount: 2, LogCount: 1, ErrorCount: 1, ServiceCount: 1, Health: "degraded"}
	}
	if req.IncludeServices {
		resp.Services = []agentv1.TelemetryServiceStatus{{ServiceID: "svc-1", Health: "degraded", PodCount: 1, ReadyPods: 0, RestartCount: 2, RecentErrorCount: 1, LastSeenUnix: 99}}
	}
	if req.IncludeLogs {
		resp.Logs = []agentv1.TelemetryLogEntry{{ServiceID: "svc-1", PodID: "pod-1", Namespace: "app", Level: "error", Message: "password=super-secret Authorization: Bearer browser-pat", Fingerprint: "fp", ObservedUnix: 99}}
	}
	return resp, nil
}

func TestLocalSessionDoesNotExposePAT(t *testing.T) {
	store := keychain.NewFakeStore()
	const secret = "pat-secret-must-not-reach-browser"
	if err := store.SetPAT(secret); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Default(), func() (keychain.Store, error) {
		return store, nil
	}))
	defer server.Close()
	res, err := http.Get(server.URL + "/api/local/session")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), secret) {
		t.Fatalf("session leaked PAT: %s", body)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"pat", "token", "authorization"} {
		if _, ok := payload[key]; ok {
			t.Fatalf("session exposed credential field %q: %s", key, body)
		}
	}
}

func TestLocalSessionVerifiesPATBeforeReportingAuthenticated(t *testing.T) {
	store := keychain.NewFakeStore()
	if err := store.SetPAT("saved-pat"); err != nil {
		t.Fatal(err)
	}
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/auth/pat/verify" || r.Header.Get("Authorization") != "Bearer saved-pat" {
			t.Fatalf("unexpected verification request: path=%s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["project_id"] != "proj-1" {
			t.Fatalf("verification body=%v err=%v", body, err)
		}
		_, _ = w.Write([]byte(`{"user_id":"user-1","org_id":"org-1","project_id":"proj-1","role":"owner"}`))
	}))
	defer cloud.Close()
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: "127.0.0.1:1", CloudURL: cloud.URL}, func() (keychain.Store, error) {
		return store, nil
	}))
	defer server.Close()

	res, err := http.Get(server.URL + "/api/local/session?project_id=proj-1")
	if err != nil {
		t.Fatal(err)
	}
	var valid map[string]any
	if err := json.NewDecoder(res.Body).Decode(&valid); err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if valid["authenticated"] != true || valid["token_status"] != "valid" || valid["cloud_connected"] != "ok" {
		t.Fatalf("valid session=%v", valid)
	}

	invalidCloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"pat invalid"}`))
	}))
	defer invalidCloud.Close()
	invalidServer := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: "127.0.0.1:1", CloudURL: invalidCloud.URL}, func() (keychain.Store, error) {
		return store, nil
	}))
	defer invalidServer.Close()
	res, err = http.Get(invalidServer.URL + "/api/local/session?project_id=proj-1")
	if err != nil {
		t.Fatal(err)
	}
	var invalid map[string]any
	if err := json.NewDecoder(res.Body).Decode(&invalid); err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if invalid["authenticated"] != false || invalid["token_status"] != "invalid" || invalid["cloud_connected"] != "ok" {
		t.Fatalf("invalid session=%v", invalid)
	}
}

func TestLocalSessionResolvesProjectFromPATWithoutBrowserStorage(t *testing.T) {
	store := keychain.NewFakeStore()
	if err := store.SetPAT("saved-pat"); err != nil {
		t.Fatal(err)
	}
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/auth/pat/verify" || r.Header.Get("Authorization") != "Bearer saved-pat" {
			t.Fatalf("unexpected verification request: path=%s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["project_id"] != "" {
			t.Fatalf("verification body=%v err=%v", body, err)
		}
		_, _ = w.Write([]byte(`{"user_id":"user-1","org_id":"org-1","project_id":"proj-1","role":"owner"}`))
	}))
	defer cloud.Close()
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: "127.0.0.1:1", CloudURL: cloud.URL}, func() (keychain.Store, error) {
		return store, nil
	}))
	defer server.Close()
	res, err := http.Get(server.URL + "/api/local/session?verify=1")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var session map[string]any
	if err := json.NewDecoder(res.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	if session["authenticated"] != true || session["org_id"] != "org-1" || session["project_id"] != "proj-1" {
		t.Fatalf("projectless session=%v", session)
	}
}

func TestLocalProxySanitizesCloudAuthFailures(t *testing.T) {
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"pat invalid"}`))
	}))
	defer cloud.Close()
	store := keychain.NewFakeStore()
	if err := store.SetPAT("saved-pat"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: "127.0.0.1:1", CloudURL: cloud.URL}, func() (keychain.Store, error) {
		return store, nil
	}))
	defer server.Close()
	res, err := http.Get(server.URL + "/api/local/projects?org_id=org-1")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized || strings.Contains(string(body), "pat invalid") || !strings.Contains(string(body), "CLOUD_AUTH_REQUIRED") {
		t.Fatalf("sanitized auth failure status=%d body=%s", res.StatusCode, body)
	}
}

func TestLocalBrowserLoginRedeemsToKeychainWithoutBrowserPAT(t *testing.T) {
	store := keychain.NewFakeStore()
	var localState string
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth/browser/start":
			var req struct {
				LocalState string `json:"local_state"`
				ProjectID  string `json:"project_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			localState = req.LocalState
			if req.ProjectID != "" {
				t.Fatalf("browser login unexpectedly required project %q", req.ProjectID)
			}
			_, _ = w.Write([]byte(`{"auth_url":"https://cloud.example.test/login","status":"pending"}`))
		case "/v1/auth/browser/redeem":
			_, _ = w.Write([]byte(`{"token":"pat_secret_should_stay_local","session":{"user_id":"u","org_id":"org","project_id":"proj","role":"owner"}}`))
		default:
			t.Fatalf("unexpected Cloud path %s", r.URL.Path)
		}
	}))
	defer cloud.Close()
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: "127.0.0.1:1", CloudURL: cloud.URL}, func() (keychain.Store, error) {
		return store, nil
	}))
	defer server.Close()

	res, err := http.Post(server.URL+"/api/local/session/login/start", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if strings.Contains(string(body), "pat_secret") || localState == "" {
		t.Fatalf("login start leaked token or missed state: body=%s state=%q", body, localState)
	}

	client := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err = client.Get(server.URL + "/api/local/session/callback?code=grant-1&state=" + url.QueryEscape(localState))
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusFound {
		t.Fatalf("callback status = %d", res.StatusCode)
	}
	got, err := store.GetPAT()
	if err != nil {
		t.Fatal(err)
	}
	if got != "pat_secret_should_stay_local" {
		t.Fatalf("stored token = %q", got)
	}
}

func TestLocalBrowserLoginReturnsSanitizedFailureToUI(t *testing.T) {
	var localState string
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			LocalState string `json:"local_state"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		localState = req.LocalState
		_, _ = w.Write([]byte(`{"auth_url":"https://cloud.example.test/login","status":"pending"}`))
	}))
	defer cloud.Close()
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: "127.0.0.1:1", CloudURL: cloud.URL}, nil))
	defer server.Close()
	res, err := http.Post(server.URL+"/api/local/session/login/start", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	client := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err = client.Get(server.URL + "/api/local/session/callback?error=GITHUB_ACCOUNT_UNLINKED&state=" + url.QueryEscape(localState))
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/?auth_error=GITHUB_ACCOUNT_UNLINKED" {
		t.Fatalf("status=%d location=%q", res.StatusCode, res.Header.Get("Location"))
	}
	res, err = client.Get(server.URL + "/api/local/session/callback?error=GITHUB_ACCOUNT_UNLINKED&state=" + url.QueryEscape(localState))
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("reused failure state status=%d", res.StatusCode)
	}
}

func TestLocalPATRotateFailurePreservesOldToken(t *testing.T) {
	store := keychain.NewFakeStore()
	if err := store.SetPAT("old-pat"); err != nil {
		t.Fatal(err)
	}
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/auth/pat/rotate" {
			t.Fatalf("unexpected Cloud path %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer cloud.Close()
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: "127.0.0.1:1", CloudURL: cloud.URL}, func() (keychain.Store, error) {
		return store, nil
	}))
	defer server.Close()
	session := localTestSession(t, server.URL)

	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/local/session/token/rotate", strings.NewReader(`{"project_id":"proj"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Local-Session", session)
	req.Header.Set("Idempotency-Key", "rotate-1")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", res.StatusCode)
	}
	got, err := store.GetPAT()
	if err != nil {
		t.Fatal(err)
	}
	if got != "old-pat" {
		t.Fatalf("old token not preserved: %q", got)
	}
}

func TestLocalLogoutRevokesAndClearsKeychain(t *testing.T) {
	store := keychain.NewFakeStore()
	if err := store.SetPAT("old-pat"); err != nil {
		t.Fatal(err)
	}
	revoked := false
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/auth/pat/revoke" {
			t.Fatalf("unexpected Cloud path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer old-pat" {
			t.Fatalf("auth = %q", got)
		}
		revoked = true
		_, _ = w.Write([]byte(`{"revoked":true}`))
	}))
	defer cloud.Close()
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: "127.0.0.1:1", CloudURL: cloud.URL}, func() (keychain.Store, error) {
		return store, nil
	}))
	defer server.Close()
	session := localTestSession(t, server.URL)
	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/local/session/logout", strings.NewReader(`{"project_id":"proj"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Local-Session", session)
	req.Header.Set("Idempotency-Key", "logout-1")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || !revoked {
		t.Fatalf("status=%d revoked=%v", res.StatusCode, revoked)
	}
	if _, err := store.GetPAT(); err == nil {
		t.Fatal("token still present after logout")
	}
}

func TestBrowserUIDoesNotStorePATOrCallCloudDirectly(t *testing.T) {
	root := filepath.Clean("../../ui")
	forbidden := []string{"localStorage", "sessionStorage", "indexedDB", "document.cookie", "NEXT_PUBLIC_CLOUD", "CloudRegistryClient", "cloudURL", "localhost:9800", "127.0.0.1:9800"}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case "node_modules", "out", ".next":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".ts") && !strings.HasSuffix(path, ".tsx") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(data)
		for _, token := range forbidden {
			if strings.Contains(text, token) {
				t.Fatalf("%s contains forbidden browser auth/direct-cloud token %q", path, token)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

type localSecretServer struct {
	agentv1.UnimplementedSecretServiceServer
	createCalls int
	revealCalls int
	rotateCalls int
	lastReq     agentv1.SecretRequest
	lastAuth    string
	err         error
}

func (s *localSecretServer) CreateSecret(ctx context.Context, req *agentv1.SecretRequest) (*agentv1.SecretResponse, error) {
	s.createCalls++
	s.lastReq = *req
	s.lastAuth = localAuthHeader(ctx)
	if s.err != nil {
		return nil, s.err
	}
	return &agentv1.SecretResponse{ProjectID: req.ProjectID, ServiceID: req.ServiceID, Name: req.Name, Namespace: req.Namespace, Username: "agent-user", Password: "agent-secret-password"}, nil
}

func (s *localSecretServer) RevealSecret(ctx context.Context, req *agentv1.SecretRequest) (*agentv1.SecretResponse, error) {
	s.revealCalls++
	s.lastReq = *req
	s.lastAuth = localAuthHeader(ctx)
	if s.err != nil {
		return nil, s.err
	}
	return &agentv1.SecretResponse{ProjectID: req.ProjectID, ServiceID: req.ServiceID, Name: req.Name, Namespace: req.Namespace, Username: "agent-user", Password: "agent-secret-password"}, nil
}

func (s *localSecretServer) RotateSecret(ctx context.Context, req *agentv1.SecretRequest) (*agentv1.SecretResponse, error) {
	s.rotateCalls++
	s.lastReq = *req
	s.lastAuth = localAuthHeader(ctx)
	if s.err != nil {
		return nil, s.err
	}
	return &agentv1.SecretResponse{ProjectID: req.ProjectID, ServiceID: req.ServiceID, Name: req.Name, Namespace: req.Namespace, Username: "agent-user", Password: "agent-secret-password"}, nil
}

func localAuthHeader(ctx context.Context) string {
	md, _ := metadata.FromIncomingContext(ctx)
	values := md.Get("authorization")
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func startLocalSecretServer(t *testing.T, service agentv1.SecretServiceServer) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	agentv1.RegisterSecretServiceServer(server, service)
	go func() { _ = server.Serve(listener) }()
	return listener.Addr().String(), server.Stop
}

type localIncidentServer struct {
	agentv1.UnimplementedIncidentServiceServer
	listCalls    int
	getCalls     int
	resolveCalls int
	lastResolve  agentv1.IncidentResolveRequest
	lastAuth     string
	err          error
}

func (s *localIncidentServer) ListIncidents(ctx context.Context, req *agentv1.IncidentListRequest) (*agentv1.IncidentListResponse, error) {
	s.listCalls++
	s.lastAuth = localAuthHeader(ctx)
	if s.err != nil {
		return nil, s.err
	}
	return &agentv1.IncidentListResponse{Incidents: []agentv1.IncidentResponse{*localIncidentResponse(req.ProjectID, "inc-1")}}, nil
}

func (s *localIncidentServer) GetIncident(ctx context.Context, req *agentv1.IncidentGetRequest) (*agentv1.IncidentResponse, error) {
	s.getCalls++
	s.lastAuth = localAuthHeader(ctx)
	if s.err != nil {
		return nil, s.err
	}
	return localIncidentResponse(req.ProjectID, req.IncidentID), nil
}

func (s *localIncidentServer) ResolveIncident(ctx context.Context, req *agentv1.IncidentResolveRequest) (*agentv1.IncidentResponse, error) {
	s.resolveCalls++
	s.lastResolve = *req
	s.lastAuth = localAuthHeader(ctx)
	if s.err != nil {
		return nil, s.err
	}
	resp := localIncidentResponse(req.ProjectID, req.IncidentID)
	resp.Status = "resolved"
	return resp, nil
}

func localIncidentResponse(projectID, incidentID string) *agentv1.IncidentResponse {
	return &agentv1.IncidentResponse{
		ProjectID:     projectID,
		IncidentID:    incidentID,
		NodeID:        "node-1",
		ServiceID:     "svc-1",
		PodID:         "pod-1",
		Status:        "open",
		Severity:      "high",
		AnomalyType:   "crash_loop",
		CreatedAtUnix: 10,
	}
}

func startLocalIncidentServer(t *testing.T, service agentv1.IncidentServiceServer) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	agentv1.RegisterIncidentServiceServer(server, service)
	go func() { _ = server.Serve(listener) }()
	return listener.Addr().String(), server.Stop
}

func localTestSession(t *testing.T, baseURL string) string {
	t.Helper()
	var session struct {
		LocalSession string `json:"local_session"`
	}
	res, err := http.Get(baseURL + "/api/local/session")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if err := json.NewDecoder(res.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	if session.LocalSession == "" {
		t.Fatal("empty local session")
	}
	return session.LocalSession
}

func TestResolveUIDirUsesEnv(t *testing.T) {
	t.Setenv("OPSI_UI_DIR", "/tmp/opsi-ui")
	if got := resolveUIDir(); !strings.HasSuffix(got, "opsi-ui") {
		t.Fatalf("dir = %q", got)
	}
}
