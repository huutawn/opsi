package registry

import (
	"context"
	"errors"
	"strings"
	"testing"

	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	serviceconfigurationv1 "github.com/opsi-dev/opsi/contracts/go/serviceconfigurationv1"
)

func TestServiceConfigurationDependenciesValidation(t *testing.T) {
	services := []ServiceRecord{
		{ID: "source-1", ProjectID: "proj-1"},
		{ID: "target-app", ProjectID: "proj-1", ContainerPort: 8080},
		{ID: "foreign-app", ProjectID: "proj-2", ContainerPort: 8080},
	}

	tests := []struct {
		name         string
		dependencies []serviceconfigurationv1.ApplicationDependency
		expectedErr  string
	}{
		{
			name:         "empty dependency set",
			dependencies: []serviceconfigurationv1.ApplicationDependency{},
		},
		{
			name: "one managed-resource dependency",
			dependencies: []serviceconfigurationv1.ApplicationDependency{
				{LogicalName: "db", TargetKind: "managed_resource", TargetIdentity: "res-1", Protocol: "postgres", InjectionPhase: "runtime"},
			},
		},
		{
			name: "one application dependency",
			dependencies: []serviceconfigurationv1.ApplicationDependency{
				{LogicalName: "api", TargetKind: "application", TargetIdentity: "target-app", Protocol: "http", InjectionPhase: "runtime"},
			},
		},
		{
			name: "logical-name duplicate",
			dependencies: []serviceconfigurationv1.ApplicationDependency{
				{LogicalName: "db", TargetKind: "managed_resource", TargetIdentity: "res-1", Protocol: "postgres", InjectionPhase: "runtime"},
				{LogicalName: "db", TargetKind: "application", TargetIdentity: "target-app", Protocol: "http", InjectionPhase: "runtime"},
			},
			expectedErr: "logical name must be unique",
		},
		{
			name: "env-name collision",
			dependencies: []serviceconfigurationv1.ApplicationDependency{
				{LogicalName: "db1", TargetKind: "managed_resource", TargetIdentity: "res-1", Protocol: "postgres", InjectionPhase: "runtime", InjectionMappings: []serviceconfigurationv1.DependencyInjectionMapping{{EnvName: "URL", SymbolicSource: "connection.url"}}},
				{LogicalName: "db2", TargetKind: "managed_resource", TargetIdentity: "res-2", Protocol: "postgres", InjectionPhase: "runtime", InjectionMappings: []serviceconfigurationv1.DependencyInjectionMapping{{EnvName: "URL", SymbolicSource: "connection.url"}}},
			},
			expectedErr: "env name URL conflicts",
		},
		{
			name: "invalid env name",
			dependencies: []serviceconfigurationv1.ApplicationDependency{
				{LogicalName: "db1", TargetKind: "managed_resource", TargetIdentity: "res-1", Protocol: "postgres", InjectionPhase: "runtime", InjectionMappings: []serviceconfigurationv1.DependencyInjectionMapping{{EnvName: "1INVALID", SymbolicSource: "connection.url"}}},
			},
			expectedErr: "invalid env name format",
		},
		{
			name: "invalid protocol",
			dependencies: []serviceconfigurationv1.ApplicationDependency{
				{LogicalName: "db1", TargetKind: "managed_resource", TargetIdentity: "res-1", Protocol: "", InjectionPhase: "runtime"},
			},
			expectedErr: "protocol is required",
		},
		{
			name: "self application dependency",
			dependencies: []serviceconfigurationv1.ApplicationDependency{
				{LogicalName: "self", TargetKind: "application", TargetIdentity: "source-1", Protocol: "http", InjectionPhase: "runtime"},
			},
			expectedErr: "a service cannot depend on itself",
		},
		{
			name: "missing target identity",
			dependencies: []serviceconfigurationv1.ApplicationDependency{
				{LogicalName: "db", TargetKind: "managed_resource", TargetIdentity: "", Protocol: "postgres", InjectionPhase: "runtime"},
			},
			expectedErr: "missing target identity",
		},
		{
			name: "foreign-project target",
			dependencies: []serviceconfigurationv1.ApplicationDependency{
				{LogicalName: "api", TargetKind: "application", TargetIdentity: "foreign-app", Protocol: "http", InjectionPhase: "runtime"},
			},
			expectedErr: "target application must belong to the project",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			draft := serviceconfigurationv1.ServiceConfigurationDraft{
				Dependencies: tt.dependencies,
			}
			_, _, err := validateServiceConfiguration(context.Background(), nil, services[0], draft, services)
			if tt.expectedErr != "" {
				if err == nil || !hasAPIErrorMessage(err, tt.expectedErr) {
					t.Fatalf("expected error containing %q, got %v", tt.expectedErr, err)
				}
			} else {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
			}
		})
	}
}

func hasAPIErrorMessage(err error, msg string) bool {
	if err == nil {
		return false
	}
	apiErr, ok := err.(APIError)
	if !ok {
		return false
	}
	return strings.Contains(apiErr.Message, msg)
}

func TestBuildDependencyCycleDetection(t *testing.T) {
	// Service A depends on Service B in build phase
	// Service B depends on Service A in build phase -> cycle!
	serviceB := ServiceRecord{
		ID:            "svc-b",
		ProjectID:     "proj-1",
		ContainerPort: 8080,
		Configuration: ServiceConfiguration{
			ServiceConfigurationDraft: ServiceConfigurationDraft{
				Dependencies: []serviceconfigurationv1.ApplicationDependency{
					{
						LogicalName:    "dep-a",
						TargetKind:     "application",
						TargetIdentity: "svc-a",
						Protocol:       "http",
						Strategy:       serviceconfigurationv1.StrategyInternalHTTP,
						AccessContext:  serviceconfigurationv1.AccessContextServer,
						InjectionPhase: serviceconfigurationv1.InjectionPhaseBuild,
					},
				},
			},
		},
	}
	serviceA := ServiceRecord{
		ID:            "svc-a",
		ProjectID:     "proj-1",
		ContainerPort: 8080,
	}

	services := []ServiceRecord{serviceA, serviceB}
	draftA := ServiceConfigurationDraft{
		Dependencies: []serviceconfigurationv1.ApplicationDependency{
			{
				LogicalName:    "dep-b",
				TargetKind:     "application",
				TargetIdentity: "svc-b",
				Protocol:       "http",
				Strategy:       serviceconfigurationv1.StrategyInternalHTTP,
				AccessContext:  serviceconfigurationv1.AccessContextServer,
				InjectionPhase: serviceconfigurationv1.InjectionPhaseBuild,
			},
		},
	}

	_, _, err := validateServiceConfiguration(context.Background(), nil, serviceA, draftA, services)
	if err == nil {
		t.Fatal("expected cycle detection error, got nil")
	}
	var apiErr APIError
	if !strings.Contains(err.Error(), "DEPENDENCY_CYCLE_DETECTED") && (!errors.As(err, &apiErr) || apiErr.Code != "DEPENDENCY_CYCLE_DETECTED") {
		t.Fatalf("expected DEPENDENCY_CYCLE_DETECTED, got %v", err)
	}
}

func TestRuntimeDependencyCyclesArePermitted(t *testing.T) {
	// Service A depends on Service B in runtime phase
	// Service B depends on Service A in runtime phase -> runtime cycles permitted!
	serviceB := ServiceRecord{
		ID:            "svc-b",
		ProjectID:     "proj-1",
		ContainerPort: 8080,
		Configuration: ServiceConfiguration{
			ServiceConfigurationDraft: ServiceConfigurationDraft{
				Dependencies: []serviceconfigurationv1.ApplicationDependency{
					{
						LogicalName:    "dep-a",
						TargetKind:     "application",
						TargetIdentity: "svc-a",
						Protocol:       "http",
						Strategy:       serviceconfigurationv1.StrategyInternalHTTP,
						AccessContext:  serviceconfigurationv1.AccessContextServer,
						InjectionPhase: serviceconfigurationv1.InjectionPhaseRuntime,
					},
				},
			},
		},
	}
	serviceA := ServiceRecord{
		ID:            "svc-a",
		ProjectID:     "proj-1",
		ContainerPort: 8080,
	}

	services := []ServiceRecord{serviceA, serviceB}
	draftA := ServiceConfigurationDraft{
		Dependencies: []serviceconfigurationv1.ApplicationDependency{
			{
				LogicalName:    "dep-b",
				TargetKind:     "application",
				TargetIdentity: "svc-b",
				Protocol:       "http",
				Strategy:       serviceconfigurationv1.StrategyInternalHTTP,
				AccessContext:  serviceconfigurationv1.AccessContextServer,
				InjectionPhase: serviceconfigurationv1.InjectionPhaseRuntime,
			},
		},
	}

	_, _, err := validateServiceConfiguration(context.Background(), nil, serviceA, draftA, services)
	if err != nil {
		t.Fatalf("runtime cycle should be permitted, got %v", err)
	}
}

func TestDependencyAccessContextMatrix(t *testing.T) {
	target := ServiceRecord{
		ID:            "target-app",
		ProjectID:     "proj-1",
		ContainerPort: 8080,
		Configuration: ServiceConfiguration{
			ServiceConfigurationDraft: ServiceConfigurationDraft{
				PublicRoute: &serviceconfigurationv1.PublicRouteIntent{Hostname: "app.example.com", Path: "/api"},
			},
		},
	}
	source := ServiceRecord{
		ID:            "source-app",
		ProjectID:     "proj-1",
		ContainerPort: 3000,
	}
	services := []ServiceRecord{source, target}

	tests := []struct {
		name           string
		accessContext  string
		injectionPhase string
		strategy       string
		consumerRoute  *serviceconfigurationv1.PublicRouteIntent
		path           string
		expectErrCode  string
	}{
		{
			name:           "browser + same_origin + build",
			accessContext:  serviceconfigurationv1.AccessContextBrowser,
			injectionPhase: serviceconfigurationv1.InjectionPhaseBuild,
			strategy:       serviceconfigurationv1.StrategySameOrigin,
			consumerRoute:  &serviceconfigurationv1.PublicRouteIntent{Hostname: "app.example.com", Path: "/"},
			path:           "/api",
		},
		{
			name:           "browser + same_origin + runtime",
			accessContext:  serviceconfigurationv1.AccessContextBrowser,
			injectionPhase: serviceconfigurationv1.InjectionPhaseRuntime,
			strategy:       serviceconfigurationv1.StrategySameOrigin,
			consumerRoute:  &serviceconfigurationv1.PublicRouteIntent{Hostname: "app.example.com", Path: "/"},
			path:           "/api",
		},
		{
			name:           "server + internal_http + runtime",
			accessContext:  serviceconfigurationv1.AccessContextServer,
			injectionPhase: serviceconfigurationv1.InjectionPhaseRuntime,
			strategy:       serviceconfigurationv1.StrategyInternalHTTP,
		},
		{
			name:           "server + internal_http + build",
			accessContext:  serviceconfigurationv1.AccessContextServer,
			injectionPhase: serviceconfigurationv1.InjectionPhaseBuild,
			strategy:       serviceconfigurationv1.StrategyInternalHTTP,
		},
		{
			name:           "server + public_http + runtime",
			accessContext:  serviceconfigurationv1.AccessContextServer,
			injectionPhase: serviceconfigurationv1.InjectionPhaseRuntime,
			strategy:       serviceconfigurationv1.StrategyPublicHTTP,
		},
		{
			name:           "server + public_http + build",
			accessContext:  serviceconfigurationv1.AccessContextServer,
			injectionPhase: serviceconfigurationv1.InjectionPhaseBuild,
			strategy:       serviceconfigurationv1.StrategyPublicHTTP,
		},
		{
			name:           "browser + internal_http -> rejected",
			accessContext:  serviceconfigurationv1.AccessContextBrowser,
			injectionPhase: serviceconfigurationv1.InjectionPhaseRuntime,
			strategy:       serviceconfigurationv1.StrategyInternalHTTP,
			expectErrCode:  "BROWSER_INTERNAL_HTTP_FORBIDDEN",
		},
		{
			name:           "server + same_origin -> rejected",
			accessContext:  serviceconfigurationv1.AccessContextServer,
			injectionPhase: serviceconfigurationv1.InjectionPhaseRuntime,
			strategy:       serviceconfigurationv1.StrategySameOrigin,
			consumerRoute:  &serviceconfigurationv1.PublicRouteIntent{Hostname: "app.example.com", Path: "/"},
			path:           "/api",
			expectErrCode:  "STRATEGY_CONTEXT_MISMATCH",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			draft := ServiceConfigurationDraft{
				PublicRoute: tt.consumerRoute,
				Dependencies: []serviceconfigurationv1.ApplicationDependency{
					{
						LogicalName:    "dep-test",
						TargetKind:     "application",
						TargetIdentity: target.ID,
						Protocol:       "http",
						AccessContext:  tt.accessContext,
						InjectionPhase: tt.injectionPhase,
						Strategy:       tt.strategy,
						Path:           tt.path,
					},
				},
			}
			_, _, err := validateServiceConfiguration(context.Background(), nil, source, draft, services)
			if tt.expectErrCode != "" {
				if err == nil || !hasAPIErrorCode(err, tt.expectErrCode) {
					t.Fatalf("expected error code %s, got %v", tt.expectErrCode, err)
				}
			} else {
				if err != nil {
					t.Fatalf("expected valid configuration, got %v", err)
				}
			}
		})
	}
}

func TestDependencyManualEnvironmentCollision(t *testing.T) {
	target := ServiceRecord{ID: "target-app", ProjectID: "proj-1", ContainerPort: 8080}
	source := ServiceRecord{ID: "source-app", ProjectID: "proj-1", ContainerPort: 3000}
	services := []ServiceRecord{source, target}

	draft := ServiceConfigurationDraft{
		Environment: []deploymentv1.EnvironmentVariable{
			{Name: "API_URL", Value: "http://manual-override.example.com"},
		},
		Dependencies: []serviceconfigurationv1.ApplicationDependency{
			{
				LogicalName:    "api",
				TargetKind:     "application",
				TargetIdentity: target.ID,
				Protocol:       "http",
				Strategy:       serviceconfigurationv1.StrategyInternalHTTP,
				AccessContext:  serviceconfigurationv1.AccessContextServer,
				InjectionPhase: serviceconfigurationv1.InjectionPhaseRuntime,
				InjectionMappings: []serviceconfigurationv1.DependencyInjectionMapping{
					{EnvName: "API_URL", SymbolicSource: "application.internal_url"},
				},
			},
		},
	}

	_, _, err := validateServiceConfiguration(context.Background(), nil, source, draft, services)
	if err == nil || !hasAPIErrorCode(err, "DEPENDENCY_ENV_CONFLICT") {
		t.Fatalf("expected DEPENDENCY_ENV_CONFLICT, got %v", err)
	}
}

func TestBuildDependencyMappingsAndStateComputation(t *testing.T) {
	target := ServiceRecord{
		ID:            "api-service",
		ProjectID:     "proj-1",
		ContainerPort: 8080,
		Configuration: ServiceConfiguration{
			ServiceConfigurationDraft: ServiceConfigurationDraft{
				PublicRoute: &serviceconfigurationv1.PublicRouteIntent{Hostname: "api.example.com", Path: "/api"},
			},
		},
	}

	// 1. Same origin build mapping uses path
	sameOriginCfg := ServiceConfiguration{
		ServiceConfigurationDraft: ServiceConfigurationDraft{
			Dependencies: []serviceconfigurationv1.ApplicationDependency{
				{
					LogicalName:    "api-dep",
					TargetKind:     "application",
					TargetIdentity: target.ID,
					Protocol:       "http",
					Strategy:       serviceconfigurationv1.StrategySameOrigin,
					AccessContext:  serviceconfigurationv1.AccessContextBrowser,
					InjectionPhase: serviceconfigurationv1.InjectionPhaseBuild,
					Path:           "/api",
					InjectionMappings: []serviceconfigurationv1.DependencyInjectionMapping{
						{EnvName: "API_BASE_PATH", SymbolicSource: "application.path"},
					},
				},
			},
		},
	}

	env1 := ComputeBuildEnvironment(sameOriginCfg, []ServiceRecord{target})
	if env1["API_BASE_PATH"] != "/api" {
		t.Fatalf("expected API_BASE_PATH=/api, got %+v", env1)
	}
	state1 := ComputeBuildDependencyState(sameOriginCfg, []ServiceRecord{target})

	// Changing target hostname does not affect same-origin path mapping
	targetChangedHost := target
	targetChangedHost.Configuration.PublicRoute = &serviceconfigurationv1.PublicRouteIntent{Hostname: "new-api.example.com", Path: "/api"}
	state1Changed := ComputeBuildDependencyState(sameOriginCfg, []ServiceRecord{targetChangedHost})
	if state1 != state1Changed {
		t.Fatalf("same-origin path build state should not change on hostname change: before=%s after=%s", state1, state1Changed)
	}

	// 2. Public HTTP build mapping uses public URL
	publicHTTPCfg := ServiceConfiguration{
		ServiceConfigurationDraft: ServiceConfigurationDraft{
			Dependencies: []serviceconfigurationv1.ApplicationDependency{
				{
					LogicalName:    "api-public",
					TargetKind:     "application",
					TargetIdentity: target.ID,
					Protocol:       "http",
					Strategy:       serviceconfigurationv1.StrategyPublicHTTP,
					AccessContext:  serviceconfigurationv1.AccessContextServer,
					InjectionPhase: serviceconfigurationv1.InjectionPhaseBuild,
					InjectionMappings: []serviceconfigurationv1.DependencyInjectionMapping{
						{EnvName: "PUBLIC_API_ORIGIN", SymbolicSource: "application.public_url"},
					},
				},
			},
		},
	}

	env2 := ComputeBuildEnvironment(publicHTTPCfg, []ServiceRecord{target})
	if env2["PUBLIC_API_ORIGIN"] != "https://api.example.com/api" {
		t.Fatalf("expected PUBLIC_API_ORIGIN=https://api.example.com/api, got %+v", env2)
	}
	state2 := ComputeBuildDependencyState(publicHTTPCfg, []ServiceRecord{target})
	state2Changed := ComputeBuildDependencyState(publicHTTPCfg, []ServiceRecord{targetChangedHost})
	if state2 == state2Changed {
		t.Fatalf("public HTTP build state must change on hostname change: before=%s after=%s", state2, state2Changed)
	}
}
