package registry

import (
	"context"
	"strings"
	"testing"
	serviceconfigurationv1 "github.com/opsi-dev/opsi/contracts/go/serviceconfigurationv1"
)

type mockDependencyTargetResolver struct {
	facts map[string]DependencyTargetFacts
}

func (m *mockDependencyTargetResolver) ResolveDependencyTarget(ctx context.Context, projectID, targetIdentity string, targetKind string) (DependencyTargetFacts, error) {
	facts, ok := m.facts[targetIdentity]
	if !ok || facts.TargetKind != targetKind {
		return DependencyTargetFacts{}, nil
	}
	return facts, nil
}

func TestDependencyTargetAuthorityValidation(t *testing.T) {
	services := []ServiceRecord{
		{ID: "source-1", ProjectID: "proj-1", EnvironmentID: "env-1"},
	}

	resolver := &mockDependencyTargetResolver{
		facts: map[string]DependencyTargetFacts{
			"res-exists":          {Exists: true, ProjectID: "proj-1", EnvironmentID: "env-1", TargetKind: "managed_resource"},
			"res-foreign-project": {Exists: true, ProjectID: "proj-2", EnvironmentID: "env-1", TargetKind: "managed_resource"},
			"res-foreign-env":     {Exists: true, ProjectID: "proj-1", EnvironmentID: "env-2", TargetKind: "managed_resource"},
			"res-deleted":         {Exists: true, ProjectID: "proj-1", EnvironmentID: "env-1", TargetKind: "managed_resource", Deleted: true},
			
			"app-exists":          {Exists: true, ProjectID: "proj-1", EnvironmentID: "env-1", TargetKind: "application"},
			"app-foreign-project": {Exists: true, ProjectID: "proj-2", EnvironmentID: "env-1", TargetKind: "application"},
			"app-foreign-env":     {Exists: true, ProjectID: "proj-1", EnvironmentID: "env-2", TargetKind: "application"},
			"app-deleted":         {Exists: true, ProjectID: "proj-1", EnvironmentID: "env-1", TargetKind: "application", Deleted: true},
			"source-1":            {Exists: true, ProjectID: "proj-1", EnvironmentID: "env-1", TargetKind: "application"},
		},
	}

	tests := []struct {
		name         string
		dependencies []serviceconfigurationv1.ApplicationDependency
		expectedErr  string
		expectedCode string
	}{
		{
			name: "managed_resource exists",
			dependencies: []serviceconfigurationv1.ApplicationDependency{
				{LogicalName: "db", TargetKind: "managed_resource", TargetIdentity: "res-exists", Protocol: "postgres", InjectionPhase: "runtime"},
			},
		},
		{
			name: "managed_resource missing",
			dependencies: []serviceconfigurationv1.ApplicationDependency{
				{LogicalName: "db", TargetKind: "managed_resource", TargetIdentity: "res-missing", Protocol: "postgres", InjectionPhase: "runtime"},
			},
			expectedCode: "DEPENDENCY_TARGET_NOT_FOUND",
		},
		{
			name: "managed_resource foreign project",
			dependencies: []serviceconfigurationv1.ApplicationDependency{
				{LogicalName: "db", TargetKind: "managed_resource", TargetIdentity: "res-foreign-project", Protocol: "postgres", InjectionPhase: "runtime"},
			},
			expectedCode: "DEPENDENCY_TARGET_FORBIDDEN",
		},
		{
			name: "managed_resource invalid env",
			dependencies: []serviceconfigurationv1.ApplicationDependency{
				{LogicalName: "db", TargetKind: "managed_resource", TargetIdentity: "res-foreign-env", Protocol: "postgres", InjectionPhase: "runtime"},
			},
			expectedCode: "DEPENDENCY_TARGET_INVALID",
		},
		{
			name: "managed_resource deleted before review",
			dependencies: []serviceconfigurationv1.ApplicationDependency{
				{LogicalName: "db", TargetKind: "managed_resource", TargetIdentity: "res-deleted", Protocol: "postgres", InjectionPhase: "runtime"},
			},
			expectedCode: "DEPENDENCY_TARGET_NOT_FOUND",
		},
		{
			name: "application exists",
			dependencies: []serviceconfigurationv1.ApplicationDependency{
				{LogicalName: "api", TargetKind: "application", TargetIdentity: "app-exists", Protocol: "http", InjectionPhase: "runtime"},
			},
		},
		{
			name: "application missing",
			dependencies: []serviceconfigurationv1.ApplicationDependency{
				{LogicalName: "api", TargetKind: "application", TargetIdentity: "app-missing", Protocol: "http", InjectionPhase: "runtime"},
			},
			expectedCode: "DEPENDENCY_TARGET_NOT_FOUND",
		},
		{
			name: "application foreign project",
			dependencies: []serviceconfigurationv1.ApplicationDependency{
				{LogicalName: "api", TargetKind: "application", TargetIdentity: "app-foreign-project", Protocol: "http", InjectionPhase: "runtime"},
			},
			expectedCode: "DEPENDENCY_TARGET_FORBIDDEN",
		},
		{
			name: "application invalid env",
			dependencies: []serviceconfigurationv1.ApplicationDependency{
				{LogicalName: "api", TargetKind: "application", TargetIdentity: "app-foreign-env", Protocol: "http", InjectionPhase: "runtime"},
			},
			expectedCode: "DEPENDENCY_TARGET_INVALID",
		},
		{
			name: "application deleted before review",
			dependencies: []serviceconfigurationv1.ApplicationDependency{
				{LogicalName: "api", TargetKind: "application", TargetIdentity: "app-deleted", Protocol: "http", InjectionPhase: "runtime"},
			},
			expectedCode: "DEPENDENCY_TARGET_NOT_FOUND",
		},
		{
			name: "application self",
			dependencies: []serviceconfigurationv1.ApplicationDependency{
				{LogicalName: "self", TargetKind: "application", TargetIdentity: "source-1", Protocol: "http", InjectionPhase: "runtime"},
			},
			expectedCode: "DEPENDENCY_INVALID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			draft := serviceconfigurationv1.ServiceConfigurationDraft{
				Dependencies: tt.dependencies,
			}
			_, _, err := validateServiceConfiguration(context.Background(), resolver, services[0], draft, services)
			if tt.expectedCode != "" {
				if err == nil {
					t.Fatalf("expected error code %q, got nil", tt.expectedCode)
				}
				if !strings.Contains(err.Error(), tt.expectedCode) && !hasAPIErrorCode(err, tt.expectedCode) {
					t.Fatalf("expected error code %q, got %v", tt.expectedCode, err)
				}
			} else {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
			}
		})
	}
}
