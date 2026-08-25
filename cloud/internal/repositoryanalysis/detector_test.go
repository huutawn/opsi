package repositoryanalysis

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type memoryRepository map[string]string

func (m memoryRepository) ListFiles(_ context.Context, _ int64, _, _ string) ([]File, bool, error) {
	out := make([]File, 0, len(m))
	for name, data := range m {
		out = append(out, File{Path: name, Size: int64(len(data))})
	}
	return out, false, nil
}
func (m memoryRepository) ReadFile(_ context.Context, _ int64, _, _, name string, limit int64) ([]byte, error) {
	data, ok := m[name]
	if !ok {
		return nil, errors.New("missing")
	}
	if int64(len(data)) > limit {
		return nil, errors.New("limit")
	}
	return []byte(data), nil
}
func analyze(t *testing.T, files memoryRepository) Result {
	return analyzeRepository(t, "owner/repo", files)
}

func analyzeRepository(t *testing.T, repository string, files memoryRepository) Result {
	t.Helper()
	result, err := (Detector{Repository: files}).Analyze(context.Background(), Request{InstallationID: 1, RepositoryID: 2, Repository: repository, SelectedRef: "main", CommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestExplicitConfigIsAuthoritativeAndNeverExecutesSource(t *testing.T) {
	files := memoryRepository{
		".opsi/opsi-cd.yaml":  "version: 2\nservices:\n  - key: api\n    build:\n      context: apps/api\n      dockerfile: apps/api/Dockerfile\n      platform: linux/amd64\n",
		"apps/api/Dockerfile": "FROM scratch\nEXPOSE 8080\nRUN do-not-execute\n",
	}
	result := analyze(t, files)
	if result.Authority != "explicit_config" || len(result.Applications) != 1 || result.Applications[0].Port != 8080 || result.Applications[0].Build.DockerfilePath != "apps/api/Dockerfile" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestAnalysisCollectionsSerializeAsArraysWhenNothingIsDetected(t *testing.T) {
	result := analyze(t, memoryRepository{"Dockerfile": "FROM scratch\nEXPOSE 8080\n"})
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"resources", "dependencies", "bindings", "secrets"} {
		if strings.Contains(string(payload), `"`+field+`":null`) {
			t.Fatalf("%s must serialize as an array: %s", field, payload)
		}
	}
}

func TestComposeIdentityServiceInference(t *testing.T) {
	files := memoryRepository{
		"compose.yaml": `services:
  api:
    build: {context: be, dockerfile: Dockerfile}
    ports: ["8080:8080"]
    depends_on: [postgres, valkey, kafka]
  web:
    build: {context: tcip-fake, dockerfile: Dockerfile}
    ports: ["3000:3000"]
    depends_on: [api]
  postgres: {image: postgres:17}
  valkey: {image: valkey/valkey:8}
  kafka: {image: apache/kafka:4}
`,
		"be/Dockerfile":           "FROM mcr.microsoft.com/dotnet/aspnet:9.0\nEXPOSE 8080",
		"be/appsettings.json":     `{"ConnectionStrings":{"Database":""},"SignalR":{"Redis":{"ConnectionString":""}},"Kafka":{"Enabled":false},"Jwt":{"SigningKey":""}}`,
		"be/README.md":            "Kafka is optional. Set Kafka__Enabled=false.",
		"tcip-fake/Dockerfile":    "FROM node:24\nEXPOSE 3000",
		"tcip-fake/src/client.ts": "fetch('/api/login'); new HubConnectionBuilder().withUrl('/hubs/notifications')",
	}
	result := analyzeRepository(t, "owner/identity-service", files)
	if len(result.Applications) != 2 {
		t.Fatalf("apps=%+v", result.Applications)
	}
	applications := map[string]Application{}
	for _, application := range result.Applications {
		applications[application.Key] = application
	}
	if api := applications["identity-api"]; api.SourceKey != "api" || api.Root != "be" || api.Build.DockerfilePath != "be/Dockerfile" || api.Port != 8080 {
		t.Fatalf("identity-api=%+v", api)
	}
	if web := applications["identity-web"]; web.SourceKey != "web" || web.Root != "tcip-fake" || web.Build.DockerfilePath != "tcip-fake/Dockerfile" || web.Port != 3000 {
		t.Fatalf("identity-web=%+v", web)
	}
	foundPostgres, foundValkeyResource := false, false
	for _, resource := range result.Resources {
		foundPostgres = foundPostgres || resource.LogicalName == "postgres" && resource.Type == "postgres" && resource.Managed
		foundValkeyResource = foundValkeyResource || resource.LogicalName == "valkey" && resource.Type == "redis" && resource.Managed
	}
	foundDB, foundValkey, foundAPI, foundHub, foundJWT := false, false, false, false, false
	for _, dependency := range result.Dependencies {
		foundAPI = foundAPI || dependency.From == "identity-web" && dependency.To == "identity-api" && dependency.Strategy == "same_origin" && dependency.Path == "/api"
		foundHub = foundHub || dependency.From == "identity-web" && dependency.To == "identity-api" && dependency.Strategy == "same_origin" && dependency.Path == "/hubs/notifications"
		for _, mapping := range dependency.Injections {
			foundDB = foundDB || mapping.EnvironmentName == "ConnectionStrings__Database"
			foundValkey = foundValkey || mapping.EnvironmentName == "SignalR__Redis__ConnectionString"
		}
	}
	for _, secret := range result.Secrets {
		foundJWT = foundJWT || secret.Name == "jwt-signing-key" && secret.ApplicationKey == "identity-api" && secret.EnvironmentName == "Jwt__SigningKey" && secret.Display == "Generated and securely stored" && secret.SecretRef == "generated://jwt-signing-key"
	}
	kafkaDisabled := false
	for _, issue := range result.Issues {
		kafkaDisabled = kafkaDisabled || issue.Code == "KAFKA_UNSUPPORTED" && !issue.Blocking && issue.Resolution == "Kafka__Enabled=false"
	}
	if !foundPostgres || !foundValkeyResource || !foundDB || !foundValkey || !foundAPI || !foundHub || !foundJWT || !kafkaDisabled {
		t.Fatalf("dependencies=%+v secrets=%+v", result.Dependencies, result.Secrets)
	}
}

func TestTruncationAndUnsupportedKafkaBlockApproval(t *testing.T) {
	files := memoryRepository{"Dockerfile": "FROM scratch", "compose.yaml": "services:\n  app: {build: .}\n  kafka: {image: apache/kafka:4}\n"}
	detector := Detector{Repository: files, Limits: Limits{MaxFiles: 1, MaxFileBytes: 1024, MaxTotalBytes: 1024, MaxDuration: 1000000000}}
	result, err := detector.Analyze(context.Background(), Request{InstallationID: 1, RepositoryID: 2, Repository: "owner/repo", SelectedRef: "main", CommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated || !result.NeedsInput() {
		t.Fatalf("expected blocking truncation: %+v", result.Issues)
	}
}

func TestInvalidExplicitConfigDoesNotFallBackToHeuristics(t *testing.T) {
	result := analyze(t, memoryRepository{".opsi/opsi-cd.yaml": "version: 99\nservices: []", "Dockerfile": "FROM scratch"})
	if result.Authority != "explicit_config_invalid" || !result.NeedsInput() || len(result.Applications) != 0 {
		t.Fatalf("unexpected fallback: %+v", result)
	}
}

func TestExpandedExplicitRuntimeAndResourcesAreAuthoritative(t *testing.T) {
	result := analyze(t, memoryRepository{
		".opsi/opsi-cd.yaml": `version: 2
resources:
- logicalName: database
  type: postgres
  managed: true
  required: true
  persistence: {persistent: true, sizeBytes: 1073741824}
services:
- key: repo-api
  build: {context: api, dockerfile: api/Dockerfile, platform: linux/amd64}
  watchPaths: []
  sharedPaths: []
  dependencies: []
  runtime:
    port: 8080
    environment: {Kafka__Enabled: "false"}
    capacity: {replicas: 2, cpuMilli: 250, memoryBytes: 268435456}
    exposure: {mode: internal}
    secrets:
    - {logicalName: jwt-signing-key, environmentName: Jwt__SigningKey, source: generated}
    dependencies:
    - target: database
      protocol: postgres
      required: true
      injections:
      - {environmentName: ConnectionStrings__Database, symbolicSource: resource.database.connection_string}
  deploy:
    production: {enabled: true, branches: [main]}
    preview: {enabled: false, pullRequests: false}
`,
		"api/Dockerfile": "FROM scratch\nEXPOSE 9999\n",
		"compose.yaml":   "services:\n  repo-api:\n    build: {context: api, dockerfile: Dockerfile}\n    ports: [\"8080:8080\"]\n",
	})
	if result.NeedsInput() || len(result.Applications) != 1 || result.Applications[0].Key != "repo-api" || result.Applications[0].SourceKey != "repo-api" {
		t.Fatalf("unexpected explicit result: %+v", result)
	}
	app := result.Applications[0]
	if app.Port != 8080 || app.Capacity.Replicas != 2 || app.Environment["Kafka__Enabled"] != "false" || len(result.Resources) != 1 || result.Resources[0].Persistence == nil || len(result.Secrets) != 1 {
		t.Fatalf("runtime intent was not retained: %+v", result)
	}
}

func TestExplicitComposeConflictAndCanonicalCollisionBlockReview(t *testing.T) {
	result := analyze(t, memoryRepository{
		".opsi/opsi-cd.yaml": `version: 2
services:
- key: api_1
  build: {context: one, dockerfile: one/Dockerfile, platform: linux/amd64}
- key: api-1
  build: {context: two, dockerfile: two/Dockerfile, platform: linux/amd64}
`,
		"one/Dockerfile": "FROM scratch\nEXPOSE 8080",
		"two/Dockerfile": "FROM scratch\nEXPOSE 8080",
		"compose.yaml": `services:
  api_1:
    build: {context: different, dockerfile: Dockerfile}
    ports: ["9090:9090"]
`,
		"different/Dockerfile": "FROM scratch\nEXPOSE 9090",
	})
	if !result.NeedsInput() {
		t.Fatalf("conflicts did not block review: %+v", result.Issues)
	}
	foundCollision, foundBuildConflict := false, false
	for _, issue := range result.Issues {
		foundCollision = foundCollision || issue.Code == "CANONICAL_KEY_COLLISION"
		foundBuildConflict = foundBuildConflict || issue.Code == "EXPLICIT_COMPOSE_BUILD_CONFLICT"
	}
	if !foundCollision || !foundBuildConflict {
		t.Fatalf("issues=%+v", result.Issues)
	}
}

func TestManifestOnlyRepositoryIsDetectedWithoutDockerfile(t *testing.T) {
	result := analyze(t, memoryRepository{
		"k8s/deployment.yaml": `apiVersion: apps/v1
kind: Deployment
metadata: {name: worker}
spec:
  template:
    spec:
      containers:
      - image: ghcr.io/example/worker@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
        ports: [{containerPort: 8080}]
`,
	})
	if len(result.Applications) != 1 || result.Applications[0].Build.Strategy != "image" || result.Applications[0].Key != "repo-worker" || result.NeedsInput() {
		t.Fatalf("manifest-only detection failed: %+v", result)
	}
}
