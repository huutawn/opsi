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

func TestExpandedV2ParseRenderAndCLIUpsertPreserveRuntimeIntent(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "apps/api"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "apps/web"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDockerfile(t, root, "apps/api/Dockerfile")
	writeDockerfile(t, root, "apps/web/Dockerfile")
	input := []byte(`# repository-owned runtime intent contains references, never secret values
version: 2
resources:
  - logicalName: database
    type: postgres
    managed: true
    required: true
    persistence:
      persistent: true
      sizeBytes: 1073741824
    settings:
      POSTGRES_DB: identity
services:
  - key: api
    build: {context: apps/api, dockerfile: apps/api/Dockerfile, platform: linux/amd64}
    watchPaths: []
    sharedPaths: []
    dependencies: []
    runtime:
      port: 8080
      environment: {Kafka__Enabled: "false"}
      capacity: {replicas: 1, cpuMilli: 250, memoryBytes: 268435456}
      exposure: {mode: internal}
      secrets:
        - {logicalName: jwt-signing-key, environmentName: Jwt__SigningKey, source: generated}
      dependencies:
        - target: database
          protocol: postgres
          required: true
          injections:
            - {environmentName: ConnectionStrings__Database, symbolicSource: resource.database.connection_string}
          verification: {type: consumer_http, path: /health/ready, expectedStatus: 200}
    deploy:
      production: {enabled: true, branches: [main]}
      preview: {enabled: false, pullRequests: false}
  - key: web
    build: {context: apps/web, dockerfile: apps/web/Dockerfile, platform: linux/amd64}
    watchPaths: []
    sharedPaths: []
    dependencies: [api]
    runtime:
      port: 3000
      bindings:
        - {kind: browser_http, target: api, path: /api}
    deploy:
      production: {enabled: true, branches: [main]}
      preview: {enabled: false, pullRequests: false}
`)
	cfg, migrated, err := validateConfigBytes(input, root)
	if err != nil || migrated {
		t.Fatalf("migrated=%t err=%v", migrated, err)
	}
	if len(cfg.Resources) != 1 || cfg.Services[0].Runtime == nil || cfg.Services[0].Runtime.Secrets[0].Source != "generated" {
		t.Fatalf("expanded config was not preserved: %+v", cfg)
	}
	rendered, err := RenderConfigV2(cfg)
	if err != nil {
		t.Fatal(err)
	}
	reparsed, _, err := validateConfigBytes(rendered, root)
	if err != nil {
		t.Fatal(err)
	}
	updated := testService("api", "apps/api/Dockerfile")
	updated.Build.Context = "apps/api"
	updated.SharedPaths = []string{"shared"}
	reparsed, err = UpsertService(reparsed, updated)
	if err != nil {
		t.Fatal(err)
	}
	if reparsed.Services[0].Runtime == nil || reparsed.Services[0].Runtime.Port != 8080 || len(reparsed.Resources) != 1 {
		t.Fatalf("CLI upsert discarded unrelated runtime/resource fields: %+v", reparsed)
	}
}

func TestExpandedV2RejectsPlaintextSecretFieldsAndSecretEnvironment(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root, "Dockerfile")
	withValue := []byte("version: 2\nservices:\n- key: api\n  build: {context: ., dockerfile: Dockerfile, platform: linux/amd64}\n  watchPaths: []\n  sharedPaths: []\n  dependencies: []\n  runtime:\n    secrets:\n    - {logicalName: token, environmentName: API_TOKEN, source: external, value: plaintext}\n  deploy:\n    production: {enabled: true, branches: [main]}\n    preview: {enabled: false, pullRequests: false}\n")
	if _, _, err := validateConfigBytes(withValue, root); err == nil {
		t.Fatal("plaintext secret field was accepted")
	}
	secretEnvironment := []byte("version: 2\nservices:\n- key: api\n  build: {context: ., dockerfile: Dockerfile, platform: linux/amd64}\n  watchPaths: []\n  sharedPaths: []\n  dependencies: []\n  runtime:\n    environment: {API_TOKEN: plaintext}\n  deploy:\n    production: {enabled: true, branches: [main]}\n    preview: {enabled: false, pullRequests: false}\n")
	if _, _, err := validateConfigBytes(secretEnvironment, root); err == nil {
		t.Fatal("secret-like non-secret environment entry was accepted")
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
