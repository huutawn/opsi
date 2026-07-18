package repository

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestV1MigrationAndUpsertPreserveServices(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root, "Dockerfile")
	legacy := []byte("apiVersion: cd.opsi.dev/v1alpha1\nkind: ServiceBuild\nmetadata:\n  serviceKey: api\nbuild:\n  context: .\n  dockerfile: Dockerfile\n  platforms: [linux/amd64]\ndeploy:\n  production:\n    branches: [main]\n  preview:\n    pullRequests: true\n")
	cfg, migrated, err := validateConfigBytes(legacy, root)
	if err != nil || !migrated || len(cfg.Services) != 1 || cfg.Services[0].Key != "api" {
		t.Fatalf("cfg=%+v migrated=%t err=%v", cfg, migrated, err)
	}
	worker := testService("worker", "Dockerfile")
	worker.Dependencies = []string{"api"}
	cfg, err = UpsertService(cfg, worker)
	if err != nil {
		t.Fatal(err)
	}
	api := cfg.Services[0]
	api.SharedPaths = []string{"shared"}
	cfg, err = UpsertService(cfg, api)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Services) != 2 || cfg.Services[0].Key != "api" || cfg.Services[1].Key != "worker" {
		t.Fatalf("services=%+v", cfg.Services)
	}
	rendered, err := RenderConfigV2(cfg)
	if err != nil {
		t.Fatal(err)
	}
	again, migratedAgain, err := validateConfigBytes(rendered, root)
	if err != nil || migratedAgain {
		t.Fatalf("migrated=%t err=%v", migratedAgain, err)
	}
	second, _ := RenderConfigV2(again)
	if string(rendered) != string(second) {
		t.Fatal("v2 migration is not idempotent")
	}
}

func TestConfigRejectsUnknownDuplicateCycleAndMissingDependency(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root, "Dockerfile")
	unknown := []byte("version: 2\nservices: []\nprojectID: forbidden\n")
	if _, _, err := validateConfigBytes(unknown, root); err == nil {
		t.Fatal("unknown field accepted")
	}
	valid, err := RenderConfig(ConfigOptions{ServiceKey: "api", Context: ".", Dockerfile: "Dockerfile", Platform: "linux/amd64", Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := validateConfigBytes(append(valid, []byte("---\n[unterminated")...), root); err == nil {
		t.Fatal("trailing malformed YAML accepted")
	}
	cases := []ConfigV2{
		{Version: 2, Services: []ServiceV2{testService("api", "Dockerfile"), testService("api", "Dockerfile")}},
		{Version: 2, Services: []ServiceV2{withDependencies(testService("api", "Dockerfile"), "missing")}},
		{Version: 2, Services: []ServiceV2{withDependencies(testService("api", "Dockerfile"), "worker"), withDependencies(testService("worker", "Dockerfile"), "api")}},
	}
	for _, cfg := range cases {
		if err := ValidateConfig(root, &cfg); err == nil {
			t.Fatalf("invalid config accepted: %+v", cfg)
		}
	}
}

func TestConfigRejectsTraversalAndEscapingSymlink(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root, "Dockerfile")
	bad := testService("api", "Dockerfile")
	bad.WatchPaths = []string{"../outside"}
	cfg := ConfigV2{Version: 2, Services: []ServiceV2{bad}}
	if err := ValidateConfig(root, &cfg); err == nil {
		t.Fatal("traversal accepted")
	}
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outside, "shared"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "shared"), filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	bad = testService("api", "Dockerfile")
	bad.SharedPaths = []string{"escape"}
	cfg.Services = []ServiceV2{bad}
	if err := ValidateConfig(root, &cfg); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("escaping symlink error=%v", err)
	}
}

func testService(key, dockerfile string) ServiceV2 {
	return ServiceV2{Key: key, Build: BuildV2{Context: ".", Dockerfile: dockerfile, Platform: "linux/amd64"}, WatchPaths: []string{}, SharedPaths: []string{}, Dependencies: []string{}, Deploy: DeployV2{Production: ProductionV2{Enabled: true, Branches: []string{"main"}}, Preview: PreviewV2{}}}
}

func withDependencies(service ServiceV2, dependencies ...string) ServiceV2 {
	service.Dependencies = dependencies
	return service
}
func writeDockerfile(t *testing.T, root, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}
