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
	"github.com/opsi-dev/opsi/agent/internal/secret"
	"github.com/opsi-dev/opsi/agent/internal/telemetry"
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

type TelemetryService struct {
	store telemetry.Store
}

type SecretService struct {
	cfg     config.Config
	service *secret.Service
}

func NewTelemetryService(store telemetry.Store) *TelemetryService {
	return &TelemetryService{store: store}
}

func NewSecretService(cfg config.Config, service *secret.Service) *SecretService {
	return &SecretService{cfg: cfg, service: service}
}

func (s *SecretService) SetupTOTP(ctx context.Context, req *agentv1.SetupTOTPRequest) (*agentv1.SetupTOTPResponse, error) {
	secretValue, uri, err := s.service.SetupTOTP(ctx, secret.AuthContext{ProjectID: req.ProjectID, UserID: req.UserID, Role: secret.Role(req.Role), PAT: req.PAT})
	if err != nil {
		return nil, mapSecretError(err)
	}
	return &agentv1.SetupTOTPResponse{Secret: secretValue, URI: uri}, nil
}

func (s *SecretService) CreateSecret(ctx context.Context, req *agentv1.SecretRequest) (*agentv1.SecretResponse, error) {
	value, err := s.service.Create(ctx, authFromSecretRequest(req), refFromSecretRequest(req, s.cfg))
	if err != nil {
		return nil, mapSecretError(err)
	}
	return secretResponse(req, s.cfg, value, false), nil
}

func (s *SecretService) RevealSecret(ctx context.Context, req *agentv1.SecretRequest) (*agentv1.SecretResponse, error) {
	value, err := s.service.Reveal(ctx, authFromSecretRequest(req), refFromSecretRequest(req, s.cfg), req.OTPRequestID, req.OTPCode, req.TOTPCode)
	if err != nil {
		return nil, mapSecretError(err)
	}
	return secretResponse(req, s.cfg, value, true), nil
}

func (s *SecretService) RotateSecret(ctx context.Context, req *agentv1.SecretRequest) (*agentv1.SecretResponse, error) {
	value, err := s.service.Rotate(ctx, authFromSecretRequest(req), refFromSecretRequest(req, s.cfg), req.OTPRequestID, req.OTPCode, req.TOTPCode)
	if err != nil {
		return nil, mapSecretError(err)
	}
	return secretResponse(req, s.cfg, value, false), nil
}

func (s *TelemetryService) Sync(req *agentv1.SyncRequest, stream agentv1.TelemetryService_SyncServer) error {
	if req.ProjectID == "" {
		return status.Error(codes.InvalidArgument, "project_id is required")
	}
	since := time.Unix(req.LastReceivedUnix, 0).UTC()
	until := time.Now().UTC()
	records, err := s.store.SyncRecords(stream.Context(), req.ProjectID, since, until, req.ResourceIDs)
	if err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	chunks, err := telemetry.BuildChunks(stream.Context(), req.ProjectID, records, int(req.MaxChunkBytes))
	if err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	for _, chunk := range chunks {
		out := &agentv1.SyncChunk{
			ProjectID:      chunk.ProjectID,
			RecordCount:    int32(chunk.RecordCount),
			Compression:    chunk.Compression,
			ChecksumSHA256: chunk.ChecksumSHA256,
			Payload:        chunk.Payload,
			Done:           chunk.Done,
		}
		if !chunk.Start.IsZero() {
			out.StartUnix = chunk.Start.Unix()
		}
		if !chunk.End.IsZero() {
			out.EndUnix = chunk.End.Unix()
		}
		if err := stream.Send(out); err != nil {
			return err
		}
	}
	return nil
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

	telemetryStore, err := telemetry.OpenSQLiteStore(cfg.SQLitePath)
	if err != nil {
		return err
	}
	defer telemetryStore.Close()

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
	agentv1.RegisterTelemetryServiceServer(grpcServer, NewTelemetryService(telemetryStore))
	agentv1.RegisterSecretServiceServer(grpcServer, NewSecretService(cfg, secretService(cfg, telemetryStore)))

	healthServer := &http.Server{
		Handler:           healthHandler(version, startedAt, cfg),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 3)
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
	if cfg.Telemetry.Enabled {
		interval := 15 * time.Second
		if cfg.Telemetry.Interval != "" {
			parsed, err := time.ParseDuration(cfg.Telemetry.Interval)
			if err != nil {
				return err
			}
			interval = parsed
		}
		logSince := time.Minute
		if cfg.Telemetry.PodLogSince != "" {
			parsed, err := time.ParseDuration(cfg.Telemetry.PodLogSince)
			if err != nil {
				return err
			}
			logSince = parsed
		}
		fallback := telemetry.RuntimeCollector{
			ProjectID: cfg.Deployment.ProjectID,
			NodeID:    cfg.NodeID,
			ServiceID: cfg.Deployment.ServiceID,
		}
		runner := telemetry.Runner{
			Store: telemetryStore,
			Collector: telemetry.KubernetesCollector{
				ProjectID:    cfg.Deployment.ProjectID,
				NodeID:       cfg.NodeID,
				KubectlPath:  cfg.Telemetry.KubectlPath,
				LogTailLines: cfg.Telemetry.PodLogTail,
				LogSince:     logSince,
				Fallback:     fallback,
			},
			Interval: interval,
		}
		go func() {
			logger.Info("agent telemetry collector started", "interval", interval.String())
			if err := runner.Run(ctx); err != nil {
				errCh <- err
			}
		}()
	}

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

func secretService(cfg config.Config, audit secret.AuditSink) *secret.Service {
	timeout := 10 * time.Second
	if cfg.Secret.CloudOTPTimeout != "" {
		if parsed, err := time.ParseDuration(cfg.Secret.CloudOTPTimeout); err == nil {
			timeout = parsed
		}
	}
	return &secret.Service{
		Store:            secret.KubernetesSecretStore{KubectlPath: cfg.Secret.KubectlPath},
		Audit:            audit,
		OTP:              secret.HTTPOTPClient{Endpoint: cfg.CloudEndpoint},
		Encryption:       secret.StaticEncryptionVerifier(cfg.Secret.EncryptionAtRestConfirmed),
		CloudOTPTimeout:  timeout,
		TOTPSecretByUser: map[string]string{},
	}
}

func authFromSecretRequest(req *agentv1.SecretRequest) secret.AuthContext {
	return secret.AuthContext{ProjectID: req.ProjectID, UserID: req.UserID, Role: secret.Role(req.Role), PAT: req.PAT}
}

func refFromSecretRequest(req *agentv1.SecretRequest, cfg config.Config) secret.SecretRef {
	return secret.SecretRef{ProjectID: req.ProjectID, ServiceID: req.ServiceID, Name: req.Name, Namespace: firstNonEmpty(req.Namespace, cfg.Secret.Namespace, cfg.Deployment.Namespace)}
}

func secretResponse(req *agentv1.SecretRequest, cfg config.Config, value secret.SecretValue, includePassword bool) *agentv1.SecretResponse {
	resp := &agentv1.SecretResponse{ProjectID: req.ProjectID, ServiceID: req.ServiceID, Name: req.Name, Namespace: firstNonEmpty(req.Namespace, cfg.Secret.Namespace, cfg.Deployment.Namespace), Username: value.Username}
	if includePassword {
		resp.Password = value.Password
	}
	return resp
}

func mapSecretError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return status.Error(codes.DeadlineExceeded, err.Error())
	}
	message := err.Error()
	switch {
	case message == "permission denied":
		return status.Error(codes.PermissionDenied, message)
	case message == "second factor verification failed":
		return status.Error(codes.PermissionDenied, message)
	case message == "k3s encryption at rest is not confirmed":
		return status.Error(codes.FailedPrecondition, message)
	default:
		return status.Error(codes.Internal, message)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
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
