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
	ProjectID    string `json:"project_id"`
	ServiceID    string `json:"service_id"`
	ServiceName  string `json:"service_name"`
	ServiceType  string `json:"service_type"`
	RepoURL      string `json:"repo_url"`
	Branch       string `json:"branch"`
	GitSHA       string `json:"git_sha"`
	Namespace    string `json:"namespace"`
	BuildContext string `json:"build_context"`
	Dockerfile   string `json:"dockerfile"`
	ManifestPath string `json:"manifest_path"`
	Registry     string `json:"registry"`
	ImageTag     string `json:"image_tag"`
	TriggeredBy  string `json:"triggered_by"`
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
