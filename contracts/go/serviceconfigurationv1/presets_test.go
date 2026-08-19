package serviceconfigurationv1

import (
	"testing"
)

func TestPresets(t *testing.T) {
	pgURL := PostgresURLPreset("database", "res-pg-1", true)
	if pgURL.LogicalName != "database" || pgURL.Protocol != ProtocolPostgres || len(pgURL.InjectionMappings) != 1 || pgURL.InjectionMappings[0].EnvName != "DATABASE_URL" {
		t.Fatalf("unexpected pgURL preset: %+v", pgURL)
	}

	pgStd := PostgresStandardPreset("database", "res-pg-1", true)
	if len(pgStd.InjectionMappings) != 5 {
		t.Fatalf("expected 5 mappings for pg standard, got %d", len(pgStd.InjectionMappings))
	}

	valkeyURL := ValkeyURLPreset("cache", "res-valkey-1", false)
	if valkeyURL.Protocol != ProtocolRedis || len(valkeyURL.InjectionMappings) != 1 || valkeyURL.InjectionMappings[0].EnvName != "REDIS_URL" {
		t.Fatalf("unexpected valkey URL preset: %+v", valkeyURL)
	}

	valkeyCache := ValkeyCachePreset("cache", "res-valkey-1", false)
	if valkeyCache.Protocol != ProtocolRedis || len(valkeyCache.InjectionMappings) != 3 {
		t.Fatalf("unexpected valkey cache preset: %+v", valkeyCache)
	}
}
