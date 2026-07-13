package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/opsi-dev/opsi/agent/internal/config"
	"github.com/opsi-dev/opsi/agent/internal/server"
)

var version = "dev"
var commit = "unknown"

type serverRunner func(
	context.Context,
	config.Config,
	string,
	*slog.Logger,
) error

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, server.Run))
}

func run(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	serve serverRunner,
) int {
	fs := flag.NewFlagSet("opsi-agent", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "path to Agent YAML configuration")
	check := fs.Bool("check", false, "validate configuration and exit")
	showVersion := fs.Bool("version", false, "print version and exit")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: opsi-agent --config <path> [--check]")
		fmt.Fprintln(stderr, "       opsi-agent --version")
		fmt.Fprintln(stderr, "options:")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "unexpected positional argument: %s\n", fs.Arg(0))
		fs.Usage()
		return 2
	}
	if *showVersion {
		fmt.Fprintf(stdout, "opsi-agent version=%s commit=%s\n", version, commit)
		return 0
	}
	if *configPath == "" {
		fmt.Fprintln(stderr, "--config is required")
		fs.Usage()
		return 2
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "load config: %v\n", err)
		return 1
	}
	if *check {
		fmt.Fprintln(stdout, "configuration valid")
		return 0
	}

	logger := slog.New(slog.NewJSONHandler(stderr, nil))
	runCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := serve(runCtx, cfg, version, logger); err != nil {
		logger.Error("agent stopped", "error", err)
		return 1
	}
	return 0
}
