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

	sameOrigin := SameOriginPreset("api", "app-api-1", "/api/v1", "API_PATH", true)
	if sameOrigin.TargetKind != TargetKindApplication || sameOrigin.Strategy != StrategySameOrigin || sameOrigin.AccessContext != AccessContextBrowser || sameOrigin.Path != "/api/v1" || len(sameOrigin.InjectionMappings) != 1 || sameOrigin.InjectionMappings[0].EnvName != "API_PATH" {
		t.Fatalf("unexpected same origin preset: %+v", sameOrigin)
	}

	internalHTTP := InternalHTTPPreset("api", "app-api-1", "API", true)
	if internalHTTP.TargetKind != TargetKindApplication || internalHTTP.Strategy != StrategyInternalHTTP || internalHTTP.AccessContext != AccessContextServer || len(internalHTTP.InjectionMappings) != 3 {
		t.Fatalf("unexpected internal HTTP preset: %+v", internalHTTP)
	}

	publicHTTP := PublicHTTPPreset("api", "app-api-1", AccessContextServer, "API_ENDPOINT", false)
	if publicHTTP.TargetKind != TargetKindApplication || publicHTTP.Strategy != StrategyPublicHTTP || publicHTTP.AccessContext != AccessContextServer || len(publicHTTP.InjectionMappings) != 1 || publicHTTP.InjectionMappings[0].EnvName != "API_ENDPOINT" {
		t.Fatalf("unexpected public HTTP preset: %+v", publicHTTP)
	}
}

