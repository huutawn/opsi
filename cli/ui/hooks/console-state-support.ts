import { LocalClient } from "@/lib/api/local-client";
import type {
  BootstrapSession,
  ConsoleState,
  DeploymentJob,
  NodeDiagnostics,
  NodeRecord,
  Project,
  ServiceRecord,
  TimelineEvent,
} from "@/lib/contracts/registry";
import { emptyFoundation, type FoundationData, type FoundationState } from "@/lib/presentation/project";
import { deriveProjectSummary, normalizeStatus, type ProjectSummaryEntry } from "@/lib/presentation/project";
import { latestActiveBootstrap } from "@/lib/presentation/infrastructure/model";

export const MAX_PROJECT_SUMMARY_REQUESTS = 6;

export type RequestRunner = <T>(request: () => Promise<T>) => Promise<T>;

export function createRequestLimiter(limit = MAX_PROJECT_SUMMARY_REQUESTS): RequestRunner {
  let active = 0;
  const queue: Array<() => void> = [];
  const next = () => {
    if (active >= limit) return;
    const start = queue.shift();
    if (!start) return;
    active += 1;
    start();
  };
  return <T>(request: () => Promise<T>) => new Promise<T>((resolve, reject) => {
    queue.push(() => {
      Promise.resolve().then(request).then(resolve, reject).finally(() => {
        active -= 1;
        next();
      });
    });
    next();
  });
}

const directRequest: RequestRunner = (request) => request();

export function secretBody(form: FormData) {
  return {
    service_id: form.get("service_id"),
    name: form.get("name"),
    namespace: form.get("namespace"),
    otp_request_id: form.get("otp_request_id"),
    otp_code: form.get("otp_code"),
    totp_code: form.get("totp_code"),
  };
}

export async function loadProject(client: LocalClient, projectID: string) {
  return Promise.all([
    client.readiness(projectID),
    client.nodes(projectID),
    client.services(projectID),
    client.deployments(projectID),
    client.bootstrapSessions(projectID),
    client.audit(projectID),
    client.support(projectID),
  ]);
}

export async function loadFoundation(client: LocalClient, projectID: string, run: RequestRunner = directRequest): Promise<FoundationData> {
  const [placementResult, topologyResult, buildsResult, telemetryResult, incidentResult] = await Promise.allSettled([
    run(() => client.placementFacts(projectID)),
    run(() => client.topology(projectID)),
    run(() => client.buildRecords(projectID)),
    run(() => client.telemetrySummary(projectID)),
    run(() => client.incidents(projectID)),
  ]);
  const telemetry = telemetryResult.status === "fulfilled" ? telemetryResult.value.services ?? [] : [];
  const incidents = incidentResult.status === "fulfilled" ? incidentResult.value.incidents ?? [] : [];
  const telemetrySource: FoundationData["sources"]["telemetry"] = telemetryResult.status === "fulfilled" && telemetryResult.value.coverage?.status !== "unavailable"
    ? "available"
    : "unavailable";
  const incidentSource: FoundationData["sources"]["incidents"] = incidentResult.status === "fulfilled" ? "available" : "unavailable";
  return {
    placement: placementResult.status === "fulfilled" ? placementResult.value : null,
    topology: topologyResult.status === "fulfilled" ? topologyResult.value : null,
    builds: buildsResult.status === "fulfilled" ? buildsResult.value.records ?? [] : [],
    telemetry,
    incidents,
    sources: {
      runtime: placementResult.status === "fulfilled" ? "available" : "unavailable",
      builds: buildsResult.status === "fulfilled" ? "available" : "unavailable",
      telemetry: telemetrySource,
      incidents: incidentSource,
    },
  };
}

export async function loadProjectSummary(client: LocalClient, project: Project, run: RequestRunner = directRequest): Promise<ProjectSummaryEntry> {
  const [readiness, services, deployments] = await Promise.all([
    run(() => client.readiness(project.id)),
    run(() => client.services(project.id)),
    run(() => client.deployments(project.id)),
  ]);
  const records = services.services ?? [];
  const foundation = await loadFoundation(client, project.id, run);
  const nodeStatuses = foundation.placement?.nodes.map((node) => normalizeStatus(node.status)) ?? [];
  const runtimeStatus = foundation.sources.runtime !== "available"
    ? "unavailable"
    : nodeStatuses.includes("failed") ? "failed"
      : nodeStatuses.includes("degraded") ? "degraded"
        : nodeStatuses.includes("unavailable") ? "unavailable"
          : nodeStatuses.length && nodeStatuses.every((status) => status === "healthy") ? "healthy" : "unknown";
  return {
    status: "ready",
    fetchedAt: Date.now(),
    environment: foundation.placement?.environments.find((item) => item.status === "active")?.name,
    runtimeStatus,
    summary: deriveProjectSummary({ project, readiness, services: records, deployments: deployments.deployments ?? [], foundation }),
  };
}

export async function reconnect(client: LocalClient, projectID: string, sessions: BootstrapSession[], deployments: DeploymentJob[]): Promise<Pick<ConsoleState, "bootstrapEvents" | "bootstrapEventsSessionID" | "deploymentEvents">> {
  const activeSession = latestActiveBootstrap(sessions) ?? [...sessions].sort((a, b) => b.created_at.localeCompare(a.created_at))[0];
  const bootstrapEvents = activeSession ? await client.bootstrapEvents(projectID, activeSession.id) : [];
  const deploymentEvents = deployments[0] ? (await client.deploymentEvents(projectID, deployments[0].id)).events ?? [] : [];
  return { bootstrapEvents, bootstrapEventsSessionID: activeSession?.id ?? "", deploymentEvents };
}

export function workspacePatch(projects: Project[]): Partial<ConsoleState & FoundationState> {
  return {
    status: "ready",
    projects,
    project: null,
    readiness: null,
    nodes: [] as NodeRecord[],
    services: [] as ServiceRecord[],
    deployments: [] as DeploymentJob[],
    sessions: [] as BootstrapSession[],
    bootstrapCommand: "",
    bootstrapCommandSessionID: "",
    audit: [],
    support: null,
    secretReveal: null,
    totpSetup: null,
    incidents: [],
    bootstrapEvents: [] as TimelineEvent[],
    bootstrapEventsSessionID: "",
    deploymentEvents: [] as TimelineEvent[],
    nodeDetail: null as NodeDiagnostics | null,
    serviceDetail: null,
    foundation: emptyFoundation,
  };
}

export function clearProjectPatch(message: string): Partial<ConsoleState & FoundationState> {
  return {
    status: "loading",
    message,
    project: null,
    readiness: null,
    nodes: [],
    services: [],
    deployments: [],
    sessions: [],
    bootstrapCommand: "",
    bootstrapCommandSessionID: "",
    bootstrapEvents: [],
    bootstrapEventsSessionID: "",
    deploymentEvents: [],
    audit: [],
    support: null,
    incidents: [],
    nodeDetail: null,
    serviceDetail: null,
    foundation: emptyFoundation,
  };
}
