package registry

import (
	"context"
	"strings"
	"testing"

	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
	serviceconfigurationv1 "github.com/opsi-dev/opsi/contracts/go/serviceconfigurationv1"
)

type mockRealizationResolver struct {
	targets map[string]DependencyTargetFacts
}

func (m *mockRealizationResolver) ResolveDependencyTarget(ctx context.Context, projectID, targetIdentity, targetKind string) (DependencyTargetFacts, error) {
	if facts, ok := m.targets[targetIdentity]; ok {
		return facts, nil
	}
	return DependencyTargetFacts{}, nil
}

func TestDependencyRealization_ValidationAndPresets(t *testing.T) {
	resolver := &mockRealizationResolver{
		targets: map[string]DependencyTargetFacts{
			"res-pg-1": {
				Exists:        true,
				ProjectID:     "proj-1",
				EnvironmentID: "env-1",
				TargetKind:    "managed_resource",
				ResourceType:  "postgres",
				Lifecycle:     "ready",
				Host:          "postgres-1.svc.cluster.local",
				Port:          5432,
				Database:      "opsi",
			},
			"res-valkey-1": {
				Exists:        true,
				ProjectID:     "proj-1",
				EnvironmentID: "env-1",
				TargetKind:    "managed_resource",
				ResourceType:  "redis",
				Lifecycle:     "ready",
				Host:          "valkey-1.svc.cluster.local",
				Port:          6379,
			},
			"res-deleted": {
				Exists:        true,
				ProjectID:     "proj-1",
				EnvironmentID: "env-1",
				TargetKind:    "managed_resource",
				ResourceType:  "postgres",
				Lifecycle:     "deleted",
				Deleted:       true,
			},
			"res-foreign": {
				Exists:        true,
				ProjectID:     "proj-other",
				EnvironmentID: "env-1",
				TargetKind:    "managed_resource",
				ResourceType:  "postgres",
				Lifecycle:     "ready",
			},
		},
	}

	source := ServiceRecord{
		ID:            "svc-app",
		ProjectID:     "proj-1",
		EnvironmentID: "env-1",
		Name:          "web",
		ContainerPort: 8080,
		HealthPath:    "/health",
	}

	// 1. PostgreSQL URL Preset
	t.Run("PostgreSQL_DATABASE_URL_Preset", func(t *testing.T) {
		draft := ServiceConfigurationDraft{
			Dependencies: []serviceconfigurationv1.ApplicationDependency{
				serviceconfigurationv1.PostgresURLPreset("database", "res-pg-1", true),
			},
		}
		validated, _, err := validateServiceConfiguration(context.Background(), resolver, source, draft, []ServiceRecord{source})
		if err != nil {
			t.Fatalf("unexpected validation error: %v", err)
		}
		if len(validated.Dependencies) != 1 || validated.Dependencies[0].InjectionMappings[0].EnvName != "DATABASE_URL" {
			t.Fatalf("unexpected validated dependencies: %+v", validated.Dependencies)
		}
	})

	// 2. PostgreSQL Standard PG* Preset
	t.Run("PostgreSQL_PG_Preset", func(t *testing.T) {
		draft := ServiceConfigurationDraft{
			Dependencies: []serviceconfigurationv1.ApplicationDependency{
				serviceconfigurationv1.PostgresStandardPreset("database", "res-pg-1", true),
			},
		}
		validated, _, err := validateServiceConfiguration(context.Background(), resolver, source, draft, []ServiceRecord{source})
		if err != nil {
			t.Fatalf("unexpected validation error: %v", err)
		}
		if len(validated.Dependencies[0].InjectionMappings) != 5 {
			t.Fatalf("expected 5 mappings, got %d", len(validated.Dependencies[0].InjectionMappings))
		}
	})

	// 3. Custom PostgreSQL Mappings
	t.Run("PostgreSQL_Custom_Mappings", func(t *testing.T) {
		draft := ServiceConfigurationDraft{
			Dependencies: []serviceconfigurationv1.ApplicationDependency{
				{
					LogicalName:    "db",
					TargetKind:     "managed_resource",
					TargetIdentity: "res-pg-1",
					Protocol:       "postgres",
					Required:       true,
					InjectionPhase: "runtime",
					InjectionMappings: []serviceconfigurationv1.DependencyInjectionMapping{
						{EnvName: "APP_DATABASE_URL", SymbolicSource: "connection.url"},
						{EnvName: "DB_HOST", SymbolicSource: "resource.host"},
						{EnvName: "DB_PORT", SymbolicSource: "resource.port"},
						{EnvName: "DB_NAME", SymbolicSource: "credential.database"},
						{EnvName: "DB_USER", SymbolicSource: "credential.username"},
						{EnvName: "DB_PASS", SymbolicSource: "credential.password"},
					},
				},
			},
		}
		_, _, err := validateServiceConfiguration(context.Background(), resolver, source, draft, []ServiceRecord{source})
		if err != nil {
			t.Fatalf("unexpected validation error: %v", err)
		}
	})

	// 4. Valkey Presets & Custom
	t.Run("Valkey_Presets_And_Custom", func(t *testing.T) {
		draft := ServiceConfigurationDraft{
			Dependencies: []serviceconfigurationv1.ApplicationDependency{
				serviceconfigurationv1.ValkeyURLPreset("cache", "res-valkey-1", false),
			},
		}
		_, _, err := validateServiceConfiguration(context.Background(), resolver, source, draft, []ServiceRecord{source})
		if err != nil {
			t.Fatalf("unexpected validation error: %v", err)
		}

		customDraft := ServiceConfigurationDraft{
			Dependencies: []serviceconfigurationv1.ApplicationDependency{
				{
					LogicalName:    "cache",
					TargetKind:     "managed_resource",
					TargetIdentity: "res-valkey-1",
					Protocol:       "redis",
					Required:       false,
					InjectionPhase: "runtime",
					InjectionMappings: []serviceconfigurationv1.DependencyInjectionMapping{
						{EnvName: "APP_REDIS_URL", SymbolicSource: "connection.url"},
						{EnvName: "CACHE_HOST", SymbolicSource: "resource.host"},
						{EnvName: "CACHE_PORT", SymbolicSource: "resource.port"},
						{EnvName: "CACHE_PASS", SymbolicSource: "credential.password"},
					},
				},
			},
		}
		_, _, err = validateServiceConfiguration(context.Background(), resolver, source, customDraft, []ServiceRecord{source})
		if err != nil {
			t.Fatalf("unexpected custom valkey error: %v", err)
		}
	})

	// 5. Redis database index remains an atomic non-secret source.
	t.Run("Valkey_Database_Index_Source", func(t *testing.T) {
		draft := ServiceConfigurationDraft{
			Dependencies: []serviceconfigurationv1.ApplicationDependency{
				{
					LogicalName:    "cache",
					TargetKind:     "managed_resource",
					TargetIdentity: "res-valkey-1",
					Protocol:       "redis",
					Required:       false,
					InjectionPhase: "runtime",
					InjectionMappings: []serviceconfigurationv1.DependencyInjectionMapping{
						{EnvName: "CACHE_DB", SymbolicSource: "credential.database"},
					},
				},
			},
		}
		_, _, err := validateServiceConfiguration(context.Background(), resolver, source, draft, []ServiceRecord{source})
		if err != nil {
			t.Fatalf("expected Redis database source to validate, got %v", err)
		}
	})

	// 6. Manual User Env Conflict
	t.Run("Manual_Env_Conflict", func(t *testing.T) {
		draft := ServiceConfigurationDraft{
			Environment: []deploymentv1.EnvironmentVariable{
				{Name: "APP_DATABASE_URL", Value: "postgres://local:5432"},
			},
			Dependencies: []serviceconfigurationv1.ApplicationDependency{
				{
					LogicalName:    "database",
					TargetKind:     "managed_resource",
					TargetIdentity: "res-pg-1",
					Protocol:       "postgres",
					Required:       true,
					InjectionPhase: "runtime",
					InjectionMappings: []serviceconfigurationv1.DependencyInjectionMapping{
						{EnvName: "APP_DATABASE_URL", SymbolicSource: "connection.url"},
					},
				},
			},
		}
		_, _, err := validateServiceConfiguration(context.Background(), resolver, source, draft, []ServiceRecord{source})
		if err == nil || !strings.Contains(err.Error(), "DEPENDENCY_ENV_CONFLICT") {
			t.Fatalf("expected DEPENDENCY_ENV_CONFLICT, got %v", err)
		}
	})

	// 7. Reserved Env Conflict
	t.Run("Reserved_Env_Conflict", func(t *testing.T) {
		for _, reserved := range []string{"PORT", "HOSTNAME", "OPSI_SERVICE", "KUBERNETES_PORT"} {
			draft := ServiceConfigurationDraft{
				Dependencies: []serviceconfigurationv1.ApplicationDependency{
					{
						LogicalName:    "database",
						TargetKind:     "managed_resource",
						TargetIdentity: "res-pg-1",
						Protocol:       "postgres",
						Required:       true,
						InjectionPhase: "runtime",
						InjectionMappings: []serviceconfigurationv1.DependencyInjectionMapping{
							{EnvName: reserved, SymbolicSource: "resource.port"},
						},
					},
				},
			}
			_, _, err := validateServiceConfiguration(context.Background(), resolver, source, draft, []ServiceRecord{source})
			if err == nil || !strings.Contains(err.Error(), "DEPENDENCY_ENV_CONFLICT") {
				t.Fatalf("expected reserved env conflict for %s, got %v", reserved, err)
			}
		}
	})

	// 8. Cross Dependency Collision
	t.Run("Cross_Dependency_Env_Collision", func(t *testing.T) {
		draft := ServiceConfigurationDraft{
			Dependencies: []serviceconfigurationv1.ApplicationDependency{
				serviceconfigurationv1.PostgresURLPreset("db1", "res-pg-1", true),
				{
					LogicalName:    "db2",
					TargetKind:     "managed_resource",
					TargetIdentity: "res-pg-1",
					Protocol:       "postgres",
					Required:       true,
					InjectionPhase: "runtime",
					InjectionMappings: []serviceconfigurationv1.DependencyInjectionMapping{
						{EnvName: "DATABASE_URL", SymbolicSource: "connection.url"},
					},
				},
			},
		}
		_, _, err := validateServiceConfiguration(context.Background(), resolver, source, draft, []ServiceRecord{source})
		if err == nil || !strings.Contains(err.Error(), "DEPENDENCY_ENV_COLLISION") {
			t.Fatalf("expected DEPENDENCY_ENV_COLLISION, got %v", err)
		}
	})

	// 9. Protocol Mismatch
	t.Run("Protocol_Mismatch", func(t *testing.T) {
		// Postgres dependency targeting Redis resource
		draft1 := ServiceConfigurationDraft{
			Dependencies: []serviceconfigurationv1.ApplicationDependency{
				{
					LogicalName:    "database",
					TargetKind:     "managed_resource",
					TargetIdentity: "res-valkey-1",
					Protocol:       "postgres",
					Required:       true,
					InjectionPhase: "runtime",
					InjectionMappings: []serviceconfigurationv1.DependencyInjectionMapping{
						{EnvName: "DATABASE_URL", SymbolicSource: "connection.url"},
					},
				},
			},
		}
		_, _, err := validateServiceConfiguration(context.Background(), resolver, source, draft1, []ServiceRecord{source})
		if err == nil || !strings.Contains(err.Error(), "DEPENDENCY_PROTOCOL_UNSUPPORTED") {
			t.Fatalf("expected DEPENDENCY_PROTOCOL_UNSUPPORTED, got %v", err)
		}

		// Redis dependency targeting Postgres resource
		draft2 := ServiceConfigurationDraft{
			Dependencies: []serviceconfigurationv1.ApplicationDependency{
				{
					LogicalName:    "cache",
					TargetKind:     "managed_resource",
					TargetIdentity: "res-pg-1",
					Protocol:       "redis",
					Required:       true,
					InjectionPhase: "runtime",
					InjectionMappings: []serviceconfigurationv1.DependencyInjectionMapping{
						{EnvName: "REDIS_URL", SymbolicSource: "connection.url"},
					},
				},
			},
		}
		_, _, err = validateServiceConfiguration(context.Background(), resolver, source, draft2, []ServiceRecord{source})
		if err == nil || !strings.Contains(err.Error(), "DEPENDENCY_PROTOCOL_UNSUPPORTED") {
			t.Fatalf("expected DEPENDENCY_PROTOCOL_UNSUPPORTED, got %v", err)
		}
	})

	// 10. Deleted Target
	t.Run("Deleted_Target", func(t *testing.T) {
		draft := ServiceConfigurationDraft{
			Dependencies: []serviceconfigurationv1.ApplicationDependency{
				serviceconfigurationv1.PostgresURLPreset("database", "res-deleted", true),
			},
		}
		_, _, err := validateServiceConfiguration(context.Background(), resolver, source, draft, []ServiceRecord{source})
		if err == nil || !strings.Contains(err.Error(), "DEPENDENCY_TARGET_NOT_FOUND") {
			t.Fatalf("expected DEPENDENCY_TARGET_NOT_FOUND, got %v", err)
		}
	})

	// 11. Foreign Scope Target
	t.Run("Foreign_Scope_Target", func(t *testing.T) {
		draft := ServiceConfigurationDraft{
			Dependencies: []serviceconfigurationv1.ApplicationDependency{
				serviceconfigurationv1.PostgresURLPreset("database", "res-foreign", true),
			},
		}
		_, _, err := validateServiceConfiguration(context.Background(), resolver, source, draft, []ServiceRecord{source})
		if err == nil || !strings.Contains(err.Error(), "DEPENDENCY_TARGET_FORBIDDEN") {
			t.Fatalf("expected DEPENDENCY_TARGET_FORBIDDEN, got %v", err)
		}
	})

	// 12. Build Phase ManagedResource Rejected
	t.Run("Build_Phase_ManagedResource_Rejected", func(t *testing.T) {
		draft := ServiceConfigurationDraft{
			Dependencies: []serviceconfigurationv1.ApplicationDependency{
				{
					LogicalName:    "database",
					TargetKind:     "managed_resource",
					TargetIdentity: "res-pg-1",
					Protocol:       "postgres",
					Required:       true,
					InjectionPhase: "build",
					InjectionMappings: []serviceconfigurationv1.DependencyInjectionMapping{
						{EnvName: "DATABASE_URL", SymbolicSource: "connection.url"},
					},
				},
			},
		}
		_, _, err := validateServiceConfiguration(context.Background(), resolver, source, draft, []ServiceRecord{source})
		if err == nil || !strings.Contains(err.Error(), "DEPENDENCY_BUILD_PHASE_UNSUPPORTED") {
			t.Fatalf("expected DEPENDENCY_BUILD_PHASE_UNSUPPORTED, got %v", err)
		}
	})

	// 13. Active Target Replacement Requires Cutover
	t.Run("Active_Target_Replacement_Safety", func(t *testing.T) {
		sourceWithActiveDep := source
		sourceWithActiveDep.Configuration = ServiceConfiguration{
			ServiceConfigurationDraft: ServiceConfigurationDraft{
				Dependencies: []serviceconfigurationv1.ApplicationDependency{
					serviceconfigurationv1.PostgresURLPreset("database", "res-pg-1", true),
				},
			},
		}

		newDraft := ServiceConfigurationDraft{
			Dependencies: []serviceconfigurationv1.ApplicationDependency{
				serviceconfigurationv1.PostgresURLPreset("database", "res-pg-2", true),
			},
		}

		_, _, err := validateServiceConfiguration(context.Background(), resolver, sourceWithActiveDep, newDraft, []ServiceRecord{sourceWithActiveDep})
		if err == nil || !strings.Contains(err.Error(), "DEPENDENCY_BINDING_REPLACEMENT_REQUIRES_EXPLICIT_MIGRATION") {
			t.Fatalf("expected DEPENDENCY_BINDING_REPLACEMENT_REQUIRES_EXPLICIT_MIGRATION, got %v", err)
		}
	})

	// 14. Multiple Dependencies Coexistence (Postgres + Valkey)
	t.Run("PostgreSQL_And_Valkey_Coexistence", func(t *testing.T) {
		draft := ServiceConfigurationDraft{
			Dependencies: []serviceconfigurationv1.ApplicationDependency{
				{
					LogicalName:    "database",
					TargetKind:     "managed_resource",
					TargetIdentity: "res-pg-1",
					Protocol:       "postgres",
					Required:       true,
					InjectionPhase: "runtime",
					InjectionMappings: []serviceconfigurationv1.DependencyInjectionMapping{
						{EnvName: "APP_DATABASE_URL", SymbolicSource: "connection.url"},
					},
				},
				{
					LogicalName:    "cache",
					TargetKind:     "managed_resource",
					TargetIdentity: "res-valkey-1",
					Protocol:       "redis",
					Required:       false,
					InjectionPhase: "runtime",
					InjectionMappings: []serviceconfigurationv1.DependencyInjectionMapping{
						{EnvName: "APP_REDIS_URL", SymbolicSource: "connection.url"},
					},
				},
			},
		}
		validated, _, err := validateServiceConfiguration(context.Background(), resolver, source, draft, []ServiceRecord{source})
		if err != nil {
			t.Fatalf("coexistence validation failed: %v", err)
		}
		if len(validated.Dependencies) != 2 {
			t.Fatalf("expected 2 dependencies, got %d", len(validated.Dependencies))
		}
	})
}

func TestPlanDependencyRealization_ZeroMutationAndSafety(t *testing.T) {
	pgResource := resourcev1.Resource{
		ID:            "res-pg-1",
		ProjectID:     "proj-1",
		EnvironmentID: "env-1",
		Type:          resourcev1.TypePostgres,
		Lifecycle:     resourcev1.LifecycleReady,
		Runtime: &resourcev1.ManagedResourceRuntime{
			Spec: resourcev1.ManagedResourceSpec{
				Connection: resourcev1.ManagedResourceConnection{
					Host:     "postgres-1.svc.cluster.local",
					Port:     5432,
					Database: "opsi",
				},
			},
		},
	}

	valkeyResource := resourcev1.Resource{
		ID:            "res-valkey-1",
		ProjectID:     "proj-1",
		EnvironmentID: "env-1",
		Type:          resourcev1.TypeRedis,
		Lifecycle:     resourcev1.LifecycleReady,
		Runtime: &resourcev1.ManagedResourceRuntime{
			Spec: resourcev1.ManagedResourceSpec{
				Connection: resourcev1.ManagedResourceConnection{
					Host: "valkey-1.svc.cluster.local",
					Port: 6379,
				},
			},
		},
	}

	getTarget := func(ctx context.Context, id string) (resourcev1.Resource, error) {
		if id == "res-pg-1" {
			return pgResource, nil
		}
		if id == "res-valkey-1" {
			return valkeyResource, nil
		}
		return resourcev1.Resource{}, ErrNotFound
	}

	config := ServiceConfiguration{
		ServiceConfigurationDraft: ServiceConfigurationDraft{
			Dependencies: []serviceconfigurationv1.ApplicationDependency{
				{
					LogicalName:    "database",
					TargetKind:     "managed_resource",
					TargetIdentity: "res-pg-1",
					Protocol:       "postgres",
					Required:       true,
					InjectionPhase: "runtime",
					InjectionMappings: []serviceconfigurationv1.DependencyInjectionMapping{
						{EnvName: "APP_DATABASE_URL", SymbolicSource: "connection.url"},
						{EnvName: "PGHOST", SymbolicSource: "resource.host"},
						{EnvName: "PGPORT", SymbolicSource: "resource.port"},
						{EnvName: "PGUSER", SymbolicSource: "credential.username"},
						{EnvName: "PGPASSWORD", SymbolicSource: "credential.password"},
						{EnvName: "PGDATABASE", SymbolicSource: "credential.database"},
					},
				},
			},
		},
	}

	// 1. Without existing binding: action = create
	plan, err := PlanDependencyRealization(context.Background(), config, nil, getTarget)
	if err != nil {
		t.Fatalf("plan failed: %v", err)
	}
	if len(plan.Dependencies) != 1 {
		t.Fatalf("expected 1 dep plan, got %d", len(plan.Dependencies))
	}
	depPlan := plan.Dependencies[0]
	if depPlan.BindingAction != "create" || depPlan.Status != "pending_binding" {
		t.Fatalf("expected create / pending_binding, got %s / %s", depPlan.BindingAction, depPlan.Status)
	}

	// Check that value previews never leak secrets
	for _, p := range depPlan.Projections {
		if p.Sensitivity == "secret" {
			if strings.Contains(p.ValuePreview, "postgres://") || strings.Contains(p.ValuePreview, "secret") {
				t.Fatalf("secret leaked in projection preview: %+v", p)
			}
			if !strings.HasPrefix(p.ValuePreview, "[managed") {
				t.Fatalf("expected safe descriptor, got %q", p.ValuePreview)
			}
		}
		if p.EnvName == "PGHOST" && p.ValuePreview != "postgres-1.svc.cluster.local" {
			t.Fatalf("expected host preview, got %q", p.ValuePreview)
		}
		if p.EnvName == "PGPORT" && p.ValuePreview != "5432" {
			t.Fatalf("expected port preview 5432, got %q", p.ValuePreview)
		}
		if p.EnvName == "PGDATABASE" && p.ValuePreview != "opsi" {
			t.Fatalf("expected database preview opsi, got %q", p.ValuePreview)
		}
	}

	// 2. With existing compatible binding: action = reused
	existingBinding := resourcev1.Binding{
		ID:          "rbind-pg-existing",
		Target:      resourcev1.EndpointReference{Kind: resourcev1.KindManagedService, ID: "res-pg-1"},
		LogicalName: "database",
		Protocol:    resourcev1.ProtocolPostgres,
		Lifecycle:   resourcev1.LifecycleReady,
	}

	planReused, err := PlanDependencyRealization(context.Background(), config, []resourcev1.Binding{existingBinding}, getTarget)
	if err != nil {
		t.Fatalf("plan with existing binding failed: %v", err)
	}
	if planReused.Dependencies[0].BindingAction != "reused" || planReused.Dependencies[0].BindingID != "rbind-pg-existing" || planReused.Dependencies[0].Status != "ready" {
		t.Fatalf("expected reused rbind-pg-existing, got %+v", planReused.Dependencies[0])
	}
}
