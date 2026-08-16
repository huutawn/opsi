import type {
  Backup,
  CreateResourceRequest,
  Resource,
  ResourceBinding,
  RetainedStorage,
  Restore,
  ApplicationCutover,
  ApplicationCutoverReview,
  ApplicationCutoverRollback,
  ApplicationCutoverFinalization,
} from "../../contracts/registry";

export type ResourceCatalogItem = {
  type: "postgres" | "redis" | "nats";
  name: string;
  displayName: string;
  description: string;
  category: "database" | "cache" | "messaging";
  defaultPort: number;
  protocol: string;
  storageRequired: boolean;
  stateful: boolean;
  defaultCPUMillicores: number;
  defaultMemoryBytes: number;
  defaultStorageBytes?: number;
};

export const POSTGRES_VERSION = "16";
export const POSTGRES_STORAGE_POLICY = "standard";

export function resourceTypeCatalog(): ResourceCatalogItem[] {
  return [
    {
      type: "postgres",
      name: "postgresql",
      displayName: "PostgreSQL 16",
      description: "Dedicated relational database instance with persistent volume storage.",
      category: "database",
      defaultPort: 5432,
      protocol: "postgres",
      storageRequired: true,
      stateful: true,
      defaultCPUMillicores: 500,
      defaultMemoryBytes: 512 * 1024 * 1024,
      defaultStorageBytes: 10 * 1024 * 1024 * 1024,
    },
    {
      type: "redis",
      name: "valkey",
      displayName: "Valkey / Redis",
      description: "High-performance in-memory key-value cache and data structure store.",
      category: "cache",
      defaultPort: 6379,
      protocol: "redis",
      storageRequired: false,
      stateful: false,
      defaultCPUMillicores: 250,
      defaultMemoryBytes: 256 * 1024 * 1024,
    },
    {
      type: "nats",
      name: "nats",
      displayName: "NATS Messaging",
      description: "Lightweight, secure, and performant pub/sub message broker.",
      category: "messaging",
      defaultPort: 4222,
      protocol: "nats",
      storageRequired: false,
      stateful: false,
      defaultCPUMillicores: 250,
      defaultMemoryBytes: 256 * 1024 * 1024,
    },
  ];
}

export function resourceCatalogItem(type: string): ResourceCatalogItem | undefined {
  const norm = (type || "").toLowerCase();
  return resourceTypeCatalog().find((item) => item.type === norm || item.name === norm);
}

export function isStatefulResource(type: string): boolean {
  const norm = (type || "").toLowerCase();
  return norm === "postgres" || norm === "postgresql";
}

export type LifecyclePresentation = {
  label: string;
  tone: "ready" | "warning" | "failed" | "neutral" | "bootstrapping";
  statusValue: string;
};

export function resourceLifecyclePresentation(lifecycle: string): LifecyclePresentation {
  const norm = (lifecycle || "").toLowerCase();
  switch (norm) {
    case "ready":
      return { label: "Ready", tone: "ready", statusValue: "ready" };
    case "provisioning":
      return { label: "Provisioning", tone: "bootstrapping", statusValue: "bootstrapping" };
    case "updating":
      return { label: "Updating", tone: "bootstrapping", statusValue: "bootstrapping" };
    case "deleting":
      return { label: "Deleting", tone: "warning", statusValue: "warning" };
    case "failed":
      return { label: "Failed", tone: "failed", statusValue: "failed" };
    case "degraded":
      return { label: "Degraded", tone: "warning", statusValue: "warning" };
    case "unplaced":
      return { label: "Unplaced", tone: "neutral", statusValue: "idle" };
    case "planned":
      return { label: "Planned", tone: "neutral", statusValue: "idle" };
    case "configured":
      return { label: "Configured", tone: "neutral", statusValue: "idle" };
    default:
      return { label: norm ? norm[0].toUpperCase() + norm.slice(1) : "Unknown", tone: "neutral", statusValue: "unknown" };
  }
}

export function serverLifecyclePresentation(status: string): LifecyclePresentation {
  switch (status) {
    case "Ready":
      return { label: "Ready", tone: "ready", statusValue: "ready" };
    case "Connecting":
    case "Bootstrapping":
      return { label: status, tone: "bootstrapping", statusValue: "bootstrapping" };
    case "Waiting":
      return { label: "Waiting", tone: "neutral", statusValue: "idle" };
    case "Offline":
      return { label: "Offline", tone: "warning", statusValue: "offline" };
    case "Failed":
      return { label: "Failed", tone: "failed", statusValue: "failed" };
    default:
      return { label: "Unknown", tone: "neutral", statusValue: "unknown" };
  }
}

export function retainedStorageLifecyclePresentation(lifecycle: string): LifecyclePresentation {
  const norm = (lifecycle || "").toLowerCase();
  switch (norm) {
    case "retained":
      return { label: "Retained", tone: "warning", statusValue: "warning" };
    case "destroying":
      return { label: "Destroying", tone: "bootstrapping", statusValue: "bootstrapping" };
    case "destroyed":
      return { label: "Destroyed", tone: "neutral", statusValue: "idle" };
    case "destroy_failed":
      return { label: "Destroy Failed", tone: "failed", statusValue: "failed" };
    default:
      return { label: "Unknown", tone: "neutral", statusValue: "unknown" };
  }
}

export function backupLifecyclePresentation(lifecycle: string): LifecyclePresentation {
  const norm = (lifecycle || "").toLowerCase();
  switch (norm) {
    case "succeeded":
      return { label: "Succeeded", tone: "ready", statusValue: "ready" };
    case "running":
    case "leased":
      return { label: "Running", tone: "bootstrapping", statusValue: "bootstrapping" };
    case "queued":
      return { label: "Queued", tone: "neutral", statusValue: "idle" };
    case "failed":
      return { label: "Failed", tone: "failed", statusValue: "failed" };
    default:
      return { label: norm ? norm[0].toUpperCase() + norm.slice(1) : "Unknown", tone: "neutral", statusValue: "unknown" };
  }
}

export function restoreLifecyclePresentation(lifecycle: string): LifecyclePresentation {
  const norm = (lifecycle || "").toLowerCase();
  switch (norm) {
    case "succeeded":
      return { label: "Succeeded", tone: "ready", statusValue: "ready" };
    case "running":
    case "leased":
      return { label: "Restoring", tone: "bootstrapping", statusValue: "bootstrapping" };
    case "verifying":
      return { label: "Verifying", tone: "bootstrapping", statusValue: "bootstrapping" };
    case "queued":
      return { label: "Queued", tone: "neutral", statusValue: "idle" };
    case "failed":
      return { label: "Failed", tone: "failed", statusValue: "failed" };
    default:
      return { label: norm ? norm[0].toUpperCase() + norm.slice(1) : "Unknown", tone: "neutral", statusValue: "unknown" };
  }
}

export function cutoverLifecyclePresentation(lifecycle: string): LifecyclePresentation {
  const norm = (lifecycle || "").toLowerCase();
  switch (norm) {
    case "succeeded":
      return { label: "Succeeded", tone: "ready", statusValue: "ready" };
    case "applying":
    case "deploying":
    case "validating":
    case "verifying":
    case "revoking_source_binding":
      return { label: norm ? norm[0].toUpperCase() + norm.slice(1).replace(/_/g, " ") : "In Progress", tone: "bootstrapping", statusValue: "bootstrapping" };
    case "queued":
      return { label: "Queued", tone: "neutral", statusValue: "idle" };
    case "failed":
      return { label: "Failed", tone: "failed", statusValue: "failed" };
    default:
      return { label: norm ? norm[0].toUpperCase() + norm.slice(1) : "Unknown", tone: "neutral", statusValue: "unknown" };
  }
}

export const ERROR_MAPPINGS: Record<string, { summary: string; action: string }> = {
  RESOURCE_BINDING_ACTIVE: {
    summary: "Resource is still connected to one or more Applications.",
    action: "Disconnect all connected applications in the Connections tab before deleting this resource.",
  },
  RESOURCE_ACTIVE_OPERATION_CONFLICT: {
    summary: "Resource has an ongoing active operation (such as backup or restore).",
    action: "Wait for the active operation to finish before modifying or deleting this resource.",
  },
  RESOURCE_NOT_FOUND: {
    summary: "Managed resource was not found.",
    action: "Verify the resource exists in the current project and environment.",
  },
  RESOURCE_NAME_INVALID: {
    summary: "Resource name is invalid.",
    action: "Provide a valid name containing lowercase alphanumeric characters and hyphens.",
  },
  RESOURCE_TYPE_UNSUPPORTED: {
    summary: "Selected resource type is unsupported for provisioning.",
    action: "Select a supported resource type (PostgreSQL, Valkey, or NATS).",
  },
  BACKUP_RESOURCE_NOT_READY: {
    summary: "PostgreSQL resource is not in Ready state.",
    action: "Wait until the PostgreSQL instance reaches Ready state before requesting a backup.",
  },
  BACKUP_ALREADY_RUNNING: {
    summary: "A backup is already running for this resource.",
    action: "Wait for the current backup to complete.",
  },
  RESTORE_TARGET_NOT_EMPTY: {
    summary: "Target PostgreSQL database already contains tables or user objects.",
    action: "Restore requires a pristine, empty PostgreSQL resource. Choose or create a newly provisioned instance.",
  },
  RESTORE_TARGET_NOT_READY: {
    summary: "Target PostgreSQL resource is not in Ready state.",
    action: "Wait until the target resource reaches Ready state before attempting restore.",
  },
  RESTORE_TARGET_HAS_BINDINGS: {
    summary: "Target PostgreSQL resource already has connected applications.",
    action: "Target must have no active application bindings prior to restore.",
  },
  RESTORE_BACKUP_NOT_READY: {
    summary: "Selected backup is not in Succeeded state.",
    action: "Select a completed, verified backup.",
  },
  RESTORE_STALE_REVIEW: {
    summary: "Restore review is stale or target database state changed.",
    action: "Re-run the restore review before applying.",
  },
  RESTORE_ALREADY_RUNNING: {
    summary: "A restore operation is already running for this target.",
    action: "Wait for the active restore operation to complete.",
  },
  CUTOVER_STALE_REVIEW: {
    summary: "Cutover review is stale. Configuration or bindings have changed since review.",
    action: "Re-run Cutover Review to validate current preflight conditions.",
  },
  CUTOVER_REVIEW_NOT_READY: {
    summary: "Cutover review did not pass preflight checks.",
    action: "Inspect review preflight errors before proceeding.",
  },
  CUTOVER_ALREADY_RUNNING: {
    summary: "A cutover operation is already running for this application.",
    action: "Wait for the active cutover operation to finish.",
  },
  CUTOVER_TARGET_NOT_READY: {
    summary: "Target PostgreSQL resource is not in Ready state.",
    action: "Ensure target instance is healthy and running before applying cutover.",
  },
  CUTOVER_SOURCE_NOT_READY: {
    summary: "Source PostgreSQL resource is not in Ready state.",
    action: "Ensure source instance is reachable to preserve rollback safety.",
  },
  ROLLBACK_CUTOVER_INELIGIBLE: {
    summary: "Cutover is ineligible for rollback.",
    action: "Rollback is only permitted while the Cutover has succeeded and before Finalize is applied.",
  },
  CUTOVER_FINALIZED: {
    summary: "Cutover has already been finalized.",
    action: "Rollback capability was closed upon finalization. Source binding has been revoked.",
  },
  RETAINED_STORAGE_STALE_REVIEW: {
    summary: "Storage destruction review expired or was invalidated.",
    action: "Re-run the storage destruction review before confirming.",
  },
  RETAINED_STORAGE_ACTIVE_REFERENCE: {
    summary: "Retained storage is still referenced by an active resource or binding.",
    action: "Ensure no active runtime references this storage before requesting destruction.",
  },
  RETAINED_STORAGE_NOT_FOUND: {
    summary: "Retained storage record was not found.",
    action: "Verify the retained storage ID exists in this project.",
  },
};

export function resourceErrorExplanation(errorCode: string, defaultMessage?: string): { summary: string; action: string } {
  if (errorCode && ERROR_MAPPINGS[errorCode]) {
    return ERROR_MAPPINGS[errorCode];
  }
  return {
    summary: defaultMessage || (errorCode ? `Operation failed (${errorCode}).` : "Operation failed."),
    action: "Retry after verifying resource status and connectivity.",
  };
}

export function cutoverWarningExplanation(warningCode: string): string {
  switch (warningCode) {
    case "TARGET_NOT_CONTINUOUSLY_SYNCHRONIZED":
      return "Target is a point-in-time restored database and may be behind Source. It is not continuously synchronized.";
    case "BACKUP_AGE_NONZERO":
      return "Backup point-in-time was taken prior to current time; writes on source after backup are not present on target.";
    case "TARGET_WRITES_MAY_NOT_EXIST_ON_SOURCE":
      return "Rollback switches Application back to SOURCE. It does not synchronize TARGET writes back into SOURCE.";
    default:
      return warningCode;
  }
}

export function formatBytes(bytes?: number): string {
  if (bytes === undefined || bytes === null || isNaN(bytes)) return "Not reported";
  if (bytes === 0) return "0 B";
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let val = bytes;
  let unitIndex = 0;
  while (val >= 1024 && unitIndex < units.length - 1) {
    val /= 1024;
    unitIndex++;
  }
  return `${val % 1 === 0 || val >= 100 || unitIndex === 0 ? Math.round(val) : val.toFixed(1)} ${units[unitIndex]}`;
}

export function canCreateBackup(resource: Resource): boolean {
  return resource.type === "postgres" && resource.lifecycle === "ready";
}

export function canRestore(backup: Backup): boolean {
  return backup.lifecycle === "succeeded";
}

export function canCutover(review: ApplicationCutoverReview): boolean {
  return (
    review.lifecycle === "succeeded" &&
    review.validation_summary.source_binding_ready &&
    review.validation_summary.target_binding_ready &&
    review.validation_summary.target_restore_ready
  );
}

export function canRollback(cutover: ApplicationCutover, finalization?: ApplicationCutoverFinalization): boolean {
  return cutover.lifecycle === "succeeded" && (!finalization || finalization.lifecycle !== "succeeded");
}

export function canFinalize(cutover: ApplicationCutover, finalization?: ApplicationCutoverFinalization): boolean {
  return cutover.lifecycle === "succeeded" && (!finalization || finalization.lifecycle !== "succeeded");
}

export function buildPostgresCreateRequest(input: {
  environmentID: string;
  name: string;
  cpuMillicores?: number;
  memoryBytes?: number;
  storageBytes?: number;
}): CreateResourceRequest {
  const item = resourceCatalogItem("postgres")!;
  const cpu = input.cpuMillicores ?? item.defaultCPUMillicores;
  const memory = input.memoryBytes ?? item.defaultMemoryBytes;
  const storageBytes = input.storageBytes ?? item.defaultStorageBytes!;

  return {
    environment_id: input.environmentID,
    name: input.name.trim().toLowerCase(),
    kind: "managed_service",
    type: "postgres",
    managed: {
      type: "postgres",
      profile: "single-node-experimental",
      version: POSTGRES_VERSION,
      replicas: 1,
      cpu_millicores: cpu,
      memory_bytes: memory,
      storage: {
        persistent: true,
        size_bytes: storageBytes,
        policy_ref: POSTGRES_STORAGE_POLICY,
      },
      connection_policy: {
        mode: "internal",
      },
    },
  };
}

export function buildValkeyCreateRequest(input: {
  environmentID: string;
  name: string;
  cpuMillicores?: number;
  memoryBytes?: number;
}): CreateResourceRequest {
  const item = resourceCatalogItem("redis")!;
  const cpu = input.cpuMillicores ?? item.defaultCPUMillicores;
  const memory = input.memoryBytes ?? item.defaultMemoryBytes;

  return {
    environment_id: input.environmentID,
    name: input.name.trim().toLowerCase(),
    kind: "managed_service",
    type: "redis",
    managed: {
      type: "redis",
      profile: "single-node-experimental",
      replicas: 1,
      cpu_millicores: cpu,
      memory_bytes: memory,
      storage: {
        persistent: false,
      },
      connection_policy: {
        mode: "internal",
      },
    },
  };
}

export function buildNATSCreateRequest(input: {
  environmentID: string;
  name: string;
  cpuMillicores?: number;
  memoryBytes?: number;
}): CreateResourceRequest {
  const item = resourceCatalogItem("nats")!;
  const cpu = input.cpuMillicores ?? item.defaultCPUMillicores;
  const memory = input.memoryBytes ?? item.defaultMemoryBytes;

  return {
    environment_id: input.environmentID,
    name: input.name.trim().toLowerCase(),
    kind: "managed_service",
    type: "nats",
    managed: {
      type: "nats",
      profile: "single-node-experimental",
      replicas: 1,
      cpu_millicores: cpu,
      memory_bytes: memory,
      storage: {
        persistent: false,
      },
      connection_policy: {
        mode: "internal",
      },
    },
  };
}

export type ResourceOperationEvent = {
  id: string;
  type: "provision" | "binding" | "backup" | "restore" | "cutover" | "rollback" | "finalize" | "delete" | "destroy_storage";
  title: string;
  lifecycle: string;
  tone: "ready" | "warning" | "failed" | "neutral" | "bootstrapping";
  timestamp: string;
  details?: string;
  failureCode?: string;
  failureMessage?: string;
};

export function compileResourceOperations(
  resource: Resource,
  bindings: ResourceBinding[],
  backups: Backup[],
  restores: Restore[],
  cutovers: ApplicationCutover[],
  rollbacks: ApplicationCutoverRollback[],
  finalizations: ApplicationCutoverFinalization[],
  retainedStorage?: RetainedStorage | null,
): ResourceOperationEvent[] {
  const events: ResourceOperationEvent[] = [];

  // Resource provision event
  const resPres = resourceLifecyclePresentation(resource.lifecycle);
  events.push({
    id: `res-${resource.id}`,
    type: "provision",
    title: `Resource ${resource.name} (${resource.type.toUpperCase()})`,
    lifecycle: resPres.label,
    tone: resPres.tone,
    timestamp: resource.created_at,
    details: `Created by ${resource.created_by || "user"} · ${resource.lifecycle}`,
    failureCode: resource.runtime?.failure_code,
    failureMessage: resource.runtime?.failure_message,
  });

  // Resource delete event if deleting
  if (resource.lifecycle === "deleting") {
    events.push({
      id: `del-${resource.id}`,
      type: "delete",
      title: "Resource Runtime Deletion",
      lifecycle: "Deleting",
      tone: "warning",
      timestamp: resource.updated_at || resource.created_at,
      details: `Runtime deletion requested by ${resource.runtime?.delete_actor || "user"}; storage retained.`,
    });
  }

  // Bindings events
  for (const b of bindings.filter((item) => item.target.id === resource.id)) {
    const bindPres = resourceLifecyclePresentation(b.lifecycle);
    events.push({
      id: `bind-${b.id}`,
      type: "binding",
      title: `Binding ${b.logical_name} → ${b.source.id}`,
      lifecycle: bindPres.label,
      tone: bindPres.tone,
      timestamp: b.created_at,
      details: `Application ${b.source.id} connected via ${b.protocol}`,
      failureCode: b.failure_code,
    });
  }

  // Backups events
  for (const bk of backups.filter((item) => item.source_resource_id === resource.id)) {
    const bkPres = backupLifecyclePresentation(bk.lifecycle);
    events.push({
      id: `backup-${bk.id}`,
      type: "backup",
      title: `Logical Backup (${formatBytes(bk.artifact_size)})`,
      lifecycle: bkPres.label,
      tone: bkPres.tone,
      timestamp: bk.completed_at || bk.started_at || bk.created_at,
      details: `Backup ${bk.id} · ${bk.source_database} · ${bk.source_postgres_version || "PostgreSQL 16"}`,
      failureCode: bk.failure_code,
      failureMessage: bk.failure_message_redacted,
    });
  }

  // Restores events (where this resource is source or target)
  for (const rest of restores.filter((item) => item.source_resource_id === resource.id || item.target_resource_id === resource.id)) {
    const restPres = restoreLifecyclePresentation(rest.lifecycle);
    const role = rest.target_resource_id === resource.id ? "Restore Target" : "Restore Source";
    events.push({
      id: `restore-${rest.id}`,
      type: "restore",
      title: `Restore (${role})`,
      lifecycle: restPres.label,
      tone: restPres.tone,
      timestamp: rest.completed_at || rest.started_at || rest.created_at,
      details: `From backup ${rest.backup_id} into ${rest.target_resource_id}`,
      failureCode: rest.failure_code,
      failureMessage: rest.failure_message_redacted,
    });
  }

  // Cutovers
  for (const cut of cutovers.filter((item) => item.source_resource_id === resource.id || item.target_resource_id === resource.id)) {
    const cutPres = cutoverLifecyclePresentation(cut.lifecycle);
    events.push({
      id: `cutover-${cut.id}`,
      type: "cutover",
      title: `Cutover Application ${cut.application_id}`,
      lifecycle: cutPres.label,
      tone: cutPres.tone,
      timestamp: cut.completed_at || cut.applied_at || cut.requested_at,
      details: `Switched from ${cut.source_resource_id} to ${cut.target_resource_id}`,
      failureCode: cut.failure_code,
      failureMessage: cut.failure_message_redacted,
    });
  }

  // Rollbacks
  for (const rb of rollbacks.filter((item) => item.source_resource_id === resource.id || item.target_resource_id === resource.id)) {
    const rbPres = cutoverLifecyclePresentation(rb.lifecycle);
    events.push({
      id: `rollback-${rb.id}`,
      type: "rollback",
      title: `Cutover Rollback ${rb.application_id}`,
      lifecycle: rbPres.label,
      tone: rbPres.tone,
      timestamp: rb.completed_at || rb.applied_at || rb.requested_at,
      details: `Reverted application traffic back to source ${rb.source_resource_id}`,
      failureCode: rb.failure_code,
      failureMessage: rb.failure_message_redacted,
    });
  }

  // Finalizations
  for (const fin of finalizations.filter((item) => item.source_resource_id === resource.id || item.target_resource_id === resource.id)) {
    const finPres = cutoverLifecyclePresentation(fin.lifecycle);
    events.push({
      id: `final-${fin.id}`,
      type: "finalize",
      title: `Cutover Finalization ${fin.application_id}`,
      lifecycle: finPres.label,
      tone: finPres.tone,
      timestamp: fin.completed_at || fin.requested_at,
      details: "Source binding revoked; source data retained.",
      failureCode: fin.failure_code,
      failureMessage: fin.failure_message_redacted,
    });
  }

  // Retained storage
  if (retainedStorage) {
    const storPres = retainedStorageLifecyclePresentation(retainedStorage.lifecycle);
    events.push({
      id: `storage-${retainedStorage.id}`,
      type: "destroy_storage",
      title: `Retained Storage (${retainedStorage.pvc_name})`,
      lifecycle: storPres.label,
      tone: storPres.tone,
      timestamp: retainedStorage.destroyed_at || retainedStorage.destroy_requested_at || retainedStorage.retained_at,
      details: `${retainedStorage.actual_size || formatBytes(retainedStorage.requested_bytes)} · ${retainedStorage.storage_class}`,
      failureCode: retainedStorage.failure_code,
      failureMessage: retainedStorage.failure_message_redacted,
    });
  }

  // Sort descending by timestamp
  return events.sort((a, b) => b.timestamp.localeCompare(a.timestamp));
}
