package commands

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
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

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","service":"opsi-cli"}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>Opsi</title></head><body><main><h1>Opsi</h1><p>Ready</p></main></body></html>`))
	})

	server := &http.Server{
		Handler:           mux,
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
