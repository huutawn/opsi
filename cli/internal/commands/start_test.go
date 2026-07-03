package commands

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opsi-dev/opsi/cli/internal/config"
)

func TestStartMuxServesHealthAndBuiltUI(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html><title>Opsi Console</title>"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(newStartMux(dir, "", config.Default()))
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

func TestStartMuxServesEmbeddedUIWhenDirMissing(t *testing.T) {
	server := httptest.NewServer(newStartMux(filepath.Join(t.TempDir(), "missing"), "", config.Default()))
	defer server.Close()

	res, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
}

func TestStartMuxLocalStatusReportsAgentUnavailable(t *testing.T) {
	server := httptest.NewServer(newStartMux(t.TempDir(), "", config.Config{AgentAddr: "127.0.0.1:1"}))
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
	server := httptest.NewServer(newStartMux(t.TempDir(), dev.URL, config.Default()))
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

func TestResolveUIDirUsesEnv(t *testing.T) {
	t.Setenv("OPSI_UI_DIR", "/tmp/opsi-ui")
	if got := resolveUIDir(); !strings.HasSuffix(got, "opsi-ui") {
		t.Fatalf("dir = %q", got)
	}
}
