package repository

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMutationCreateAddUpdateAndIdempotentApply(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"api", "worker"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		writeDockerfile(t, root, filepath.ToSlash(filepath.Join(dir, "Dockerfile")))
	}
	service := CDService{}
	api := testService("api", "api/Dockerfile")
	api.Build.Context = "api"
	request := MutationRequest{Repository: root, ConfigPath: ".opsi/opsi-cd.yaml", WorkflowPath: ".github/workflows/opsi-cd.yaml", Service: api}
	first, err := service.ApplyMutation(request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Files[0].Action != "created" {
		t.Fatalf("first=%+v", first.Files)
	}
	worker := testService("worker", "worker/Dockerfile")
	worker.Build.Context = "worker"
	worker.Dependencies = []string{"api"}
	request.Service = worker
	request.Force = true
	request.Confirmed = true
	second, err := service.ApplyMutation(request)
	if err != nil {
		t.Fatal(err)
	}
	if second.Files[0].Action != "updated" {
		t.Fatalf("second=%+v", second.Files)
	}
	third, err := service.ApplyMutation(request)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range third.Files {
		if file.Action != "unchanged" {
			t.Fatalf("repeat changed file: %+v", file)
		}
	}
	api.SharedPaths = []string{"shared"}
	request.Service = api
	if _, err := service.ApplyMutation(request); err != nil {
		t.Fatal(err)
	}
	cfg, _, _, err := LoadConfig(root, request.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Services) != 2 || cfg.Services[0].Key != "api" || cfg.Services[1].Key != "worker" || cfg.Services[0].SharedPaths[0] != "shared" {
		t.Fatalf("cfg=%+v", cfg)
	}
}

func TestMutationMigratesV1AndRepeatIsUnchanged(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root, "Dockerfile")
	if err := os.MkdirAll(filepath.Join(root, ".opsi"), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := []byte("apiVersion: cd.opsi.dev/v1alpha1\nkind: ServiceBuild\nmetadata:\n  serviceKey: api\nbuild:\n  context: .\n  dockerfile: Dockerfile\n  platforms: [linux/amd64]\ndeploy:\n  production:\n    branches: [main]\n  preview:\n    pullRequests: false\n")
	if err := os.WriteFile(filepath.Join(root, ".opsi/opsi-cd.yaml"), legacy, 0o644); err != nil {
		t.Fatal(err)
	}
	request := MutationRequest{Repository: root, ConfigPath: ".opsi/opsi-cd.yaml", WorkflowPath: ".github/workflows/opsi-cd.yaml", Service: testService("api", "Dockerfile"), Force: true, Confirmed: true}
	service := CDService{}
	first, err := service.ApplyMutation(request)
	if err != nil {
		t.Fatal(err)
	}
	if !first.MigratedV1 || first.Files[0].Action != "updated" {
		t.Fatalf("first=%+v", first)
	}
	second, err := service.ApplyMutation(request)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range second.Files {
		if file.Action != "unchanged" {
			t.Fatalf("repeat changed file: %+v", file)
		}
	}
}
