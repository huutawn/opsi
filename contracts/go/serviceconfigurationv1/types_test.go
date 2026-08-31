package serviceconfigurationv1

import "testing"

func TestDependencyStateHashDeterministic(t *testing.T) {
	d1 := ServiceConfigurationDraft{
		Dependencies: []ApplicationDependency{
			{
				LogicalName: "b", TargetKind: "managed_resource", TargetIdentity: "res-1", Protocol: "postgres", InjectionPhase: "runtime",
				InjectionMappings: []DependencyInjectionMapping{
					{EnvName: "URL2", SymbolicSource: "src2"},
					{EnvName: "URL1", SymbolicSource: "src1"},
				},
			},
			{
				LogicalName: "a", TargetKind: "managed_resource", TargetIdentity: "res-2", Protocol: "redis", InjectionPhase: "runtime",
			},
		},
	}

	d2 := ServiceConfigurationDraft{
		Dependencies: []ApplicationDependency{
			{
				LogicalName: "a", TargetKind: "managed_resource", TargetIdentity: "res-2", Protocol: "redis", InjectionPhase: "runtime",
			},
			{
				LogicalName: "b", TargetKind: "managed_resource", TargetIdentity: "res-1", Protocol: "postgres", InjectionPhase: "runtime",
				InjectionMappings: []DependencyInjectionMapping{
					{EnvName: "URL1", SymbolicSource: "src1"},
					{EnvName: "URL2", SymbolicSource: "src2"},
				},
			},
		},
	}

	h1 := StateHash(d1)
	h2 := StateHash(d2)

	if h1 != h2 {
		t.Fatalf("hashes should be deterministic, got %s and %s", h1, h2)
	}
}

func TestDependencyTemplateParticipatesInStateHash(t *testing.T) {
	draft := ServiceConfigurationDraft{Dependencies: []ApplicationDependency{{LogicalName: "database", TargetKind: "managed_resource", TargetIdentity: "res-1", Protocol: "postgres", InjectionPhase: "runtime", InjectionMappings: []DependencyInjectionMapping{{EnvName: "DATABASE_DSN", SymbolicSource: SourceConnectionTemplate, Template: "host={{host}}"}}}}}
	first := StateHash(draft)
	draft.Dependencies[0].InjectionMappings[0].Template = "server={{host}}"
	if first == StateHash(draft) {
		t.Fatal("template edit did not invalidate the state hash")
	}
}

func TestDependencyTemplateWhitespaceChangesStateHashAndSurvivesNormalization(t *testing.T) {
	draft := ServiceConfigurationDraft{Dependencies: []ApplicationDependency{{LogicalName: "database", TargetKind: TargetKindManagedResource, TargetIdentity: "res-1", Protocol: ProtocolPostgres, InjectionPhase: InjectionPhaseRuntime, InjectionMappings: []DependencyInjectionMapping{{EnvName: "DATABASE_DSN", SymbolicSource: SourceConnectionTemplate, Template: " host={{host}}\n"}}}}}
	first := StateHash(draft)
	normalized := Normalize(draft)
	if normalized.Dependencies[0].InjectionMappings[0].Template != " host={{host}}\n" {
		t.Fatalf("normalized template=%q", normalized.Dependencies[0].InjectionMappings[0].Template)
	}
	draft.Dependencies[0].InjectionMappings[0].Template = "host={{host}}\n"
	if first == StateHash(draft) {
		t.Fatal("leading whitespace did not change the state hash")
	}
}
