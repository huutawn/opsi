package actionv1

import (
	"context"
	"encoding/json"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/encoding"
	"google.golang.org/grpc/status"
)

const ActionServiceName = "opsi.action.v1.ActionService"

type JSONCodec struct{}

func (JSONCodec) Name() string                           { return "json" }
func (JSONCodec) Marshal(value any) ([]byte, error)      { return json.Marshal(value) }
func (JSONCodec) Unmarshal(data []byte, value any) error { return DecodeStrict(data, value) }
func init()                                              { encoding.RegisterCodec(JSONCodec{}) }

type ActionServiceServer interface {
	Catalog(context.Context, *CatalogRequest) (*CatalogResponse, error)
	Preflight(context.Context, *PreflightRequest) (*ActionPreflight, error)
	GetChallenge(context.Context, *ChallengeRequest) (*ApprovalChallenge, error)
	Execute(context.Context, *ExecuteRequest) (*ActionResult, error)
	Status(context.Context, *StatusRequest) (*ActionResult, error)
}

type UnimplementedActionServiceServer struct{}

func (UnimplementedActionServiceServer) Catalog(context.Context, *CatalogRequest) (*CatalogResponse, error) {
	return nil, status.Error(codes.Unimplemented, "method Catalog not implemented")
}
func (UnimplementedActionServiceServer) Preflight(context.Context, *PreflightRequest) (*ActionPreflight, error) {
	return nil, status.Error(codes.Unimplemented, "method Preflight not implemented")
}
func (UnimplementedActionServiceServer) GetChallenge(context.Context, *ChallengeRequest) (*ApprovalChallenge, error) {
	return nil, status.Error(codes.Unimplemented, "method GetChallenge not implemented")
}
func (UnimplementedActionServiceServer) Execute(context.Context, *ExecuteRequest) (*ActionResult, error) {
	return nil, status.Error(codes.Unimplemented, "method Execute not implemented")
}
func (UnimplementedActionServiceServer) Status(context.Context, *StatusRequest) (*ActionResult, error) {
	return nil, status.Error(codes.Unimplemented, "method Status not implemented")
}

func RegisterActionServiceServer(server grpc.ServiceRegistrar, service ActionServiceServer) {
	server.RegisterService(&ActionService_ServiceDesc, service)
}

type ActionServiceClient interface {
	Catalog(context.Context, *CatalogRequest, ...grpc.CallOption) (*CatalogResponse, error)
	Preflight(context.Context, *PreflightRequest, ...grpc.CallOption) (*ActionPreflight, error)
	GetChallenge(context.Context, *ChallengeRequest, ...grpc.CallOption) (*ApprovalChallenge, error)
	Execute(context.Context, *ExecuteRequest, ...grpc.CallOption) (*ActionResult, error)
	Status(context.Context, *StatusRequest, ...grpc.CallOption) (*ActionResult, error)
}

type actionServiceClient struct{ cc grpc.ClientConnInterface }

func NewActionServiceClient(cc grpc.ClientConnInterface) ActionServiceClient {
	return &actionServiceClient{cc: cc}
}

func invoke[T any](ctx context.Context, cc grpc.ClientConnInterface, method string, input any, options ...grpc.CallOption) (*T, error) {
	out := new(T)
	options = append([]grpc.CallOption{grpc.ForceCodec(JSONCodec{})}, options...)
	err := cc.Invoke(ctx, "/"+ActionServiceName+"/"+method, input, out, options...)
	return out, err
}
func (c *actionServiceClient) Catalog(ctx context.Context, in *CatalogRequest, opts ...grpc.CallOption) (*CatalogResponse, error) {
	return invoke[CatalogResponse](ctx, c.cc, "Catalog", in, opts...)
}
func (c *actionServiceClient) Preflight(ctx context.Context, in *PreflightRequest, opts ...grpc.CallOption) (*ActionPreflight, error) {
	return invoke[ActionPreflight](ctx, c.cc, "Preflight", in, opts...)
}
func (c *actionServiceClient) GetChallenge(ctx context.Context, in *ChallengeRequest, opts ...grpc.CallOption) (*ApprovalChallenge, error) {
	return invoke[ApprovalChallenge](ctx, c.cc, "GetChallenge", in, opts...)
}
func (c *actionServiceClient) Execute(ctx context.Context, in *ExecuteRequest, opts ...grpc.CallOption) (*ActionResult, error) {
	return invoke[ActionResult](ctx, c.cc, "Execute", in, opts...)
}
func (c *actionServiceClient) Status(ctx context.Context, in *StatusRequest, opts ...grpc.CallOption) (*ActionResult, error) {
	return invoke[ActionResult](ctx, c.cc, "Status", in, opts...)
}

func unaryHandler[Req any](method string, call func(ActionServiceServer, context.Context, *Req) (any, error)) grpc.MethodHandler {
	return func(service any, ctx context.Context, decode func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
		request := new(Req)
		if err := decode(request); err != nil {
			return nil, err
		}
		if interceptor == nil {
			return call(service.(ActionServiceServer), ctx, request)
		}
		info := &grpc.UnaryServerInfo{Server: service, FullMethod: "/" + ActionServiceName + "/" + method}
		return interceptor(ctx, request, info, func(ctx context.Context, req any) (any, error) {
			return call(service.(ActionServiceServer), ctx, req.(*Req))
		})
	}
}

var ActionService_ServiceDesc = grpc.ServiceDesc{
	ServiceName: ActionServiceName,
	HandlerType: (*ActionServiceServer)(nil),
	Methods: []grpc.MethodDesc{
		{MethodName: "Catalog", Handler: unaryHandler("Catalog", func(s ActionServiceServer, c context.Context, r *CatalogRequest) (any, error) { return s.Catalog(c, r) })},
		{MethodName: "Preflight", Handler: unaryHandler("Preflight", func(s ActionServiceServer, c context.Context, r *PreflightRequest) (any, error) {
			return s.Preflight(c, r)
		})},
		{MethodName: "GetChallenge", Handler: unaryHandler("GetChallenge", func(s ActionServiceServer, c context.Context, r *ChallengeRequest) (any, error) {
			return s.GetChallenge(c, r)
		})},
		{MethodName: "Execute", Handler: unaryHandler("Execute", func(s ActionServiceServer, c context.Context, r *ExecuteRequest) (any, error) { return s.Execute(c, r) })},
		{MethodName: "Status", Handler: unaryHandler("Status", func(s ActionServiceServer, c context.Context, r *StatusRequest) (any, error) { return s.Status(c, r) })},
	},
	Metadata: "contracts/action/v1/action.proto",
}
