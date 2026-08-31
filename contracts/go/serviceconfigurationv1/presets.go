package serviceconfigurationv1

const (
	TargetKindApplication     = "application"
	TargetKindManagedResource = "managed_resource"

	InjectionPhaseRuntime = "runtime"
	InjectionPhaseBuild   = "build"

	StrategySameOrigin   = "same_origin"
	StrategyInternalHTTP = "internal_http"
	StrategyPublicHTTP   = "public_http"

	AccessContextServer  = "server"
	AccessContextBrowser = "browser"

	ProtocolHTTP     = "http"
	ProtocolPostgres = "postgres"
	ProtocolRedis    = "redis"
	ProtocolNATS     = "nats"

	SourceResourceHost       = "resource.host"
	SourceResourcePort       = "resource.port"
	SourceCredentialDatabase = "credential.database"
	SourceCredentialUsername = "credential.username"
	SourceCredentialPassword = "credential.password"
	SourceConnectionURL      = "connection.url"
	SourcePostgresURI        = "connection.postgres.uri"
	SourcePostgresNpgsql     = "connection.postgres.npgsql"
	SourcePostgresJDBC       = "connection.postgres.jdbc"
	SourcePostgresPDODSN     = "connection.postgres.pdo_dsn"
	SourceRedisURI           = "connection.redis.uri"
	SourceRedisStackExchange = "connection.redis.stackexchange"
	SourceNATSURI            = "connection.nats.uri"
	SourceConnectionTemplate = "connection.template"

	SourceApplicationInternalURL  = "application.internal_url"
	SourceApplicationInternalHost = "application.internal_host"
	SourceApplicationInternalPort = "application.internal_port"
	SourceApplicationPublicURL    = "application.public_url"
	SourceApplicationPublicHost   = "application.public_host"
	SourceApplicationPublicPort   = "application.public_port"
	SourceApplicationPublicScheme = "application.public_scheme"
	SourceApplicationPath         = "application.path"
	SourceApplicationURL          = "application.url"
)

// PostgresURLPreset creates an ApplicationDependency configured with a typed PostgreSQL URI.
func PostgresURLPreset(logicalName, targetIdentity string, required bool) ApplicationDependency {
	return ApplicationDependency{
		LogicalName:    logicalName,
		TargetKind:     TargetKindManagedResource,
		TargetIdentity: targetIdentity,
		Protocol:       ProtocolPostgres,
		Required:       required,
		InjectionPhase: InjectionPhaseRuntime,
		InjectionMappings: []DependencyInjectionMapping{
			{EnvName: "DATABASE_URL", SymbolicSource: SourcePostgresURI},
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

// ValkeyURLPreset creates an ApplicationDependency configured with a typed Redis URI.
func ValkeyURLPreset(logicalName, targetIdentity string, required bool) ApplicationDependency {
	return ApplicationDependency{
		LogicalName:    logicalName,
		TargetKind:     TargetKindManagedResource,
		TargetIdentity: targetIdentity,
		Protocol:       ProtocolRedis,
		Required:       required,
		InjectionPhase: InjectionPhaseRuntime,
		InjectionMappings: []DependencyInjectionMapping{
			{EnvName: "REDIS_URL", SymbolicSource: SourceRedisURI},
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

// SameOriginPreset creates an ApplicationDependency configured for browser same-origin routing.
func SameOriginPreset(logicalName, targetIdentity, path, envName string, required bool) ApplicationDependency {
	if path == "" {
		path = "/api"
	}
	if envName == "" {
		envName = "API_PATH"
	}
	return ApplicationDependency{
		LogicalName:    logicalName,
		TargetKind:     TargetKindApplication,
		TargetIdentity: targetIdentity,
		Protocol:       ProtocolHTTP,
		Strategy:       StrategySameOrigin,
		AccessContext:  AccessContextBrowser,
		Path:           path,
		Required:       required,
		InjectionPhase: InjectionPhaseRuntime,
		InjectionMappings: []DependencyInjectionMapping{
			{EnvName: envName, SymbolicSource: SourceApplicationPath},
		},
	}
}

// InternalHTTPPreset creates an ApplicationDependency configured for server-to-server internal HTTP communication.
func InternalHTTPPreset(logicalName, targetIdentity, envPrefix string, required bool) ApplicationDependency {
	if envPrefix == "" {
		envPrefix = "API"
	}
	return ApplicationDependency{
		LogicalName:    logicalName,
		TargetKind:     TargetKindApplication,
		TargetIdentity: targetIdentity,
		Protocol:       ProtocolHTTP,
		Strategy:       StrategyInternalHTTP,
		AccessContext:  AccessContextServer,
		Required:       required,
		InjectionPhase: InjectionPhaseRuntime,
		InjectionMappings: []DependencyInjectionMapping{
			{EnvName: envPrefix + "_URL", SymbolicSource: SourceApplicationInternalURL},
			{EnvName: envPrefix + "_HOST", SymbolicSource: SourceApplicationInternalHost},
			{EnvName: envPrefix + "_PORT", SymbolicSource: SourceApplicationInternalPort},
		},
	}
}

// PublicHTTPPreset creates an ApplicationDependency configured for accessing a public HTTP endpoint.
func PublicHTTPPreset(logicalName, targetIdentity, accessContext, envName string, required bool) ApplicationDependency {
	if accessContext == "" {
		accessContext = AccessContextServer
	}
	if envName == "" {
		envName = "PUBLIC_API_URL"
	}
	return ApplicationDependency{
		LogicalName:    logicalName,
		TargetKind:     TargetKindApplication,
		TargetIdentity: targetIdentity,
		Protocol:       ProtocolHTTP,
		Strategy:       StrategyPublicHTTP,
		AccessContext:  accessContext,
		Required:       required,
		InjectionPhase: InjectionPhaseRuntime,
		InjectionMappings: []DependencyInjectionMapping{
			{EnvName: envName, SymbolicSource: SourceApplicationPublicURL},
		},
	}
}
