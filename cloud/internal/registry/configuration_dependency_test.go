package registry

import (
	"context"
	"errors"
	"strings"
	"testing"

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
