package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opsi-dev/opsi/cli/internal/keychain"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
	serviceconfigurationv1 "github.com/opsi-dev/opsi/contracts/go/serviceconfigurationv1"
	topologyv1 "github.com/opsi-dev/opsi/contracts/go/topologyv1"
	verificationv1 "github.com/opsi-dev/opsi/contracts/go/verificationv1"
)

func setupMockCloudServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()

	projectID := "proj-test-123"
	mux := http.NewServeMux()

	// 1. Auth verify
	mux.HandleFunc("/v1/auth/pat/verify", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"valid":      true,
			"org_id":     "org-test-999",
			"project_id": projectID,
			"role":       "developer",
		})
	})

	// 2. Org projects
	mux.HandleFunc("/api/orgs/org-test-999/projects", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"projects": []map[string]any{
				{
					"id":     projectID,
					"org_id": "org-test-999",
					"name":   "Test Project",
					"slug":   "test-project",
					"status": "active",
				},
			},
		})
	})

	// 3. Topology
	mux.HandleFunc(fmt.Sprintf("/api/projects/%s/topology", projectID), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(topologyv1.Plan{
			ID:        "top-plan-1",
			ProjectID: projectID,
			Revision:  42,
			StateHash: "abc123statehash",
			Assignments: []topologyv1.Assignment{
				{
					ServiceKey: "web-api",
					RuntimeID:  "rt-node-1",
					Replicas:   2,
				},
			},
		})
	})

	// 4. Services
	mux.HandleFunc(fmt.Sprintf("/api/projects/%s/services", projectID), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"services": []map[string]any{
				{
					"id":         "svc-web-1",
					"project_id": projectID,
					"name":       "web-api",
					"status":     "active",
				},
			},
		})
	})

	// 5. Service configuration
	mux.HandleFunc(fmt.Sprintf("/api/projects/%s/services/svc-web-1/configuration", projectID), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(serviceconfigurationv1.Configuration{
			ServiceConfigurationDraft: serviceconfigurationv1.ServiceConfigurationDraft{
				SchemaVersion: serviceconfigurationv1.SchemaVersion,
				Environment: []deploymentv1.EnvironmentVariable{
					{Name: "PORT", Value: "8080"},
					{Name: "DATABASE_URL", Value: "secret-reference"},
				},
				PublicRoute: &serviceconfigurationv1.PublicRouteIntent{
					Hostname: "api.example.com",
					Path:     "/",
				},
				ResourceBindings: []serviceconfigurationv1.ResourceBinding{
					{LogicalName: "database", BindingID: "rb-pg-1"},
				},
				Dependencies: []serviceconfigurationv1.ApplicationDependency{
					{
						LogicalName:    "database",
						TargetKind:     "managed_resource",
						TargetIdentity: "postgres-main",
						Protocol:       "postgres",
						Required:       true,
						InjectionPhase: "runtime",
						VerificationContract: &serviceconfigurationv1.DependencyVerificationContract{
							Type:           "consumer_http",
							Path:           "/healthz",
							ExpectedStatus: 200,
						},
					},
				},
			},
			Revision:  5,
			StateHash: "hash-cfg-5",
		})
	})

	// 6. GitHub bindings
	mux.HandleFunc(fmt.Sprintf("/v1/projects/%s/github/bindings", projectID), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"bindings": []map[string]any{
				{
					"id":               "ghb-1",
					"project_id":       projectID,
					"service_id":       "svc-web-1",
					"service_key":      "web-api",
					"repository_id":    12345,
					"selected_ref":     "main",
					"application_root": "src/backend",
					"build_context":    ".",
					"build_strategy":   "buildpacks",
					"status":           "active",
				},
			},
		})
	})

	// 7. Managed resources
	mux.HandleFunc(fmt.Sprintf("/api/projects/%s/resources", projectID), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"resources": []resourcev1.Resource{
				{
					ID:            "res-pg-1",
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
			},
		})
	})

	// 8. Resource get
	mux.HandleFunc(fmt.Sprintf("/api/projects/%s/resources/res-pg-1", projectID), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resourcev1.Resource{
			ID:            "res-pg-1",
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

	// 9. Resource bindings
	mux.HandleFunc(fmt.Sprintf("/api/projects/%s/resource-bindings", projectID), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"bindings": []resourcev1.Binding{
				{
					ID:            "rb-pg-1",
					ProjectID:     projectID,
					EnvironmentID: "production",
					LogicalName:   "database",
					Lifecycle:     resourcev1.LifecycleReady,
					Target: resourcev1.EndpointReference{
						Kind: resourcev1.KindManagedService,
						ID:   "res-pg-1",
					},
				},
			},
		})
	})

	// 10. Nodes
	mux.HandleFunc(fmt.Sprintf("/api/projects/%s/nodes", projectID), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"nodes": []map[string]any{
				{
					"id":         "node-1",
					"project_id": projectID,
					"name":       "node-alpha",
					"role":       "worker",
					"status":     "ready",
				},
			},
		})
	})

	// 11. BuildRecords
	mux.HandleFunc(fmt.Sprintf("/api/projects/%s/build-records", projectID), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"records": []map[string]any{
				{
					"id":          "br-101",
					"project_id":  projectID,
					"service_id":  "svc-web-1",
					"service_key": "web-api",
					"workload": map[string]any{
						"sha": "1111222233334444555566667777888899990000",
					},
					"build": map[string]any{
						"build_strategy": "buildpacks",
						"oci_digest":     "sha256:1111222233334444555566667777888899990000111122223333444455556666",
						"status":         "accepted",
					},
				},
			},
		})
	})

	mux.HandleFunc(fmt.Sprintf("/api/projects/%s/build-records/br-101", projectID), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":          "br-101",
			"project_id":  projectID,
			"service_id":  "svc-web-1",
			"service_key": "web-api",
			"workload": map[string]any{
				"sha": "1111222233334444555566667777888899990000",
			},
			"build": map[string]any{
				"build_strategy": "buildpacks",
				"oci_digest":     "sha256:1111222233334444555566667777888899990000111122223333444455556666",
				"status":         "accepted",
			},
		})
	})

	// 12. Deployments
	mux.HandleFunc(fmt.Sprintf("/api/projects/%s/deployments", projectID), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"deployments": []map[string]any{
				{
					"id":              "dep-job-500",
					"project_id":      projectID,
					"service_id":      "svc-web-1",
					"runtime_id":      "rt-node-1",
					"status":          "succeeded",
					"rollout_state":   "healthy",
					"build_record_id": "br-101",
				},
			},
		})
	})

	mux.HandleFunc(fmt.Sprintf("/api/projects/%s/deployments/dep-job-500", projectID), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":              "dep-job-500",
			"project_id":      projectID,
			"service_id":      "svc-web-1",
			"runtime_id":      "rt-node-1",
			"status":          "succeeded",
			"rollout_state":   "healthy",
			"build_record_id": "br-101",
		})
	})

	// 13. Preflight
	mux.HandleFunc(fmt.Sprintf("/api/projects/%s/deployments/preflight", projectID), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(deploymentv1.PreflightResult{
			Status:        deploymentv1.PreflightStatusPass,
			PreflightHash: "hash-preflight-pass",
			Checks: []deploymentv1.PreflightCheck{
				{
					ID:       "chk-1",
					Code:     "BUILD_RECORD_VALID",
					Severity: "PASS",
					Message:  "Build record is valid",
				},
			},
		})
	})

	// 14. Source risk report
	mux.HandleFunc(fmt.Sprintf("/v1/projects/%s/applications/svc-web-1/source-risk-report", projectID), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"report": map[string]any{
				"id":               "srr-1",
				"project_id":       projectID,
				"application_id":   "svc-web-1",
				"commit_sha":       "1111222233334444555566667777888899990000",
				"application_root": "src/backend",
				"scanner_version":  "opsi.source-scanner/v1",
				"analysis_status":  "complete",
				"findings": []map[string]any{
					{
						"finding_id":    "SOURCE_LOOPBACK_ENDPOINT:main.go:10",
						"rule_id":       "SOURCE_LOOPBACK_ENDPOINT",
						"severity":      "WARN",
						"confidence":    "HIGH",
						"safe_evidence": "localhost endpoint observed",
					},
				},
			},
		})
	})

	// 15. Dependency verification
	mux.HandleFunc(fmt.Sprintf("/v1/projects/%s/dependencies/database/verification", projectID), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"run": verificationv1.VerificationRun{
				ID:                    "dvr-900",
				ProjectID:             projectID,
				ConsumerApplicationID: "svc-web-1",
				DependencyLogicalName: "database",
				OverallStatus:         verificationv1.RunStatusVerified,
				ProviderHealth: verificationv1.ProviderHealthLayer{
					Status:       verificationv1.LayerStatusHealthy,
					ProviderKind: "postgres",
				},
				ContractResolution: verificationv1.ContractResolutionLayer{
					Status:            verificationv1.LayerStatusResolved,
					InjectionComplete: true,
				},
				Connection: verificationv1.ConnectionLayer{
					Status:    verificationv1.LayerStatusVerified,
					LatencyMs: 5,
				},
				ConsumerHealth: verificationv1.ConsumerHealthLayer{
					Status:    verificationv1.LayerStatusHealthy,
					ReadyPods: 2,
					TotalPods: 2,
				},
				ConsumerAssertion: verificationv1.ConsumerAssertionLayer{
					Status:     verificationv1.LayerStatusVerified,
					StatusCode: 200,
				},
			},
		})
	})

	// 16. Deployment verifications history
	mux.HandleFunc(fmt.Sprintf("/v1/projects/%s/deployments/dep-job-500/verifications", projectID), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"runs": []verificationv1.VerificationRun{
				{
					ID:                    "dvr-900",
					ProjectID:             projectID,
					ConsumerApplicationID: "svc-web-1",
					DependencyLogicalName: "database",
					OverallStatus:         verificationv1.RunStatusVerified,
				},
			},
		})
	})

	server := httptest.NewServer(mux)
	return server, projectID
}

func createTestConfigFile(t *testing.T, cloudURL string) string {
	t.Helper()
	tempDir := t.TempDir()
	cfgPath := filepath.Join(tempDir, "config.yaml")
	content := fmt.Sprintf("cloud_url: %s\n", cloudURL)
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return cfgPath
}

func TestMCPTools_AllReadToolsAcceptance(t *testing.T) {
	cloudServer, projectID := setupMockCloudServer(t)
	defer cloudServer.Close()

	cfgPath := createTestConfigFile(t, cloudServer.URL)
	repoRoot, commitSHA := setupTestGitRepo(t)

	s := NewServer(ServerOptions{
		Version:          "1.0.0-test",
		ConfigPath:       cfgPath,
		DefaultProjectID: projectID,
		RepoRoot:         repoRoot,
		KeychainFactory: func() (keychain.Store, error) {
			return &memoryKeychain{pat: "valid-test-pat"}, nil
		},
	})
	ctx := context.Background()

	callTool := func(name string, args map[string]any) *CallToolResult {
		rawArgs, _ := json.Marshal(args)
		reqBytes, _ := json.Marshal(JSONRPCRequest{
			JSONRPC: "2.0",
			ID:      100,
			Method:  "tools/call",
			Params:  json.RawMessage(fmt.Sprintf(`{"name":%q,"arguments":%s}`, name, string(rawArgs))),
		})
		resp, err := s.HandleMessage(ctx, reqBytes)
		if err != nil {
			t.Fatalf("HandleMessage for tool %s failed: %v", name, err)
		}
		if resp.Error != nil {
			t.Fatalf("unexpected JSON-RPC error for tool %s: %v", name, resp.Error)
		}
		res, ok := resp.Result.(CallToolResult)
		if !ok {
			t.Fatalf("expected CallToolResult for tool %s, got %T", name, resp.Result)
		}
		return &res
	}

	// 1. project_context
	res := callTool("project_context", map[string]any{"project_id": projectID})
	if res.IsError {
		t.Fatalf("project_context failed: %s", res.Content[0].Text)
	}
	if !strings.Contains(res.Content[0].Text, `"project_id": "proj-test-123"`) {
		t.Errorf("expected project_id in result, got: %s", res.Content[0].Text)
	}

	// 2. topology
	res = callTool("topology", map[string]any{"project_id": projectID})
	if res.IsError || !strings.Contains(res.Content[0].Text, "web-api") {
		t.Fatalf("topology failed: %s", res.Content[0].Text)
	}

	// 3. applications_list
	res = callTool("applications_list", map[string]any{"project_id": projectID})
	if res.IsError || !strings.Contains(res.Content[0].Text, "web-api") {
		t.Fatalf("applications_list failed: %s", res.Content[0].Text)
	}

	// 4. application_get
	res = callTool("application_get", map[string]any{"application_id": "svc-web-1", "project_id": projectID})
	if res.IsError || !strings.Contains(res.Content[0].Text, "api.example.com") {
		t.Fatalf("application_get failed: %s", res.Content[0].Text)
	}

	// 5. application_dependencies
	res = callTool("application_dependencies", map[string]any{"application_id": "svc-web-1", "project_id": projectID})
	if res.IsError || !strings.Contains(res.Content[0].Text, "postgres") {
		t.Fatalf("application_dependencies failed: %s", res.Content[0].Text)
	}

	// 6. managed_resources_list
	res = callTool("managed_resources_list", map[string]any{"project_id": projectID})
	if res.IsError || !strings.Contains(res.Content[0].Text, "postgres-main") {
		t.Fatalf("managed_resources_list failed: %s", res.Content[0].Text)
	}

	// 7. managed_resource_get
	res = callTool("managed_resource_get", map[string]any{"resource_id": "res-pg-1", "project_id": projectID})
	if res.IsError || !strings.Contains(res.Content[0].Text, "postgres-main") {
		t.Fatalf("managed_resource_get failed: %s", res.Content[0].Text)
	}

	// 8. build_records_list
	res = callTool("build_records_list", map[string]any{"project_id": projectID})
	if res.IsError || !strings.Contains(res.Content[0].Text, "br-101") {
		t.Fatalf("build_records_list failed: %s", res.Content[0].Text)
	}

	// 9. build_record_get
	res = callTool("build_record_get", map[string]any{"build_record_id": "br-101", "project_id": projectID})
	if res.IsError || !strings.Contains(res.Content[0].Text, "buildpacks") {
		t.Fatalf("build_record_get failed: %s", res.Content[0].Text)
	}

	// 10. deployments_list
	res = callTool("deployments_list", map[string]any{"project_id": projectID})
	if res.IsError || !strings.Contains(res.Content[0].Text, "dep-job-500") {
		t.Fatalf("deployments_list failed: %s", res.Content[0].Text)
	}

	// 11. deployment_get
	res = callTool("deployment_get", map[string]any{"deployment_id": "dep-job-500", "project_id": projectID})
	if res.IsError || !strings.Contains(res.Content[0].Text, "succeeded") {
		t.Fatalf("deployment_get failed: %s", res.Content[0].Text)
	}

	// 12. deployment_preflight
	res = callTool("deployment_preflight", map[string]any{"service_id": "svc-web-1", "build_record_id": "br-101", "project_id": projectID})
	if res.IsError || !strings.Contains(res.Content[0].Text, "PASS") {
		t.Fatalf("deployment_preflight failed: %s", res.Content[0].Text)
	}

	// 13. source_risk_report
	res = callTool("source_risk_report", map[string]any{"application_id": "svc-web-1", "project_id": projectID})
	if res.IsError || !strings.Contains(res.Content[0].Text, "SOURCE_LOOPBACK_ENDPOINT") {
		t.Fatalf("source_risk_report failed: %s", res.Content[0].Text)
	}

	// 14. dependency_verification_latest
	res = callTool("dependency_verification_latest", map[string]any{"dependency_logical_name": "database", "project_id": projectID})
	if res.IsError || !strings.Contains(res.Content[0].Text, "VERIFIED") {
		t.Fatalf("dependency_verification_latest failed: %s", res.Content[0].Text)
	}

	// 15. dependency_verification_history
	res = callTool("dependency_verification_history", map[string]any{"deployment_job_id": "dep-job-500", "project_id": projectID})
	if res.IsError || !strings.Contains(res.Content[0].Text, "dvr-900") {
		t.Fatalf("dependency_verification_history failed: %s", res.Content[0].Text)
	}

	// 16. source_files_list
	res = callTool("source_files_list", map[string]any{"application_id": "svc-web-1", "commit_sha": commitSHA, "project_id": projectID})
	if res.IsError || !strings.Contains(res.Content[0].Text, "main.go") {
		t.Fatalf("source_files_list failed: %s", res.Content[0].Text)
	}

	// 17. source_file_read
	res = callTool("source_file_read", map[string]any{"application_id": "svc-web-1", "relative_path": "main.go", "commit_sha": commitSHA, "project_id": projectID})
	if res.IsError || !strings.Contains(res.Content[0].Text, "[REDACTED]") {
		t.Fatalf("source_file_read failed: %s", res.Content[0].Text)
	}

	// 18. source_search
	res = callTool("source_search", map[string]any{"application_id": "svc-web-1", "query": "Hello world", "commit_sha": commitSHA, "project_id": projectID})
	if res.IsError || !strings.Contains(res.Content[0].Text, "main.go") {
		t.Fatalf("source_search failed: %s", res.Content[0].Text)
	}
}

func TestMCPTools_UnauthenticatedAndAmbiguousErrors(t *testing.T) {
	cloudServer, projectID := setupMockCloudServer(t)
	defer cloudServer.Close()
	cfgPath := createTestConfigFile(t, cloudServer.URL)

	// 1. Test unauthenticated (missing PAT)
	unauthServer := NewServer(ServerOptions{
		Version:          "1.0.0-test",
		ConfigPath:       cfgPath,
		DefaultProjectID: projectID,
		KeychainFactory: func() (keychain.Store, error) {
			return &memoryKeychain{pat: ""}, nil
		},
	})

	reqBytes, _ := json.Marshal(JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"project_context","arguments":{}}`),
	})

	resp, _ := unauthServer.HandleMessage(context.Background(), reqBytes)
	res, ok := resp.Result.(CallToolResult)
	if !ok || !res.IsError || !strings.Contains(res.Content[0].Text, ErrCodeAuthRequired) {
		t.Fatalf("expected AUTH_REQUIRED error when PAT is missing, got: %+v", resp)
	}
}
