package tlsconfig

import (
	"testing"

	"github.com/opsi-dev/opsi/agent/internal/config"
)

func TestServerCredentialsRejectsPartialTLSConfig(t *testing.T) {
	_, err := ServerCredentials(config.TLSConfig{RequireClientCert: true})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestServerCredentialsAllowsLocalInsecureWhenUnset(t *testing.T) {
	creds, err := ServerCredentials(config.TLSConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if creds != nil {
		t.Fatal("expected nil credentials")
	}
}
