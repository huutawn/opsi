import type {
  ApplicationDependency,
  DependencyInjectionMapping,
  PreflightCheck,
  VerificationRun,
} from "@/lib/contracts/registry";

export type PostgresPreset = "DATABASE_URL" | "PG_CONVENTIONAL" | "CUSTOM";
export type ValkeyPreset = "REDIS_URL" | "CUSTOM";

export const POSTGRES_CONVENTIONAL_MAPPINGS: DependencyInjectionMapping[] = [
  { env_name: "PGHOST", symbolic_source: "endpoint.host" },
  { env_name: "PGPORT", symbolic_source: "endpoint.port" },
  { env_name: "PGDATABASE", symbolic_source: "database.name" },
  { env_name: "PGUSER", symbolic_source: "credential.username" },
  { env_name: "PGPASSWORD", symbolic_source: "credential.password" },
];

export const POSTGRES_DATABASE_URL_MAPPING = (envName = "APP_DATABASE_URL"): DependencyInjectionMapping[] => [
  { env_name: envName, symbolic_source: "connection.url" },
];

export const VALKEY_REDIS_URL_MAPPING = (envName = "APP_REDIS_URL"): DependencyInjectionMapping[] => [
  { env_name: envName, symbolic_source: "connection.url" },
];

export const SYMBOLIC_SOURCE_LABELS: Record<string, string> = {
  "connection.url": "Connection URL",
  "credential.password": "Credential password",
  "credential.username": "Credential username",
  "database.name": "Database name",
  "endpoint.host": "Host endpoint",
  "endpoint.port": "Port number",
  "internal.url": "Internal service URL",
  "public.url": "Public service URL",
};

export function formatSymbolicSource(source: string, targetType?: string): string {
  if (source === "credential.password") {
    const prefix = targetType === "postgres" ? "PostgreSQL" : targetType === "redis" ? "Valkey" : "Target";
    return `${prefix} password (symbolic reference)`;
  }
  if (source === "database.name") {
    return "Database name";
  }
  const label = SYMBOLIC_SOURCE_LABELS[source] || source;
  const prefix = targetType === "postgres" ? "PostgreSQL" : targetType === "redis" ? "Valkey" : "Target";
  return `${prefix} ${label.toLowerCase()}`;
}

export function getPresetMappings(
  protocol: "postgres" | "redis",
  preset: PostgresPreset | ValkeyPreset,
  customEnvName?: string
): DependencyInjectionMapping[] {
  if (protocol === "postgres") {
    if (preset === "DATABASE_URL")
      return [{ env_name: customEnvName || "DATABASE_URL", symbolic_source: "connection.url" }];
    if (preset === "PG_CONVENTIONAL") return [...POSTGRES_CONVENTIONAL_MAPPINGS];
  }
  if (protocol === "redis") {
    if (preset === "REDIS_URL")
      return [{ env_name: customEnvName || "REDIS_URL", symbolic_source: "connection.url" }];
  }
  return [];
}

export function validateStrategyMatrix(
  callerContext: "browser" | "server",
  strategy: "same_origin" | "internal_http" | "public_http"
): { valid: boolean; error?: string } {
  if (callerContext === "browser" && strategy === "internal_http") {
    return {
      valid: false,
      error: "Browser caller context cannot route to private cluster endpoints (internal_http). Choose same_origin or public_http.",
    };
  }
  if (callerContext === "server" && strategy === "same_origin") {
    return {
      valid: false,
      error: "same_origin is only valid for browser caller context. Choose internal_http or public_http.",
    };
  }
  return { valid: true };
}

export function detectPostgresPreset(mappings: DependencyInjectionMapping[] = []): PostgresPreset {
  if (mappings.length === 1 && (mappings[0].env_name === "DATABASE_URL" || mappings[0].env_name === "APP_DATABASE_URL") && mappings[0].symbolic_source === "connection.url") {
    return "DATABASE_URL";
  }
  const conventionalNames = new Set(["PGHOST", "PGPORT", "PGDATABASE", "PGUSER", "PGPASSWORD"]);
  if (mappings.length === 5 && mappings.every((m) => conventionalNames.has(m.env_name))) {
    return "PG_CONVENTIONAL";
  }
  return "CUSTOM";
}

export function detectValkeyPreset(mappings: DependencyInjectionMapping[] = []): ValkeyPreset {
  if (mappings.length === 1 && (mappings[0].env_name === "REDIS_URL" || mappings[0].env_name === "APP_REDIS_URL") && mappings[0].symbolic_source === "connection.url") {
    return "REDIS_URL";
  }
  return "CUSTOM";
}

export type EdgeState =
  | "Declared"
  | "Needs setup"
  | "Ready"
  | "Warning"
  | "Blocked"
  | "Verified"
  | "Partially verified"
  | "Failed"
  | "Stale";

export function getEdgeState(
  dependency: ApplicationDependency,
  hasBinding: boolean,
  preflightCheck?: PreflightCheck,
  verification?: VerificationRun | null
): EdgeState {
  if (verification) {
    if (verification.overall_status === "VERIFIED") return "Verified";
    if (verification.overall_status === "PARTIALLY_VERIFIED") return "Partially verified";
    if (verification.overall_status === "FAILED") return "Failed";
    if (verification.overall_status === "STALE") return "Stale";
  }
  if (preflightCheck) {
    if (preflightCheck.severity === "BLOCK") return "Blocked";
    if (preflightCheck.severity === "WARN") return "Warning";
  }
  if (dependency.target_kind === "managed_service" && !hasBinding) {
    return "Needs setup";
  }
  return "Ready";
}

export function calculateEdgeState(hasBinding: boolean, protocol: string, logicalName: string) {
  const isBound = hasBinding;
  const status = isBound ? "Ready" : "Needs setup";
  const protoLabel = protocol === "postgres" ? "PostgreSQL" : protocol === "redis" ? "Valkey" : "HTTP";
  return {
    status,
    strokeDasharray: isBound ? undefined : "6 4",
    label: `${protoLabel} · ${logicalName} · ${status}`,
  };
}
