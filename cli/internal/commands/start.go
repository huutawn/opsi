package commands

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/spf13/cobra"
)

func newStartCommand() *cobra.Command {
	var addr string
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the local Opsi web server",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runStart(cmd.Context(), addr, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:9780", "local web server address")
	return cmd
}

func runStart(ctx context.Context, addr string, out io.Writer) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	defer listener.Close()

	server := &http.Server{
		Handler:           newStartMux(resolveUIDir()),
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

func newStartMux(uiDir string) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","service":"opsi-cli"}`))
	})
	mux.Handle("/", newUIHandler(uiDir))
	return mux
}

func newUIHandler(uiDir string) http.Handler {
	if _, err := os.Stat(filepath.Join(uiDir, "index.html")); err != nil {
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
