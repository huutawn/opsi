package server

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/opsi-dev/opsi/agent/internal/secret"
	"github.com/opsi-dev/opsi/agent/internal/svcatalog"
	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type roleVerifier struct{ role secret.Role }

func (v roleVerifier) VerifyAuth(context.Context, secret.AuthContext) (secret.AuthContext, error) {
	return secret.AuthContext{ProjectID: "demo", UserID: "user", Role: v.role}, nil
}

func TestServiceManagerCreateAndGet(t *testing.T) {
	store, err := svcatalog.OpenStore(filepath.Join(t.TempDir(), "opsi.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	service := NewServiceManagerService(store, svcatalog.Manager{Store: store, Applier: svcatalog.DryRunApplier{}}, nil)
	created, err := service.CreateManagedService(context.Background(), &agentv1.CreateManagedServiceRequest{
		ProjectID: "demo",
		Name:      "cache",
		Type:      "redis",
		Namespace: "prod",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.SecretName != "opsi-svc-cache" || created.Host != "cache.prod.svc.cluster.local" {
		t.Fatalf("bad created service: %#v", created)
	}

	got, err := service.GetManagedService(context.Background(), &agentv1.GetManagedServiceRequest{ProjectID: "demo", ID: "cache"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "cache" || got.Type != "redis" || got.Status != "created" {
		t.Fatalf("bad service status: %#v", got)
	}
}

func TestServiceManagerRegisterExternalAndDelete(t *testing.T) {
	store, err := svcatalog.OpenStore(filepath.Join(t.TempDir(), "opsi.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	service := NewServiceManagerService(store, svcatalog.Manager{Store: store, Applier: svcatalog.DryRunApplier{}}, nil)
	registered, err := service.RegisterExternalService(context.Background(), &agentv1.RegisterExternalServiceRequest{
		ProjectID: "demo",
		Name:      "legacy-cache",
		Type:      "redis",
		Host:      "192.168.1.10",
	})
	if err != nil {
		t.Fatal(err)
	}
	if registered.Mode != "external" || registered.SecretName != "opsi-svc-legacy-cache" {
		t.Fatalf("bad registered service: %#v", registered)
	}
	deleted, err := service.DeleteManagedService(context.Background(), &agentv1.DeleteManagedServiceRequest{ProjectID: "demo", ID: "legacy-cache"})
	if err != nil {
		t.Fatal(err)
	}
	if !deleted.Deleted {
		t.Fatalf("not deleted: %#v", deleted)
	}
}

func TestServiceManagerListCatalog(t *testing.T) {
	resp, err := NewServiceManagerService(nil, svcatalog.Manager{}, nil).ListCatalog(context.Background(), &agentv1.ListCatalogRequest{})
	if err != nil {
		t.Fatal(err)
	}
	foundRedis := false
	foundKafkaUnsupported := false
	for _, item := range resp.Services {
		if item.Type == "redis" && item.ManagedSupported {
			foundRedis = true
		}
		if item.Type == "kafka" && !item.ManagedSupported {
			foundKafkaUnsupported = true
		}
	}
	if !foundRedis || !foundKafkaUnsupported {
		t.Fatalf("unexpected catalog: %#v", resp.Services)
	}
}

func TestAgentRBACMatrixDeniesViewerMutations(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer pat"))
	manager := NewServiceManagerService(nil, svcatalog.Manager{}, roleVerifier{role: secret.RoleViewer})
	if _, err := manager.CreateManagedService(ctx, &agentv1.CreateManagedServiceRequest{ProjectID: "demo", Name: "cache", Type: "redis"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("viewer create status = %v err=%v", status.Code(err), err)
	}
}

func TestAgentRBACMatrixAllowsDeveloperMutations(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer pat"))
	store, err := svcatalog.OpenStore(filepath.Join(t.TempDir(), "opsi.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager := NewServiceManagerService(store, svcatalog.Manager{Store: store, Applier: svcatalog.DryRunApplier{}}, roleVerifier{role: secret.RoleDeveloper})
	if _, err := manager.CreateManagedService(ctx, &agentv1.CreateManagedServiceRequest{ProjectID: "demo", Name: "cache", Type: "redis"}); err != nil {
		t.Fatalf("developer create: %v", err)
	}
}
