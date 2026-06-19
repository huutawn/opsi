package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/opsi-dev/opsi/agent/internal/config"
	"github.com/opsi-dev/opsi/agent/internal/deploy"
	"github.com/opsi-dev/opsi/agent/internal/tlsconfig"
	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type StatusService struct {
	version   string
	startedAt time.Time
	cfg       config.Config
}

func NewStatusService(version string, startedAt time.Time, cfg config.Config) *StatusService {
	return &StatusService{version: version, startedAt: startedAt, cfg: cfg}
}

func (s *StatusService) Status(context.Context, *agentv1.StatusRequest) (*agentv1.StatusResponse, error) {
	return &agentv1.StatusResponse{
		Version:        s.version,
		UptimeSeconds:  int64(time.Since(s.startedAt).Seconds()),
		NodeID:         s.cfg.NodeID,
		Health:         "ok",
		CloudConnected: false,
		StartedAtUnix:  s.startedAt.Unix(),
	}, nil
}

type DeploymentService struct {
	cfg    config.Config
	engine *deploy.Engine
}

func NewDeploymentService(cfg config.Config, engine *deploy.Engine) *DeploymentService {
	return &DeploymentService{cfg: cfg, engine: engine}
}

func (s *DeploymentService) Deploy(req *agentv1.DeployRequest, stream agentv1.DeploymentService_DeployServer) error {
	resolved, err := deploy.RequestFromContract(req, s.cfg.Deployment)
	if err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	_, err = s.engine.Deploy(stream.Context(), resolved, func(event *deploy.ProgressEvent) error {
		out := &agentv1.ProgressEvent{
			OperationID: event.OperationID,
			ProjectID:   event.ProjectID,
			ServiceID:   event.ServiceID,
			ServiceName: event.ServiceName,
			Phase:       event.Phase,
			Message:     event.Message,
			Percent:     event.Percent,
		}
		if event.Err != nil {
			out.Error = &agentv1.ServiceError{Code: agentv1.ErrorCodeInternal, Message: event.Err.Error()}
		}
		return stream.Send(out)
	})
	if err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	return nil
}

func Run(ctx context.Context, cfg config.Config, version string, logger *slog.Logger) error {
	startedAt := time.Now().UTC()

	store, err := deploy.OpenSQLiteStore(cfg.SQLitePath)
	if err != nil {
		return err
	}
	defer store.Close()

	engineCfg, err := deploymentEngineConfig(cfg)
	if err != nil {
		return err
	}
	engine := deploy.NewEngine(store, engineCfg)

	grpcListener, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return err
	}
	defer grpcListener.Close()

	healthListener, err := net.Listen("tcp", cfg.HealthAddr)
	if err != nil {
		return err
	}
	defer healthListener.Close()

	creds, err := tlsconfig.ServerCredentials(cfg.TLS)
	if err != nil {
		return err
	}
	var grpcOptions []grpc.ServerOption
	if creds != nil {
		grpcOptions = append(grpcOptions, grpc.Creds(creds))
	} else {
		logger.Warn("gRPC TLS is not configured; use only for local development")
	}

	grpcServer := grpc.NewServer(grpcOptions...)
	agentv1.RegisterStatusServiceServer(grpcServer, NewStatusService(version, startedAt, cfg))
	agentv1.RegisterDeploymentServiceServer(grpcServer, NewDeploymentService(cfg, engine))

	healthServer := &http.Server{
		Handler:           healthHandler(version, startedAt, cfg),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 2)
	go func() {
		logger.Info("agent gRPC server listening", "addr", cfg.ListenAddr)
		if err := grpcServer.Serve(grpcListener); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			errCh <- err
		}
	}()
	go func() {
		logger.Info("agent health server listening", "addr", cfg.HealthAddr)
		if err := healthServer.Serve(healthListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		logger.Info("agent shutdown requested")
	case err := <-errCh:
		return err
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stopped := make(chan struct{})
	go func() {
		grpcServer.GracefulStop()
		close(stopped)
	}()

	select {
	case <-stopped:
	case <-shutdownCtx.Done():
		grpcServer.Stop()
	}

	return healthServer.Shutdown(shutdownCtx)
}

func deploymentEngineConfig(cfg config.Config) (deploy.EngineConfig, error) {
	rolloutTimeout := 10 * time.Minute
	if cfg.Deployment.RolloutTimeout != "" {
		parsed, err := time.ParseDuration(cfg.Deployment.RolloutTimeout)
		if err != nil {
			return deploy.EngineConfig{}, err
		}
		rolloutTimeout = parsed
	}
	pollInterval := 5 * time.Second
	if cfg.Deployment.PollInterval != "" {
		parsed, err := time.ParseDuration(cfg.Deployment.PollInterval)
		if err != nil {
			return deploy.EngineConfig{}, err
		}
		pollInterval = parsed
	}
	engineCfg := deploy.EngineConfig{
		Git:            deploy.ExecGitClient{},
		Builder:        deploy.ExecBuilder{},
		K3s:            deploy.KubectlAdapter{},
		BuildRoot:      cfg.Deployment.BuildRoot,
		RolloutTimeout: rolloutTimeout,
		PollInterval:   pollInterval,
	}
	if cfg.Deployment.DryRun {
		engineCfg.Git = deploy.DryRunGitClient{}
		engineCfg.Builder = deploy.DryRunBuilder{}
		engineCfg.K3s = deploy.DryRunK3sAdapter{}
	}
	return engineCfg, nil
}

func healthHandler(version string, startedAt time.Time, cfg config.Config) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":          "ok",
			"version":         version,
			"node_id":         cfg.NodeID,
			"uptime_seconds":  int64(time.Since(startedAt).Seconds()),
			"cloud_connected": false,
		})
	})
	return mux
}
