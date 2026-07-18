package agentclient

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"math/big"
	"net"
	"strings"
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

func TestVerifyRemoteTLSRejectsWrongPinAndServerName(t *testing.T) {
	addr, pin, stop := startPinnedTLSServer(t)
	defer stop()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	valid := config.Config{AgentAddr: addr, TLS: config.TLSConfig{PinnedServerCertSHA256: pin, ServerName: "127.0.0.1"}}
	if err := verifyRemoteTLS(ctx, valid); err != nil {
		t.Fatalf("verify valid TLS: %v", err)
	}

	wrongPin := valid
	wrongPin.TLS.PinnedServerCertSHA256 = strings.Repeat("a", 64)
	if err := verifyRemoteTLS(ctx, wrongPin); err == nil || !strings.Contains(err.Error(), "certificate pin mismatch") {
		t.Fatalf("wrong-pin error: %v", err)
	}

	wrongName := valid
	wrongName.TLS.ServerName = "wrong-agent-name.invalid"
	if err := verifyRemoteTLS(ctx, wrongName); err == nil || !strings.Contains(err.Error(), "certificate name mismatch") {
		t.Fatalf("wrong-server-name error: %v", err)
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

func startPinnedTLSServer(t *testing.T) (string, string, func()) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
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
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS13})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				_ = connection.(*tls.Conn).Handshake()
				_ = connection.Close()
			}()
		}
	}()
	fingerprint := sha256.Sum256(certificateDER)
	return listener.Addr().String(), hex.EncodeToString(fingerprint[:]), func() { _ = listener.Close() }
}
