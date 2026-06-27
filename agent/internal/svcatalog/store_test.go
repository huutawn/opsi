package svcatalog

import (
	"context"
	"path/filepath"
	"testing"
)

func TestStorePersistsManagedServiceByProject(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	err = store.UpsertManagedService(ctx, ManagedService{
		ID:            "mydb",
		ProjectID:     "demo",
		Name:          "mydb",
		Type:          "postgresql",
		Namespace:     "default",
		Mode:          "managed",
		Host:          "mydb.default.svc.cluster.local",
		Port:          "5432",
		Version:       "16",
		Config:        map[string]string{"database": "app"},
		SecretName:    "opsi-svc-mydb",
		ConfigMapName: "opsi-bind-mydb",
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := store.GetManagedService(ctx, "demo", "mydb")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ProjectID != "demo" || got.Config["database"] != "app" || got.Status != "unknown" {
		t.Fatalf("bad service: %#v", got)
	}
	if other, err := store.GetManagedService(ctx, "other", "mydb"); err != nil || other != nil {
		t.Fatalf("project isolation failed: %#v %v", other, err)
	}
}

func TestStorePersistsBinding(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	err = store.UpsertBinding(context.Background(), ServiceBinding{
		ProjectID:           "demo",
		AppServiceID:        "api",
		DependencyServiceID: "mydb",
		Namespace:           "default",
		EnvPolicy:           map[string]string{"expose_as_default": "true"},
	})
	if err != nil {
		t.Fatal(err)
	}
}
