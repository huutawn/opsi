package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/opsi-dev/opsi/cli/internal/agentclient"
	"github.com/opsi-dev/opsi/cli/internal/config"
	"github.com/opsi-dev/opsi/cli/ui"
	"github.com/spf13/cobra"
)

func newStartCommand(configPath *string) *cobra.Command {
	var addr, devUI string
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the local Opsi web server",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runStart(cmd.Context(), addr, devUI, *configPath, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:9780", "local web server address")
	cmd.Flags().StringVar(&devUI, "dev-ui", "", "proxy UI requests to a local dev server")
	return cmd
}

func runStart(ctx context.Context, addr, devUI, configPath string, out io.Writer) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	defer listener.Close()

	server := &http.Server{
		Handler:           newStartMux(resolveUIDir(), devUI, cfg),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Serve(listener)
	}()

	_, _ = fmt.Fprintf(out, "Local Web UI listening on http://%s\n", listener.Addr().String())

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

func newStartMux(uiDir, devUI string, cfg config.Config) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","service":"opsi-cli"}`))
	})
	mux.HandleFunc("/api/local/status", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 800*time.Millisecond)
		defer cancel()
		status, err := agentclient.New(cfg).Status(ctx)
		w.Header().Set("content-type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(status)
	})
	mux.Handle("/", newUIHandler(uiDir, devUI))
	return mux
}

func newUIHandler(uiDir, devUI string) http.Handler {
	if devUI != "" {
		target, err := url.Parse(devUI)
		if err != nil || target.Scheme == "" || target.Host == "" {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "invalid --dev-ui URL", http.StatusBadRequest)
			})
		}
		return httputil.NewSingleHostReverseProxy(target)
	}
	if _, err := os.Stat(filepath.Join(uiDir, "index.html")); err != nil {
		static, embedErr := ui.Static()
		if embedErr == nil {
			return http.FileServer(http.FS(static))
		}
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "Opsi UI build not found. Run `npm run build` in cli/ui first.", http.StatusServiceUnavailable)
		})
	}
	return http.FileServer(http.Dir(uiDir))
}

func resolveUIDir() string {
	if dir := os.Getenv("OPSI_UI_DIR"); dir != "" {
		return dir
	}
	candidates := []string{"ui/out", "cli/ui/out"}
	if _, file, _, ok := runtime.Caller(0); ok {
		candidates = append(candidates, filepath.Join(filepath.Dir(file), "..", "..", "ui", "out"))
	}
	for _, dir := range candidates {
		if _, err := os.Stat(filepath.Join(dir, "index.html")); err == nil {
			return dir
		}
	}
	return candidates[0]
}
