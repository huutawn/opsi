package agentclient

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"

	"github.com/opsi-dev/opsi/cli/internal/config"
	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

type Client struct {
	cfg config.Config
}

func New(cfg config.Config) *Client { return &Client{cfg: cfg} }

func WithPAT(ctx context.Context, pat string) context.Context {
	if strings.TrimSpace(pat) == "" {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+pat)
}

func (c *Client) Status(ctx context.Context) (*agentv1.StatusResponse, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	return agentv1.NewStatusServiceClient(conn).Status(ctx, &agentv1.StatusRequest{})
}

func (c *Client) Deploy(ctx context.Context, req *agentv1.DeployRequest, onEvent func(*agentv1.ProgressEvent) error) error {
	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	stream, err := agentv1.NewDeploymentServiceClient(conn).Deploy(ctx, req)
	if err != nil {
		return err
	}
	for {
		event, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if onEvent != nil {
			if err := onEvent(event); err != nil {
				return err
			}
		}
	}
}

func (c *Client) Sync(ctx context.Context, req *agentv1.SyncRequest, onChunk func(*agentv1.SyncChunk) error) error {
	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	stream, err := agentv1.NewTelemetryServiceClient(conn).Sync(ctx, req)
	if err != nil {
		return err
	}
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if onChunk != nil {
			if err := onChunk(chunk); err != nil {
				return err
			}
		}
	}
}

func (c *Client) QueryTelemetry(ctx context.Context, req *agentv1.TelemetryQueryRequest) (*agentv1.TelemetryQueryResponse, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return agentv1.NewTelemetryServiceClient(conn).QueryTelemetry(ctx, req)
}

func (c *Client) SetupTOTP(ctx context.Context, req *agentv1.SetupTOTPRequest) (*agentv1.SetupTOTPResponse, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return agentv1.NewSecretServiceClient(conn).SetupTOTP(ctx, req)
}

func (c *Client) CreateSecret(ctx context.Context, req *agentv1.SecretRequest) (*agentv1.SecretResponse, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return agentv1.NewSecretServiceClient(conn).CreateSecret(ctx, req)
}

func (c *Client) RevealSecret(ctx context.Context, req *agentv1.SecretRequest) (*agentv1.SecretResponse, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return agentv1.NewSecretServiceClient(conn).RevealSecret(ctx, req)
}

func (c *Client) RotateSecret(ctx context.Context, req *agentv1.SecretRequest) (*agentv1.SecretResponse, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return agentv1.NewSecretServiceClient(conn).RotateSecret(ctx, req)
}

func (c *Client) ListCatalog(ctx context.Context) (*agentv1.ListCatalogResponse, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return agentv1.NewServiceManagerServiceClient(conn).ListCatalog(ctx, &agentv1.ListCatalogRequest{})
}

func (c *Client) CreateManagedService(ctx context.Context, req *agentv1.CreateManagedServiceRequest) (*agentv1.ManagedServiceResponse, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return agentv1.NewServiceManagerServiceClient(conn).CreateManagedService(ctx, req)
}

func (c *Client) RegisterExternalService(ctx context.Context, req *agentv1.RegisterExternalServiceRequest) (*agentv1.ManagedServiceResponse, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return agentv1.NewServiceManagerServiceClient(conn).RegisterExternalService(ctx, req)
}

func (c *Client) GetManagedService(ctx context.Context, req *agentv1.GetManagedServiceRequest) (*agentv1.ManagedServiceResponse, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return agentv1.NewServiceManagerServiceClient(conn).GetManagedService(ctx, req)
}

func (c *Client) DeleteManagedService(ctx context.Context, req *agentv1.DeleteManagedServiceRequest) (*agentv1.DeleteManagedServiceResponse, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return agentv1.NewServiceManagerServiceClient(conn).DeleteManagedService(ctx, req)
}

func (c *Client) ListIncidents(ctx context.Context, req *agentv1.IncidentListRequest) (*agentv1.IncidentListResponse, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return agentv1.NewIncidentServiceClient(conn).ListIncidents(ctx, req)
}

func (c *Client) GetIncident(ctx context.Context, req *agentv1.IncidentGetRequest) (*agentv1.IncidentResponse, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return agentv1.NewIncidentServiceClient(conn).GetIncident(ctx, req)
}

func (c *Client) ResolveIncident(ctx context.Context, req *agentv1.IncidentResolveRequest) (*agentv1.IncidentResponse, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return agentv1.NewIncidentServiceClient(conn).ResolveIncident(ctx, req)
}

func (c *Client) dial(ctx context.Context) (*grpc.ClientConn, error) {
	creds, err := transportCredentials(c.cfg)
	if err != nil {
		return nil, err
	}
	conn, err := grpc.DialContext(ctx, c.cfg.AgentAddr, grpc.WithTransportCredentials(creds), grpc.WithBlock())
	if err != nil {
		return nil, fmt.Errorf("connect agent: %w", err)
	}
	return conn, nil
}

func transportCredentials(cfg config.Config) (credentials.TransportCredentials, error) {
	if !cfg.TLS.Enabled() {
		if !isLoopback(cfg.AgentAddr) {
			return nil, errors.New("non-loopback Agent connections require TLS certificate pinning")
		}
		return insecure.NewCredentials(), nil
	}

	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS13}
	serverName := cfg.TLS.ServerName
	if serverName == "" {
		host, _, err := net.SplitHostPort(cfg.AgentAddr)
		if err != nil {
			serverName = cfg.AgentAddr
		} else {
			serverName = host
		}
	}
	tlsCfg.ServerName = serverName

	if cfg.TLS.CACertPath != "" {
		pool, err := loadCertPool(cfg.TLS.CACertPath)
		if err != nil {
			return nil, err
		}
		tlsCfg.RootCAs = pool
	}

	if cfg.TLS.ClientCertPath != "" {
		cert, err := tls.LoadX509KeyPair(cfg.TLS.ClientCertPath, cfg.TLS.ClientKeyPath)
		if err != nil {
			return nil, fmt.Errorf("load client key pair: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}

	if cfg.TLS.PinnedServerCertSHA256 != "" {
		expected := normalizeFingerprint(cfg.TLS.PinnedServerCertSHA256)
		if len(expected) != sha256.Size*2 {
			return nil, errors.New("server certificate pin must be a SHA-256 fingerprint")
		}
		tlsCfg.InsecureSkipVerify = cfg.TLS.CACertPath == ""
		tlsCfg.VerifyConnection = func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return fmt.Errorf("server certificate pin mismatch: no peer certificate")
			}
			actualBytes := sha256.Sum256(state.PeerCertificates[0].Raw)
			actual := hex.EncodeToString(actualBytes[:])
			if actual != expected {
				return fmt.Errorf("server certificate pin mismatch")
			}
			if err := state.PeerCertificates[0].VerifyHostname(serverName); err != nil {
				return fmt.Errorf("server certificate name mismatch")
			}
			return nil
		}
	}

	return credentials.NewTLS(tlsCfg), nil
}

func isLoopback(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func loadCertPool(path string) (*x509.CertPool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read CA cert: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		return nil, fmt.Errorf("parse CA cert: no certificates found")
	}
	return pool, nil
}

func normalizeFingerprint(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, ":", "")
	return value
}
