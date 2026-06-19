package tlsconfig

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	"github.com/opsi-dev/opsi/agent/internal/config"
	"google.golang.org/grpc/credentials"
)

func ServerCredentials(cfg config.TLSConfig) (credentials.TransportCredentials, error) {
	if !cfg.Enabled() {
		return nil, nil
	}
	if cfg.ServerCertPath == "" || cfg.ServerKeyPath == "" {
		return nil, fmt.Errorf("server certificate and key are required when TLS is enabled")
	}

	cert, err := tls.LoadX509KeyPair(cfg.ServerCertPath, cfg.ServerKeyPath)
	if err != nil {
		return nil, fmt.Errorf("load server key pair: %w", err)
	}

	tlsCfg := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
	}

	if cfg.RequireClientCert {
		pool, err := loadCertPool(cfg.CACertPath)
		if err != nil {
			return nil, err
		}
		tlsCfg.ClientCAs = pool
		tlsCfg.ClientAuth = tls.RequireAndVerifyClientCert
	}

	return credentials.NewTLS(tlsCfg), nil
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
