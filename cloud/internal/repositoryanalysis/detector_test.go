package repositoryanalysis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
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
		foundPostgres = foundPostgres || resource.LogicalName == "postgres" && resource.Type == "postgres" && resource.Managed && resource.Persistence != nil && resource.Persistence.Persistent
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

func TestAcceptanceProfileExcludesLowSignalSourceAndReadsRootDocs(t *testing.T) {
	files := memoryRepository{
		"compose.yaml": `services:
  postgres: {image: postgres:17}
  redis: {image: redis:7}
  kafka: {image: apache/kafka:4}
  kafka-init: {image: apache/kafka:4}
`,
		"be/Dockerfile": "FROM scratch\nEXPOSE 8080\n",
		"be/src/IdentityService.Api/appsettings.json":                           `{"Kafka":{"Enabled":true}}`,
		"be/src/IdentityService.Api/appsettings.Development.json":               `{"ConnectionStrings":{"Database":""},"SignalR":{"Redis":{"ConnectionString":""}},"Jwt":{"Issuer":"identity-service","Audience":"identity-api","Key":"must-not-be-copied","AccessTokenMinutes":15,"RefreshTokenDays":30}}`,
		"be/src/IdentityService.Api/Program.cs":                                 `builder.Configuration["Jwt:Issuer"]; builder.Configuration["Jwt:Audience"]; builder.Configuration["Jwt:Key"]; app.MapHub<NotificationHub>("/hubs/notifications");`,
		"docs/opsi-acceptance-profile.md":                                       "Set `Kafka__Enabled=false` for this acceptance profile.",
		"tcip-fake/Dockerfile":                                                  "FROM scratch\nEXPOSE 3000\n",
		"tcip-fake/next.config.ts":                                              `const backend = process.env.BACKEND_URL; async rewrites() { return [{source: "/api/:path*", destination: backend + "/api/:path*"}, {source: "/hubs/:path*", destination: backend + "/hubs/:path*"}] }`,
		"tcip-fake/features/notifications/services/notification-hub.service.ts": `new HubConnectionBuilder().withUrl("/hubs/notifications");`,
	}
	for i := 0; i < 160; i++ {
		files[fmt.Sprintf("be/src/IdentityService.Api/Services/Impl/Service%03d.cs", i)] = "sealed class Service {}"
	}
	result := analyzeRepository(t, "owner/learn-asp-.net", files)
	if result.Truncated || result.EvidenceCoverage.CandidatesFound >= DefaultLimits().MaxFiles {
		t.Fatalf("coverage=%+v truncation=%q", result.EvidenceCoverage, result.TruncationReason)
	}
	if len(result.Applications) != 2 {
		t.Fatalf("applications=%+v", result.Applications)
	}
	applicationByRoot := map[string]string{}
	postgresStorageValid := false
	for _, application := range result.Applications {
		applicationByRoot[application.Root] = application.Key
	}
	for _, resource := range result.Resources {
		postgresStorageValid = postgresStorageValid || resource.Type == "postgres" && resource.Persistence != nil && resource.Persistence.Persistent && resource.Persistence.SizeBytes == resourcev1.DefaultPostgresStorageBytes && resource.Persistence.PolicyRef == resourcev1.StoragePolicyDefault
	}
	if !postgresStorageValid {
		t.Fatalf("PostgreSQL storage default was not materialized in the reviewed plan: %+v", result.Resources)
	}
	backendKey, frontendKey := applicationByRoot["be"], applicationByRoot["tcip-fake"]
	for _, application := range result.Applications {
		if application.Key != backendKey {
			continue
		}
		want := map[string]string{"Jwt__Issuer": "identity-service", "Jwt__Audience": "identity-api", "Jwt__AccessTokenMinutes": "15", "Jwt__RefreshTokenDays": "30"}
		for name, value := range want {
			if application.Environment[name] != value {
				t.Fatalf("backend environment %s=%q, want %q: %+v", name, application.Environment[name], value, application.Environment)
			}
		}
		for _, value := range application.Environment {
			if value == "must-not-be-copied" {
				t.Fatalf("JWT key leaked into environment: %+v", application.Environment)
			}
		}
	}
	foundJWTKey := false
	for _, secret := range result.Secrets {
		foundJWTKey = foundJWTKey || secret.ApplicationKey == backendKey && secret.Name == "jwt-signing-key" && secret.EnvironmentName == "Jwt__Key" && secret.Generated
	}
	if !foundJWTKey {
		t.Fatalf("exact JWT key reference was not inferred: %+v", result.Secrets)
	}
	proxyDependencies := 0
	for _, dependency := range result.Dependencies {
		if dependency.From == backendKey && dependency.To == frontendKey {
			t.Fatalf("server route declaration must not become a browser dependency: %+v", dependency)
		}
		if dependency.From == frontendKey && dependency.To == backendKey {
			proxyDependencies++
			if dependency.Strategy != "internal_http" || dependency.Path != "/api" || len(dependency.Injections) != 1 || dependency.Injections[0].EnvironmentName != "BACKEND_URL" || dependency.Injections[0].SymbolicSource != "application.internal_url" {
				t.Fatalf("proxy dependency=%+v", dependency)
			}
			evidence := strings.Builder{}
			for _, item := range dependency.Evidence {
				evidence.WriteString(item.Reason)
			}
			if !strings.Contains(evidence.String(), "/api") || !strings.Contains(evidence.String(), "/hubs/notifications") {
				t.Fatalf("proxy routes missing from evidence: %+v", dependency.Evidence)
			}
		}
	}
	if proxyDependencies != 1 {
		t.Fatalf("proxy dependencies=%d all=%+v", proxyDependencies, result.Dependencies)
	}
	kafkaResources, kafkaIssues := 0, 0
	for _, resource := range result.Resources {
		if resource.Type == "kafka" {
			kafkaResources++
			if resource.LogicalName != "kafka" || resource.Managed || resource.Required {
				t.Fatalf("kafka resource=%+v", resource)
			}
		}
	}
	for _, issue := range result.Issues {
		if issue.Code == "KAFKA_UNSUPPORTED" {
			kafkaIssues++
			if issue.Blocking || issue.Resolution != "Kafka__Enabled=false" {
				t.Fatalf("kafka issue=%+v", issue)
			}
		}
		if issue.Code == "ANALYSIS_TRUNCATED" {
			t.Fatalf("unexpected truncation issue=%+v", issue)
		}
	}
	if kafkaResources != 1 || kafkaIssues != 1 {
		t.Fatalf("resources=%+v issues=%+v", result.Resources, result.Issues)
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

func TestKafkaInitMergesIntoDisabledKafkaEvidence(t *testing.T) {
	result := analyze(t, memoryRepository{
		"compose.yaml": `services:
  api:
    build: {context: api, dockerfile: Dockerfile}
    depends_on: [kafka-init]
    environment: {Kafka__Enabled: "false"}
  kafka: {image: apache/kafka:4}
  kafka-init: {image: apache/kafka:4}
`,
		"api/Dockerfile": "FROM scratch\nEXPOSE 8080\n",
	})
	kafkaResources, kafkaIssues := 0, 0
	for _, resource := range result.Resources {
		if resource.Type == "kafka" {
			kafkaResources++
			if resource.LogicalName != "kafka" || resource.Managed || resource.Required || len(resource.Evidence) != 2 {
				t.Fatalf("kafka resource=%+v", resource)
			}
		}
	}
	for _, issue := range result.Issues {
		if issue.Code == "KAFKA_UNSUPPORTED" {
			kafkaIssues++
			if issue.Blocking || issue.Resolution != "Kafka__Enabled=false" {
				t.Fatalf("kafka issue=%+v", issue)
			}
		}
	}
	if kafkaResources != 1 || kafkaIssues != 1 {
		t.Fatalf("resources=%+v issues=%+v", result.Resources, result.Issues)
	}
	for _, dependency := range result.Dependencies {
		if dependency.Protocol == "kafka" && (dependency.To != "kafka" || dependency.Required) {
			t.Fatalf("dependency=%+v", dependency)
		}
	}
}

func TestScopedAnalysisUsesSameCommitAndCanonicalScopeHash(t *testing.T) {
	files := memoryRepository{
		"api/Dockerfile":       "FROM scratch\nEXPOSE 8080\n",
		"web/Dockerfile":       "FROM scratch\nEXPOSE 3000\n",
		"api/vendor/client.ts": "fetch('/ignored')",
	}
	detector := Detector{Repository: files, Now: func() time.Time { return time.Unix(1, 0).UTC() }}
	request := Request{InstallationID: 1, RepositoryID: 2, Repository: "owner/repo", SelectedRef: "main", CommitSHA: strings.Repeat("a", 40), Scope: Scope{ApplicationRoots: []string{"api", "api"}, ExcludePaths: []string{"api/vendor"}}}
	first, err := detector.Analyze(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.Scope = Scope{ExcludePaths: []string{"api/vendor"}, ApplicationRoots: []string{"api"}}
	second, err := detector.Analyze(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.CommitSHA != request.CommitSHA || first.ScopeHash == "" || first.ScopeHash != second.ScopeHash || len(first.Applications) != 1 || first.Applications[0].Root != "api" || first.EvidenceCoverage.CandidatesFound != 1 {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
}

type delayedRepository struct {
	files   memoryRepository
	reverse bool
	delay   time.Duration
	mu      sync.Mutex
	active  int
	peak    int
}

func (r *delayedRepository) ListFiles(ctx context.Context, installationID int64, repository, sha string) ([]File, bool, error) {
	return r.files.ListFiles(ctx, installationID, repository, sha)
}

func (r *delayedRepository) ReadFile(ctx context.Context, _ int64, _, _, name string, _ int64) ([]byte, error) {
	r.mu.Lock()
	r.active++
	if r.active > r.peak {
		r.peak = r.active
	}
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.active--
		r.mu.Unlock()
	}()
	delay := r.delay
	if r.reverse == (name < "app-08/Dockerfile") {
		delay += 15 * time.Millisecond
	}
	select {
	case <-time.After(delay):
		return []byte(r.files[name]), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestBoundedConcurrentReadsAreCompletionOrderIndependent(t *testing.T) {
	files := memoryRepository{}
	for i := 0; i < 16; i++ {
		files[fmt.Sprintf("app-%02d/Dockerfile", i)] = fmt.Sprintf("FROM scratch\nEXPOSE %d\n", 8000+i)
	}
	now := func() time.Time { return time.Unix(10, 0).UTC() }
	analyzeDelayed := func(reverse bool) (Result, int, time.Duration) {
		repository := &delayedRepository{files: files, reverse: reverse, delay: 15 * time.Millisecond}
		started := time.Now()
		result, err := (Detector{Repository: repository, Now: now}).Analyze(context.Background(), Request{InstallationID: 1, RepositoryID: 2, Repository: "owner/repo", SelectedRef: "main", CommitSHA: strings.Repeat("a", 40)})
		if err != nil {
			t.Fatal(err)
		}
		return result, repository.peak, time.Since(started)
	}
	first, peak, elapsed := analyzeDelayed(false)
	second, secondPeak, _ := analyzeDelayed(true)
	firstJSON, _ := json.Marshal(first)
	secondJSON, _ := json.Marshal(second)
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("analysis changed with completion order\nfirst=%s\nsecond=%s", firstJSON, secondJSON)
	}
	if peak < 2 || peak > 8 || secondPeak > 8 || elapsed >= 250*time.Millisecond {
		t.Fatalf("peak=%d second_peak=%d elapsed=%s", peak, secondPeak, elapsed)
	}
}

func TestTruncationReasonsAreSpecific(t *testing.T) {
	tests := []struct {
		name   string
		files  memoryRepository
		limits Limits
		reason string
	}{
		{name: "file_count", files: memoryRepository{"Dockerfile": "FROM scratch", "README.md": "docs"}, limits: Limits{MaxFiles: 1, MaxFileBytes: 100, MaxTotalBytes: 100, MaxDuration: time.Second}, reason: "file_count"},
		{name: "blob_size", files: memoryRepository{"Dockerfile": "FROM scratch\nEXPOSE 8080"}, limits: Limits{MaxFiles: 10, MaxFileBytes: 4, MaxTotalBytes: 100, MaxDuration: time.Second}, reason: "blob_size"},
		{name: "total_bytes", files: memoryRepository{"a/Dockerfile": "FROM scratch", "b/Dockerfile": "FROM scratch"}, limits: Limits{MaxFiles: 10, MaxFileBytes: 100, MaxTotalBytes: 15, MaxDuration: time.Second}, reason: "total_bytes"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := (Detector{Repository: test.files, Limits: test.limits}).Analyze(context.Background(), Request{InstallationID: 1, RepositoryID: 2, Repository: "owner/repo", SelectedRef: "main", CommitSHA: strings.Repeat("a", 40)})
			if err != nil {
				t.Fatal(err)
			}
			if !result.Truncated || result.TruncationReason != test.reason {
				t.Fatalf("result=%+v", result)
			}
		})
	}
}

func TestOversizedExplicitConfigBlocksWithoutFallingBack(t *testing.T) {
	result, err := (Detector{
		Repository: memoryRepository{
			".opsi/opsi-cd.yaml": "version: 2\nservices:\n  - key: api\n",
			"compose.yaml":       "services:\n  api:\n    image: example/api:latest\n    ports: [\"8080:8080\"]\n",
		},
		Limits: Limits{MaxFiles: 10, MaxFileBytes: 8, MaxTotalBytes: 100, MaxDuration: time.Second},
	}).Analyze(context.Background(), Request{InstallationID: 1, RepositoryID: 2, Repository: "owner/repo", SelectedRef: "main", CommitSHA: strings.Repeat("a", 40)})
	if err != nil {
		t.Fatal(err)
	}
	if result.Authority != "explicit_config_unreadable" || result.TruncationReason != "blob_size" {
		t.Fatalf("result=%+v", result)
	}
	issueCodes := map[string]bool{}
	for _, issue := range result.Issues {
		issueCodes[issue.Code] = issue.Blocking
	}
	if !issueCodes["EXPLICIT_CONFIG_UNREADABLE"] || !issueCodes["ANALYSIS_TRUNCATED"] {
		t.Fatalf("issues=%+v", result.Issues)
	}
}
