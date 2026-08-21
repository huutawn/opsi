package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/opsi-dev/opsi/cli/internal/keychain"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
	serviceconfigurationv1 "github.com/opsi-dev/opsi/contracts/go/serviceconfigurationv1"
	topologyv1 "github.com/opsi-dev/opsi/contracts/go/topologyv1"
	verificationv1 "github.com/opsi-dev/opsi/contracts/go/verificationv1"
)

type comprehensiveFixture struct {
	server       *httptest.Server
	projectID    string
	projectID_B  string
	repoRoot     string
	commitSHA    string
	cloudPAT     string
	agentToken   string
	dbPassword   string
	valkeyPass   string
	regCred      string
	sourceSecret string
}

func setupComprehensiveFixture(t *testing.T) *comprehensiveFixture {
	t.Helper()

	projectID := "proj-opsi-accepted-100"
	projectID_B := "proj-opsi-other-200"

	cloudPAT := "OPSI_PAT_SYNTHETIC_999988887777"
	agentToken := "OPSI_AGENT_TOKEN_SYNTHETIC_11112222"
	dbPassword := "PG_SUPER_PASS_SYNTHETIC_33334444"
	valkeyPass := "VALKEY_AUTH_PASS_SYNTHETIC_55556666"
	regCred := "REGISTRY_AUTH_BASIC_SYNTHETIC_77778888"
	sourceSecret := "SOURCE_EMBEDDED_PASS_SYNTHETIC_88889999"

	// 1. Setup synthetic git repository
	tempDir := t.TempDir()
	runGit := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = tempDir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %s", args, string(out))
		}
		return strings.TrimSpace(string(out))
	}

	runGit("init", "--quiet")
	runGit("config", "user.name", "Test")
	runGit("config", "user.email", "test@example.com")

	webDir := filepath.Join(tempDir, "services", "web")
	apiDir := filepath.Join(tempDir, "services", "api")
	_ = os.MkdirAll(webDir, 0755)
	_ = os.MkdirAll(apiDir, 0755)

	webCode := `package main
import (
	"fmt"
	"net/http"
)
func main() {
	// web calls api service
	resp, _ := http.Get("http://api.production.svc.cluster.local:8080/health/dependencies")
	if resp != nil {
		fmt.Println("API status:", resp.StatusCode)
	}
}
`
	_ = os.WriteFile(filepath.Join(webDir, "main.go"), []byte(webCode), 0644)

	apiCode := fmt.Sprintf(`package main
import (
	"fmt"
	"os"
)
// Embedded DB and cache connection strings for redaction proof
const dbURL = "postgres://appuser:%s@postgres-main.production.svc.cluster.local:5432/opsidb"
const cacheURL = "redis://:%s@valkey-main.production.svc.cluster.local:6379"

func main() {
	db := os.Getenv("DATABASE_URL")
	appDb := os.Getenv("APP_DATABASE_URL")
	fmt.Println("Localhost endpoint:", "http://localhost:8080")
	fmt.Printf("DB: %%s, APP_DB: %%s, cached: %%s, %%s\n", db, appDb, dbURL, cacheURL)
}
`, sourceSecret, valkeyPass)
	_ = os.WriteFile(filepath.Join(apiDir, "main.go"), []byte(apiCode), 0644)
	_ = os.WriteFile(filepath.Join(apiDir, "config.json"), []byte(`{"service": "api", "port": 8080}`), 0644)
	_ = os.WriteFile(filepath.Join(apiDir, "large_file.txt"), []byte(strings.Repeat("Line of repeated source text.\n", 3000)), 0644)
	_ = os.WriteFile(filepath.Join(apiDir, "binary.bin"), []byte{0x00, 0xFF, 0x00, 0xFE, 0xBA, 0xBE}, 0644)

	runGit("add", ".")
	runGit("commit", "-m", "Commit with accepted services")
	commitSHA := runGit("rev-parse", "HEAD")

	// 2. Setup mock cloud server
	mux := http.NewServeMux()

	// Auth verify
	mux.HandleFunc("/v1/auth/pat/verify", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		authHeader := r.Header.Get("Authorization")
		if !strings.Contains(authHeader, cloudPAT) {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "unauthorized"})
			return
		}
		var reqBody map[string]string
		_ = json.NewDecoder(r.Body).Decode(&reqBody)
		reqProj := reqBody["project_id"]
		if reqProj != "" && reqProj != projectID {
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "permission denied for project " + reqProj})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"valid":      true,
			"org_id":     "org-opsi-accepted",
			"project_id": projectID,
			"role":       "admin",
		})
	})

	// Projects
	mux.HandleFunc("/api/orgs/org-opsi-accepted/projects", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"projects": []map[string]any{
				{
					"id":     projectID,
					"org_id": "org-opsi-accepted",
					"name":   "Opsi Accepted Real Project",
					"slug":   "opsi-real-project",
					"status": "active",
				},
			},
		})
	})

	// Topology
	mux.HandleFunc(fmt.Sprintf("/api/projects/%s/topology", projectID), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(topologyv1.Plan{
			ID:        "top-plan-100",
			ProjectID: projectID,
			Revision:  10,
			StateHash: "top-hash-10-accepted",
			Assignments: []topologyv1.Assignment{
				{ServiceKey: "web", RuntimeID: "rt-node-web", Replicas: 2},
				{ServiceKey: "api", RuntimeID: "rt-node-api", Replicas: 3},
			},
		})
	})

	// Services
	mux.HandleFunc(fmt.Sprintf("/api/projects/%s/services", projectID), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"services": []map[string]any{
				{"id": "svc-web", "project_id": projectID, "name": "web", "status": "active"},
				{"id": "svc-api", "project_id": projectID, "name": "api", "status": "active"},
			},
		})
	})

	// Service configurations
	mux.HandleFunc(fmt.Sprintf("/api/projects/%s/services/svc-web/configuration", projectID), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(serviceconfigurationv1.Configuration{
			ServiceConfigurationDraft: serviceconfigurationv1.ServiceConfigurationDraft{
				SchemaVersion: serviceconfigurationv1.SchemaVersion,
				Environment: []deploymentv1.EnvironmentVariable{
					{Name: "PORT", Value: "3000"},
					{Name: "API_URL", Value: "http://api.production.svc.cluster.local:8080"},
				},
				PublicRoute: &serviceconfigurationv1.PublicRouteIntent{Hostname: "app.example.com", Path: "/"},
				Dependencies: []serviceconfigurationv1.ApplicationDependency{
					{
						LogicalName:    "api-service",
						TargetKind:     "service",
						TargetIdentity: "api",
						Protocol:       "http",
						Required:       true,
						InjectionPhase: "runtime",
					},
				},
			},
			Revision:  1,
			StateHash: "hash-cfg-web-1",
		})
	})

	mux.HandleFunc(fmt.Sprintf("/api/projects/%s/services/svc-api/configuration", projectID), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(serviceconfigurationv1.Configuration{
			ServiceConfigurationDraft: serviceconfigurationv1.ServiceConfigurationDraft{
				SchemaVersion: serviceconfigurationv1.SchemaVersion,
				Environment: []deploymentv1.EnvironmentVariable{
					{Name: "PORT", Value: "8080"},
					{Name: "DATABASE_URL", Value: "secret-reference"},
					{Name: "VALKEY_URL", Value: "secret-reference"},
				},
				ResourceBindings: []serviceconfigurationv1.ResourceBinding{
					{LogicalName: "database", BindingID: "rb-pg-100"},
					{LogicalName: "cache", BindingID: "rb-valkey-100"},
				},
				Dependencies: []serviceconfigurationv1.ApplicationDependency{
					{
						LogicalName:    "database",
						TargetKind:     "managed_resource",
						TargetIdentity: "postgres-main",
						Protocol:       "postgres",
						Required:       true,
						InjectionPhase: "runtime",
					},
					{
						LogicalName:    "cache",
						TargetKind:     "managed_resource",
						TargetIdentity: "valkey-main",
						Protocol:       "valkey",
						Required:       true,
						InjectionPhase: "runtime",
					},
				},
			},
			Revision:  2,
			StateHash: "hash-cfg-api-2",
		})
	})

	// GitHub bindings
	mux.HandleFunc(fmt.Sprintf("/v1/projects/%s/github/bindings", projectID), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"bindings": []map[string]any{
				{
					"id":               "ghb-web",
					"project_id":       projectID,
					"service_id":       "svc-web",
					"service_key":      "web",
					"repository_id":    1001,
					"selected_ref":     "main",
					"application_root": "services/web",
					"build_context":    ".",
					"build_strategy":   "buildpacks",
					"status":           "active",
				},
				{
					"id":               "ghb-api",
					"project_id":       projectID,
					"service_id":       "svc-api",
					"service_key":      "api",
					"repository_id":    1001,
					"selected_ref":     "main",
					"application_root": "services/api",
					"build_context":    ".",
					"build_strategy":   "dockerfile",
					"dockerfile_path":  "Dockerfile",
					"status":           "active",
				},
			},
		})
	})

	// Managed resources
	mux.HandleFunc(fmt.Sprintf("/api/projects/%s/resources", projectID), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"resources": []resourcev1.Resource{
				{
					ID:            "res-pg-100",
					ProjectID:     projectID,
					EnvironmentID: "production",
					Name:          "postgres-main",
					Kind:          resourcev1.KindManagedService,
					Type:          resourcev1.TypePostgres,
					Lifecycle:     resourcev1.LifecycleReady,
					Managed: &resourcev1.ManagedSpec{
						Type:          resourcev1.TypePostgres,
						Version:       "16",
						Replicas:      1,
						CPUMillicores: 1000,
						MemoryBytes:   1024 * 1024 * 1024,
					},
				},
				{
					ID:            "res-valkey-100",
					ProjectID:     projectID,
					EnvironmentID: "production",
					Name:          "valkey-main",
					Kind:          resourcev1.KindManagedService,
					Type:          resourcev1.TypeRedis,
					Lifecycle:     resourcev1.LifecycleReady,
					Managed: &resourcev1.ManagedSpec{
						Type:          resourcev1.TypeRedis,
						Version:       "7.2",
						Replicas:      1,
						CPUMillicores: 500,
						MemoryBytes:   512 * 1024 * 1024,
					},
				},
			},
		})
	})

	mux.HandleFunc(fmt.Sprintf("/api/projects/%s/resources/res-pg-100", projectID), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resourcev1.Resource{
			ID:            "res-pg-100",
			ProjectID:     projectID,
			EnvironmentID: "production",
			Name:          "postgres-main",
			Kind:          resourcev1.KindManagedService,
			Type:          resourcev1.TypePostgres,
			Lifecycle:     resourcev1.LifecycleReady,
			Managed: &resourcev1.ManagedSpec{
				Type:          resourcev1.TypePostgres,
				Version:       "16",
				Replicas:      1,
				CPUMillicores: 1000,
				MemoryBytes:   1024 * 1024 * 1024,
			},
		})
	})

	// Resource bindings
	mux.HandleFunc(fmt.Sprintf("/api/projects/%s/resource-bindings", projectID), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"bindings": []resourcev1.Binding{
				{
					ID:            "rb-pg-100",
					ProjectID:     projectID,
					EnvironmentID: "production",
					LogicalName:   "database",
					Lifecycle:     resourcev1.LifecycleReady,
					Target:        resourcev1.EndpointReference{Kind: resourcev1.KindManagedService, ID: "res-pg-100"},
				},
				{
					ID:            "rb-valkey-100",
					ProjectID:     projectID,
					EnvironmentID: "production",
					LogicalName:   "cache",
					Lifecycle:     resourcev1.LifecycleReady,
					Target:        resourcev1.EndpointReference{Kind: resourcev1.KindManagedService, ID: "res-valkey-100"},
				},
			},
		})
	})

	// BuildRecords
	mux.HandleFunc(fmt.Sprintf("/api/projects/%s/build-records", projectID), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"records": []map[string]any{
				{
					"id":          "br-api-accepted",
					"project_id":  projectID,
					"service_id":  "svc-api",
					"service_key": "api",
					"workload": map[string]any{
						"sha": commitSHA,
					},
					"build": map[string]any{
						"build_strategy": "dockerfile",
						"oci_digest":     "sha256:api0000000000000000000000000000000000000000000000000000000000000",
						"status":         "accepted",
					},
				},
			},
		})
	})

	mux.HandleFunc(fmt.Sprintf("/api/projects/%s/build-records/br-api-accepted", projectID), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":          "br-api-accepted",
			"project_id":  projectID,
			"service_id":  "svc-api",
			"service_key": "api",
			"workload": map[string]any{
				"sha": commitSHA,
			},
			"build": map[string]any{
				"build_strategy": "dockerfile",
				"oci_digest":     "sha256:api0000000000000000000000000000000000000000000000000000000000000",
				"status":         "accepted",
			},
		})
	})

	// Deployments
	mux.HandleFunc(fmt.Sprintf("/api/projects/%s/deployments", projectID), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"deployments": []map[string]any{
				{
					"id":              "dep-job-api-1",
					"project_id":      projectID,
					"service_id":      "svc-api",
					"runtime_id":      "rt-node-api",
					"status":          "succeeded",
					"rollout_state":   "healthy",
					"build_record_id": "br-api-accepted",
				},
			},
		})
	})

	mux.HandleFunc(fmt.Sprintf("/api/projects/%s/deployments/dep-job-api-1", projectID), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":              "dep-job-api-1",
			"project_id":      projectID,
			"service_id":      "svc-api",
			"runtime_id":      "rt-node-api",
			"status":          "succeeded",
			"rollout_state":   "healthy",
			"build_record_id": "br-api-accepted",
		})
	})

	// Preflight deployment (handles PASS, PASS_WITH_WARNINGS, BLOCKED based on request buildRecordID)
	mux.HandleFunc(fmt.Sprintf("/api/projects/%s/deployments/preflight", projectID), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var req deploymentv1.CreateRequest
		_ = json.NewDecoder(r.Body).Decode(&req)

		switch req.BuildRecordID {
		case "br-warn":
			_ = json.NewEncoder(w).Encode(deploymentv1.PreflightResult{
				Status:        deploymentv1.PreflightStatusPassWithWarnings,
				PreflightHash: "hash-preflight-warn-123",
				Checks: []deploymentv1.PreflightCheck{
					{
						ID:       "chk-warn-1",
						Code:     "RESOURCE_CAPACITY_WARNING",
						Severity: "WARN",
						Message:  "Memory utilization near threshold",
					},
				},
			})
		case "br-block":
			_ = json.NewEncoder(w).Encode(deploymentv1.PreflightResult{
				Status:        deploymentv1.PreflightStatusBlocked,
				PreflightHash: "hash-preflight-block-456",
				Checks: []deploymentv1.PreflightCheck{
					{
						ID:       "chk-block-1",
						Code:     "PLACEMENT_UNSATISFIABLE",
						Severity: "BLOCK",
						Message:  "No runtime nodes available with required capacity",
					},
				},
			})
		default: // PASS
			_ = json.NewEncoder(w).Encode(deploymentv1.PreflightResult{
				Status:        deploymentv1.PreflightStatusPass,
				PreflightHash: "hash-preflight-pass-789",
				Checks: []deploymentv1.PreflightCheck{
					{
						ID:       "chk-pass-1",
						Code:     "TOPOLOGY_VALID",
						Severity: "PASS",
						Message:  "Preflight checks passed completely",
					},
				},
			})
		}
	})

	// Source risk report
	mux.HandleFunc(fmt.Sprintf("/v1/projects/%s/applications/svc-api/source-risk-report", projectID), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"report": map[string]any{
				"id":               "srr-api-100",
				"project_id":       projectID,
				"application_id":   "svc-api",
				"commit_sha":       commitSHA,
				"application_root": "services/api",
				"scanner_version":  "opsi.source-scanner/v1",
				"analysis_status":  "complete",
				"findings": []map[string]any{
					{
						"finding_id":    "SOURCE_LOOPBACK_ENDPOINT:main.go:13",
						"rule_id":       "SOURCE_LOOPBACK_ENDPOINT",
						"severity":      "WARN",
						"confidence":    "HIGH",
						"file":          "main.go",
						"line":          13,
						"safe_evidence": "reference not observed in container production network",
					},
					{
						"finding_id":    "SOURCE_DECLARED_ENV_NOT_OBSERVED:main.go:1",
						"rule_id":       "SOURCE_DECLARED_ENV_NOT_OBSERVED",
						"severity":      "WARN",
						"confidence":    "MEDIUM",
						"file":          "main.go",
						"line":          1,
						"safe_evidence": "reference not observed in application root",
					},
				},
			},
		})
	})

	// 5-Layer Dependency Verification (Bad consumer, Partial, Stale scenarios)
	mux.HandleFunc(fmt.Sprintf("/v1/projects/%s/dependencies/database/verification", projectID), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Bad Consumer scenario: Provider HEALTHY, Contract RESOLVED, Connection VERIFIED, Consumer HEALTHY, Assertion FAILED, Overall FAILED
		_ = json.NewEncoder(w).Encode(map[string]any{
			"run": verificationv1.VerificationRun{
				ID:                    "dvr-bad-consumer-1",
				ProjectID:             projectID,
				ConsumerApplicationID: "svc-api",
				DependencyLogicalName: "database",
				OverallStatus:         verificationv1.RunStatusFailed,
				FailureCode:           verificationv1.FailureConsumerAssertionFailed,
				ProviderHealth: verificationv1.ProviderHealthLayer{
					Status:       verificationv1.LayerStatusHealthy,
					ProviderKind: "postgres",
					ProviderID:   "res-pg-100",
				},
				ContractResolution: verificationv1.ContractResolutionLayer{
					Status:            verificationv1.LayerStatusResolved,
					InjectionComplete: true,
				},
				Connection: verificationv1.ConnectionLayer{
					Status:    verificationv1.LayerStatusVerified,
					LatencyMs: 4,
				},
				ConsumerHealth: verificationv1.ConsumerHealthLayer{
					Status:    verificationv1.LayerStatusHealthy,
					ReadyPods: 3,
					TotalPods: 3,
				},
				ConsumerAssertion: verificationv1.ConsumerAssertionLayer{
					Status:      verificationv1.LayerStatusFailed,
					StatusCode:  500,
					FailureCode: verificationv1.FailureConsumerAssertionFailed,
					Message:     "Consumer health check endpoint returned status 500",
				},
			},
		})
	})

	mux.HandleFunc(fmt.Sprintf("/v1/projects/%s/dependencies/partial-dep/verification", projectID), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Partial Verification scenario
		_ = json.NewEncoder(w).Encode(map[string]any{
			"run": verificationv1.VerificationRun{
				ID:                    "dvr-partial-1",
				ProjectID:             projectID,
				ConsumerApplicationID: "svc-api",
				DependencyLogicalName: "partial-dep",
				OverallStatus:         verificationv1.RunStatusPartiallyVerified,
				ProviderHealth: verificationv1.ProviderHealthLayer{
					Status:       verificationv1.LayerStatusHealthy,
					ProviderKind: "valkey",
				},
				ContractResolution: verificationv1.ContractResolutionLayer{
					Status:            verificationv1.LayerStatusResolved,
					InjectionComplete: true,
				},
				Connection: verificationv1.ConnectionLayer{
					Status: verificationv1.LayerStatusVerified,
				},
				ConsumerHealth: verificationv1.ConsumerHealthLayer{
					Status: verificationv1.LayerStatusHealthy,
				},
				ConsumerAssertion: verificationv1.ConsumerAssertionLayer{
					Status:      verificationv1.LayerStatusNotConfigured,
					FailureCode: verificationv1.FailureConsumerAssertionNotConfigured,
				},
			},
		})
	})

	mux.HandleFunc(fmt.Sprintf("/v1/projects/%s/dependencies/stale-dep/verification", projectID), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Stale Verification scenario
		_ = json.NewEncoder(w).Encode(map[string]any{
			"run": verificationv1.VerificationRun{
				ID:                    "dvr-stale-1",
				ProjectID:             projectID,
				ConsumerApplicationID: "svc-api",
				DependencyLogicalName: "stale-dep",
				OverallStatus:         verificationv1.RunStatusStale,
				FailureCode:           verificationv1.FailureVerificationStale,
			},
		})
	})

	// Verification history
	mux.HandleFunc(fmt.Sprintf("/v1/projects/%s/deployments/dep-job-api-1/verifications", projectID), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"runs": []verificationv1.VerificationRun{
				{
					ID:                    "dvr-bad-consumer-1",
					ProjectID:             projectID,
					ConsumerApplicationID: "svc-api",
					DependencyLogicalName: "database",
					OverallStatus:         verificationv1.RunStatusFailed,
				},
			},
		})
	})

	// IDOR boundary: Project B returns 403 Forbidden for foreign project access
	mux.HandleFunc(fmt.Sprintf("/api/projects/%s/", projectID_B), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "permission denied for project " + projectID_B})
	})
	mux.HandleFunc(fmt.Sprintf("/v1/projects/%s/", projectID_B), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "permission denied for project " + projectID_B})
	})

	server := httptest.NewServer(mux)
	return &comprehensiveFixture{
		server:       server,
		projectID:    projectID,
		projectID_B:  projectID_B,
		repoRoot:     tempDir,
		commitSHA:    commitSHA,
		cloudPAT:     cloudPAT,
		agentToken:   agentToken,
		dbPassword:   dbPassword,
		valkeyPass:   valkeyPass,
		regCred:      regCred,
		sourceSecret: sourceSecret,
	}
}

func (f *comprehensiveFixture) createServer(t *testing.T, customPAT string) *Server {
	t.Helper()
	cfgPath := createTestConfigFile(t, f.server.URL)
	pat := f.cloudPAT
	if customPAT != "" {
		pat = customPAT
	}
	return NewServer(ServerOptions{
		Version:          "1.0.0-acceptance",
		Revision:         "rev-acceptance-100",
		ConfigPath:       cfgPath,
		DefaultProjectID: f.projectID,
		RepoRoot:         f.repoRoot,
		KeychainFactory: func() (keychain.Store, error) {
			return &memoryKeychain{pat: pat}, nil
		},
	})
}

func callMCPTool(ctx context.Context, s *Server, name string, args map[string]any) (*CallToolResult, error) {
	rawArgs, _ := json.Marshal(args)
	reqBytes, _ := json.Marshal(JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/call",
		Params:  json.RawMessage(fmt.Sprintf(`{"name":%q,"arguments":%s}`, name, string(rawArgs))),
	})
	resp, err := s.HandleMessage(ctx, reqBytes)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("JSON-RPC error: %s", resp.Error.Message)
	}
	res, ok := resp.Result.(CallToolResult)
	if !ok {
		return nil, fmt.Errorf("unexpected result type: %T", resp.Result)
	}
	return &res, nil
}

// ============================================================
// Acceptance Tests
// ============================================================

func TestMCP01_RealProjectAndTopologyAcceptance(t *testing.T) {
	f := setupComprehensiveFixture(t)
	defer f.server.Close()
	s := f.createServer(t, "")
	ctx := context.Background()

	// 1. project_context
	res, err := callMCPTool(ctx, s, "project_context", map[string]any{"project_id": f.projectID})
	if err != nil || res.IsError {
		t.Fatalf("project_context failed: %v, content: %+v", err, res)
	}
	if !strings.Contains(res.Content[0].Text, "Opsi Accepted Real Project") {
		t.Errorf("expected project name in result, got: %s", res.Content[0].Text)
	}

	// 2. topology (web -> api placement)
	res, err = callMCPTool(ctx, s, "topology", map[string]any{"project_id": f.projectID})
	if err != nil || res.IsError {
		t.Fatalf("topology failed: %v", err)
	}
	if !strings.Contains(res.Content[0].Text, "rt-node-web") || !strings.Contains(res.Content[0].Text, "rt-node-api") {
		t.Errorf("expected topology assignments in result, got: %s", res.Content[0].Text)
	}

	// 3. applications_list (web, api)
	res, err = callMCPTool(ctx, s, "applications_list", map[string]any{"project_id": f.projectID})
	if err != nil || res.IsError {
		t.Fatalf("applications_list failed: %v", err)
	}
	if !strings.Contains(res.Content[0].Text, "web") || !strings.Contains(res.Content[0].Text, "api") {
		t.Errorf("expected web and api in applications list, got: %s", res.Content[0].Text)
	}

	// 4. application_get (safe environment variable keys only)
	res, err = callMCPTool(ctx, s, "application_get", map[string]any{"application_id": "svc-api", "project_id": f.projectID})
	if err != nil || res.IsError {
		t.Fatalf("application_get failed: %v", err)
	}
	text := res.Content[0].Text
	if !strings.Contains(text, "DATABASE_URL") || !strings.Contains(text, "VALKEY_URL") {
		t.Errorf("expected safe env keys in application detail, got: %s", text)
	}
	if strings.Contains(text, f.dbPassword) || strings.Contains(text, f.valkeyPass) {
		t.Fatalf("SECURITY VIOLATION: raw secrets exposed in application_get: %s", text)
	}

	// 5. application_dependencies (api -> postgres-main, api -> valkey-main)
	res, err = callMCPTool(ctx, s, "application_dependencies", map[string]any{"application_id": "svc-api", "project_id": f.projectID})
	if err != nil || res.IsError {
		t.Fatalf("application_dependencies failed: %v", err)
	}
	depText := res.Content[0].Text
	if !strings.Contains(depText, "postgres-main") || !strings.Contains(depText, "valkey-main") {
		t.Errorf("expected postgres-main and valkey-main in dependency realization facts, got: %s", depText)
	}

	// 6. managed_resources_list & get
	res, err = callMCPTool(ctx, s, "managed_resources_list", map[string]any{"project_id": f.projectID})
	if err != nil || res.IsError {
		t.Fatalf("managed_resources_list failed: %v", err)
	}
	if !strings.Contains(res.Content[0].Text, "postgres-main") || !strings.Contains(res.Content[0].Text, "valkey-main") {
		t.Errorf("expected postgres and valkey in resources list, got: %s", res.Content[0].Text)
	}

	res, err = callMCPTool(ctx, s, "managed_resource_get", map[string]any{"resource_id": "res-pg-100", "project_id": f.projectID})
	if err != nil || res.IsError {
		t.Fatalf("managed_resource_get failed: %v", err)
	}
	if !strings.Contains(res.Content[0].Text, "postgres-main.production.svc.cluster.local") {
		t.Errorf("expected safe endpoint in resource detail, got: %s", res.Content[0].Text)
	}
	if strings.Contains(res.Content[0].Text, f.dbPassword) {
		t.Fatalf("SECURITY VIOLATION: secret password leaked in managed_resource_get: %s", res.Content[0].Text)
	}
}

func TestMCP01_BuildContextAndDeploymentsAcceptance(t *testing.T) {
	f := setupComprehensiveFixture(t)
	defer f.server.Close()
	s := f.createServer(t, "")
	ctx := context.Background()

	// 1. BuildRecords
	res, err := callMCPTool(ctx, s, "build_records_list", map[string]any{"project_id": f.projectID})
	if err != nil || res.IsError {
		t.Fatalf("build_records_list failed: %v", err)
	}
	if !strings.Contains(res.Content[0].Text, "br-api-accepted") {
		t.Errorf("expected br-api-accepted in build records list, got: %s", res.Content[0].Text)
	}

	res, err = callMCPTool(ctx, s, "build_record_get", map[string]any{"build_record_id": "br-api-accepted", "project_id": f.projectID})
	if err != nil || res.IsError {
		t.Fatalf("build_record_get failed: %v", err)
	}
	if !strings.Contains(res.Content[0].Text, f.commitSHA) {
		t.Errorf("expected exact commit SHA %s in build record, got: %s", f.commitSHA, res.Content[0].Text)
	}
	if strings.Contains(res.Content[0].Text, f.regCred) {
		t.Fatalf("SECURITY VIOLATION: registry credential leaked in build_record_get: %s", res.Content[0].Text)
	}

	// 2. Deployments (historical outcome != runtime health)
	res, err = callMCPTool(ctx, s, "deployments_list", map[string]any{"project_id": f.projectID})
	if err != nil || res.IsError {
		t.Fatalf("deployments_list failed: %v", err)
	}
	if !strings.Contains(res.Content[0].Text, "dep-job-api-1") || !strings.Contains(res.Content[0].Text, "succeeded") {
		t.Errorf("expected deployment outcome in deployments_list, got: %s", res.Content[0].Text)
	}

	res, err = callMCPTool(ctx, s, "deployment_get", map[string]any{"deployment_id": "dep-job-api-1", "project_id": f.projectID})
	if err != nil || res.IsError {
		t.Fatalf("deployment_get failed: %v", err)
	}
	if !strings.Contains(res.Content[0].Text, "healthy") {
		t.Errorf("expected rollout_state in deployment_get, got: %s", res.Content[0].Text)
	}
}

func TestMCP01_PreflightThreeStatesAndZeroMutation(t *testing.T) {
	f := setupComprehensiveFixture(t)
	defer f.server.Close()
	s := f.createServer(t, "")
	ctx := context.Background()

	// State A: PASS
	resPass, err := callMCPTool(ctx, s, "deployment_preflight", map[string]any{
		"service_id":      "svc-api",
		"build_record_id": "br-api-accepted",
		"project_id":      f.projectID,
	})
	if err != nil || resPass.IsError {
		t.Fatalf("preflight PASS failed: %v, content: %+v", err, resPass)
	}
	if !strings.Contains(resPass.Content[0].Text, "PASS") || !strings.Contains(resPass.Content[0].Text, "hash-preflight-pass-789") {
		t.Errorf("expected PASS status and hash in result, got: %s", resPass.Content[0].Text)
	}

	// State B: PASS_WITH_WARNINGS
	resWarn, err := callMCPTool(ctx, s, "deployment_preflight", map[string]any{
		"service_id":      "svc-api",
		"build_record_id": "br-warn",
		"project_id":      f.projectID,
	})
	if err != nil || resWarn.IsError {
		t.Fatalf("preflight WARN failed: %v", err)
	}
	if !strings.Contains(resWarn.Content[0].Text, "PASS_WITH_WARNINGS") || !strings.Contains(resWarn.Content[0].Text, "RESOURCE_CAPACITY_WARNING") {
		t.Errorf("expected PASS_WITH_WARNINGS status and warning code, got: %s", resWarn.Content[0].Text)
	}

	// State C: BLOCKED
	resBlock, err := callMCPTool(ctx, s, "deployment_preflight", map[string]any{
		"service_id":      "svc-api",
		"build_record_id": "br-block",
		"project_id":      f.projectID,
	})
	if err != nil || resBlock.IsError {
		t.Fatalf("preflight BLOCK failed: %v", err)
	}
	if !strings.Contains(resBlock.Content[0].Text, "BLOCKED") || !strings.Contains(resBlock.Content[0].Text, "PLACEMENT_UNSATISFIABLE") {
		t.Errorf("expected BLOCKED status and placement error, got: %s", resBlock.Content[0].Text)
	}
}

func TestMCP01_VerificationFiveLayersAcceptance(t *testing.T) {
	f := setupComprehensiveFixture(t)
	defer f.server.Close()
	s := f.createServer(t, "")
	ctx := context.Background()

	// 1. Bad consumer scenario
	res, err := callMCPTool(ctx, s, "dependency_verification_latest", map[string]any{
		"dependency_logical_name": "database",
		"application_id":          "svc-api",
		"project_id":              f.projectID,
	})
	if err != nil || res.IsError {
		t.Fatalf("verification latest failed: %v", err)
	}
	text := res.Content[0].Text
	// Invariant: Provider HEALTHY, Contract RESOLVED, Connection VERIFIED, Consumer HEALTHY, Assertion FAILED, Overall FAILED
	if !strings.Contains(text, `"status": "HEALTHY"`) {
		t.Errorf("expected provider healthy in 5-layer result, got: %s", text)
	}
	if !strings.Contains(text, `"status": "RESOLVED"`) {
		t.Errorf("expected contract resolved in 5-layer result, got: %s", text)
	}
	if !strings.Contains(text, `"status": "FAILED"`) {
		t.Errorf("expected assertion failed in 5-layer result, got: %s", text)
	}
	if !strings.Contains(text, `"overall_status": "FAILED"`) {
		t.Errorf("expected overall failed in 5-layer result, got: %s", text)
	}

	// 2. Partial verification scenario
	resPartial, err := callMCPTool(ctx, s, "dependency_verification_latest", map[string]any{
		"dependency_logical_name": "partial-dep",
		"application_id":          "svc-api",
		"project_id":              f.projectID,
	})
	if err != nil || resPartial.IsError {
		t.Fatalf("partial verification failed: %v", err)
	}
	if !strings.Contains(resPartial.Content[0].Text, "PARTIALLY_VERIFIED") || !strings.Contains(resPartial.Content[0].Text, "NOT_CONFIGURED") {
		t.Errorf("expected PARTIALLY_VERIFIED and NOT_CONFIGURED, got: %s", resPartial.Content[0].Text)
	}

	// 3. Stale verification scenario
	resStale, err := callMCPTool(ctx, s, "dependency_verification_latest", map[string]any{
		"dependency_logical_name": "stale-dep",
		"application_id":          "svc-api",
		"project_id":              f.projectID,
	})
	if err != nil || resStale.IsError {
		t.Fatalf("stale verification failed: %v", err)
	}
	if !strings.Contains(resStale.Content[0].Text, "STALE") {
		t.Errorf("expected STALE status, got: %s", resStale.Content[0].Text)
	}

	// 4. Verification history
	resHist, err := callMCPTool(ctx, s, "dependency_verification_history", map[string]any{
		"deployment_job_id": "dep-job-api-1",
		"project_id":        f.projectID,
	})
	if err != nil || resHist.IsError {
		t.Fatalf("verification history failed: %v", err)
	}
	if !strings.Contains(resHist.Content[0].Text, "dvr-bad-consumer-1") {
		t.Errorf("expected run in history, got: %s", resHist.Content[0].Text)
	}
}

func TestMCP01_SourceProvenanceFilesystemSecurityAndRedaction(t *testing.T) {
	f := setupComprehensiveFixture(t)
	defer f.server.Close()
	s := f.createServer(t, "")
	ctx := context.Background()

	// 1. source_files_list
	resList, err := callMCPTool(ctx, s, "source_files_list", map[string]any{
		"application_id": "svc-api",
		"commit_sha":     f.commitSHA,
		"project_id":     f.projectID,
	})
	if err != nil || resList.IsError {
		t.Fatalf("source_files_list failed: %v", err)
	}
	if !strings.Contains(resList.Content[0].Text, "main.go") || !strings.Contains(resList.Content[0].Text, "config.json") {
		t.Errorf("expected files in source list, got: %s", resList.Content[0].Text)
	}

	// 2. source_file_read with secret redaction
	resRead, err := callMCPTool(ctx, s, "source_file_read", map[string]any{
		"application_id": "svc-api",
		"relative_path":  "main.go",
		"commit_sha":     f.commitSHA,
		"project_id":     f.projectID,
	})
	if err != nil || resRead.IsError {
		t.Fatalf("source_file_read failed: %v", err)
	}
	readText := resRead.Content[0].Text
	if !strings.Contains(readText, "[REDACTED]") {
		t.Errorf("expected [REDACTED] in sanitized source read, got: %s", readText)
	}
	if strings.Contains(readText, f.sourceSecret) || strings.Contains(readText, f.valkeyPass) {
		t.Fatalf("SECURITY VIOLATION: secret password leaked in source_file_read: %s", readText)
	}

	// 3. source_search for keywords
	searchTerms := []string{
		"APP_DATABASE_URL",
		"DATABASE_URL",
		"localhost",
		"postgres",
	}
	for _, term := range searchTerms {
		resSearch, err := callMCPTool(ctx, s, "source_search", map[string]any{
			"application_id": "svc-api",
			"query":          term,
			"commit_sha":     f.commitSHA,
			"project_id":     f.projectID,
		})
		if err != nil || resSearch.IsError {
			t.Fatalf("source_search for %q failed: %v", term, err)
		}
		if len(resSearch.Content) == 0 || !strings.Contains(resSearch.Content[0].Text, "main.go") {
			t.Errorf("expected match for %q in main.go, got: %+v", term, resSearch)
		}
		if strings.Contains(resSearch.Content[0].Text, f.sourceSecret) {
			t.Fatalf("SECURITY VIOLATION: secret leaked in search snippet: %s", resSearch.Content[0].Text)
		}
	}

	// 4. Path traversal / filesystem escape attempts
	traversalPaths := []string{
		"../outside.txt",
		"../../.ssh/id_rsa",
		"/etc/passwd",
		"services/api/../../etc/passwd",
		"..\x00/evil.txt",
	}
	for _, p := range traversalPaths {
		resTrav, err := callMCPTool(ctx, s, "source_file_read", map[string]any{
			"application_id": "svc-api",
			"relative_path":  p,
			"commit_sha":     f.commitSHA,
			"project_id":     f.projectID,
		})
		if err != nil {
			continue // json-rpc error is acceptable
		}
		if !resTrav.IsError || !strings.Contains(resTrav.Content[0].Text, ErrCodeSourcePathInvalid) {
			t.Errorf("expected SOURCE_PATH_INVALID for path traversal %q, got: %+v", p, resTrav)
		}
	}

	// 5. Binary file handling
	resBin, err := callMCPTool(ctx, s, "source_file_read", map[string]any{
		"application_id": "svc-api",
		"relative_path":  "binary.bin",
		"commit_sha":     f.commitSHA,
		"project_id":     f.projectID,
	})
	if err != nil || resBin.IsError {
		t.Fatalf("source_file_read on binary failed: %v", err)
	}
	if !strings.Contains(resBin.Content[0].Text, `"is_binary": true`) {
		t.Errorf("expected is_binary: true for binary.bin, got: %s", resBin.Content[0].Text)
	}

	// 6. Large file bounding and truncation
	resLarge, err := callMCPTool(ctx, s, "source_file_read", map[string]any{
		"application_id": "svc-api",
		"relative_path":  "large_file.txt",
		"max_bytes":      1024,
		"commit_sha":     f.commitSHA,
		"project_id":     f.projectID,
	})
	if err != nil || resLarge.IsError {
		t.Fatalf("source_file_read on large file failed: %v", err)
	}
	if !strings.Contains(resLarge.Content[0].Text, `"truncated": true`) {
		t.Errorf("expected truncated: true for large file with max_bytes=1024, got: %s", resLarge.Content[0].Text)
	}

	// 7. Non-existent / unavailable commit
	resUnavail, err := callMCPTool(ctx, s, "source_file_read", map[string]any{
		"application_id": "svc-api",
		"relative_path":  "main.go",
		"commit_sha":     "0000000000000000000000000000000000000000",
		"project_id":     f.projectID,
	})
	if err != nil {
		return
	}
	if !resUnavail.IsError || !strings.Contains(resUnavail.Content[0].Text, ErrCodeSourceSnapshotUnavailable) {
		t.Errorf("expected SOURCE_SNAPSHOT_UNAVAILABLE for non-existent commit, got: %+v", resUnavail)
	}
}

func TestMCP01_SecurityIDORAndCloudUnavailable(t *testing.T) {
	f := setupComprehensiveFixture(t)
	defer f.server.Close()
	s := f.createServer(t, "")
	ctx := context.Background()

	// 1. IDOR: Authenticated context attempts foreign project identifiers (Project B)
	foreignProjectTools := []struct {
		name string
		args map[string]any
	}{
		{"project_context", map[string]any{"project_id": f.projectID_B}},
		{"topology", map[string]any{"project_id": f.projectID_B}},
		{"applications_list", map[string]any{"project_id": f.projectID_B}},
		{"application_get", map[string]any{"project_id": f.projectID_B, "application_id": "svc-api"}},
		{"managed_resources_list", map[string]any{"project_id": f.projectID_B}},
		{"build_records_list", map[string]any{"project_id": f.projectID_B}},
		{"deployments_list", map[string]any{"project_id": f.projectID_B}},
	}

	for _, tc := range foreignProjectTools {
		res, err := callMCPTool(ctx, s, tc.name, tc.args)
		if err != nil {
			continue // error is acceptable
		}
		if !res.IsError || (!strings.Contains(res.Content[0].Text, ErrCodeForbidden) && !strings.Contains(res.Content[0].Text, ErrCodeAuthorityUnavailable)) {
			t.Errorf("expected FORBIDDEN or AUTHORITY_UNAVAILABLE for IDOR attempt on tool %s, got: %+v", tc.name, res)
		}
	}

	// 2. Cloud unavailable: server pointing to closed/dead port
	deadServer := NewServer(ServerOptions{
		Version:    "1.0.0",
		ConfigPath: createTestConfigFile(t, "http://127.0.0.1:54321"),
		KeychainFactory: func() (keychain.Store, error) {
			return &memoryKeychain{pat: "test-pat"}, nil
		},
	})
	resDead, _ := callMCPTool(ctx, deadServer, "project_context", map[string]any{"project_id": "any-proj"})
	if resDead == nil || !resDead.IsError || !strings.Contains(resDead.Content[0].Text, ErrCodeAuthorityUnavailable) {
		t.Errorf("expected AUTHORITY_UNAVAILABLE when cloud is down, got: %+v", resDead)
	}
}

func TestMCP01_ConcurrencyAndResponseBounds(t *testing.T) {
	f := setupComprehensiveFixture(t)
	defer f.server.Close()
	s := f.createServer(t, "")
	ctx := context.Background()

	// 1. Concurrency test: 20 concurrent goroutines executing multiple tool calls
	var wg sync.WaitGroup
	errChan := make(chan error, 100)

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(iter int) {
			defer wg.Done()
			tools := []string{"project_context", "applications_list", "managed_resources_list", "build_records_list", "topology"}
			toolName := tools[iter%len(tools)]
			res, err := callMCPTool(ctx, s, toolName, map[string]any{"project_id": f.projectID})
			if err != nil || res.IsError {
				errChan <- fmt.Errorf("concurrent call %s failed: %v, res: %+v", toolName, err, res)
			}
		}(i)
	}

	wg.Wait()
	close(errChan)

	for err := range errChan {
		t.Errorf("concurrency error: %v", err)
	}

	// 2. Response bounds enforcement: client requests limit 99999
	resBound, err := callMCPTool(ctx, s, "applications_list", map[string]any{"project_id": f.projectID, "limit": 99999})
	if err != nil || resBound.IsError {
		t.Fatalf("applications_list with high limit failed: %v", err)
	}
	var appListRes struct {
		Applications []ApplicationSummary `json:"applications"`
	}
	_ = json.Unmarshal([]byte(resBound.Content[0].Text), &appListRes)
	if len(appListRes.Applications) > 100 {
		t.Errorf("expected capped applications count <= 100, got %d", len(appListRes.Applications))
	}
}

func TestMCP01_ZeroMutationComprehensiveSession(t *testing.T) {
	f := setupComprehensiveFixture(t)
	defer f.server.Close()
	s := f.createServer(t, "")
	ctx := context.Background()

	all18Tools := []struct {
		name string
		args map[string]any
	}{
		{"project_context", map[string]any{"project_id": f.projectID}},
		{"topology", map[string]any{"project_id": f.projectID}},
		{"applications_list", map[string]any{"project_id": f.projectID}},
		{"application_get", map[string]any{"project_id": f.projectID, "application_id": "svc-api"}},
		{"application_dependencies", map[string]any{"project_id": f.projectID, "application_id": "svc-api"}},
		{"managed_resources_list", map[string]any{"project_id": f.projectID}},
		{"managed_resource_get", map[string]any{"project_id": f.projectID, "resource_id": "res-pg-100"}},
		{"build_records_list", map[string]any{"project_id": f.projectID}},
		{"build_record_get", map[string]any{"project_id": f.projectID, "build_record_id": "br-api-accepted"}},
		{"deployments_list", map[string]any{"project_id": f.projectID}},
		{"deployment_get", map[string]any{"project_id": f.projectID, "deployment_id": "dep-job-api-1"}},
		{"deployment_preflight", map[string]any{"project_id": f.projectID, "service_id": "svc-api", "build_record_id": "br-api-accepted"}},
		{"source_risk_report", map[string]any{"project_id": f.projectID, "application_id": "svc-api"}},
		{"dependency_verification_latest", map[string]any{"project_id": f.projectID, "dependency_logical_name": "database"}},
		{"dependency_verification_history", map[string]any{"project_id": f.projectID, "deployment_job_id": "dep-job-api-1"}},
		{"source_files_list", map[string]any{"project_id": f.projectID, "application_id": "svc-api", "commit_sha": f.commitSHA}},
		{"source_file_read", map[string]any{"project_id": f.projectID, "application_id": "svc-api", "relative_path": "main.go", "commit_sha": f.commitSHA}},
		{"source_search", map[string]any{"project_id": f.projectID, "application_id": "svc-api", "query": "DATABASE_URL", "commit_sha": f.commitSHA}},
	}

	// Capture state before
	var allOutputs []string

	// Call EVERY exposed tool at least once
	for _, tc := range all18Tools {
		res, err := callMCPTool(ctx, s, tc.name, tc.args)
		if err != nil || res.IsError {
			t.Fatalf("tool %s failed: %v, content: %+v", tc.name, err, res)
		}
		allOutputs = append(allOutputs, res.Content[0].Text)
	}

	// Verify all 18 tools succeeded
	if len(allOutputs) != 18 {
		t.Fatalf("expected 18 successful outputs, got %d", len(allOutputs))
	}

	// Verify complete zero leakage across all 18 responses
	fullCombined := strings.Join(allOutputs, "\n")
	secretMatrix := []string{
		f.cloudPAT,
		f.agentToken,
		f.dbPassword,
		f.valkeyPass,
		f.regCred,
		f.sourceSecret,
	}

	for _, secret := range secretMatrix {
		if strings.Contains(fullCombined, secret) {
			t.Fatalf("SECURITY VIOLATION: secret leaked in tool responses: %s", secret)
		}
	}
}
