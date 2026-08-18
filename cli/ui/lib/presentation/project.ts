import type {
  BuildRecord,
  DeploymentJob,
  IncidentResponse,
  PlacementFacts,
  Project,
  Readiness,
  ServiceRecord,
  TelemetryServiceStatus,
  TopologyPlan,
} from "@/lib/contracts/registry";

export type PresentationStatus = "healthy" | "degraded" | "failed" | "unknown" | "unavailable" | "in_progress";
export type SourceAvailability = "available" | "unavailable" | "not_reported";

export type FoundationData = {
  placement: PlacementFacts | null;
  topology: TopologyPlan | null;
  builds: BuildRecord[];
  telemetry: TelemetryServiceStatus[];
  incidents: IncidentResponse[];
  sources: {
    runtime: SourceAvailability;
    builds: SourceAvailability;
    telemetry: SourceAvailability;
    incidents: SourceAvailability;
  };
};

export type FoundationState = { foundation: FoundationData };

export const emptyFoundation: FoundationData = {
  placement: null,
  topology: null,
  builds: [],
  telemetry: [],
  incidents: [],
  sources: { runtime: "not_reported", builds: "not_reported", telemetry: "not_reported", incidents: "not_reported" },
};

const statusMap: Record<string, PresentationStatus> = {
  active: "healthy",
  available: "healthy",
  completed: "healthy",
  healthy: "healthy",
  live: "healthy",
  ready: "healthy",
  succeeded: "healthy",
  degraded: "degraded",
  partial: "degraded",
  rolled_back: "degraded",
  warning: "degraded",
  blocked: "failed",
  cancelled: "failed",
  dead_letter: "failed",
  error: "failed",
  failed: "failed",
  rollback_failed: "failed",
  applying: "in_progress",
  bootstrapping: "in_progress",
  created: "in_progress",
  installing: "in_progress",
  pending: "in_progress",
  prepared: "in_progress",
  queued: "in_progress",
  rolling_back: "in_progress",
  running: "in_progress",
  waiting: "in_progress",
  waiting_agent: "in_progress",
  disconnected: "unavailable",
  offline: "unavailable",
  unavailable: "unavailable",
};

export function normalizeStatus(value?: string | null): PresentationStatus {
  return statusMap[String(value ?? "").trim().toLowerCase()] ?? "unknown";
}

export function statusLabel(status: PresentationStatus) {
  return {
    healthy: "Healthy",
    degraded: "Degraded",
    failed: "Failed",
    unknown: "Unknown",
    unavailable: "Unavailable",
    in_progress: "In progress",
  }[status];
}

export function shortIdentifier(value?: string, size = 12) {
  if (!value) return "Not reported";
  const digest = value.includes("@") ? value.slice(value.lastIndexOf("@") + 1) : value;
  return digest.length > size ? `${digest.slice(0, size)}…` : digest;
}

const dateTime = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" });
const relative = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });

export function formatTimestamp(value?: string | number | null, now = Date.now()) {
  if (!value) return "Not reported";
  const time = typeof value === "number" ? (value < 10_000_000_000 ? value * 1000 : value) : Date.parse(value);
  if (!Number.isFinite(time)) return "Not reported";
  const seconds = Math.round((time - now) / 1000);
  if (Math.abs(seconds) < 60) return relative.format(seconds, "second");
  const minutes = Math.round(seconds / 60);
  if (Math.abs(minutes) < 60) return relative.format(minutes, "minute");
  const hours = Math.round(minutes / 60);
  if (Math.abs(hours) < 24) return relative.format(hours, "hour");
  return dateTime.format(time);
}

function latest<T>(items: T[], timestamp: (item: T) => string | number | undefined) {
  return [...items].sort((a, b) => toMillis(timestamp(b)) - toMillis(timestamp(a)))[0];
}

function toMillis(value?: string | number) {
  if (typeof value === "number") return value < 10_000_000_000 ? value * 1000 : value;
  return value ? Date.parse(value) || 0 : 0;
}

export function readinessAggregate(services: TelemetryServiceStatus[]) {
  return services.reduce(
    (total, service) => ({ ready: total.ready + service.ready_pods, desired: total.desired + service.pod_count }),
    { ready: 0, desired: 0 },
  );
}

export type AttentionItem = {
  id: string;
  status: PresentationStatus;
  title: string;
  detail: string;
  target: { view: "delivery" | "services" | "infrastructure" | "observability"; tab?: string };
};

export function deriveAttention(data: {
  builds: BuildRecord[];
  deployments: DeploymentJob[];
  telemetry: TelemetryServiceStatus[];
  incidents: IncidentResponse[];
  placement: PlacementFacts | null;
  sources: FoundationData["sources"];
}): AttentionItem[] {
  const items: AttentionItem[] = [];
  const build = latest(data.builds, (item) => item.created_at);
  const deployment = latest(data.deployments, (item) => item.updated_at ?? item.created_at);
  if (build && normalizeStatus(build.build.status) === "failed") {
    items.push({ id: `build-${build.id}`, status: "failed", title: `Latest build failed for ${build.service_key}`, detail: shortIdentifier(build.workload.sha), target: { view: "delivery", tab: "builds" } });
  }
  if (deployment && ["failed", "degraded"].includes(normalizeStatus(deployment.rollout_state ?? deployment.status))) {
    items.push({ id: `deployment-${deployment.id}`, status: normalizeStatus(deployment.rollout_state ?? deployment.status), title: `Latest rollout ${deployment.rollout_state ?? deployment.status}`, detail: deployment.service_id, target: { view: "delivery", tab: "deployments" } });
  }
  for (const service of data.telemetry) {
    if (service.ready_pods < service.pod_count) {
      items.push({ id: `service-${service.service_id}`, status: "degraded", title: `${service.service_id} is not fully ready`, detail: `${service.ready_pods}/${service.pod_count} ready`, target: { view: "services" } });
    }
  }
  for (const incident of data.incidents.filter((item) => item.status !== "resolved")) {
    items.push({ id: `incident-${incident.incident_id}`, status: incident.severity === "critical" ? "failed" : "degraded", title: `${incident.severity || "Open"} incident`, detail: incident.service_id || incident.anomaly_type || incident.incident_id, target: { view: "observability", tab: "incidents" } });
  }
  const unhealthyNode = data.placement?.nodes.find((node) => normalizeStatus(node.status) !== "healthy");
  if (unhealthyNode) {
    items.push({ id: `node-${unhealthyNode.id}`, status: normalizeStatus(unhealthyNode.status), title: "Runtime needs attention", detail: `${unhealthyNode.id}: ${unhealthyNode.status}`, target: { view: "infrastructure", tab: "runtime" } });
  }
  for (const [source, availability] of Object.entries(data.sources)) {
    if (availability === "unavailable") {
      const title =
        source === "runtime"
          ? "Agent unavailable"
          : source === "incidents"
            ? "Incident source missing"
            : `${sourceLabel(source)} unavailable`;
      items.push({
        id: `source-${source}`,
        status: "unavailable",
        title,
        detail: "The Local API did not receive this source.",
        target:
          source === "builds"
            ? { view: "delivery", tab: "builds" }
            : source === "runtime"
              ? { view: "infrastructure", tab: "runtime" }
              : { view: "observability", tab: source === "incidents" ? "incidents" : "health" },
      });
    }
  }
  return items.slice(0, 8);
}

function sourceLabel(source: string) {
  return source === "builds" ? "Build source" : source === "runtime" ? "Runtime source" : source[0].toUpperCase() + source.slice(1);
}

export function deriveProjectSummary(input: {
  project: Project;
  readiness: Readiness | null;
  services: ServiceRecord[];
  deployments: DeploymentJob[];
  foundation: FoundationData;
}) {
  const { foundation } = input;
  const readiness = readinessAggregate(foundation.telemetry);
  const openIncidents = foundation.incidents.filter((item) => item.status !== "resolved").length;
  const latestBuild = latest(foundation.builds, (item) => item.created_at);
  const latestDeployment = latest(input.deployments, (item) => item.updated_at ?? item.created_at);
  const attention = deriveAttention({ ...foundation, deployments: input.deployments });
  const readinessStatus = normalizeStatus(input.readiness?.status);
  let overall: PresentationStatus = "healthy";
  if (attention.some((item) => item.status === "failed") || readinessStatus === "failed") overall = "failed";
  else if (attention.some((item) => item.status === "degraded") || readinessStatus === "degraded") overall = "degraded";
  else if (attention.some((item) => item.status === "unavailable")) overall = "unavailable";
  else if (readinessStatus === "in_progress") overall = "in_progress";
  else if (attention.some((item) => item.status === "unknown") || readinessStatus === "unknown") overall = "unknown";
  else if (foundation.sources.telemetry !== "available" || foundation.sources.runtime !== "available") overall = "unknown";
  else if (input.services.length === 0 && foundation.telemetry.length === 0) overall = "unknown";
  else if (foundation.telemetry.length === 0 && input.services.length > 0) overall = "unknown";

  const timestamps = [
    latestBuild?.created_at,
    latestDeployment?.updated_at ?? latestDeployment?.created_at,
    ...foundation.telemetry.map((item) => item.last_seen_unix),
    ...foundation.incidents.map((item) => item.resolved_at_unix ?? item.created_at_unix),
  ].filter(Boolean) as Array<string | number>;
  const updatedAt = latest(timestamps, (item) => item);

  return {
    overall,
    readiness,
    openIncidents,
    latestBuild,
    latestDeployment,
    attention,
    serviceCount: input.services.length,
    updatedAt,
  };
}

export type ProjectSummary = ReturnType<typeof deriveProjectSummary>;
export const PROJECT_SUMMARY_TTL_MS = 30_000;
export type ProjectSummaryEntry = {
  status: "loading" | "ready" | "error";
  fetchedAt?: number;
  refreshing?: boolean;
  stale?: boolean;
  environment?: string;
  runtimeStatus?: PresentationStatus;
  summary?: ProjectSummary;
  error?: string;
};

export function serviceRows(input: {
  services: ServiceRecord[];
  telemetry: TelemetryServiceStatus[];
  telemetrySource: SourceAvailability;
  deployments: DeploymentJob[];
  placement: PlacementFacts | null;
  topology: TopologyPlan | null;
}) {
  return input.services.map((service) => {
    const telemetry = input.telemetry.find((item) => item.service_id === service.id);
    const deployment = latest(input.deployments.filter((item) => item.service_id === service.id), (item) => item.updated_at ?? item.created_at);
    const key = input.placement?.services.find((item) => item.id === service.id)?.key ?? service.name;
    const assignment = input.topology?.assignments.find((item) => item.service_key === key);
    const runtime = input.placement?.runtimes.find((item) => item.id === assignment?.runtime_id);
    const environment = input.placement?.environments.find((item) => item.id === assignment?.environment_id);
    const desired = telemetry?.pod_count ?? service.replicas;
    const ready = telemetry?.ready_pods;
    const health = telemetry
      ? (ready !== undefined && desired !== undefined && ready < desired ? "degraded" : normalizeStatus(telemetry.health))
      : input.telemetrySource === "unavailable" ? "unavailable" : "unknown";
    return {
      service,
      telemetry,
      deployment,
      placement: assignment ? `${environment?.name ?? assignment.environment_id} / ${runtime?.name ?? assignment.runtime_id}` : "Unplaced",
      runtime: runtime?.name,
      environment: environment?.name,
      ready,
      desired,
      health,
      release: deployment?.current_digest ?? deployment?.terminal_result?.current_digest ?? deployment?.terminal_result?.application_image_id ?? service.image,
    };
  }).sort((a, b) => statusOrder(a.health) - statusOrder(b.health) || a.service.name.localeCompare(b.service.name));
}

function statusOrder(status: PresentationStatus) {
  return { failed: 0, degraded: 1, unavailable: 2, unknown: 3, in_progress: 4, healthy: 5 }[status];
}

export function deliveryActivity(deployments: DeploymentJob[]) {
  const dated = deployments
    .map((item) => ({ item, time: toMillis(item.updated_at ?? item.created_at) }))
    .filter((item) => item.time > 0)
    .sort((a, b) => a.time - b.time);
  const days = new Set(dated.map(({ time }) => new Date(time).toISOString().slice(0, 10)));
  if (dated.length < 3 || days.size < 2) return { kind: "timeline" as const, events: dated.map(({ item }) => item) };
  const buckets = new Map<string, Record<"succeeded" | "failed" | "rolled_back" | "cancelled" | "other", number>>();
  for (const { item, time } of dated) {
    const day = new Date(time).toISOString().slice(0, 10);
    const bucket = buckets.get(day) ?? { succeeded: 0, failed: 0, rolled_back: 0, cancelled: 0, other: 0 };
    const status = item.rollout_state ?? item.status;
    bucket[status in bucket ? status as keyof typeof bucket : "other"] += 1;
    buckets.set(day, bucket);
  }
  return { kind: "chart" as const, buckets: [...buckets].map(([day, counts]) => ({ day, ...counts })) };
}
