package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/opsi-dev/opsi/cloud/internal/auth"
	"github.com/opsi-dev/opsi/cloud/internal/buildrecord"
	"github.com/opsi-dev/opsi/cloud/internal/deploymentpolicy"
	"github.com/opsi-dev/opsi/cloud/internal/otp"
	"github.com/opsi-dev/opsi/cloud/internal/postgres"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
	"github.com/opsi-dev/opsi/cloud/internal/topology"
	"github.com/opsi-dev/opsi/cloud/internal/webhookrelay"
)

var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "admin" {
		return runAdmin(args[1:], stdout, stderr)
	}
	fs := flag.NewFlagSet("opsi-cloud", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", "127.0.0.1:9800", "HTTP listen address")
	configPath := fs.String("config", "", "cloud JSON config path")
	showVersion := fs.Bool("version", false, "print version")
	check := fs.Bool("check", false, "validate configuration and exit")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "unexpected arguments")
		return 2
	}
	if *showVersion {
		fmt.Fprintln(stdout, version)
		return 0
	}
	cfg, err := webhookrelay.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	var githubAppClient *webhookrelay.GitHubAppClient
	if cfg.GitHubApp.InstallationEnabled() {
		githubAppClient, err = webhookrelay.NewGitHubAppClient(cfg.GitHubApp, nil, nil)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	if *check {
		fmt.Fprintln(stdout, "configuration valid")
		return 0
	}
	if err := serveCloud(*addr, cfg, githubAppClient, stderr); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func serveCloud(addr string, cfg webhookrelay.Config, githubAppClient *webhookrelay.GitHubAppClient, stderr io.Writer) error {
	logger := slog.New(slog.NewTextHandler(stderr, nil))
	relay := webhookrelay.NewServer(cfg)
	if githubAppClient != nil {
		relay.SetGitHubAppClient(githubAppClient)
		logger.Info("GitHub App signer loaded", "app_id", cfg.GitHubApp.AppID)
	}
	var db *sql.DB
	var err error
	if cfg.DatabaseURL != "" {
		db, err = sql.Open("pgx", cfg.DatabaseURL)
		if err != nil {
			return fmt.Errorf("open postgres: %w", err)
		}
		defer db.Close()
		if err := postgres.Migrate(context.Background(), db); err != nil {
			return fmt.Errorf("migrate postgres: %w", err)
		}
		relay.Queue = webhookrelay.NewPostgresQueue(db)
		relay.Auth = &auth.Service{Store: auth.PostgresStore{DB: db}}
		postgresRegistry := registry.PostgresService{DB: db}
		relay.Registry = postgresRegistry
		relay.BuildRecords.Store = buildrecord.PostgresStore{DB: db}
		relay.BuildRecords.Bindings = postgresRegistry
		relay.Topology = topology.Service{Store: topology.PostgresStore{DB: db}, Facts: postgresRegistry, HeartbeatTTL: time.Duration(cfg.Placement.HeartbeatTTL), ReservedCPU: cfg.Placement.ReservedCPUMilli, ReservedMemory: cfg.Placement.ReservedMemoryBytes}
		relay.Policies = deploymentpolicy.Service{Store: deploymentpolicy.PostgresStore{DB: db}, BuildRecords: relay.BuildRecords.Store, Bindings: postgresRegistry, Topology: relay.Topology}
		relay.BuildRecords.AuditSink = func(event buildrecord.AuditEvent) {
			postgresRegistry.AuditWorkload(event.ProjectID, "BUILD_RECORD_SUBMITTED", event.RecordID, event.Result, map[string]any{"repository_id": event.RepositoryID, "run_id": event.RunID, "run_attempt": event.RunAttempt, "service_key": event.ServiceKey, "sha": event.SHA, "config_hash": event.ConfigHash, "oci_digest": event.OCIDigest})
		}
		relay.OTP.Store = otp.PostgresStore{DB: db}
		relay.SetHealthCheck(db.PingContext)
		if cfg.BootstrapSecretKey != "" {
			credentials, err := webhookrelay.NewPostgresCredentialVault(db, cfg.BootstrapSecretKey)
			if err != nil {
				return fmt.Errorf("configure credential vault: %w", err)
			}
			registrations, err := webhookrelay.NewPostgresRegistrationVault(db, cfg.BootstrapSecretKey)
			if err != nil {
				return fmt.Errorf("configure registration vault: %w", err)
			}
			relay.SetSecurityStores(credentials, registrations, webhookrelay.NewPostgresRateLimiter(db))
		}
	}
	if err := configureGitHubAppEventSink(relay, cfg); err != nil {
		return err
	}
	if cfg.SMTP.Host != "" {
		relay.OTP.Sender = otp.SMTPSender{Config: otp.SMTPConfig{Host: cfg.SMTP.Host, Port: cfg.SMTP.Port, Username: cfg.SMTP.Username, Password: cfg.SMTP.Password, From: cfg.SMTP.From}}
	} else if cfg.OTP.OutboxPath != "" {
		relay.OTP.Sender = otp.FileOutboxSender{Path: cfg.OTP.OutboxPath}
	}

	server := &http.Server{
		Addr:              addr,
		Handler:           relay.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      45 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() {
		logger.Info("cloud relay listening", "addr", addr)
		errCh <- server.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("server failed: %w", err)
		}
	}
	return nil
}

func configureGitHubAppEventSink(relay *webhookrelay.Server, cfg webhookrelay.Config) error {
	if !cfg.GitHubApp.InstallationEnabled() {
		return nil
	}
	if relay == nil || relay.Registry == nil {
		return fmt.Errorf("configure GitHub App event sink: registry is unavailable")
	}
	relay.SetGitHubAppEventSink(webhookrelay.RegistryGitHubAppEventSink{Registry: relay.Registry})
	return nil
}
