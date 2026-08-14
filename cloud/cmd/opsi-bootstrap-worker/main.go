package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/opsi-dev/opsi/cloud/internal/bootstrapworker"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
)

var version = "dev"

func main() {
	configPath := flag.String("config", "", "bootstrap worker JSON config path")
	claimURL := flag.String("claim-url", "", "claim one reviewed bootstrap session")
	check := flag.Bool("check", false, "validate config and exit")
	showVersion := flag.Bool("version", false, "print version")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if *claimURL != "" {
		token := os.Getenv("OPSI_BOOTSTRAP_TOKEN")
		_ = os.Unsetenv("OPSI_BOOTSTRAP_TOKEN")
		if token == "" {
			logger.Error("claim bootstrap", "error", "OPSI_BOOTSTRAP_TOKEN is required")
			os.Exit(1)
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := bootstrapworker.RunClaimed(ctx, *claimURL, token, logger); err != nil {
			logger.Error("bootstrap session stopped", "error", registry.RedactString(err.Error()))
			os.Exit(1)
		}
		return
	}
	if *configPath == "" {
		logger.Error("load config", "error", "config path is required")
		os.Exit(1)
	}
	cfg, err := bootstrapworker.LoadConfig(*configPath)
	if err != nil {
		logger.Error("load config", "error", registry.RedactString(err.Error()))
		os.Exit(1)
	}
	if *check {
		if err := cfg.Validate(); err != nil {
			logger.Error("validate config", "error", registry.RedactString(err.Error()))
			os.Exit(1)
		}
		logger.Info("bootstrap worker config valid")
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := bootstrapworker.Run(ctx, cfg); err != nil {
		logger.Error("bootstrap worker stopped", "error", registry.RedactString(err.Error()))
		os.Exit(1)
	}
}
