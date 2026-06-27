package server

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/opsi-dev/opsi/agent/internal/svcatalog"
	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
)

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
