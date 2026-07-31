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

export async function loadFoundation(client: LocalClient, projectID: string, services: ServiceRecord[], agentStatus: string): Promise<FoundationData> {
  const [placementResult, topologyResult, buildsResult] = await Promise.allSettled([
    client.placementFacts(projectID),
    client.topology(projectID),
    client.buildRecords(projectID),
  ]);
  let telemetry: FoundationData["telemetry"] = [];
  let incidents: FoundationData["incidents"] = [];
  let telemetrySource: FoundationData["sources"]["telemetry"] = agentStatus === "ok" ? "available" : "unavailable";
  let incidentSource: FoundationData["sources"]["incidents"] = agentStatus === "ok" ? "available" : "unavailable";
  if (agentStatus === "ok") {
    const [telemetryResults, incidentResult] = await Promise.all([
      Promise.allSettled(services.map((service) => client.telemetryService(projectID, service.id))),
      client.incidents(projectID).then((value) => ({ ok: true as const, value })).catch(() => ({ ok: false as const })),
    ]);
    telemetrySource = telemetryResults.some((result) => result.status === "rejected") ? "unavailable" : "available";
    telemetry = telemetryResults.flatMap((result) => result.status === "fulfilled" ? result.value.services ?? [] : []);
    if (incidentResult.ok) incidents = incidentResult.value.incidents ?? [];
    else incidentSource = "unavailable";
  }
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

export async function loadProjectSummary(client: LocalClient, project: Project, agentStatus: string): Promise<ProjectSummaryEntry> {
  const [readiness, services, deployments] = await Promise.all([
    client.readiness(project.id),
    client.services(project.id),
    client.deployments(project.id),
  ]);
  const records = services.services ?? [];
  const foundation = await loadFoundation(client, project.id, records, agentStatus);
  const nodeStatuses = foundation.placement?.nodes.map((node) => normalizeStatus(node.status)) ?? [];
  const runtimeStatus = foundation.sources.runtime !== "available"
    ? "unavailable"
    : nodeStatuses.includes("failed") ? "failed"
      : nodeStatuses.includes("degraded") ? "degraded"
        : nodeStatuses.includes("unavailable") ? "unavailable"
          : nodeStatuses.length && nodeStatuses.every((status) => status === "healthy") ? "healthy" : "unknown";
  return {
    status: "ready",
    environment: foundation.placement?.environments.find((item) => item.status === "active")?.name,
    runtimeStatus,
    summary: deriveProjectSummary({ project, readiness, services: records, deployments: deployments.deployments ?? [], foundation }),
  };
}

export async function reconnect(client: LocalClient, projectID: string, sessions: BootstrapSession[], deployments: DeploymentJob[]): Promise<Pick<ConsoleState, "bootstrapEvents" | "deploymentEvents">> {
  const activeSession = sessions.find((item) => ["created", "preflight", "installing", "waiting_agent"].includes(item.status)) ?? sessions[0];
  const bootstrapEvents = activeSession ? await client.bootstrapEvents(projectID, activeSession.id) : [];
  const deploymentEvents = deployments[0] ? (await client.deploymentEvents(projectID, deployments[0].id)).events ?? [] : [];
  return { bootstrapEvents, deploymentEvents };
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
    audit: [],
    support: null,
    secretReveal: null,
    totpSetup: null,
    incidents: [],
    bootstrapEvents: [] as TimelineEvent[],
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
    bootstrapEvents: [],
    deploymentEvents: [],
    audit: [],
    support: null,
    incidents: [],
    nodeDetail: null,
    serviceDetail: null,
    foundation: emptyFoundation,
  };
}
