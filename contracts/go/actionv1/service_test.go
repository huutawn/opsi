package actionv1

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

type testActionService struct {
	UnimplementedActionServiceServer
}

func (testActionService) Catalog(context.Context, *CatalogRequest) (*CatalogResponse, error) {
	return &CatalogResponse{SchemaVersion: SchemaVersion, Actions: []CatalogAction{{Kind: ActionRestartWorkload, Risk: RiskR2}}}, nil
}

func TestActionServiceJSONGRPCRoundTrip(t *testing.T) {
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	RegisterActionServiceServer(server, testActionService{})
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()
	conn, err := grpc.DialContext(context.Background(), "bufnet", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }), grpc.WithInsecure())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	response, err := NewActionServiceClient(conn).Catalog(context.Background(), &CatalogRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Actions) != 1 || response.Actions[0].Kind != ActionRestartWorkload {
		t.Fatalf("unexpected response: %#v", response)
	}
}
