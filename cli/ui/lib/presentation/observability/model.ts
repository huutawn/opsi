import { formatDate, formatFreshness as formatFreshnessI18n, translate, type Locale } from "../../i18n/index.ts";
import type {
  AuditEvent,
  DeploymentJob,
  IncidentResponse,
  NodeRecord,
  PlacementFacts,
  Resource,
  ResourceBinding,
  ServiceRecord,
  TelemetryServiceStatus,
  TopologyPlan,
} from "../../contracts/registry.ts";

export type WorkloadRuntimeStatus = "ready" | "degraded" | "failed" | "in_progress" | "not_deployed" | "unknown";
export type ExposureRuntimeStatus = "ready" | "failed" | "not_configured" | "unknown";
export type DeploymentOutcomeStatus = "succeeded" | "failed" | "in_progress" | "unknown";
export type ServerRuntimeStatus = "ready" | "offline" | "failed" | "unknown";
export type ResourceRuntimeStatus = "ready" | "degraded" | "failed" | "provisioning" | "unknown";

export type ApplicationRuntimeSummary = {
  id: string;
  key: string;
  name: string;
  environment: string;
  configurationRevision: number;
  configurationStateHash?: string;
  deployedDigest?: string;
  shortDigest: string;
  workloadStatus: WorkloadRuntimeStatus;
  workloadLabel: string;
  readyReplicas: number;
  desiredReplicas: number;
  replicasLabel: string;
  serverPlacement: string;
  nodeID?: string;
  exposureStatus: ExposureRuntimeStatus;
  exposureLabel: string;
  exposureHostname?: string;
  exposurePath?: string;
  exposureURL?: string;
  lastDeploymentOutcome: DeploymentOutcomeStatus;
  lastDeploymentLabel: string;
  lastDeploymentID?: string;
  lastDeploymentRolloutState?: string;
  lastDeploymentCompletedAt?: string;
  lastSeenUnix?: number;
  lastSeenFreshness: string;
  restartCount: number;
  recentErrorCount: number;
  failureReason?: string;
  boundResourceCount: number;
  boundResourceTypes: string[];
  boundResourceNames: string[];
};

export type ServerRuntimeSummary = {
  id: string;
  name: string;
  role: string;
  publicHost: string;
  status: ServerRuntimeStatus;
  statusLabel: string;
  agentConnected: boolean;
  agentVersion?: string;
  cpuCores?: number;
  memoryMB?: number;
  placedWorkloadCount: number;
  degradedWorkloadCount: number;
  placedServices: string[];
  lastSeenUnix?: number;
  lastSeenFreshness: string;
};

export type ResourceRuntimeSummary = {
  id: string;
  name: string;
  type: "postgres" | "valkey" | "nats" | string;
  typeLabel: string;
  version?: string;
  status: ResourceRuntimeStatus;
  statusLabel: string;
  applicationBindingCount: number;
  boundServiceKeys: string[];
  serverPlacement: string;
  nodeID?: string;
  allocatedCPU?: number;
  allocatedMemoryBytes?: number;
  storageBytes?: number;
  persistentStorage: boolean;
  createdAt?: string;
  updatedAt?: string;
  lastOperation?: string;
  lastFailure?: string;
};

export type ActionableFailure = {
  id: string;
  category: "build" | "deployment" | "workload" | "exposure" | "server" | "resource";
  categoryLabel: string;
  title: string;
  explanation: string;
  code?: string;
  timestamp?: string | number;
  freshness: string;
  target: {
    view: "delivery" | "infrastructure" | "observability" | "services";
    tab?: string;
    service?: string;
    deployment?: string;
    server?: string;
    resource?: string;
  };
};

export type RuntimeOverviewSummary = {
  applications: {
    ready: number;
    degraded: number;
    failed: number;
    unknown: number;
    total: number;
  };
  servers: {
    ready: number;
    offline: number;
    failed: number;
    unknown: number;
    total: number;
  };
  resources: {
    ready: number;
    degraded: number;
    failed: number;
    unknown: number;
    total: number;
  };
  delivery: {
    active: number;
    failed: number;
    succeeded: number;
  };
  actionableFailures: ActionableFailure[];
  lastObservationUnix?: number;
  freshness: string;
};

export type RuntimeEvent = {
  id: string;
  timestamp: string | number;
  formattedTime: string;
  freshness: string;
  category: "deployment" | "workload" | "exposure" | "server" | "resource" | "audit";
  entityType: string;
  entityID: string;
  entityName?: string;
  title: string;
  detail: string;
  status: "succeeded" | "failed" | "in_progress" | "info" | "warning";
  untrustedContent?: boolean;
};

export type SafeLogEntry = {
  serviceID?: string;
  podID?: string;
  namespace?: string;
  level: "info" | "warn" | "error" | "debug" | string;
  message: string;
  fingerprint: string;
  observedUnix: number;
  formattedTime: string;
  freshness: string;
  untrustedContent: true;
};

export function formatFreshness(timestamp?: string | number | null, now = Date.now(), locale: Locale = "en"): string {
  return formatFreshnessI18n(timestamp, locale, now);
}

export function formatShortDigest(digest?: string, size = 12): string {
  if (!digest) return "Not reported";
  let raw = digest.includes("@") ? digest.slice(digest.lastIndexOf("@") + 1) : digest;
  if (raw.startsWith("sha256:")) raw = raw.slice(7);
  return raw.length > size ? `${raw.slice(0, size)}…` : raw;
}

export function formatReplicas(ready?: number, desired?: number): string {
  if (ready === undefined && desired === undefined) return "Not reported";
  if (ready === undefined) return `?/${desired ?? 0}`;
  return `${ready}/${desired ?? ready}`;
}

export function formatResourceTypeLabel(type: string): string {
  const normalized = type.toLowerCase().trim();
  switch (normalized) {
    case "postgres":
    case "postgresql":
      return "PostgreSQL";
    case "valkey":
    case "redis":
      return "Valkey";
    case "nats":
      return "NATS";
    default:
      return type.toUpperCase();
  }
}

export function deriveWorkloadRuntimeStatus(
  telemetry?: TelemetryServiceStatus,
  hasDeployment = false,
  agentAvailable = true,
): WorkloadRuntimeStatus {
  if (!agentAvailable) return "unknown";
  if (!telemetry) {
    return hasDeployment ? "unknown" : "not_deployed";
  }
  const health = String(telemetry.health || "").toLowerCase();
  const ready = telemetry.ready_pods;
  const desired = telemetry.pod_count;

  if (health === "failed" || health === "critical") return "failed";
  if (health === "degraded" || (desired > 0 && ready < desired)) return "degraded";
  if (ready >= desired && desired > 0 && (health === "healthy" || health === "ready" || health === "live")) {
    return "ready";
  }
  if (health === "healthy" || health === "ready") return "ready";
  return "unknown";
}

export function deriveDeploymentOutcome(deployment?: DeploymentJob): DeploymentOutcomeStatus {
  if (!deployment) return "unknown";
  const status = String(deployment.rollout_state || deployment.status || "").toLowerCase();
  if (status === "succeeded" || status === "completed" || status === "active") return "succeeded";
  if (status === "failed" || status === "blocked" || status === "cancelled" || status === "rollback_failed" || status === "dead_letter") {
    return "failed";
  }
  if (status === "running" || status === "in_progress" || status === "pending" || status === "rolling_back" || status === "applying" || status === "waiting" || status === "queued") {
    return "in_progress";
  }
  return "unknown";
}

export function deriveExposureRuntimeStatus(
  serviceKey: string,
  serviceID: string,
  exposures: DeploymentJob[] = [],
  placementFacts?: PlacementFacts | null,
): { status: ExposureRuntimeStatus; hostname?: string; path?: string; url?: string } {
  // Check topology placement facts if present
  const placedExposures = (placementFacts as unknown as { exposures?: Array<{ service_key?: string; service_id?: string; status?: string; hostname?: string; path?: string; url?: string }> })?.exposures;
  const placedExposure = placedExposures?.find(
    (e) => e.service_key === serviceKey || e.service_id === serviceID,
  );
  if (placedExposure) {
    const status = String(placedExposure.status || "").toLowerCase();
    const expStatus: ExposureRuntimeStatus = status === "ready" || status === "active" || status === "healthy"
      ? "ready"
      : status === "failed" || status === "error"
      ? "failed"
      : "unknown";
    return {
      status: expStatus,
      hostname: placedExposure.hostname,
      path: placedExposure.path,
      url: placedExposure.url,
    };
  }

  // Check exposures deployment jobs
  const matchingJobs = exposures.filter(
    (job) => job.service_id === serviceID || job.service_id === serviceKey,
  );
  if (matchingJobs.length === 0) {
    return { status: "not_configured" };
  }
  const latest = matchingJobs[0];
  const outcome = deriveDeploymentOutcome(latest);
  return {
    status: outcome === "succeeded" ? "ready" : outcome === "failed" ? "failed" : "unknown",
  };
}

export function deriveServerPlacement(
  serviceID: string,
  serviceKey: string,
  topology?: TopologyPlan | null,
  placement?: PlacementFacts | null,
  nodes: NodeRecord[] = [],
): { nodeID?: string; nodeName: string } {
  const assignment = topology?.assignments?.find(
    (a) => a.service_key === serviceKey || (a as unknown as { service_id?: string }).service_id === serviceID,
  );

  if (!assignment) {
    return { nodeName: "Unplaced" };
  }

  const runtime = placement?.runtimes?.find((r) => r.id === assignment.runtime_id);
  const runtimeNode = placement?.nodes?.find((n) => n.runtime_id === assignment.runtime_id);
  const matchedNode = nodes.find(
    (n) => n.id === runtimeNode?.id || n.id === assignment.runtime_id || n.id === (assignment as unknown as { node_id?: string }).node_id,
  );

  const nodeID = matchedNode?.id || runtimeNode?.id || (assignment as unknown as { node_id?: string }).node_id;
  const nodeName = matchedNode?.name || runtime?.name || assignment.runtime_id || "Unplaced";

  return {
    nodeID,
    nodeName,
  };
}

export function deriveApplicationRuntimeSummaries(inputs: {
  services: ServiceRecord[];
  telemetry?: TelemetryServiceStatus[];
  deployments?: DeploymentJob[];
  exposures?: DeploymentJob[];
  placement?: PlacementFacts | null;
  topology?: TopologyPlan | null;
  bindings?: ResourceBinding[];
  resources?: Resource[];
  nodes?: NodeRecord[];
  agentAvailable?: boolean;
}): ApplicationRuntimeSummary[] {
  const {
    services,
    telemetry = [],
    deployments = [],
    exposures = [],
    placement = null,
    topology = null,
    bindings = [],
    resources = [],
    nodes = [],
    agentAvailable = true,
  } = inputs;

  const appServices = services.filter((s) => s.type === "application" || !s.type);

  return appServices.map((service) => {
    const rawService = service as unknown as {
      key?: string;
      environment_id?: string;
      deployed_digest?: string;
      current_configuration_revision?: number;
      current_configuration_state_hash?: string;
    };
    const serviceKey = rawService.key ?? placement?.services?.find((item) => item.id === service.id)?.key ?? service.name;
    const serviceTelemetry = telemetry.find(
      (t) => t.service_id === service.id || t.service_id === serviceKey,
    );

    const serviceDeployments = deployments.filter(
      (d) => d.service_id === service.id || d.service_id === serviceKey,
    );
    const latestDeployment = serviceDeployments[0];

    const hasDeployment = serviceDeployments.length > 0 || Boolean(rawService.deployed_digest);
    const workloadStatus = deriveWorkloadRuntimeStatus(serviceTelemetry, hasDeployment, agentAvailable);

    const desiredReplicas = serviceTelemetry?.pod_count ?? service.replicas ?? (hasDeployment ? 1 : 0);
    const readyReplicas = serviceTelemetry?.ready_pods ?? (workloadStatus === "ready" ? desiredReplicas : 0);

    const placementInfo = deriveServerPlacement(service.id, serviceKey, topology, placement, nodes);
    const exposureInfo = deriveExposureRuntimeStatus(serviceKey, service.id, exposures, placement);

    const serviceBindings = bindings.filter((b) => {
      const srcId = b.source?.id || (b as unknown as { service_id?: string; service_key?: string }).service_id || (b as unknown as { service_id?: string; service_key?: string }).service_key;
      return srcId === service.id || srcId === serviceKey;
    });
    const boundResources = serviceBindings
      .map((b) => {
        const resId = b.target?.id || (b as unknown as { resource_id?: string }).resource_id;
        return resources.find((r) => r.id === resId);
      })
      .filter((r): r is Resource => Boolean(r));

    const lastDeploymentOutcome = deriveDeploymentOutcome(latestDeployment);

    const deployedDigest = rawService.deployed_digest || latestDeployment?.desired_digest;

    return {
      id: service.id,
      key: serviceKey,
      name: service.name || serviceKey,
      environment: rawService.environment_id || "Production",
      configurationRevision: rawService.current_configuration_revision ?? 1,
      configurationStateHash: rawService.current_configuration_state_hash,
      deployedDigest,
      shortDigest: formatShortDigest(deployedDigest),
      workloadStatus,
      workloadLabel: labelForWorkloadStatus(workloadStatus),
      readyReplicas,
      desiredReplicas,
      replicasLabel: formatReplicas(readyReplicas, desiredReplicas),
      serverPlacement: placementInfo.nodeName,
      nodeID: placementInfo.nodeID,
      exposureStatus: exposureInfo.status,
      exposureLabel: labelForExposureStatus(exposureInfo.status),
      exposureHostname: exposureInfo.hostname,
      exposurePath: exposureInfo.path,
      exposureURL: exposureInfo.url,
      lastDeploymentOutcome,
      lastDeploymentLabel: labelForDeploymentOutcome(lastDeploymentOutcome),
      lastDeploymentID: latestDeployment?.id,
      lastDeploymentRolloutState: latestDeployment?.rollout_state ?? latestDeployment?.status,
      lastDeploymentCompletedAt: latestDeployment?.updated_at ?? latestDeployment?.created_at,
      lastSeenUnix: serviceTelemetry?.last_seen_unix,
      lastSeenFreshness: formatFreshness(serviceTelemetry?.last_seen_unix),
      restartCount: serviceTelemetry?.restart_count ?? 0,
      recentErrorCount: serviceTelemetry?.recent_error_count ?? 0,
      failureReason: latestDeployment?.failure_message_redacted || (latestDeployment as unknown as { failure_message?: string })?.failure_message || latestDeployment?.failure_code,
      boundResourceCount: boundResources.length,
      boundResourceTypes: Array.from(new Set(boundResources.map((r) => formatResourceTypeLabel(r.type)))),
      boundResourceNames: boundResources.map((r) => r.name),
    };
  });
}

export function deriveServerRuntimeSummaries(inputs: {
  nodes: NodeRecord[];
  telemetry?: TelemetryServiceStatus[];
  placement?: PlacementFacts | null;
  topology?: TopologyPlan | null;
  services?: ServiceRecord[];
  agentAvailable?: boolean;
}): ServerRuntimeSummary[] {
  const { nodes, telemetry = [], placement = null, topology = null, services = [], agentAvailable = true } = inputs;

  return nodes.map((node) => {
    const rawStatus = String(node.status || "").toLowerCase();
    let status: ServerRuntimeStatus = "unknown";
    if (rawStatus === "offline" || rawStatus === "disconnected") {
      status = "offline";
    } else if (rawStatus === "ready" || rawStatus === "healthy" || rawStatus === "active") {
      status = "ready";
    } else if (rawStatus === "failed" || rawStatus === "error") {
      status = "failed";
    }

    // Find services assigned to this node
    const assignments = (topology?.assignments ?? []).filter((a) => {
      const runtimeNode = placement?.nodes?.find((n) => n.runtime_id === a.runtime_id);
      return runtimeNode?.id === node.id || a.runtime_id === node.id || (a as unknown as { node_id?: string }).node_id === node.id;
    });

    const placedServiceKeys = Array.from(
      new Set(
        assignments
          .map((a) => a.service_key || (a as unknown as { service_id?: string }).service_id)
          .filter((k): k is string => Boolean(k)),
      ),
    );
    const placedServices: string[] = placedServiceKeys.map((key) => {
      const s = services.find((srv) => (srv as unknown as { key?: string }).key === key || srv.id === key || srv.name === key);
      return s?.name || key;
    });

    let degradedCount = 0;
    for (const serviceKey of placedServiceKeys) {
      const tel = telemetry.find((t) => t.service_id === serviceKey || (services.find((s) => (s as unknown as { key?: string }).key === serviceKey)?.id === t.service_id));
      if (tel && (tel.health === "degraded" || tel.health === "failed" || tel.ready_pods < tel.pod_count)) {
        degradedCount++;
      }
    }

    const lastSeenUnix = node.last_seen_at
      ? Math.floor(new Date(node.last_seen_at).getTime() / 1000)
      : undefined;

    return {
      id: node.id,
      name: node.name || node.id,
      role: node.role || "Server",
      publicHost: node.public_host || "127.0.0.1",
      status,
      statusLabel: status === "ready" ? "Ready" : status === "offline" ? "Offline" : status === "failed" ? "Failed" : "Unknown",
      agentConnected: agentAvailable && Boolean(node.agent_id) && status !== "offline",
      agentVersion: node.agent_version || "canonical",
      cpuCores: node.cpu_cores,
      memoryMB: node.memory_mb,
      placedWorkloadCount: placedServiceKeys.length,
      degradedWorkloadCount: degradedCount,
      placedServices,
      lastSeenUnix,
      lastSeenFreshness: formatFreshness(node.last_seen_at),
    };
  });
}

export function deriveResourceRuntimeSummaries(inputs: {
  resources: Resource[];
  bindings?: ResourceBinding[];
  nodes?: NodeRecord[];
  topology?: TopologyPlan | null;
  placement?: PlacementFacts | null;
}): ResourceRuntimeSummary[] {
  const { resources, bindings = [], nodes = [], topology = null, placement = null } = inputs;

  return resources.map((res) => {
    const rawRes = res as unknown as { status?: string; version?: string; cpu_cores?: number; memory_bytes?: number; storage_bytes?: number; persistent?: boolean; last_operation?: string; last_failure?: string; server_id?: string; node_id?: string };
    const rawStatus = String(rawRes.status || res.lifecycle || "").toLowerCase();
    let status: ResourceRuntimeStatus = "unknown";
    if (rawStatus === "ready" || rawStatus === "healthy" || rawStatus === "active" || rawStatus === "provisioned") {
      status = "ready";
    } else if (rawStatus === "degraded" || rawStatus === "warning") {
      status = "degraded";
    } else if (rawStatus === "failed" || rawStatus === "error") {
      status = "failed";
    } else if (rawStatus === "provisioning" || rawStatus === "creating" || rawStatus === "updating" || rawStatus === "deprovisioning") {
      status = "provisioning";
    }

    const resBindings = bindings.filter((b) => {
      const resId = b.target?.id || (b as unknown as { resource_id?: string }).resource_id;
      return resId === res.id;
    });
    const boundServiceKeys = Array.from(
      new Set(
        resBindings.map((b) => {
          const srcId = b.source?.id || (b as unknown as { service_key?: string; service_id?: string }).service_key || (b as unknown as { service_key?: string; service_id?: string }).service_id;
          return srcId || "unknown";
        }),
      ),
    );

    // Find server placement
    const resNodeID = res.runtime?.spec?.assignment?.node_id || rawRes.node_id || rawRes.server_id;
    let nodeName = "Unplaced";
    if (resNodeID) {
      const node = nodes.find((n) => n.id === resNodeID);
      nodeName = node?.name || resNodeID;
    } else {
      const assignments = (topology as unknown as { resource_assignments?: Array<{ resource_id: string; node_id: string }> })?.resource_assignments ?? (placement as unknown as { resource_assignments?: Array<{ resource_id: string; node_id: string }> })?.resource_assignments;
      const assign = assignments?.find((a) => a.resource_id === res.id);
      if (assign?.node_id) {
        const node = nodes.find((n) => n.id === assign.node_id);
        nodeName = node?.name || assign.node_id;
      }
    }

    const version = res.runtime?.spec?.version || res.managed?.version || rawRes.version;
    const allocatedCPU = res.runtime?.spec?.cpu_millicores ? res.runtime.spec.cpu_millicores / 1000 : rawRes.cpu_cores;
    const allocatedMemoryBytes = res.runtime?.spec?.memory_bytes || rawRes.memory_bytes;
    const storageBytes = res.runtime?.spec?.storage?.size_bytes || rawRes.storage_bytes;
    const persistentStorage = res.runtime?.spec?.storage?.persistent ?? rawRes.persistent ?? true;
    const lastFailure = res.runtime?.failure_message || rawRes.last_failure;

    return {
      id: res.id,
      name: res.name,
      type: res.type,
      typeLabel: formatResourceTypeLabel(res.type),
      version,
      status,
      statusLabel: labelForResourceStatus(status),
      applicationBindingCount: resBindings.length,
      boundServiceKeys,
      serverPlacement: nodeName,
      nodeID: resNodeID,
      allocatedCPU,
      allocatedMemoryBytes,
      storageBytes,
      persistentStorage,
      createdAt: res.created_at,
      updatedAt: res.updated_at,
      lastOperation: rawRes.last_operation,
      lastFailure,
    };
  });
}

export function deriveRuntimeOverview(inputs: {
  applications: ApplicationRuntimeSummary[];
  servers: ServerRuntimeSummary[];
  resources: ResourceRuntimeSummary[];
  deployments?: DeploymentJob[];
  incidents?: IncidentResponse[];
  audit?: AuditEvent[];
}): RuntimeOverviewSummary {
  const { applications, servers, resources, deployments = [], incidents = [], audit = [] } = inputs;

  const appCounts = {
    ready: applications.filter((a) => a.workloadStatus === "ready").length,
    degraded: applications.filter((a) => a.workloadStatus === "degraded").length,
    failed: applications.filter((a) => a.workloadStatus === "failed").length,
    unknown: applications.filter((a) => a.workloadStatus === "unknown" || a.workloadStatus === "not_deployed").length,
    total: applications.length,
  };

  const serverCounts = {
    ready: servers.filter((s) => s.status === "ready").length,
    offline: servers.filter((s) => s.status === "offline").length,
    failed: servers.filter((s) => s.status === "failed").length,
    unknown: servers.filter((s) => s.status === "unknown").length,
    total: servers.length,
  };

  const resourceCounts = {
    ready: resources.filter((r) => r.status === "ready").length,
    degraded: resources.filter((r) => r.status === "degraded").length,
    failed: resources.filter((r) => r.status === "failed").length,
    unknown: resources.filter((r) => r.status === "unknown" || r.status === "provisioning").length,
    total: resources.length,
  };

  const activeDeployments = deployments.filter((d) => deriveDeploymentOutcome(d) === "in_progress");
  const failedDeployments = deployments.filter((d) => deriveDeploymentOutcome(d) === "failed");
  const succeededDeployments = deployments.filter((d) => deriveDeploymentOutcome(d) === "succeeded");

  const actionableFailures: ActionableFailure[] = [];

  // Workload degradation/failures
  for (const app of applications) {
    if (app.workloadStatus === "degraded" || app.workloadStatus === "failed") {
      actionableFailures.push({
        id: `workload-${app.id}`,
        category: "workload",
        categoryLabel: "Workload Degraded",
        title: `Workload ${app.name} is ${app.workloadLabel}`,
        explanation: `${app.readyReplicas}/${app.desiredReplicas} replicas ready on ${app.serverPlacement}${app.restartCount > 0 ? ` (${app.restartCount} restarts)` : ""}.`,
        timestamp: app.lastSeenUnix ? app.lastSeenUnix * 1000 : undefined,
        freshness: app.lastSeenFreshness,
        target: { view: "observability", tab: "applications", service: app.id },
      });
    }
    // Exposure failure
    if (app.exposureStatus === "failed") {
      actionableFailures.push({
        id: `exposure-${app.id}`,
        category: "exposure",
        categoryLabel: "Exposure Failed",
        title: `Exposure routing failed for ${app.name}`,
        explanation: `Workload is ${app.workloadLabel}, but exposure endpoint ${app.exposureHostname || app.exposureURL || ""} is failing.`,
        target: { view: "observability", tab: "applications", service: app.id },
        freshness: "Current state",
      });
    }
  }

  // Server offline / failures
  for (const srv of servers) {
    if (srv.status === "offline" || srv.status === "failed") {
      actionableFailures.push({
        id: `server-${srv.id}`,
        category: "server",
        categoryLabel: srv.status === "offline" ? "Server Offline" : "Server Failed",
        title: `Server ${srv.name} is ${srv.statusLabel}`,
        explanation: `Agent heartbeat is unavailable on ${srv.publicHost}. ${srv.placedWorkloadCount} placed workloads affected.`,
        timestamp: srv.lastSeenUnix ? srv.lastSeenUnix * 1000 : undefined,
        freshness: srv.lastSeenFreshness,
        target: { view: "observability", tab: "servers", server: srv.id },
      });
    }
  }

  // Resource degradation / failures
  for (const res of resources) {
    if (res.status === "degraded" || res.status === "failed") {
      actionableFailures.push({
        id: `resource-${res.id}`,
        category: "resource",
        categoryLabel: "Resource Failed",
        title: `${res.typeLabel} resource ${res.name} is ${res.statusLabel}`,
        explanation: `${res.applicationBindingCount} bound applications affected on ${res.serverPlacement}.${res.lastFailure ? ` Detail: ${res.lastFailure}` : ""}`,
        timestamp: res.updatedAt,
        freshness: formatFreshness(res.updatedAt),
        target: { view: "observability", tab: "resources", resource: res.id },
      });
    }
  }

  // Recent failed deployments
  for (const dep of failedDeployments.slice(0, 3)) {
    const failureText = dep.failure_message_redacted || (dep as unknown as { failure_message?: string })?.failure_message || dep.failure_code || "Rollout did not reach factual verification.";
    actionableFailures.push({
      id: `deployment-${dep.id}`,
      category: "deployment",
      categoryLabel: "Deployment Failed",
      title: `Deployment failed for ${dep.service_id}`,
      explanation: failureText,
      code: dep.failure_code,
      timestamp: dep.updated_at || dep.created_at,
      freshness: formatFreshness(dep.updated_at || dep.created_at),
      target: { view: "delivery", tab: "deployments", service: dep.service_id, deployment: dep.id },
    });
  }

  // Open incidents
  for (const inc of incidents.filter((i) => i.status !== "resolved").slice(0, 3)) {
    actionableFailures.push({
      id: `incident-${inc.incident_id}`,
      category: "workload",
      categoryLabel: "Open Incident",
      title: `Incident ${inc.incident_id}`,
      explanation: `Anomaly: ${inc.anomaly_type || inc.status} on ${inc.service_id || inc.node_id || "workload"}. Severity: ${inc.severity || "warning"}.`,
      timestamp: inc.created_at_unix ? inc.created_at_unix * 1000 : undefined,
      freshness: formatFreshness(inc.created_at_unix),
      target: { view: "observability", tab: "applications", service: inc.service_id },
    });
  }

  // Failed audit actions
  for (const aud of audit.filter((a) => a.result === "failure").slice(0, 3)) {
    actionableFailures.push({
      id: `audit-${aud.id}`,
      category: "deployment",
      categoryLabel: "Operation Failed",
      title: `Action ${aud.action.replaceAll("_", " ")} Failed`,
      explanation: `Actor ${aud.actor_user_id || "system"} failed on ${aud.resource_type} ${aud.resource_id}.`,
      timestamp: aud.created_at,
      freshness: formatFreshness(aud.created_at),
      target: { view: "observability", tab: "overview" },
    });
  }

  // Find latest observation timestamp
  const observationTimestamps = [
    ...applications.map((a) => a.lastSeenUnix ? a.lastSeenUnix * 1000 : 0),
    ...servers.map((s) => s.lastSeenUnix ? s.lastSeenUnix * 1000 : 0),
  ].filter((t) => t > 0);
  const latestObservation = observationTimestamps.length > 0 ? Math.max(...observationTimestamps) : undefined;

  return {
    applications: appCounts,
    servers: serverCounts,
    resources: resourceCounts,
    delivery: {
      active: activeDeployments.length,
      failed: failedDeployments.length,
      succeeded: succeededDeployments.length,
    },
    actionableFailures,
    lastObservationUnix: latestObservation ? Math.floor(latestObservation / 1000) : undefined,
    freshness: formatFreshness(latestObservation),
  };
}

export function deriveApplicationEvents(
  serviceID: string,
  serviceKey: string,
  deployments: DeploymentJob[] = [],
  audit: AuditEvent[] = [],
): RuntimeEvent[] {
  const events: RuntimeEvent[] = [];

  // Deployment events
  for (const dep of deployments.filter((d) => d.service_id === serviceID || d.service_id === serviceKey)) {
    const outcome = deriveDeploymentOutcome(dep);
    const failureMsg = dep.failure_message_redacted || (dep as unknown as { failure_message?: string })?.failure_message;
    events.push({
      id: `dep-${dep.id}`,
      timestamp: dep.updated_at || dep.created_at,
      formattedTime: formatDateTime(dep.updated_at || dep.created_at),
      freshness: formatFreshness(dep.updated_at || dep.created_at),
      category: "deployment",
      entityType: "Application Deployment",
      entityID: dep.id,
      title: outcome === "succeeded" ? "Workload Rollout Succeeded" : outcome === "failed" ? "Workload Rollout Failed" : "Workload Rollout In Progress",
      detail: failureMsg || `Rollout revision state: ${dep.rollout_state || dep.status}. Digest: ${formatShortDigest(dep.desired_digest)}.`,
      status: outcome === "succeeded" ? "succeeded" : outcome === "failed" ? "failed" : "in_progress",
    });
  }

  // Audit events
  for (const aud of audit.filter((a) => a.resource_id === serviceID || a.resource_id === serviceKey)) {
    events.push({
      id: `audit-${aud.id}`,
      timestamp: aud.created_at,
      formattedTime: formatDateTime(aud.created_at),
      freshness: formatFreshness(aud.created_at),
      category: "audit",
      entityType: aud.resource_type || "Service",
      entityID: aud.resource_id,
      title: aud.action.replaceAll("_", " "),
      detail: `Actor ${aud.actor_user_id || "system"} executed ${aud.action} with outcome ${aud.result}.`,
      status: aud.result === "success" ? "succeeded" : "failed",
    });
  }

  return events.sort((a, b) => toMillis(b.timestamp) - toMillis(a.timestamp));
}

function formatDateTime(value?: string | number | null, locale: Locale = "en"): string {
  return formatDate(value, locale);
}

function toMillis(value?: string | number): number {
  if (typeof value === "number") return value < 10_000_000_000 ? value * 1000 : value;
  return value ? Date.parse(value) || 0 : 0;
}

function labelForWorkloadStatus(status: WorkloadRuntimeStatus, locale: Locale = "en"): string {
  return translate(`status.${status}`, locale, {
    ready: "Ready",
    degraded: "Degraded",
    failed: "Failed",
    in_progress: "In Progress",
    not_deployed: "Not Deployed",
    unknown: "Unknown",
  }[status] ?? status);
}

function labelForExposureStatus(status: ExposureRuntimeStatus, locale: Locale = "en"): string {
  return translate(`status.${status}`, locale, {
    ready: "Ready",
    failed: "Failed",
    not_configured: "Not Configured",
    unknown: "Unknown",
  }[status] ?? status);
}

function labelForDeploymentOutcome(status: DeploymentOutcomeStatus, locale: Locale = "en"): string {
  return translate(`status.${status}`, locale, {
    succeeded: "Succeeded",
    failed: "Failed",
    in_progress: "In Progress",
    unknown: "Unknown",
  }[status] ?? status);
}

function labelForResourceStatus(status: ResourceRuntimeStatus, locale: Locale = "en"): string {
  const fallbackMap: Record<ResourceRuntimeStatus, string> = {
    ready: "Ready",
    degraded: "Degraded",
    failed: "Failed",
    unknown: "Unknown",
    provisioning: "Provisioning",
  };
  return translate(`status.${status}`, locale, fallbackMap[status] ?? status);
}
