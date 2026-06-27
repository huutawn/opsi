package agentv1

import (
	"context"
	"encoding/json"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/encoding"
	"google.golang.org/grpc/status"
)

const (
	StatusServiceName     = "opsi.agent.v1.StatusService"
	DeploymentServiceName = "opsi.agent.v1.DeploymentService"
	TelemetryServiceName  = "opsi.agent.v1.TelemetryService"
	SecretServiceName     = "opsi.agent.v1.SecretService"
	IncidentServiceName   = "opsi.agent.v1.IncidentService"
	JSONCodecName         = "json"
)

type ErrorCode string

const (
	ErrorCodeUnspecified      ErrorCode = "UNSPECIFIED"
	ErrorCodeAuthFailed       ErrorCode = "AUTH_FAILED"
	ErrorCodeUnavailable      ErrorCode = "UNAVAILABLE"
	ErrorCodeInvalidInput     ErrorCode = "INVALID_INPUT"
	ErrorCodeTimeout          ErrorCode = "TIMEOUT"
	ErrorCodePermissionDenied ErrorCode = "PERMISSION_DENIED"
	ErrorCodeInternal         ErrorCode = "INTERNAL"
)

type StatusRequest struct{}

type StatusResponse struct {
	Version        string         `json:"version"`
	UptimeSeconds  int64          `json:"uptime_seconds"`
	NodeID         string         `json:"node_id"`
	Health         string         `json:"health"`
	CloudConnected bool           `json:"cloud_connected"`
	StartedAtUnix  int64          `json:"started_at_unix"`
	Errors         []ServiceError `json:"errors,omitempty"`
}

type ServiceError struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

type ProgressEvent struct {
	OperationID string        `json:"operation_id"`
	Phase       string        `json:"phase"`
	Message     string        `json:"message"`
	Percent     int32         `json:"percent"`
	Error       *ServiceError `json:"error,omitempty"`
	ProjectID   string        `json:"project_id,omitempty"`
	ServiceID   string        `json:"service_id,omitempty"`
	ServiceName string        `json:"service_name,omitempty"`
}

type DeployRequest struct {
	ProjectID                     string   `json:"project_id"`
	ServiceID                     string   `json:"service_id"`
	ServiceName                   string   `json:"service_name"`
	ServiceType                   string   `json:"service_type"`
	RepoURL                       string   `json:"repo_url"`
	Branch                        string   `json:"branch"`
	GitSHA                        string   `json:"git_sha"`
	Namespace                     string   `json:"namespace"`
	BuildContext                  string   `json:"build_context"`
	Dockerfile                    string   `json:"dockerfile"`
	ManifestPath                  string   `json:"manifest_path"`
	Registry                      string   `json:"registry"`
	ImageTag                      string   `json:"image_tag"`
	TriggeredBy                   string   `json:"triggered_by"`
	WatchPaths                    []string `json:"watch_paths,omitempty"`
	TerminationGracePeriodSeconds int32    `json:"termination_grace_period_seconds,omitempty"`
	ResourceRequestsJSON          string   `json:"resource_requests_json,omitempty"`
	ResourceLimitsJSON            string   `json:"resource_limits_json,omitempty"`
	IngressEnabled                bool     `json:"ingress_enabled,omitempty"`
}

type SyncRequest struct {
	ProjectID        string   `json:"project_id"`
	LastReceivedUnix int64    `json:"last_received_unix"`
	ResourceIDs      []string `json:"resource_ids,omitempty"`
	MaxChunkBytes    int32    `json:"max_chunk_bytes,omitempty"`
}

type SyncChunk struct {
	ProjectID      string `json:"project_id"`
	StartUnix      int64  `json:"start_unix"`
	EndUnix        int64  `json:"end_unix"`
	RecordCount    int32  `json:"record_count"`
	Compression    string `json:"compression"`
	ChecksumSHA256 string `json:"checksum_sha256"`
	Payload        []byte `json:"payload,omitempty"`
	Done           bool   `json:"done"`
}

type SetupTOTPRequest struct {
	ProjectID string `json:"project_id"`
	UserID    string `json:"user_id"`
	Role      string `json:"role"`
	PAT       string `json:"pat"`
}

type SetupTOTPResponse struct {
	Secret string `json:"secret"`
	URI    string `json:"uri"`
}

type SecretRequest struct {
	ProjectID    string `json:"project_id"`
	ServiceID    string `json:"service_id"`
	Name         string `json:"name"`
	Namespace    string `json:"namespace"`
	UserID       string `json:"user_id"`
	Role         string `json:"role"`
	PAT          string `json:"pat"`
	OTPCode      string `json:"otp_code"`
	TOTPCode     string `json:"totp_code"`
	OTPRequestID string `json:"otp_request_id"`
}

type SecretResponse struct {
	ProjectID string `json:"project_id"`
	ServiceID string `json:"service_id"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Username  string `json:"username"`
	Password  string `json:"password,omitempty"`
}

type IncidentAnalyzeRequest struct {
	ProjectID  string `json:"project_id"`
	IncidentID string `json:"incident_id"`
	UserID     string `json:"user_id"`
	Role       string `json:"role"`
	PAT        string `json:"pat"`
}

type IncidentActionRequest struct {
	ProjectID  string `json:"project_id"`
	IncidentID string `json:"incident_id"`
	ActionID   string `json:"action_id"`
	UserID     string `json:"user_id"`
	Role       string `json:"role"`
	PAT        string `json:"pat"`
}

type IncidentResponse struct {
	IncidentID            string              `json:"incident_id"`
	ProjectID             string              `json:"project_id"`
	ServiceID             string              `json:"service_id,omitempty"`
	Status                string              `json:"status"`
	RootCause             string              `json:"root_cause,omitempty"`
	Confidence            float64             `json:"confidence,omitempty"`
	ContributingFactors   []string            `json:"contributing_factors,omitempty"`
	RecommendedActions    []RecommendedAction `json:"recommended_actions,omitempty"`
	MitigationActionsJSON string              `json:"mitigation_actions_json,omitempty"`
	ResolvedAtUnix        int64               `json:"resolved_at_unix,omitempty"`
	MTTRSeconds           int64               `json:"mttr_seconds,omitempty"`
}

type RecommendedAction struct {
	ID           string            `json:"id"`
	Type         string            `json:"type"`
	Description  string            `json:"description"`
	RollbackSafe bool              `json:"rollback_safe,omitempty"`
	Params       map[string]string `json:"params,omitempty"`
}

type JSONCodec struct{}

func (JSONCodec) Name() string { return JSONCodecName }

func (JSONCodec) Marshal(v any) ([]byte, error) { return json.Marshal(v) }

func (JSONCodec) Unmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }

func init() {
	encoding.RegisterCodec(JSONCodec{})
}

type StatusServiceServer interface {
	Status(context.Context, *StatusRequest) (*StatusResponse, error)
}

type UnimplementedStatusServiceServer struct{}

func (UnimplementedStatusServiceServer) Status(context.Context, *StatusRequest) (*StatusResponse, error) {
	return nil, status.Error(codes.Unimplemented, "method Status not implemented")
}

func RegisterStatusServiceServer(server grpc.ServiceRegistrar, service StatusServiceServer) {
	server.RegisterService(&StatusService_ServiceDesc, service)
}

type StatusServiceClient interface {
	Status(ctx context.Context, in *StatusRequest, opts ...grpc.CallOption) (*StatusResponse, error)
}

type statusServiceClient struct {
	cc grpc.ClientConnInterface
}

func NewStatusServiceClient(cc grpc.ClientConnInterface) StatusServiceClient {
	return &statusServiceClient{cc: cc}
}

func (c *statusServiceClient) Status(ctx context.Context, in *StatusRequest, opts ...grpc.CallOption) (*StatusResponse, error) {
	out := new(StatusResponse)
	opts = append([]grpc.CallOption{grpc.ForceCodec(JSONCodec{})}, opts...)
	err := c.cc.Invoke(ctx, "/"+StatusServiceName+"/Status", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func statusHandler(service any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(StatusRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return service.(StatusServiceServer).Status(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     service,
		FullMethod: "/" + StatusServiceName + "/Status",
	}
	handler := func(ctx context.Context, req any) (any, error) {
		return service.(StatusServiceServer).Status(ctx, req.(*StatusRequest))
	}
	return interceptor(ctx, in, info, handler)
}

var StatusService_ServiceDesc = grpc.ServiceDesc{
	ServiceName: StatusServiceName,
	HandlerType: (*StatusServiceServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "Status",
			Handler:    statusHandler,
		},
	},
	Streams:  []grpc.StreamDesc{},
	Metadata: "contracts/agent/v1/status.proto",
}

type DeploymentServiceServer interface {
	Deploy(*DeployRequest, DeploymentService_DeployServer) error
}

type UnimplementedDeploymentServiceServer struct{}

func (UnimplementedDeploymentServiceServer) Deploy(*DeployRequest, DeploymentService_DeployServer) error {
	return status.Error(codes.Unimplemented, "method Deploy not implemented")
}

type DeploymentService_DeployServer interface {
	Send(*ProgressEvent) error
	grpc.ServerStream
}

func RegisterDeploymentServiceServer(server grpc.ServiceRegistrar, service DeploymentServiceServer) {
	server.RegisterService(&DeploymentService_ServiceDesc, service)
}

type DeploymentServiceClient interface {
	Deploy(ctx context.Context, in *DeployRequest, opts ...grpc.CallOption) (DeploymentService_DeployClient, error)
}

type deploymentServiceClient struct {
	cc grpc.ClientConnInterface
}

func NewDeploymentServiceClient(cc grpc.ClientConnInterface) DeploymentServiceClient {
	return &deploymentServiceClient{cc: cc}
}

func (c *deploymentServiceClient) Deploy(ctx context.Context, in *DeployRequest, opts ...grpc.CallOption) (DeploymentService_DeployClient, error) {
	opts = append([]grpc.CallOption{grpc.ForceCodec(JSONCodec{})}, opts...)
	stream, err := c.cc.NewStream(ctx, &DeploymentService_ServiceDesc.Streams[0], "/"+DeploymentServiceName+"/Deploy", opts...)
	if err != nil {
		return nil, err
	}
	x := &deploymentServiceDeployClient{stream}
	if err := x.ClientStream.SendMsg(in); err != nil {
		return nil, err
	}
	if err := x.ClientStream.CloseSend(); err != nil {
		return nil, err
	}
	return x, nil
}

type DeploymentService_DeployClient interface {
	Recv() (*ProgressEvent, error)
	grpc.ClientStream
}

type deploymentServiceDeployClient struct {
	grpc.ClientStream
}

func (x *deploymentServiceDeployClient) Recv() (*ProgressEvent, error) {
	m := new(ProgressEvent)
	if err := x.ClientStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

func deploymentDeployHandler(service any, stream grpc.ServerStream) error {
	in := new(DeployRequest)
	if err := stream.RecvMsg(in); err != nil {
		return err
	}
	return service.(DeploymentServiceServer).Deploy(in, &deploymentServiceDeployServer{stream})
}

type deploymentServiceDeployServer struct {
	grpc.ServerStream
}

func (x *deploymentServiceDeployServer) Send(m *ProgressEvent) error {
	return x.ServerStream.SendMsg(m)
}

var DeploymentService_ServiceDesc = grpc.ServiceDesc{
	ServiceName: DeploymentServiceName,
	HandlerType: (*DeploymentServiceServer)(nil),
	Methods:     []grpc.MethodDesc{},
	Streams: []grpc.StreamDesc{
		{
			StreamName:    "Deploy",
			Handler:       deploymentDeployHandler,
			ServerStreams: true,
		},
	},
	Metadata: "contracts/agent/v1/status.proto",
}

type TelemetryServiceServer interface {
	Sync(*SyncRequest, TelemetryService_SyncServer) error
}

type UnimplementedTelemetryServiceServer struct{}

func (UnimplementedTelemetryServiceServer) Sync(*SyncRequest, TelemetryService_SyncServer) error {
	return status.Error(codes.Unimplemented, "method Sync not implemented")
}

type TelemetryService_SyncServer interface {
	Send(*SyncChunk) error
	grpc.ServerStream
}

func RegisterTelemetryServiceServer(server grpc.ServiceRegistrar, service TelemetryServiceServer) {
	server.RegisterService(&TelemetryService_ServiceDesc, service)
}

type TelemetryServiceClient interface {
	Sync(ctx context.Context, in *SyncRequest, opts ...grpc.CallOption) (TelemetryService_SyncClient, error)
}

type telemetryServiceClient struct {
	cc grpc.ClientConnInterface
}

func NewTelemetryServiceClient(cc grpc.ClientConnInterface) TelemetryServiceClient {
	return &telemetryServiceClient{cc: cc}
}

func (c *telemetryServiceClient) Sync(ctx context.Context, in *SyncRequest, opts ...grpc.CallOption) (TelemetryService_SyncClient, error) {
	opts = append([]grpc.CallOption{grpc.ForceCodec(JSONCodec{})}, opts...)
	stream, err := c.cc.NewStream(ctx, &TelemetryService_ServiceDesc.Streams[0], "/"+TelemetryServiceName+"/Sync", opts...)
	if err != nil {
		return nil, err
	}
	x := &telemetryServiceSyncClient{stream}
	if err := x.ClientStream.SendMsg(in); err != nil {
		return nil, err
	}
	if err := x.ClientStream.CloseSend(); err != nil {
		return nil, err
	}
	return x, nil
}

type TelemetryService_SyncClient interface {
	Recv() (*SyncChunk, error)
	grpc.ClientStream
}

type telemetryServiceSyncClient struct {
	grpc.ClientStream
}

func (x *telemetryServiceSyncClient) Recv() (*SyncChunk, error) {
	m := new(SyncChunk)
	if err := x.ClientStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

func telemetrySyncHandler(service any, stream grpc.ServerStream) error {
	in := new(SyncRequest)
	if err := stream.RecvMsg(in); err != nil {
		return err
	}
	return service.(TelemetryServiceServer).Sync(in, &telemetryServiceSyncServer{stream})
}

type telemetryServiceSyncServer struct {
	grpc.ServerStream
}

func (x *telemetryServiceSyncServer) Send(m *SyncChunk) error {
	return x.ServerStream.SendMsg(m)
}

var TelemetryService_ServiceDesc = grpc.ServiceDesc{
	ServiceName: TelemetryServiceName,
	HandlerType: (*TelemetryServiceServer)(nil),
	Methods:     []grpc.MethodDesc{},
	Streams: []grpc.StreamDesc{
		{
			StreamName:    "Sync",
			Handler:       telemetrySyncHandler,
			ServerStreams: true,
		},
	},
	Metadata: "contracts/agent/v1/status.proto",
}

type SecretServiceServer interface {
	SetupTOTP(context.Context, *SetupTOTPRequest) (*SetupTOTPResponse, error)
	CreateSecret(context.Context, *SecretRequest) (*SecretResponse, error)
	RevealSecret(context.Context, *SecretRequest) (*SecretResponse, error)
	RotateSecret(context.Context, *SecretRequest) (*SecretResponse, error)
}

type UnimplementedSecretServiceServer struct{}

func (UnimplementedSecretServiceServer) SetupTOTP(context.Context, *SetupTOTPRequest) (*SetupTOTPResponse, error) {
	return nil, status.Error(codes.Unimplemented, "method SetupTOTP not implemented")
}

func (UnimplementedSecretServiceServer) CreateSecret(context.Context, *SecretRequest) (*SecretResponse, error) {
	return nil, status.Error(codes.Unimplemented, "method CreateSecret not implemented")
}

func (UnimplementedSecretServiceServer) RevealSecret(context.Context, *SecretRequest) (*SecretResponse, error) {
	return nil, status.Error(codes.Unimplemented, "method RevealSecret not implemented")
}

func (UnimplementedSecretServiceServer) RotateSecret(context.Context, *SecretRequest) (*SecretResponse, error) {
	return nil, status.Error(codes.Unimplemented, "method RotateSecret not implemented")
}

func RegisterSecretServiceServer(server grpc.ServiceRegistrar, service SecretServiceServer) {
	server.RegisterService(&SecretService_ServiceDesc, service)
}

type SecretServiceClient interface {
	SetupTOTP(ctx context.Context, in *SetupTOTPRequest, opts ...grpc.CallOption) (*SetupTOTPResponse, error)
	CreateSecret(ctx context.Context, in *SecretRequest, opts ...grpc.CallOption) (*SecretResponse, error)
	RevealSecret(ctx context.Context, in *SecretRequest, opts ...grpc.CallOption) (*SecretResponse, error)
	RotateSecret(ctx context.Context, in *SecretRequest, opts ...grpc.CallOption) (*SecretResponse, error)
}

type secretServiceClient struct {
	cc grpc.ClientConnInterface
}

func NewSecretServiceClient(cc grpc.ClientConnInterface) SecretServiceClient {
	return &secretServiceClient{cc: cc}
}

func (c *secretServiceClient) SetupTOTP(ctx context.Context, in *SetupTOTPRequest, opts ...grpc.CallOption) (*SetupTOTPResponse, error) {
	out := new(SetupTOTPResponse)
	opts = append([]grpc.CallOption{grpc.ForceCodec(JSONCodec{})}, opts...)
	err := c.cc.Invoke(ctx, "/"+SecretServiceName+"/SetupTOTP", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *secretServiceClient) CreateSecret(ctx context.Context, in *SecretRequest, opts ...grpc.CallOption) (*SecretResponse, error) {
	out := new(SecretResponse)
	opts = append([]grpc.CallOption{grpc.ForceCodec(JSONCodec{})}, opts...)
	err := c.cc.Invoke(ctx, "/"+SecretServiceName+"/CreateSecret", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *secretServiceClient) RevealSecret(ctx context.Context, in *SecretRequest, opts ...grpc.CallOption) (*SecretResponse, error) {
	out := new(SecretResponse)
	opts = append([]grpc.CallOption{grpc.ForceCodec(JSONCodec{})}, opts...)
	err := c.cc.Invoke(ctx, "/"+SecretServiceName+"/RevealSecret", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *secretServiceClient) RotateSecret(ctx context.Context, in *SecretRequest, opts ...grpc.CallOption) (*SecretResponse, error) {
	out := new(SecretResponse)
	opts = append([]grpc.CallOption{grpc.ForceCodec(JSONCodec{})}, opts...)
	err := c.cc.Invoke(ctx, "/"+SecretServiceName+"/RotateSecret", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func setupTOTPHandler(service any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(SetupTOTPRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return service.(SecretServiceServer).SetupTOTP(ctx, in)
	}
	info := &grpc.UnaryServerInfo{Server: service, FullMethod: "/" + SecretServiceName + "/SetupTOTP"}
	handler := func(ctx context.Context, req any) (any, error) {
		return service.(SecretServiceServer).SetupTOTP(ctx, req.(*SetupTOTPRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func createSecretHandler(service any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(SecretRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return service.(SecretServiceServer).CreateSecret(ctx, in)
	}
	info := &grpc.UnaryServerInfo{Server: service, FullMethod: "/" + SecretServiceName + "/CreateSecret"}
	handler := func(ctx context.Context, req any) (any, error) {
		return service.(SecretServiceServer).CreateSecret(ctx, req.(*SecretRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func revealSecretHandler(service any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(SecretRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return service.(SecretServiceServer).RevealSecret(ctx, in)
	}
	info := &grpc.UnaryServerInfo{Server: service, FullMethod: "/" + SecretServiceName + "/RevealSecret"}
	handler := func(ctx context.Context, req any) (any, error) {
		return service.(SecretServiceServer).RevealSecret(ctx, req.(*SecretRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func rotateSecretHandler(service any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(SecretRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return service.(SecretServiceServer).RotateSecret(ctx, in)
	}
	info := &grpc.UnaryServerInfo{Server: service, FullMethod: "/" + SecretServiceName + "/RotateSecret"}
	handler := func(ctx context.Context, req any) (any, error) {
		return service.(SecretServiceServer).RotateSecret(ctx, req.(*SecretRequest))
	}
	return interceptor(ctx, in, info, handler)
}

var SecretService_ServiceDesc = grpc.ServiceDesc{
	ServiceName: SecretServiceName,
	HandlerType: (*SecretServiceServer)(nil),
	Methods: []grpc.MethodDesc{
		{MethodName: "SetupTOTP", Handler: setupTOTPHandler},
		{MethodName: "CreateSecret", Handler: createSecretHandler},
		{MethodName: "RevealSecret", Handler: revealSecretHandler},
		{MethodName: "RotateSecret", Handler: rotateSecretHandler},
	},
	Streams:  []grpc.StreamDesc{},
	Metadata: "contracts/agent/v1/status.proto",
}

type IncidentServiceServer interface {
	AnalyzeIncident(context.Context, *IncidentAnalyzeRequest) (*IncidentResponse, error)
	ApproveIncidentAction(context.Context, *IncidentActionRequest) (*IncidentResponse, error)
	ResolveIncident(context.Context, *IncidentActionRequest) (*IncidentResponse, error)
}

type UnimplementedIncidentServiceServer struct{}

func (UnimplementedIncidentServiceServer) AnalyzeIncident(context.Context, *IncidentAnalyzeRequest) (*IncidentResponse, error) {
	return nil, status.Error(codes.Unimplemented, "method AnalyzeIncident not implemented")
}

func (UnimplementedIncidentServiceServer) ApproveIncidentAction(context.Context, *IncidentActionRequest) (*IncidentResponse, error) {
	return nil, status.Error(codes.Unimplemented, "method ApproveIncidentAction not implemented")
}

func (UnimplementedIncidentServiceServer) ResolveIncident(context.Context, *IncidentActionRequest) (*IncidentResponse, error) {
	return nil, status.Error(codes.Unimplemented, "method ResolveIncident not implemented")
}

func RegisterIncidentServiceServer(server grpc.ServiceRegistrar, service IncidentServiceServer) {
	server.RegisterService(&IncidentService_ServiceDesc, service)
}

type IncidentServiceClient interface {
	AnalyzeIncident(ctx context.Context, in *IncidentAnalyzeRequest, opts ...grpc.CallOption) (*IncidentResponse, error)
	ApproveIncidentAction(ctx context.Context, in *IncidentActionRequest, opts ...grpc.CallOption) (*IncidentResponse, error)
	ResolveIncident(ctx context.Context, in *IncidentActionRequest, opts ...grpc.CallOption) (*IncidentResponse, error)
}

type incidentServiceClient struct{ cc grpc.ClientConnInterface }

func NewIncidentServiceClient(cc grpc.ClientConnInterface) IncidentServiceClient {
	return &incidentServiceClient{cc: cc}
}

func (c *incidentServiceClient) AnalyzeIncident(ctx context.Context, in *IncidentAnalyzeRequest, opts ...grpc.CallOption) (*IncidentResponse, error) {
	out := new(IncidentResponse)
	opts = append([]grpc.CallOption{grpc.ForceCodec(JSONCodec{})}, opts...)
	err := c.cc.Invoke(ctx, "/"+IncidentServiceName+"/AnalyzeIncident", in, out, opts...)
	return out, err
}

func (c *incidentServiceClient) ApproveIncidentAction(ctx context.Context, in *IncidentActionRequest, opts ...grpc.CallOption) (*IncidentResponse, error) {
	out := new(IncidentResponse)
	opts = append([]grpc.CallOption{grpc.ForceCodec(JSONCodec{})}, opts...)
	err := c.cc.Invoke(ctx, "/"+IncidentServiceName+"/ApproveIncidentAction", in, out, opts...)
	return out, err
}

func (c *incidentServiceClient) ResolveIncident(ctx context.Context, in *IncidentActionRequest, opts ...grpc.CallOption) (*IncidentResponse, error) {
	out := new(IncidentResponse)
	opts = append([]grpc.CallOption{grpc.ForceCodec(JSONCodec{})}, opts...)
	err := c.cc.Invoke(ctx, "/"+IncidentServiceName+"/ResolveIncident", in, out, opts...)
	return out, err
}

func analyzeIncidentHandler(service any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(IncidentAnalyzeRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return service.(IncidentServiceServer).AnalyzeIncident(ctx, in)
	}
	info := &grpc.UnaryServerInfo{Server: service, FullMethod: "/" + IncidentServiceName + "/AnalyzeIncident"}
	handler := func(ctx context.Context, req any) (any, error) {
		return service.(IncidentServiceServer).AnalyzeIncident(ctx, req.(*IncidentAnalyzeRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func approveIncidentActionHandler(service any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(IncidentActionRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return service.(IncidentServiceServer).ApproveIncidentAction(ctx, in)
	}
	info := &grpc.UnaryServerInfo{Server: service, FullMethod: "/" + IncidentServiceName + "/ApproveIncidentAction"}
	handler := func(ctx context.Context, req any) (any, error) {
		return service.(IncidentServiceServer).ApproveIncidentAction(ctx, req.(*IncidentActionRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func resolveIncidentHandler(service any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(IncidentActionRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return service.(IncidentServiceServer).ResolveIncident(ctx, in)
	}
	info := &grpc.UnaryServerInfo{Server: service, FullMethod: "/" + IncidentServiceName + "/ResolveIncident"}
	handler := func(ctx context.Context, req any) (any, error) {
		return service.(IncidentServiceServer).ResolveIncident(ctx, req.(*IncidentActionRequest))
	}
	return interceptor(ctx, in, info, handler)
}

var IncidentService_ServiceDesc = grpc.ServiceDesc{
	ServiceName: IncidentServiceName,
	HandlerType: (*IncidentServiceServer)(nil),
	Methods: []grpc.MethodDesc{
		{MethodName: "AnalyzeIncident", Handler: analyzeIncidentHandler},
		{MethodName: "ApproveIncidentAction", Handler: approveIncidentActionHandler},
		{MethodName: "ResolveIncident", Handler: resolveIncidentHandler},
	},
	Streams:  []grpc.StreamDesc{},
	Metadata: "contracts/agent/v1/status.proto",
}
