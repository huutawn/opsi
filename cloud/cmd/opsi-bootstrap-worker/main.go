package main

import (
	"context"
	"errors"
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
	check := flag.Bool("check", false, "validate config and exit")
	showVersion := flag.Bool("version", false, "print version")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
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
	if err := bootstrapworker.RunOnce(ctx, cfg); err != nil {
		logger.Error("bootstrap worker stopped", "error", registry.RedactString(err.Error()))
		if errors.Is(err, bootstrapworker.ErrRuntimeUnsupported) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}
