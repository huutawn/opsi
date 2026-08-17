import type { AuditEvent, BreakGlassPolicy, ResourceBinding, SupportSummary } from "@/lib/contracts/registry";
import type { LocalSessionStatus } from "@/lib/api/local-client";
import type { ConsoleRoute } from "@/features/console/navigation";

export type AuditEventCategory =
  | "access"
  | "server"
  | "application"
  | "build"
  | "deployment"
  | "managed_resource"
  | "dr_backup_restore"
  | "cutover"
  | "storage"
  | "security";

export type ActorClassification = {
  kind: "human" | "machine" | "agent" | "worker" | "github_actions" | "cloud" | "unknown";
  label: string;
  identifier: string;
  isMachine: boolean;
  isHuman: boolean;
};

export type AuditResultOutcome = "succeeded" | "requested" | "failed" | "denied";

export type AuditRow = {
  id: string;
  timestamp: string;
  formattedTime: string;
  action: string;
  actionLabel: string;
  category: AuditEventCategory;
  categoryLabel: string;
  actor: ActorClassification;
  targetType: string;
  targetID: string;
  targetDisplay: string;
  outcome: AuditResultOutcome;
  isHighImpact: boolean;
  impactReason: string;
  metadata: Record<string, unknown>;
  safeMetadataEntries: Array<[string, string]>;
  requestID: string;
  crossLink: { label: string; route: Partial<ConsoleRoute> } | null;
};

export type SecuritySummary = {
  totalLoadedEvents: number;
  deniedEventsCount: number;
  failedEventsCount: number;
  highImpactEventsCount: number;
  recentDeniedEvents: AuditRow[];
  recentHighImpactEvents: AuditRow[];
  recentSecurityEvents: AuditRow[];
  scopedRoleSafety: Array<{ attribute: string; safe: boolean; description: string }>;
  breakGlassFacts: BreakGlassPolicy | null;
};

const SENSITIVE_KEY_PATTERNS = [
  /password/i,
  /secret/i,
  /token/i,
  /authorization/i,
  /cookie/i,
  /bearer/i,
  /private_?key/i,
  /database_?url/i,
  /registry_?auth/i,
  /registry_?password/i,
  /lease_?token/i,
  /review_?token/i,
  /cutover_?token/i,
  /ssh_?password/i,
  /ssh_?private_?key/i,
  /kubeconfig/i,
  /app_?secret/i,
  /\bpat\b/i,
];

const CREDENTIAL_URL_PATTERN = /(https?|postgres|postgresql|mysql|redis|nats):\/\/([^:]+):([^@]+)@/i;

const HIGH_IMPACT_ACTIONS: Record<string, string> = {
  SERVER_REMOVED: "Removes server from infrastructure capacity",
  NODE_LIFECYCLE_REQUESTED: "Server lifecycle modification (drain / remove)",
  NODE_LIFECYCLE_REQUEST_REJECTED: "Rejected server lifecycle request",
  NODE_MARKED_OFFLINE: "Server marked offline (operator confirmed reset)",
  RESOURCE_DELETED: "Destructive managed resource deletion",
  RESOURCE_BINDING_REVOKED: "Revokes application database credentials",
  RETAINED_STORAGE_DESTROY_REQUESTED: "Storage destruction requested",
  RETAINED_STORAGE_DESTROYED: "Irreversible persistent storage destruction",
  CUTOVER_FINALIZE_REQUESTED: "Cutover finalization requested (source revocation)",
  CUTOVER_FINALIZED: "Irreversible source database credential revocation",
  CUTOVER_ROLLBACK_REQUESTED: "Cutover rollback requested",
  CUTOVER_ROLLBACK_SUCCEEDED: "Database cutover rolled back to source",
  DEPLOYMENT_ROLLBACK_STARTED: "Deployment rollback to previous revision",
  DEPLOYMENT_ROLLBACK_COMPLETED: "Deployment rolled back to verified revision",
  BOOTSTRAP_MANUAL_RETRY_REQUESTED: "Manual server bootstrap retry",
  AGENT_REVOKED: "Agent credentials revoked",
  AGENT_CREDENTIAL_ROTATED: "Agent TLS/signature credentials rotated",
  PAT_REVOKED: "Personal access token revoked",
  PAT_ROTATED: "Personal access token rotated",
  SECRET_REVEALED: "Sensitive secret value revealed under review",
  SECRET_ROTATED: "Secret payload rotated",
};

export function isHighImpactAction(action: string): { highImpact: boolean; reason: string } {
  const upper = (action ?? "").toUpperCase();
  const direct = HIGH_IMPACT_ACTIONS[upper];
  if (direct) return { highImpact: true, reason: direct };

  if (
    upper.includes("DESTROY") ||
    upper.includes("ROLLBACK") ||
    upper.includes("REVOKE") ||
    upper.includes("CUTOVER_FINALIZE") ||
    upper.includes("CUTOVER_FINALIZED") ||
    upper.includes("DELETE") ||
    upper.includes("OFFLINE") ||
    upper.includes("REMOVE")
  ) {
    return { highImpact: true, reason: "High-impact destructive or rollback operation" };
  }
  return { highImpact: false, reason: "" };
}

export function categorizeAuditEvent(action: string, resourceType?: string): { category: AuditEventCategory; label: string } {
  const upper = action.toUpperCase();
  const res = (resourceType ?? "").toLowerCase();

  if (upper.startsWith("ADMIN_BOOTSTRAP") || upper.startsWith("AUTH_") || upper.startsWith("PAT_") || upper.includes("RBAC_") || upper.includes("OTP_") || upper.includes("OAUTH")) {
    return { category: "access", label: "Access & Auth" };
  }
  if (upper.startsWith("BOOTSTRAP_") || upper.startsWith("NODE_") || upper.startsWith("SERVER_") || upper.startsWith("AGENT_") || res === "node" || res === "bootstrap_session" || res === "agent") {
    return { category: "server", label: "Server & Node" };
  }
  if (upper.startsWith("BUILD_") || res === "build_record" || res === "build_job") {
    return { category: "build", label: "Build & Provenance" };
  }
  if (upper.startsWith("DEPLOYMENT_") || upper.startsWith("EXPOSURE_") || res === "deployment_job" || res === "deployment_policy") {
    return { category: "deployment", label: "Deployment & Rollback" };
  }
  if (upper.startsWith("CUTOVER_") || res.includes("cutover")) {
    return { category: "cutover", label: "Cutover & Migration" };
  }
  if (upper.startsWith("BACKUP_") || upper.startsWith("RESTORE_") || res === "backup" || res === "restore" || res === "restore_review") {
    return { category: "dr_backup_restore", label: "Backup & Restore" };
  }
  if (upper.startsWith("RETAINED_STORAGE_") || res === "retained_storage" || res === "storage") {
    return { category: "storage", label: "Persistent Storage" };
  }
  if (upper.startsWith("RESOURCE_BINDING_") || res === "resource_binding") {
    return { category: "managed_resource", label: "Resource Binding" };
  }
  if (upper.startsWith("RESOURCE_") || res === "resource" || res === "managed_service") {
    return { category: "managed_resource", label: "Managed Resource" };
  }
  if (upper.startsWith("SERVICE_") || upper.startsWith("GITHUB_") || res === "service" || res === "github_binding") {
    return { category: "application", label: "Application & Source" };
  }
  if (upper.startsWith("SECRET_") || upper.startsWith("DR_SECRET_") || res === "secret") {
    return { category: "security", label: "Security & Secrets" };
  }

  return { category: "security", label: "Security & Audit" };
}

export function classifyActor(actorType?: string, actorUserID?: string): ActorClassification {
  const type = (actorType ?? "").trim().toLowerCase();
  const userID = (actorUserID ?? "").trim();

  if (["human", "user", "operator"].includes(type)) {
    return {
      kind: "human",
      label: "Human actor",
      identifier: userID || "authenticated user",
      isMachine: false,
      isHuman: true,
    };
  }

  if (["agent"].includes(type)) {
    return {
      kind: "agent",
      label: "Agent",
      identifier: userID || "node agent",
      isMachine: true,
      isHuman: false,
    };
  }

  if (["worker", "bootstrap_worker"].includes(type)) {
    return {
      kind: "worker",
      label: "Worker automation",
      identifier: userID || "cloud worker",
      isMachine: true,
      isHuman: false,
    };
  }

  if (["github_actions", "actions"].includes(type)) {
    return {
      kind: "github_actions",
      label: "GitHub Actions",
      identifier: userID || "OIDC workflow",
      isMachine: true,
      isHuman: false,
    };
  }

  if (["cloud", "control_plane"].includes(type)) {
    return {
      kind: "cloud",
      label: "Cloud control plane",
      identifier: userID || "cloud reconciler",
      isMachine: true,
      isHuman: false,
    };
  }

  if (["machine", "system", "automation"].includes(type)) {
    return {
      kind: "machine",
      label: "Machine actor",
      identifier: userID || "system reconciler",
      isMachine: true,
      isHuman: false,
    };
  }

  if (userID) {
    return {
      kind: "human",
      label: "Authenticated actor",
      identifier: userID,
      isMachine: false,
      isHuman: true,
    };
  }

  return {
    kind: "unknown",
    label: "Unknown actor",
    identifier: "unclassified",
    isMachine: false,
    isHuman: false,
  };
}

export function classifyResult(result: string, action?: string): AuditResultOutcome {
  const normResult = (result ?? "").trim().toLowerCase();
  const normAction = (action ?? "").trim().toUpperCase();

  if (normResult === "denied" || normResult === "rejected" || normAction.includes("REJECTED") || normAction.includes("DENIED")) {
    return "denied";
  }
  if (normResult === "failed" || normResult === "failure" || normAction.includes("FAILED")) {
    return "failed";
  }
  if (normAction.endsWith("_REQUESTED") || normAction.endsWith("_STARTED") || normAction.endsWith("_PENDING")) {
    return "requested";
  }
  if (normResult === "success" || normResult === "succeeded" || normResult === "ok") {
    return "succeeded";
  }
  if (normResult === "requested" || normResult === "queued" || normResult === "pending") {
    return "requested";
  }
  return "succeeded";
}

export function isSensitiveKey(key: string): boolean {
  return SENSITIVE_KEY_PATTERNS.some((pattern) => pattern.test(key));
}

export function redactSensitiveString(value: string): string {
  if (CREDENTIAL_URL_PATTERN.test(value)) {
    return value.replace(CREDENTIAL_URL_PATTERN, "$1://$2:[REDACTED]@");
  }
  if (value.includes("BEGIN OPENSSH PRIVATE KEY") || value.includes("BEGIN RSA PRIVATE KEY") || value.includes("BEGIN PRIVATE KEY")) {
    return "[PRIVATE KEY REDACTED]";
  }
  return value;
}

export function sanitizeMetadataValue(value: unknown): unknown {
  if (value === null || value === undefined) return null;
  if (typeof value === "string") return redactSensitiveString(value);
  if (typeof value === "number" || typeof value === "boolean") return value;
  if (Array.isArray(value)) {
    return value.slice(0, 10).map((item) => sanitizeMetadataValue(item));
  }
  if (typeof value === "object") {
    const safeObj: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(value as Record<string, unknown>)) {
      if (isSensitiveKey(k)) continue;
      safeObj[k] = sanitizeMetadataValue(v);
    }
    return safeObj;
  }
  return "[Redacted]";
}

export function safeAuditMetadata(metadata?: Record<string, unknown>): Array<[string, string]> {
  if (!metadata || typeof metadata !== "object") return [];
  const entries: Array<[string, string]> = [];

  for (const [key, rawValue] of Object.entries(metadata)) {
    if (isSensitiveKey(key)) continue;
    const sanitized = sanitizeMetadataValue(rawValue);
    if (sanitized === null || sanitized === undefined) continue;

    let displayString = "";
    if (typeof sanitized === "string" || typeof sanitized === "number" || typeof sanitized === "boolean") {
      displayString = String(sanitized);
    } else if (Array.isArray(sanitized)) {
      displayString = sanitized.slice(0, 8).map((v) => (typeof v === "object" ? JSON.stringify(v) : String(v))).join(", ");
    } else if (typeof sanitized === "object") {
      displayString = JSON.stringify(sanitized);
    } else {
      displayString = "Structured value";
    }

    if (displayString) {
      entries.push([key, displayString]);
    }
    if (entries.length >= 16) break;
  }

  return entries;
}

export function deriveAuditCrossLinks(event: AuditEvent): { label: string; route: Partial<ConsoleRoute> } | null {
  const resourceType = (event.resource_type ?? "").toLowerCase();
  const resourceID = (event.resource_id ?? "").trim();
  const metadata = event.metadata_redacted ?? {};

  if (resourceType === "service" || resourceType === "application") {
    return { label: "Open Application", route: { view: "services", service: resourceID } };
  }
  if (resourceType === "build_record" || resourceType === "build_job") {
    return { label: "Open Build in Delivery", route: { view: "delivery", tab: "builds", build: resourceID } };
  }
  if (resourceType === "deployment_job" || resourceType === "deployment") {
    return { label: "Open Deployment in Delivery", route: { view: "delivery", tab: "deployments", deployment: resourceID } };
  }
  if (resourceType === "node" || resourceType === "server") {
    return { label: "Open Server in Infrastructure", route: { view: "infrastructure", tab: "servers", server: resourceID } };
  }
  if (resourceType === "bootstrap_session") {
    return { label: "Open Server Bootstrap", route: { view: "infrastructure", tab: "servers", session: resourceID } };
  }
  if (resourceType === "resource" || resourceType === "managed_service" || resourceType === "resource_binding") {
    const resID = (metadata.resource_id as string) || (metadata.target_resource_id as string) || resourceID;
    return { label: "Open Resource in Infrastructure", route: { view: "infrastructure", tab: "resources", resource: resID } };
  }
  if (resourceType === "retained_storage") {
    return { label: "Open Retained Storage", route: { view: "infrastructure", tab: "storage", storage: resourceID } };
  }
  if (resourceType === "backup" || resourceType === "restore" || resourceType === "restore_review") {
    return { label: "Open Managed Resources", route: { view: "infrastructure", tab: "resources" } };
  }
  if (metadata.service_id && typeof metadata.service_id === "string") {
    return { label: "Open Application", route: { view: "services", service: metadata.service_id } };
  }

  return null;
}

export function formatAuditActionName(action: string): string {
  if (!action) return "Unknown action";
  return action
    .replace(/_/g, " ")
    .toLowerCase()
    .replace(/\b\w/g, (c) => c.toUpperCase());
}

export function deriveAuditRow(event: AuditEvent): AuditRow {
  const { category, label: categoryLabel } = categorizeAuditEvent(event.action, event.resource_type);
  const actor = classifyActor(event.actor_type, event.actor_user_id);
  const outcome = classifyResult(event.result, event.action);
  const { highImpact, reason: impactReason } = isHighImpactAction(event.action);
  const safeMetadataEntries = safeAuditMetadata(event.metadata_redacted);
  const requestID = String(event.metadata_redacted?.request_id ?? event.metadata_redacted?.correlation_id ?? "");
  const crossLink = deriveAuditCrossLinks(event);

  let targetDisplay = `${event.resource_type || "resource"}/${event.resource_id || "unspecified"}`;
  if (event.metadata_redacted?.repository && typeof event.metadata_redacted.repository === "string") {
    targetDisplay = `${event.metadata_redacted.repository} (${targetDisplay})`;
  } else if (event.metadata_redacted?.service_id && typeof event.metadata_redacted.service_id === "string") {
    targetDisplay = `${event.metadata_redacted.service_id} · ${targetDisplay}`;
  }

  return {
    id: event.id,
    timestamp: event.created_at,
    formattedTime: formatTimestamp(event.created_at),
    action: event.action,
    actionLabel: formatAuditActionName(event.action),
    category,
    categoryLabel,
    actor,
    targetType: event.resource_type || "resource",
    targetID: event.resource_id || "",
    targetDisplay,
    outcome,
    isHighImpact: highImpact,
    impactReason,
    metadata: event.metadata_redacted ?? {},
    safeMetadataEntries,
    requestID,
    crossLink,
  };
}

export function deriveSecuritySummary(
  events: AuditEvent[],
  session?: LocalSessionStatus | null,
  bindings?: ResourceBinding[],
  support?: SupportSummary | null,
): SecuritySummary {
  const rows = events.map(deriveAuditRow);
  const deniedEvents = rows.filter((r) => r.outcome === "denied");
  const failedEvents = rows.filter((r) => r.outcome === "failed");
  const highImpactEvents = rows.filter((r) => r.isHighImpact);
  const securityEvents = rows.filter((r) => r.category === "security" || r.category === "access");

  const scopedRoleSafety = [
    { attribute: "LOGIN", safe: true, description: "Role can authenticate via scoped password." },
    { attribute: "NOSUPERUSER", safe: true, description: "Cannot bypass database permissions or access system tables." },
    { attribute: "NOCREATEDB", safe: true, description: "Cannot create or drop top-level database instances." },
    { attribute: "NOCREATEROLE", safe: true, description: "Cannot create, alter, or elevate PostgreSQL user accounts." },
    { attribute: "NOREPLICATION", safe: true, description: "Cannot initiate WAL streaming or bypass physical barriers." },
    { attribute: "NOBYPASSRLS", safe: true, description: "Row-level security policies remain strictly enforced." },
  ];

  return {
    totalLoadedEvents: rows.length,
    deniedEventsCount: deniedEvents.length,
    failedEventsCount: failedEvents.length,
    highImpactEventsCount: highImpactEvents.length,
    recentDeniedEvents: deniedEvents.slice(0, 5),
    recentHighImpactEvents: highImpactEvents.slice(0, 5),
    recentSecurityEvents: securityEvents.slice(0, 5),
    scopedRoleSafety,
    breakGlassFacts: support?.break_glass_policy ?? null,
  };
}

export function formatTimestamp(iso: string): string {
  if (!iso) return "Unknown";
  const parsed = Date.parse(iso);
  if (Number.isNaN(parsed)) return iso;
  const date = new Date(parsed);
  return date.toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  });
}

export function mapSecurityError(code: string, message?: string): { code: string; message: string; action: string } {
  switch (code) {
    case "AUTHENTICATION_REQUIRED":
    case "AUTH_REQUIRED":
    case "CLOUD_AUTH_REQUIRED":
      return {
        code,
        message: "Your session requires authentication.",
        action: "Sign in with GitHub in Settings or Workspace to restore access.",
      };
    case "FORBIDDEN":
    case "PERMISSION_DENIED":
    case "RBAC_DENIED":
      return {
        code,
        message: "You do not have permission to perform this action.",
        action: "Contact a project Owner or Admin to verify your assigned role.",
      };
    case "BOOTSTRAP_RETRY_FORBIDDEN":
      return {
        code,
        message: "Only Owner or Admin can retry server bootstrap.",
        action: "Request an Owner or Admin to retry this server bootstrap session.",
      };
    case "RESOURCE_BINDING_ACTIVE":
      return {
        code,
        message: "Active Resource Binding exists for this target.",
        action: "Revoke or disconnect the binding before deleting the resource.",
      };
    case "CUTOVER_FINALIZED":
      return {
        code,
        message: "Cutover is already finalized and source credentials are revoked.",
        action: "Finalized cutovers are immutable. Create a new target review if further migration is needed.",
      };
    case "RETAINED_STORAGE_ACTIVE_REFERENCE":
      return {
        code,
        message: "Storage volume has active resource references.",
        action: "Ensure no active workload references this persistent volume before destruction.",
      };
    default:
      return {
        code: code || "SECURITY_OPERATION_ERROR",
        message: message || "A security or authorization constraint prevented this operation.",
        action: "Check project permissions and review current authority state.",
      };
  }
}
