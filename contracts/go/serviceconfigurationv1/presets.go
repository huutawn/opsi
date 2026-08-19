package serviceconfigurationv1

const (
	TargetKindApplication     = "application"
	TargetKindManagedResource = "managed_resource"

	InjectionPhaseRuntime = "runtime"
	InjectionPhaseBuild   = "build"

	ProtocolPostgres = "postgres"
	ProtocolRedis    = "redis"
	ProtocolNATS     = "nats"

	SourceResourceHost       = "resource.host"
	SourceResourcePort       = "resource.port"
	SourceCredentialDatabase = "credential.database"
	SourceCredentialUsername = "credential.username"
	SourceCredentialPassword = "credential.password"
	SourceConnectionURL      = "connection.url"
)

// PostgresURLPreset creates an ApplicationDependency configured with DATABASE_URL <- connection.url.
func PostgresURLPreset(logicalName, targetIdentity string, required bool) ApplicationDependency {
	return ApplicationDependency{
		LogicalName:    logicalName,
		TargetKind:     TargetKindManagedResource,
		TargetIdentity: targetIdentity,
		Protocol:       ProtocolPostgres,
		Required:       required,
		InjectionPhase: InjectionPhaseRuntime,
		InjectionMappings: []DependencyInjectionMapping{
			{EnvName: "DATABASE_URL", SymbolicSource: SourceConnectionURL},
		},
	}
}

// PostgresStandardPreset creates an ApplicationDependency configured with PG* environment mappings.
func PostgresStandardPreset(logicalName, targetIdentity string, required bool) ApplicationDependency {
	return ApplicationDependency{
		LogicalName:    logicalName,
		TargetKind:     TargetKindManagedResource,
		TargetIdentity: targetIdentity,
		Protocol:       ProtocolPostgres,
		Required:       required,
		InjectionPhase: InjectionPhaseRuntime,
		InjectionMappings: []DependencyInjectionMapping{
			{EnvName: "PGDATABASE", SymbolicSource: SourceCredentialDatabase},
			{EnvName: "PGHOST", SymbolicSource: SourceResourceHost},
			{EnvName: "PGPASSWORD", SymbolicSource: SourceCredentialPassword},
			{EnvName: "PGPORT", SymbolicSource: SourceResourcePort},
			{EnvName: "PGUSER", SymbolicSource: SourceCredentialUsername},
		},
	}
}

// ValkeyURLPreset creates an ApplicationDependency configured with REDIS_URL <- connection.url.
func ValkeyURLPreset(logicalName, targetIdentity string, required bool) ApplicationDependency {
	return ApplicationDependency{
		LogicalName:    logicalName,
		TargetKind:     TargetKindManagedResource,
		TargetIdentity: targetIdentity,
		Protocol:       ProtocolRedis,
		Required:       required,
		InjectionPhase: InjectionPhaseRuntime,
		InjectionMappings: []DependencyInjectionMapping{
			{EnvName: "REDIS_URL", SymbolicSource: SourceConnectionURL},
		},
	}
}

// ValkeyCachePreset creates an ApplicationDependency configured with CACHE_* environment mappings.
func ValkeyCachePreset(logicalName, targetIdentity string, required bool) ApplicationDependency {
	return ApplicationDependency{
		LogicalName:    logicalName,
		TargetKind:     TargetKindManagedResource,
		TargetIdentity: targetIdentity,
		Protocol:       ProtocolRedis,
		Required:       required,
		InjectionPhase: InjectionPhaseRuntime,
		InjectionMappings: []DependencyInjectionMapping{
			{EnvName: "CACHE_HOST", SymbolicSource: SourceResourceHost},
			{EnvName: "CACHE_PASSWORD", SymbolicSource: SourceCredentialPassword},
			{EnvName: "CACHE_PORT", SymbolicSource: SourceResourcePort},
		},
	}
}
