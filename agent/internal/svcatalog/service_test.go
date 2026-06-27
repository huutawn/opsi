package svcatalog

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

type recordingApplier struct {
	namespace string
	manifest  string
}

func (r *recordingApplier) Apply(_ context.Context, namespace string, manifest []byte) error {
	r.namespace = namespace
	r.manifest = string(manifest)
	return nil
}

func TestManagerCreateManagedAppliesAndStores(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	applier := &recordingApplier{}
	manager := Manager{Store: store, Applier: applier}

	service, err := manager.CreateManaged(context.Background(), CreateManagedRequest{
		ProjectID: "demo",
		Name:      "cache",
		Type:      "redis",
		Namespace: "prod",
	})
	if err != nil {
		t.Fatal(err)
	}
	if service.ID != "cache" || service.Status != "created" || applier.namespace != "prod" {
		t.Fatalf("bad create result: service=%#v namespace=%q", service, applier.namespace)
	}
	if !strings.Contains(applier.manifest, "name: opsi-svc-cache") {
		t.Fatalf("manifest was not applied:\n%s", applier.manifest)
	}
	got, err := store.GetManagedService(context.Background(), "demo", "cache")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.SecretName != "opsi-svc-cache" || got.Config["host"] != "cache.prod.svc.cluster.local" {
		t.Fatalf("service not stored: %#v", got)
	}
}

type recordingRunner struct {
	input []byte
	name  string
	args  []string
}

func (r *recordingRunner) Run(_ context.Context, input []byte, name string, args ...string) ([]byte, error) {
	r.input = input
	r.name = name
	r.args = args
	return []byte("ok"), nil
}

func TestKubectlApplierUsesStdinApply(t *testing.T) {
	runner := &recordingRunner{}
	err := (KubectlApplier{KubectlPath: "kubectl-test", Runner: runner}).Apply(context.Background(), "prod", []byte("apiVersion: v1\nkind: Service\n"))
	if err != nil {
		t.Fatal(err)
	}
	if runner.name != "kubectl-test" || strings.Join(runner.args, " ") != "apply -n prod -f -" || !strings.Contains(string(runner.input), "kind: Service") {
		t.Fatalf("bad kubectl call: name=%q args=%v input=%q", runner.name, runner.args, runner.input)
	}
}
