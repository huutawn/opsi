package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/opsi-dev/opsi/agent/internal/config"
	"github.com/opsi-dev/opsi/agent/internal/deploy"
	"github.com/opsi-dev/opsi/agent/internal/incident"
	"github.com/opsi-dev/opsi/agent/internal/secret"
	"github.com/opsi-dev/opsi/agent/internal/svcatalog"
	"github.com/opsi-dev/opsi/agent/internal/telemetry"
	"github.com/opsi-dev/opsi/agent/internal/tlsconfig"
	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
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
	auth   secret.AuthVerifier
}

type TelemetryService struct {
	store telemetry.Store
	auth  secret.AuthVerifier
}

type SecretService struct {
	cfg     config.Config
	service *secret.Service
}

type IncidentService struct {
	service *incident.Service
}

type ServiceManagerService struct {
	store   *svcatalog.Store
	manager svcatalog.Manager
	auth    secret.AuthVerifier
}

func NewTelemetryService(store telemetry.Store, auth secret.AuthVerifier) *TelemetryService {
	return &TelemetryService{store: store, auth: auth}
}

func NewSecretService(cfg config.Config, service *secret.Service) *SecretService {
	return &SecretService{cfg: cfg, service: service}
}

func NewIncidentService(service *incident.Service) *IncidentService {
	return &IncidentService{service: service}
}

func NewServiceManagerService(store *svcatalog.Store, manager svcatalog.Manager, auth secret.AuthVerifier) *ServiceManagerService {
	return &ServiceManagerService{store: store, manager: manager, auth: auth}
}

func (s *ServiceManagerService) ListCatalog(context.Context, *agentv1.ListCatalogRequest) (*agentv1.ListCatalogResponse, error) {
	catalog := svcatalog.BuiltInCatalog()
	resp := &agentv1.ListCatalogResponse{}
	for _, serviceType := range catalog.Types() {
		schema, _ := catalog.Get(serviceType)
		item := agentv1.CatalogService{
			Type:             schema.Type,
			DisplayName:      schema.DisplayName,
			ManagedSupported: svcatalog.ManagedSupported(schema.Type),
		}
		for _, key := range schema.ConfigKeys {
			item.ConfigKeys = append(item.ConfigKeys, agentv1.CatalogConfigKey{Key: key.Key, Default: key.Default, Required: key.Required})
		}
		for _, key := range schema.SecretKeys {
			item.SecretKeys = append(item.SecretKeys, key.Key)
		}
		item.EnvVars = svcatalog.SortedEnvKeys(schema.EnvMapping)
		resp.Services = append(resp.Services, item)
	}
	return resp, nil
}

func (s *ServiceManagerService) CreateManagedService(ctx context.Context, req *agentv1.CreateManagedServiceRequest) (*agentv1.ManagedServiceResponse, error) {
	if req.ProjectID == "" {
		return nil, status.Error(codes.InvalidArgument, "project_id is required")
	}
	if s.auth != nil {
		if _, err := s.auth.VerifyAuth(ctx, secret.AuthContext{ProjectID: req.ProjectID, PAT: bearerToken(ctx)}); err != nil {
			return nil, status.Error(codes.Unauthenticated, err.Error())
		}
	}
	service, err := s.manager.CreateManaged(ctx, svcatalog.CreateManagedRequest{ProjectID: req.ProjectID, Name: req.Name, Type: req.Type, Namespace: req.Namespace, Overrides: req.Overrides})
	if err != nil {
		return nil, mapServiceCatalogError(err)
	}
	return managedServiceResponse(service), nil
}

func (s *ServiceManagerService) GetManagedService(ctx context.Context, req *agentv1.GetManagedServiceRequest) (*agentv1.ManagedServiceResponse, error) {
	if req.ProjectID == "" || req.ID == "" {
		return nil, status.Error(codes.InvalidArgument, "project_id and id are required")
	}
	if s.auth != nil {
		if _, err := s.auth.VerifyAuth(ctx, secret.AuthContext{ProjectID: req.ProjectID, PAT: bearerToken(ctx)}); err != nil {
			return nil, status.Error(codes.Unauthenticated, err.Error())
		}
	}
	service, err := s.store.GetManagedService(ctx, req.ProjectID, req.ID)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if service == nil {
		return nil, status.Error(codes.NotFound, "managed service not found")
	}
	return managedServiceResponse(service), nil
}

func (s *IncidentService) AnalyzeIncident(ctx context.Context, req *agentv1.IncidentAnalyzeRequest) (*agentv1.IncidentResponse, error) {
	req.PAT = firstNonEmpty(req.PAT, bearerToken(ctx))
	rec, rca, err := s.service.Analyze(ctx, incident.AnalyzeRequest{ProjectID: req.ProjectID, IncidentID: req.IncidentID, UserID: req.UserID, Role: req.Role, PAT: req.PAT})
	if err != nil {
		return nil, mapIncidentError(err)
	}
	return incidentResponse(rec, rca), nil
}

func (s *IncidentService) ApproveIncidentAction(ctx context.Context, req *agentv1.IncidentActionRequest) (*agentv1.IncidentResponse, error) {
	req.PAT = firstNonEmpty(req.PAT, bearerToken(ctx))
	rec, rca, err := s.service.Approve(ctx, incident.ActionRequest{ProjectID: req.ProjectID, IncidentID: req.IncidentID, ActionID: req.ActionID, UserID: req.UserID, Role: req.Role, PAT: req.PAT})
	if err != nil {
		return nil, mapIncidentError(err)
	}
	return incidentResponse(rec, rca), nil
}

func (s *IncidentService) ResolveIncident(ctx context.Context, req *agentv1.IncidentActionRequest) (*agentv1.IncidentResponse, error) {
	req.PAT = firstNonEmpty(req.PAT, bearerToken(ctx))
	rec, rca, err := s.service.Resolve(ctx, incident.ActionRequest{ProjectID: req.ProjectID, IncidentID: req.IncidentID, UserID: req.UserID, Role: req.Role, PAT: req.PAT})
	if err != nil {
		return nil, mapIncidentError(err)
	}
	return incidentResponse(rec, rca), nil
}

func (s *SecretService) SetupTOTP(ctx context.Context, req *agentv1.SetupTOTPRequest) (*agentv1.SetupTOTPResponse, error) {
	secretValue, uri, err := s.service.SetupTOTP(ctx, secret.AuthContext{ProjectID: req.ProjectID, UserID: req.UserID, Role: secret.Role(req.Role), PAT: firstNonEmpty(req.PAT, bearerToken(ctx))})
	if err != nil {
		return nil, mapSecretError(err)
	}
	return &agentv1.SetupTOTPResponse{Secret: secretValue, URI: uri}, nil
}

func (s *SecretService) CreateSecret(ctx context.Context, req *agentv1.SecretRequest) (*agentv1.SecretResponse, error) {
	req.PAT = firstNonEmpty(req.PAT, bearerToken(ctx))
	value, err := s.service.Create(ctx, authFromSecretRequest(req), refFromSecretRequest(req, s.cfg))
	if err != nil {
		return nil, mapSecretError(err)
	}
	return secretResponse(req, s.cfg, value, false), nil
}

func (s *SecretService) RevealSecret(ctx context.Context, req *agentv1.SecretRequest) (*agentv1.SecretResponse, error) {
	req.PAT = firstNonEmpty(req.PAT, bearerToken(ctx))
	value, err := s.service.Reveal(ctx, authFromSecretRequest(req), refFromSecretRequest(req, s.cfg), req.OTPRequestID, req.OTPCode, req.TOTPCode)
	if err != nil {
		return nil, mapSecretError(err)
	}
	return secretResponse(req, s.cfg, value, true), nil
}

func (s *SecretService) RotateSecret(ctx context.Context, req *agentv1.SecretRequest) (*agentv1.SecretResponse, error) {
	req.PAT = firstNonEmpty(req.PAT, bearerToken(ctx))
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
	if s.auth != nil {
		if _, err := s.auth.VerifyAuth(stream.Context(), secret.AuthContext{ProjectID: req.ProjectID, PAT: bearerToken(stream.Context())}); err != nil {
			return status.Error(codes.Unauthenticated, err.Error())
		}
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

func NewDeploymentService(cfg config.Config, engine *deploy.Engine, auth secret.AuthVerifier) *DeploymentService {
	return &DeploymentService{cfg: cfg, engine: engine, auth: auth}
}

func (s *DeploymentService) Deploy(req *agentv1.DeployRequest, stream agentv1.DeploymentService_DeployServer) error {
	resolved, err := deploy.RequestFromContract(req, s.cfg.Deployment)
	if err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	if s.auth != nil {
		if _, err := s.auth.VerifyAuth(stream.Context(), secret.AuthContext{ProjectID: resolved.ProjectID, PAT: bearerToken(stream.Context())}); err != nil {
			return status.Error(codes.Unauthenticated, err.Error())
		}
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

	serviceStore, err := svcatalog.OpenStore(cfg.SQLitePath)
	if err != nil {
		return err
	}
	defer serviceStore.Close()

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
	authVerifier := authVerifier(cfg)
	agentv1.RegisterDeploymentServiceServer(grpcServer, NewDeploymentService(cfg, engine, authVerifier))
	agentv1.RegisterServiceManagerServiceServer(grpcServer, NewServiceManagerService(serviceStore, serviceManager(cfg, serviceStore), authVerifier))
	agentv1.RegisterTelemetryServiceServer(grpcServer, NewTelemetryService(telemetryStore, authVerifier))
	agentv1.RegisterSecretServiceServer(grpcServer, NewSecretService(cfg, secretService(cfg, telemetryStore, authVerifier)))
	agentv1.RegisterIncidentServiceServer(grpcServer, NewIncidentService(incidentService(cfg, telemetryStore, authVerifier)))

	healthServer := &http.Server{
		Handler:           healthHandler(version, startedAt, cfg),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 4)
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
		var metricsCollector telemetry.Collector
		if cfg.Telemetry.CAdvisorEndpoint != "" {
			cadvisorTimeout := 5 * time.Second
			if cfg.Telemetry.CAdvisorTimeout != "" {
				if parsed, err := time.ParseDuration(cfg.Telemetry.CAdvisorTimeout); err == nil {
					cadvisorTimeout = parsed
				}
			}
			metricsCollector = telemetry.CAdvisorCollector{Endpoint: cfg.Telemetry.CAdvisorEndpoint, Timeout: cadvisorTimeout, ProjectID: cfg.Deployment.ProjectID, NodeID: cfg.NodeID}
		}
		runner := telemetry.Runner{
			Store: telemetryStore,
			Analyzer: &telemetry.Analyzer{
				Store: telemetryStore,
			},
			Collector: telemetry.KubernetesCollector{
				ProjectID:    cfg.Deployment.ProjectID,
				NodeID:       cfg.NodeID,
				KubectlPath:  cfg.Telemetry.KubectlPath,
				LogTailLines: cfg.Telemetry.PodLogTail,
				LogSince:     logSince,
				Metrics:      metricsCollector,
				LogWatch:     true,
				Fallback:     fallback,
			},
			Interval:          interval,
			MaxRecordsPerTick: cfg.Telemetry.MaxRecordsPerTick,
		}
		go func() {
			logger.Info("agent telemetry collector started", "interval", interval.String())
			if err := runner.Run(ctx); err != nil {
				errCh <- err
			}
		}()
		if cfg.Deployment.PublicEndpoint != "" {
			healthInterval := time.Minute
			if cfg.Telemetry.ExternalHealthInterval != "" {
				if parsed, err := time.ParseDuration(cfg.Telemetry.ExternalHealthInterval); err == nil {
					healthInterval = parsed
				}
			}
			checker := telemetry.SyntheticChecker{Store: telemetryStore}
			target := telemetry.SyntheticTarget{ProjectID: cfg.Deployment.ProjectID, ServiceID: cfg.Deployment.ServiceID, NodeID: cfg.NodeID, PublicURL: cfg.Deployment.PublicEndpoint, InternalReady: true}
			go func() {
				logger.Info("agent external health checker started", "interval", healthInterval.String(), "url", cfg.Deployment.PublicEndpoint)
				if err := checker.RunEvery(ctx, healthInterval, target); err != nil && !errors.Is(err, context.Canceled) {
					errCh <- err
				}
			}()
		}
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

func secretService(cfg config.Config, audit secret.AuditSink, auth secret.AuthVerifier) *secret.Service {
	timeout := 10 * time.Second
	if cfg.Secret.CloudOTPTimeout != "" {
		if parsed, err := time.ParseDuration(cfg.Secret.CloudOTPTimeout); err == nil {
			timeout = parsed
		}
	}
	store := secret.KubernetesSecretStore{KubectlPath: cfg.Secret.KubectlPath, TOTPNamespace: firstNonEmpty(cfg.Secret.TOTPNamespace, cfg.Secret.Namespace)}
	var restarter secret.RolloutRestarter
	if cfg.Secret.RolloutRestartOnRotate {
		restarter = secret.KubernetesRolloutRestarter{KubectlPath: cfg.Secret.KubectlPath, Timeout: cfg.Deployment.RolloutTimeout}
	}
	return &secret.Service{
		Store:            store,
		TOTPStore:        store,
		Auth:             auth,
		Audit:            audit,
		OTP:              secret.HTTPOTPClient{Endpoint: cfg.CloudEndpoint},
		Encryption:       secret.StaticEncryptionVerifier(cfg.Secret.EncryptionAtRestConfirmed),
		Restarter:        restarter,
		CloudOTPTimeout:  timeout,
		TOTPSecretByUser: map[string]string{},
	}
}

func authVerifier(cfg config.Config) secret.AuthVerifier {
	if !cfg.Auth.Enabled {
		return nil
	}
	ttl := 15 * time.Minute
	if cfg.Auth.VerifyCacheTTL != "" {
		if parsed, err := time.ParseDuration(cfg.Auth.VerifyCacheTTL); err == nil {
			ttl = parsed
		}
	}
	return &secret.HTTPAuthVerifier{Endpoint: cfg.CloudEndpoint, CacheTTL: ttl}
}

func authFromSecretRequest(req *agentv1.SecretRequest) secret.AuthContext {
	return secret.AuthContext{ProjectID: req.ProjectID, UserID: req.UserID, Role: secret.Role(req.Role), PAT: req.PAT}
}

func bearerToken(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	for _, value := range md.Get("authorization") {
		parts := strings.Fields(value)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			return parts[1]
		}
		if token := strings.TrimSpace(strings.TrimPrefix(value, "Bearer ")); token != "" && token != value {
			return token
		}
	}
	if values := md.Get("x-opsi-pat"); len(values) > 0 {
		return values[0]
	}
	return ""
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
	case strings.Contains(message, "PAT") || strings.Contains(message, "cloud auth"):
		return status.Error(codes.Unauthenticated, message)
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

func mapIncidentError(err error) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	switch {
	case strings.Contains(message, "PAT") || strings.Contains(message, "cloud auth"):
		return status.Error(codes.Unauthenticated, message)
	case message == "permission denied":
		return status.Error(codes.PermissionDenied, message)
	case strings.Contains(message, "not found"):
		return status.Error(codes.NotFound, message)
	case strings.Contains(message, "invalid") || strings.Contains(message, "required"):
		return status.Error(codes.InvalidArgument, message)
	default:
		return status.Error(codes.Internal, message)
	}
}

func incidentResponse(rec *telemetry.IncidentRecord, r incident.RCA) *agentv1.IncidentResponse {
	resp := &agentv1.IncidentResponse{
		IncidentID:            rec.ID,
		ProjectID:             rec.ProjectID,
		ServiceID:             rec.ServiceID,
		Status:                rec.Status,
		RootCause:             r.RootCause,
		Confidence:            r.Confidence,
		ContributingFactors:   r.ContributingFactors,
		MitigationActionsJSON: rec.MitigationActions,
		MTTRSeconds:           rec.MTTRSeconds,
	}
	if !rec.ResolvedAt.IsZero() {
		resp.ResolvedAtUnix = rec.ResolvedAt.Unix()
	}
	for _, action := range r.RecommendedActions {
		resp.RecommendedActions = append(resp.RecommendedActions, agentv1.RecommendedAction{ID: action.ID, Type: action.Type, Description: action.Description, RollbackSafe: action.RollbackSafe, Params: action.Params})
	}
	return resp
}

func incidentService(cfg config.Config, store telemetry.Store, auth secret.AuthVerifier) *incident.Service {
	return &incident.Service{
		Store:       store,
		Audit:       store.(secret.AuditSink),
		Auth:        auth,
		Cloud:       incident.HTTPAnalyzerClient{Endpoint: cfg.CloudEndpoint},
		KubectlPath: cfg.Telemetry.KubectlPath,
		Namespace:   firstNonEmpty(cfg.Deployment.Namespace, cfg.Secret.Namespace),
		DryRun:      cfg.Deployment.DryRun || cfg.Deployment.BuilderMode == "dry_run",
	}
}

func serviceManager(cfg config.Config, store *svcatalog.Store) svcatalog.Manager {
	var applier svcatalog.ManifestApplier = svcatalog.KubectlApplier{KubectlPath: firstNonEmpty(cfg.Secret.KubectlPath, cfg.Telemetry.KubectlPath)}
	if cfg.Deployment.DryRun || cfg.Deployment.BuilderMode == "dry_run" {
		applier = svcatalog.DryRunApplier{}
	}
	return svcatalog.Manager{Store: store, Applier: applier}
}

func managedServiceResponse(service *svcatalog.ManagedService) *agentv1.ManagedServiceResponse {
	resp := &agentv1.ManagedServiceResponse{
		ID:            service.ID,
		ProjectID:     service.ProjectID,
		Name:          service.Name,
		Type:          service.Type,
		Namespace:     service.Namespace,
		Mode:          service.Mode,
		Status:        service.Status,
		Host:          service.Host,
		Port:          service.Port,
		Version:       service.Version,
		Config:        service.Config,
		SecretName:    service.SecretName,
		ConfigMapName: service.ConfigMapName,
	}
	if !service.CreatedAt.IsZero() {
		resp.CreatedAtUnix = service.CreatedAt.Unix()
	}
	if !service.UpdatedAt.IsZero() {
		resp.UpdatedAtUnix = service.UpdatedAt.Unix()
	}
	return resp
}

func mapServiceCatalogError(err error) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	switch {
	case strings.Contains(message, "required") || strings.Contains(message, "unknown") || strings.Contains(message, "not implemented") || strings.Contains(message, "must be"):
		return status.Error(codes.InvalidArgument, message)
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
		Builder:        deploy.ContainerdBuilder{NerdctlPath: cfg.Deployment.NerdctlPath, Namespace: cfg.Deployment.ContainerdNS},
		K3s:            deploy.KubectlAdapter{},
		BuildRoot:      cfg.Deployment.BuildRoot,
		RolloutTimeout: rolloutTimeout,
		PollInterval:   pollInterval,
	}
	switch cfg.Deployment.BuilderMode {
	case "docker":
		engineCfg.Builder = deploy.ExecBuilder{}
	case "dry_run":
		engineCfg.Builder = deploy.DryRunBuilder{}
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
