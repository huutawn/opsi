package server

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opsi-dev/opsi/agent/internal/config"
	"github.com/opsi-dev/opsi/agent/internal/deploy"
	"github.com/opsi-dev/opsi/agent/internal/secret"
	"github.com/opsi-dev/opsi/agent/internal/svcatalog"
	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type roleVerifier struct{ role secret.Role }

func (v roleVerifier) VerifyAuth(context.Context, secret.AuthContext) (secret.AuthContext, error) {
	return secret.AuthContext{ProjectID: "demo", UserID: "user", Role: v.role}, nil
}

type fakeDeployStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s fakeDeployStream) Context() context.Context          { return s.ctx }
func (s fakeDeployStream) Send(*agentv1.ProgressEvent) error { return nil }

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

func TestDeploymentDependencyValidation(t *testing.T) {
	store, err := svcatalog.OpenStore(filepath.Join(t.TempDir(), "opsi.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	for _, service := range []svcatalog.ManagedService{
		{ID: "primary-db", ProjectID: "demo", Name: "primary-db", Type: "postgresql", Namespace: "default", Mode: "managed", Host: "primary-db.default.svc.cluster.local", Port: "5432", SecretName: "opsi-svc-primary-db"},
		{ID: "analytics-db", ProjectID: "demo", Name: "analytics-db", Type: "postgresql", Namespace: "default", Mode: "managed", Host: "analytics-db.default.svc.cluster.local", Port: "5432", SecretName: "opsi-svc-analytics-db"},
		{ID: "cache", ProjectID: "demo", Name: "cache", Type: "redis", Namespace: "default", Mode: "managed", Host: "cache.default.svc.cluster.local", Port: "6379", SecretName: "opsi-svc-cache"},
	} {
		if err := store.UpsertManagedService(ctx, service); err != nil {
			t.Fatal(err)
		}
	}
	svc := &DeploymentService{serviceStore: store}
	req := deploy.Request{ProjectID: "demo", ServiceID: "api", Namespace: "default", DependsOn: []deploy.ServiceDependency{{Name: "primary-db"}, {Name: "cache"}}}
	if err := svc.validateDependencies(ctx, req); err != nil {
		t.Fatal(err)
	}
	req.DependsOn = []deploy.ServiceDependency{{Name: "primary-db"}, {Name: "analytics-db"}}
	if err := svc.validateDependencies(ctx, req); err == nil || !strings.Contains(err.Error(), "env collision") {
		t.Fatalf("expected collision error, got %v", err)
	}
	req.DependsOn = []deploy.ServiceDependency{{Name: "missing"}}
	if err := svc.validateDependencies(ctx, req); err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("expected missing dependency error, got %v", err)
	}
}

func TestAgentRBACMatrixDeniesViewerMutations(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer pat"))
	manager := NewServiceManagerService(nil, svcatalog.Manager{}, roleVerifier{role: secret.RoleViewer})
	if _, err := manager.CreateManagedService(ctx, &agentv1.CreateManagedServiceRequest{ProjectID: "demo", Name: "cache", Type: "redis"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("viewer create status = %v err=%v", status.Code(err), err)
	}
	deployer := NewDeploymentService(configForRBAC(), nil, roleVerifier{role: secret.RoleViewer}, nil)
	err := deployer.Deploy(&agentv1.DeployRequest{ProjectID: "demo", ServiceID: "api", ServiceName: "api", RepoURL: "https://example.test/repo.git", GitSHA: "abcdef", ManifestPath: "k8s/deployment.yaml"}, fakeDeployStream{ctx: ctx})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("viewer deploy status = %v err=%v", status.Code(err), err)
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

func configForRBAC() config.Config {
	cfg := config.Default()
	cfg.Deployment.ProjectID = "demo"
	cfg.Deployment.ManifestPath = "k8s/deployment.yaml"
	return cfg
}
