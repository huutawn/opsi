package commands

import (
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
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cli/internal/keychain"
	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
)

func TestTelemetryCommandUsesSelectedTLSAgentAndKeychainPAT(t *testing.T) {
	service := &commandTelemetryQueryServer{}
	addr, pin, stop := startTLSTelemetryAgent(t, service)
	defer stop()

	configPath := filepath.Join(t.TempDir(), "cli.yaml")
	configData := "agent_addr: " + addr + "\ncloud_url: https://cloud.example.test\ntls:\n  pinned_server_cert_sha256: " + pin + "\n  server_name: 127.0.0.1\n"
	if err := os.WriteFile(configPath, []byte(configData), 0o600); err != nil {
		t.Fatal(err)
	}
	store := keychain.NewFakeStore()
	if err := store.SetPAT("telemetry-pat-canary"); err != nil {
		t.Fatal(err)
	}
	command := NewRootCommand(Options{KeychainFactory: func() (keychain.Store, error) { return store, nil }})
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs([]string{"telemetry", "query", "--config", configPath, "--project-id", "proj-1", "--service-id", "svc-1", "--include-logs", "--limit", "7", "--json"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("telemetry stderr=%q", stderr.String())
	}
	var response agentv1.TelemetryQueryResponse
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil || response.ProjectID != "proj-1" || response.Source != "fake-tls-agent" {
		t.Fatalf("telemetry response=%+v err=%v", response, err)
	}
	if service.authorization != "Bearer telemetry-pat-canary" || service.request == nil || service.request.ServiceID != "svc-1" || !service.request.IncludeLogs || service.request.Limit != 7 {
		t.Fatalf("authorization=%q request=%+v", service.authorization, service.request)
	}
	if bytes.Contains(stdout.Bytes(), []byte("telemetry-pat-canary")) {
		t.Fatalf("telemetry stdout leaked PAT: %s", stdout.String())
	}
}

type commandTelemetryQueryServer struct {
	agentv1.UnimplementedTelemetryServiceServer
	authorization string
	request       *agentv1.TelemetryQueryRequest
}

func (s *commandTelemetryQueryServer) QueryTelemetry(ctx context.Context, request *agentv1.TelemetryQueryRequest) (*agentv1.TelemetryQueryResponse, error) {
	values, _ := metadata.FromIncomingContext(ctx)
	s.authorization = firstMetadata(values.Get("authorization"))
	s.request = request
	return &agentv1.TelemetryQueryResponse{ProjectID: request.ProjectID, Source: "fake-tls-agent", PayloadPolicy: "redacted"}, nil
}

func firstMetadata(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func startTLSTelemetryAgent(t *testing.T, service agentv1.TelemetryServiceServer) (string, string, func()) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "agent.test"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Minute),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
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
	agentv1.RegisterTelemetryServiceServer(server, service)
	go func() { _ = server.Serve(listener) }()
	fingerprint := sha256.Sum256(certificateDER)
	return listener.Addr().String(), hex.EncodeToString(fingerprint[:]), server.Stop
}
