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
