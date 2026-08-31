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
	backupdomain "github.com/opsi-dev/opsi/cloud/internal/backup"
	"github.com/opsi-dev/opsi/cloud/internal/bootstrapworker"
	"github.com/opsi-dev/opsi/cloud/internal/buildjob"
	"github.com/opsi-dev/opsi/cloud/internal/buildrecord"
	"github.com/opsi-dev/opsi/cloud/internal/cloudflare"
	cutoverdomain "github.com/opsi-dev/opsi/cloud/internal/cutover"
	"github.com/opsi-dev/opsi/cloud/internal/deploymentpolicy"
	"github.com/opsi-dev/opsi/cloud/internal/deploymentworkflow"
	"github.com/opsi-dev/opsi/cloud/internal/otp"
	"github.com/opsi-dev/opsi/cloud/internal/postgres"
	"github.com/opsi-dev/opsi/cloud/internal/publichostname"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
	"github.com/opsi-dev/opsi/cloud/internal/resource"
	restoredomain "github.com/opsi-dev/opsi/cloud/internal/restore"
	"github.com/opsi-dev/opsi/cloud/internal/sourcereport"
	"github.com/opsi-dev/opsi/cloud/internal/topology"
	"github.com/opsi-dev/opsi/cloud/internal/verificationstore"
	"github.com/opsi-dev/opsi/cloud/internal/webhookrelay"
)

var version = "dev"

const bootstrapRunnerPath = "/usr/local/bin/opsi-bootstrap-worker"

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
	if cfg.Cloudflare.Enabled() {
		client, clientErr := cloudflare.New(cloudflare.Options{ZoneID: cfg.Cloudflare.ZoneID, APIToken: cfg.Cloudflare.APIToken, Domain: cfg.DeploymentDomain})
		if clientErr != nil {
			return fmt.Errorf("configure Cloudflare: %w", clientErr)
		}
		startupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if clientErr = client.ValidateZone(startupCtx); clientErr != nil {
			return clientErr
		}
		if clientErr = client.ReconcileZoneRules(startupCtx); clientErr != nil {
			return fmt.Errorf("reconcile Cloudflare zone rules: %w", clientErr)
		}
		relay.Cloudflare = client
	}
	if cfg.BootstrapWorkerConfig != "" {
		install, err := bootstrapworker.LoadInstallConfig(cfg.BootstrapWorkerConfig)
		if err != nil {
			return fmt.Errorf("load bootstrap install config: %w", err)
		}
		if err := relay.SetBootstrapRunner(install, bootstrapRunnerPath); err != nil {
			return fmt.Errorf("configure bootstrap command runner: %w", err)
		}
	}
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
		relay.Auth = &auth.Service{Store: auth.PostgresStore{DB: db}}
		postgresRegistry := registry.PostgresService{DB: db}
		relay.Resources = resource.Service{Store: resource.PostgresStore{DB: db}, Scopes: postgresRegistry}
		postgresRegistry.DependencyResolver = webhookrelay.DependencyResolverAdapter{Registry: postgresRegistry, Resources: relay.Resources}
		relay.Registry = postgresRegistry
		relay.Backups.Store = backupdomain.PostgresStore{DB: db}
		relay.Restores.Store = restoredomain.PostgresStore{DB: db}
		relay.Cutovers.Store = cutoverdomain.PostgresStore{DB: db}
		relay.Backups.Resources = relay.Resources
		relay.Restores.Resources, relay.Restores.Backups, relay.Restores.Artifacts = relay.Resources, relay.Backups, relay.Backups.Artifacts
		relay.Cutovers.Applications, relay.Cutovers.Resources, relay.Cutovers.Restores, relay.Cutovers.Backups, relay.Cutovers.Credentials = postgresRegistry, relay.Resources, relay.Restores, relay.Backups, relay.Resources.Credentials
		relay.Resources.Operations = []resource.ActiveOperationAuthority{relay.Backups, relay.Restores}
		if cfg.BootstrapSecretKey != "" {
			credentialVault, vaultErr := webhookrelay.NewPostgresManagedResourceCredentialVault(db, cfg.BootstrapSecretKey)
			if vaultErr != nil {
				return fmt.Errorf("configure managed resource credential vault: %w", vaultErr)
			}
			relay.Resources.Credentials = credentialVault
			relay.Backups.Resources = relay.Resources
			relay.Restores.Resources = relay.Resources
			relay.Cutovers.Credentials = credentialVault
		}
		relay.BuildJobs.Store = buildjob.PostgresStore{DB: db}
		relay.BuildJobs.Sources = postgresRegistry
		relay.BuildRecords.Store = buildrecord.PostgresStore{DB: db}
		relay.BuildRecords.Bindings = postgresRegistry
		relay.SourceReports = sourcereport.PostgresStore{DB: db}
		relay.Verifications = verificationstore.PostgresStore{DB: db}
		relay.Topology = topology.Service{Store: topology.PostgresStore{DB: db}, Facts: postgresRegistry, HeartbeatTTL: time.Duration(cfg.Placement.HeartbeatTTL), ReservedCPU: cfg.Placement.ReservedCPUMilli, ReservedMemory: cfg.Placement.ReservedMemoryBytes}
		relay.Policies = deploymentpolicy.Service{Store: deploymentpolicy.PostgresStore{DB: db}, BuildRecords: relay.BuildRecords.Store, Bindings: postgresRegistry, Topology: relay.Topology}
		relay.DeploymentRuns.Store = deploymentworkflow.PostgresStore{DB: db}
		hostnameStore := publichostname.PostgresStore{DB: db}
		if err := hostnameStore.Backfill(context.Background(), cfg.DeploymentDomain); err != nil {
			return fmt.Errorf("backfill public hostname allocations: %w", err)
		}
		relay.PublicHostnames = publichostname.Service{Store: hostnameStore, Limit: cfg.PublicHostnameLimit}
		relay.Cutovers.Deployments = postgresRegistry
		relay.Cutovers.BuildRecords = relay.BuildRecords.Store
		relay.Cutovers.Topology = relay.Topology
		relay.Cutovers.Policies = relay.Policies
		relay.Cutovers.RuntimeResolver = relay.Resources
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
			if cfg.BuildRegistry.Visibility == "private" {
				pullVault, err := webhookrelay.NewPostgresRegistryPullCredentialVault(db, cfg.BootstrapSecretKey)
				if err != nil {
					return fmt.Errorf("configure registry pull credential vault: %w", err)
				}
				relay.RegistryPullCredentials = webhookrelay.NewGHCRRegistryPullCredentialProvider(cfg.BuildRegistry, pullVault, cfg.RegistryPull)
			}
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
	go relay.RunDeploymentWorkflow(ctx, fmt.Sprintf("cloud-%d", os.Getpid()))
	go relay.RunPublicHostnameReconciler(ctx)
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
