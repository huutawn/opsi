"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { LocalClient } from "@/lib/api/local-client";
import type {
  AuditEvent,
  DeploymentJob,
  IncidentEvidence,
  IncidentResponse,
  NodeRecord,
  PlacementFacts,
  Resource,
  ResourceBinding,
  ServiceRecord,
  TelemetryLogEntry,
  TelemetryServiceStatus,
  TelemetrySummary,
  TopologyPlan,
} from "@/lib/contracts/registry";
import type { ConsoleController } from "@/features/console/types";
import {
  deriveApplicationEvents,
  deriveApplicationRuntimeSummaries,
  deriveResourceRuntimeSummaries,
  deriveRuntimeOverview,
  deriveServerRuntimeSummaries,
  type ApplicationRuntimeSummary,
  type ResourceRuntimeSummary,
  type RuntimeEvent,
  type RuntimeOverviewSummary,
  type ServerRuntimeSummary,
} from "@/lib/presentation/observability/model";

export type SourceState = "loading" | "ready" | "empty" | "unavailable" | "partial";
export type SourceStates = {
  registry: SourceState;
  telemetry: SourceState;
  incidents: SourceState;
  support: SourceState;
  nodes: SourceState;
  resources: SourceState;
  deployments: SourceState;
};

export type LogState = {
  rows: TelemetryLogEntry[];
  nextCursor?: string;
  source: SourceState;
  error: string;
};

export type ObservabilityData = {
  summary: TelemetrySummary | null;
  telemetry: TelemetryServiceStatus[];
  services: ServiceRecord[];
  nodes: NodeRecord[];
  resources: Resource[];
  bindings: ResourceBinding[];
  deployments: DeploymentJob[];
  exposures: DeploymentJob[];
  audit: AuditEvent[];
  placement: PlacementFacts | null;
  topology: TopologyPlan | null;
  incidents: IncidentResponse[];
  selectedIncident: IncidentResponse | null;
  evidence: IncidentEvidence | null;
  evidenceState: SourceState;
  evidenceError: string;
  sources: SourceStates;
  logs: LogState;
  error: string;
  // Presentation model projections
  applications: ApplicationRuntimeSummary[];
  servers: ServerRuntimeSummary[];
  managedResources: ResourceRuntimeSummary[];
  overview: RuntimeOverviewSummary;
};

const emptyLogs: LogState = { rows: [], source: "empty", error: "" };
const emptySources: SourceStates = {
  registry: "empty",
  telemetry: "empty",
  incidents: "empty",
  support: "empty",
  nodes: "empty",
  resources: "empty",
  deployments: "empty",
};

const emptyOverview: RuntimeOverviewSummary = {
  applications: { ready: 0, degraded: 0, failed: 0, unknown: 0, total: 0 },
  servers: { ready: 0, offline: 0, failed: 0, unknown: 0, total: 0 },
  resources: { ready: 0, degraded: 0, failed: 0, unknown: 0, total: 0 },
  delivery: { active: 0, failed: 0, succeeded: 0 },
  actionableFailures: [],
  freshness: "Not reported",
};

const emptyData: ObservabilityData = {
  summary: null,
  telemetry: [],
  services: [],
  nodes: [],
  resources: [],
  bindings: [],
  deployments: [],
  exposures: [],
  audit: [],
  placement: null,
  topology: null,
  incidents: [],
  selectedIncident: null,
  evidence: null,
  evidenceState: "empty",
  evidenceError: "",
  sources: emptySources,
  logs: emptyLogs,
  error: "",
  applications: [],
  servers: [],
  managedResources: [],
  overview: emptyOverview,
};

export function useObservabilityData(console: ConsoleController) {
  const projectID = console.route.projectID || console.state.project?.id || "";
  const environmentID = console.route.environment ?? "";
  const client = useMemo(() => new LocalClient(), []);
  const sequence = useRef(0);
  const incidentSequence = useRef(0);
  const [data, setData] = useState<ObservabilityData>(() => seedData(console));

  const load = useCallback(async () => {
    if (!projectID) return;
    const current = ++sequence.current;
    setData((previous) => ({
      ...previous,
      error: "",
      sources: {
        ...previous.sources,
        telemetry: previous.summary ? previous.sources.telemetry : "loading",
        incidents: previous.incidents.length ? previous.sources.incidents : "loading",
        nodes: previous.nodes.length ? previous.sources.nodes : "loading",
        resources: previous.resources.length ? previous.sources.resources : "loading",
        deployments: previous.deployments.length ? previous.sources.deployments : "loading",
      },
    }));

    const [
      summaryRes,
      servicesRes,
      nodesRes,
      resourcesRes,
      bindingsRes,
      deploymentsRes,
      exposuresRes,
      placementRes,
      topologyRes,
      auditRes,
      incidentsRes,
    ] = await Promise.allSettled([
      client.telemetrySummary(projectID),
      client.services(projectID),
      client.nodes(projectID),
      client.resources(projectID, environmentID || undefined),
      client.resourceBindings(projectID, environmentID || undefined),
      client.deployments(projectID),
      client.exposures(projectID),
      client.placementFacts(projectID),
      client.topology(projectID),
      client.audit(projectID),
      client.incidents(projectID),
    ]);

    if (current !== sequence.current) return;

    const services = servicesRes.status === "fulfilled" ? servicesRes.value.services ?? [] : console.state.services;
    const nodes = nodesRes.status === "fulfilled" ? nodesRes.value : console.state.nodes;
    const resources = resourcesRes.status === "fulfilled" ? resourcesRes.value : [];
    const bindings = bindingsRes.status === "fulfilled" ? bindingsRes.value : [];
    const deployments = deploymentsRes.status === "fulfilled" ? deploymentsRes.value.deployments ?? [] : console.state.deployments;
    const exposures = exposuresRes.status === "fulfilled" ? exposuresRes.value.exposures ?? [] : [];
    const placement = placementRes.status === "fulfilled" ? placementRes.value : console.state.foundation.placement;
    const topology = topologyRes.status === "fulfilled" ? topologyRes.value : console.state.foundation.topology;
    const audit = auditRes.status === "fulfilled" ? auditRes.value.events ?? [] : console.state.audit;
    const incidents = incidentsRes.status === "fulfilled" ? incidentsRes.value.incidents ?? [] : console.state.incidents;
    const summary = summaryRes.status === "fulfilled" ? summaryRes.value : null;

    // Fetch individual service telemetries if services exist
    let telemetry: TelemetryServiceStatus[] = [];
    if (services.length > 0) {
      const serviceTelemetryRes = await Promise.allSettled(
        services.map((service) => client.telemetryService(projectID, service.id)),
      );
      if (current !== sequence.current) return;
      telemetry = serviceTelemetryRes.flatMap((res) => (res.status === "fulfilled" ? res.value.services ?? [] : []));
    }

    const agentAvailable = console.session?.agent_connected === "ok";

    // Compute presentation models
    const applications = deriveApplicationRuntimeSummaries({
      services,
      telemetry,
      deployments,
      exposures,
      placement,
      topology,
      bindings,
      resources,
      nodes,
      agentAvailable,
    });

    const servers = deriveServerRuntimeSummaries({
      nodes,
      telemetry,
      placement,
      topology,
      services,
      agentAvailable,
    });

    const managedResources = deriveResourceRuntimeSummaries({
      resources,
      bindings,
      nodes,
      topology,
      placement,
    });

    const overview = deriveRuntimeOverview({
      applications,
      servers,
      resources: managedResources,
      deployments,
      incidents,
      audit,
    });

    const hasAnyFailure =
      summaryRes.status === "rejected" ||
      servicesRes.status === "rejected" ||
      nodesRes.status === "rejected" ||
      resourcesRes.status === "rejected" ||
      deploymentsRes.status === "rejected" ||
      incidentsRes.status === "rejected";

    setData((previous) => ({
      ...previous,
      summary,
      telemetry,
      services,
      nodes,
      resources,
      bindings,
      deployments,
      exposures,
      placement,
      topology,
      audit,
      incidents,
      applications,
      servers,
      managedResources,
      overview,
      sources: {
        registry: services.length ? "ready" : "empty",
        telemetry: summaryRes.status === "fulfilled" ? (telemetry.length ? "ready" : "empty") : previous.summary ? "partial" : "unavailable",
        incidents: incidentsRes.status === "fulfilled" ? (incidents.length ? "ready" : "empty") : previous.incidents.length ? "partial" : "unavailable",
        support: console.state.support ? "ready" : "unavailable",
        nodes: nodesRes.status === "fulfilled" ? (nodes.length ? "ready" : "empty") : "unavailable",
        resources: resourcesRes.status === "fulfilled" ? (resources.length ? "ready" : "empty") : "unavailable",
        deployments: deploymentsRes.status === "fulfilled" ? (deployments.length ? "ready" : "empty") : "unavailable",
      },
      error: hasAnyFailure ? "A refresh source failed; last factual data is preserved." : "",
    }));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [client, console.session?.agent_connected, console.state.support, environmentID, projectID]);

  const loadLogs = useCallback(
    async (params: { serviceID?: string; cursor?: string; level?: string; query?: string } = {}) => {
      if (!projectID) return { logs: [] };
      setData((previous) => ({
        ...previous,
        logs: { ...previous.logs, source: previous.logs.rows.length ? previous.logs.source : "loading", error: "" },
      }));
      try {
        const result = await client.logs(projectID, { serviceID: params.serviceID, cursor: params.cursor, limit: 100 });
        setData((previous) => ({
          ...previous,
          logs: {
            rows: params.cursor ? [...previous.logs.rows, ...(result.logs ?? [])] : result.logs ?? [],
            nextCursor: result.next_cursor,
            source: result.logs?.length ? "ready" : "empty",
            error: "",
          },
        }));
        return result;
      } catch (error) {
        setData((previous) => ({
          ...previous,
          logs: {
            ...previous.logs,
            source: previous.logs.rows.length ? "partial" : "unavailable",
            error: (error as Error).message,
          },
        }));
        throw error;
      }
    },
    [client, projectID],
  );

  const selectIncident = useCallback(
    async (incidentID: string) => {
      if (!projectID || !incidentID) return;
      const current = ++incidentSequence.current;
      setData((previous) => ({
        ...previous,
        selectedIncident: previous.incidents.find((item) => item.incident_id === incidentID) ?? null,
        evidence: null,
        evidenceState: "loading",
        evidenceError: "",
        sources: { ...previous.sources, incidents: "loading" },
      }));
      const [incidentRes, evidenceRes] = await Promise.allSettled([
        client.incident(projectID, incidentID),
        client.incidentEvidence(projectID, incidentID),
      ]);
      if (current !== incidentSequence.current) return;
      const incident = incidentRes.status === "fulfilled" ? incidentRes.value.incident : data.incidents.find((item) => item.incident_id === incidentID) ?? null;
      const validEvidence = evidenceRes.status === "fulfilled" && isIncidentEvidence(evidenceRes.value);
      setData((previous) => ({
        ...previous,
        selectedIncident: incident,
        evidence: validEvidence ? evidenceRes.value : null,
        evidenceState: validEvidence ? "ready" : "unavailable",
        sources: { ...previous.sources, incidents: incident ? "ready" : "unavailable" },
        evidenceError:
          evidenceRes.status === "rejected"
            ? (evidenceRes.reason as Error).message
            : validEvidence
            ? ""
            : "Incident evidence failed structural validation.",
      }));
    },
    [client, data.incidents, projectID],
  );

  const getApplicationEvents = useCallback(
    (serviceID: string, serviceKey: string): RuntimeEvent[] => {
      return deriveApplicationEvents(serviceID, serviceKey, data.deployments, data.audit);
    },
    [data.audit, data.deployments],
  );

  useEffect(() => {
    if (!projectID) {
      setData(emptyData);
      return;
    }
    void load();
  }, [projectID, environmentID]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    const incidentID = console.route.incident;
    if (incidentID && data.incidents.some((item) => item.incident_id === incidentID) && data.selectedIncident?.incident_id !== incidentID) {
      void selectIncident(incidentID);
    }
  }, [console.route.incident, data.incidents, data.selectedIncident?.incident_id, selectIncident]);

  return useMemo(
    () => ({ data, load, loadLogs, selectIncident, getApplicationEvents }),
    [data, getApplicationEvents, load, loadLogs, selectIncident],
  );
}

function seedData(console: ConsoleController): ObservabilityData {
  const foundation = console.state.foundation;
  const services = console.state.services;
  const nodes = console.state.nodes;
  const deployments = console.state.deployments;
  const audit = console.state.audit;
  const incidents = foundation.incidents;
  const support = console.state.support;
  const telemetry = foundation.telemetry;
  const agentAvailable = console.session?.agent_connected === "ok";

  const applications = deriveApplicationRuntimeSummaries({
    services,
    telemetry,
    deployments,
    placement: foundation.placement,
    topology: foundation.topology,
    nodes,
    agentAvailable,
  });

  const servers = deriveServerRuntimeSummaries({
    nodes,
    telemetry,
    placement: foundation.placement,
    topology: foundation.topology,
    services,
    agentAvailable,
  });

  const managedResources: ResourceRuntimeSummary[] = [];

  const overview = deriveRuntimeOverview({
    applications,
    servers,
    resources: managedResources,
    deployments,
    incidents,
    audit,
  });

  return {
    ...emptyData,
    services,
    nodes,
    deployments,
    audit,
    telemetry,
    incidents,
    placement: foundation.placement,
    topology: foundation.topology,
    applications,
    servers,
    managedResources,
    overview,
    sources: {
      registry: services.length ? "ready" : "empty",
      telemetry: foundation.sources.telemetry === "available" ? (telemetry.length ? "ready" : "empty") : foundation.sources.telemetry === "unavailable" ? "unavailable" : "empty",
      incidents: foundation.sources.incidents === "available" ? (incidents.length ? "ready" : "empty") : foundation.sources.incidents === "unavailable" ? "unavailable" : "empty",
      support: support ? "ready" : "unavailable",
      nodes: nodes.length ? "ready" : "empty",
      resources: "empty",
      deployments: deployments.length ? "ready" : "empty",
    },
  };
}

export function overallHealth(data: ObservabilityData, services: number, agentAvailable: boolean) {
  if (!services) return "unknown";
  if (!agentAvailable || data.sources.telemetry === "unavailable" || data.sources.incidents === "unavailable") return "unknown";
  if (data.sources.telemetry === "partial" || data.sources.incidents === "partial" || data.telemetry.some((item) => item.ready_pods < item.pod_count || ["degraded", "failed", "critical"].includes(item.health))) return "degraded";
  return data.sources.telemetry === "ready" ? "healthy" : "unknown";
}

export function safeLogMessage(message: string) {
  return /(?:authorization:\s*bearer|password\s*=|token\s*=|private[_ -]?key|secret\s*=)/i.test(message)
    ? "[message hidden: backend redaction contract violation]"
    : message;
}

const MAX_EVIDENCE_COVERAGE = 32;
const MAX_EVIDENCE_TIMELINE = 256;
const MAX_EVIDENCE_PODS = 256;
const MAX_EVIDENCE_ITEMS = 256;

export function isIncidentEvidence(value: unknown): value is IncidentEvidence {
  if (!isRecord(value) || value.schema_version !== "opsi.incident_evidence/v1" || typeof value.content_sha256 !== "string" || !/^[a-f0-9]{64}$/.test(value.content_sha256)) return false;
  if (!isIncidentIdentity(value.identity) || !timestamp(value.generated_at_unix) || !isRecord(value.observation_window) || !timestamp(value.observation_window.start_unix) || !timestamp(value.observation_window.end_unix) || value.observation_window.start_unix > value.observation_window.end_unix) return false;
  if (!isRecord(value.deployment) || !isRecord(value.rollout) || !boundedObject(value.deployment) || !boundedObject(value.rollout)) return false;
  if (!boundedArray(value.coverage, MAX_EVIDENCE_COVERAGE, isCoverage)) return false;
  if (!optionalArray(value.timeline, MAX_EVIDENCE_TIMELINE, (item) => isRecord(item) && timestamp(item.observed_at_unix) && text(item.source) && text(item.kind) && optionalText(item.detail) && typeof item.untrusted_content === "boolean")) return false;
  if (!optionalArray(value.pods, MAX_EVIDENCE_PODS, (item) => isRecord(item) && text(item.pod_id) && optionalText(item.namespace) && optionalText(item.node_id) && count(item.ready_containers) && count(item.total_containers) && item.ready_containers <= item.total_containers && count(item.restart_count) && optionalText(item.observed_digest))) return false;
  if (!optionalArray(value.kubernetes_events, MAX_EVIDENCE_ITEMS, (item) => isRecord(item) && timestamp(item.observed_at_unix) && optionalText(item.namespace) && optionalText(item.object_kind) && optionalText(item.object_name) && optionalText(item.type) && optionalText(item.reason) && optionalText(item.message) && typeof item.untrusted_content === "boolean")) return false;
  if (!optionalArray(value.log_fingerprints, MAX_EVIDENCE_ITEMS, (item) => isRecord(item) && text(item.fingerprint) && optionalText(item.level) && count(item.count) && timestamp(item.first_observed_unix) && timestamp(item.last_observed_unix) && item.first_observed_unix <= item.last_observed_unix && optionalText(item.excerpt) && typeof item.untrusted_content === "boolean")) return false;
  if (!optionalArray(value.audit_references, MAX_EVIDENCE_ITEMS, (item) => isRecord(item) && text(item.audit_id) && text(item.action) && text(item.resource_type) && text(item.resource_id) && text(item.result) && timestamp(item.created_at_unix))) return false;
  if (!optionalArray(value.truncations, 32, (item) => isRecord(item) && text(item.section) && count(item.omitted_items) && typeof item.utf8_safe === "boolean")) return false;
  return true;
}

function isIncidentIdentity(value: unknown) {
  return isRecord(value) && text(value.incident_id) && text(value.project_id) && text(value.status) && optionalText(value.service_id) && optionalText(value.node_id) && optionalText(value.pod_id) && (value.created_at_unix === undefined || timestamp(value.created_at_unix));
}

function isCoverage(value: unknown) {
  return isRecord(value) && text(value.source) && text(value.status) && optionalText(value.reason_code) && count(value.item_count) && typeof value.truncated === "boolean";
}

function optionalArray(value: unknown, maximum: number, validate: (item: unknown) => boolean) {
  return value === undefined || boundedArray(value, maximum, validate);
}

function boundedArray(value: unknown, maximum: number, validate: (item: unknown) => boolean) {
  return Array.isArray(value) && value.length <= maximum && value.every(validate);
}

function boundedObject(value: Record<string, unknown>) {
  return Object.keys(value).length <= 16 && Object.values(value).every((item) => item === undefined || typeof item === "string" && item.length <= 512 || Array.isArray(item) && item.length <= 32 && item.every(text));
}

function isRecord(value: unknown): value is Record<string, unknown> { return Boolean(value) && typeof value === "object" && !Array.isArray(value); }
function text(value: unknown): value is string { return typeof value === "string" && value.length > 0 && value.length <= 512; }
function optionalText(value: unknown) { return value === undefined || typeof value === "string" && value.length <= 2048; }
function count(value: unknown): value is number { return Number.isSafeInteger(value) && (value as number) >= 0; }
function timestamp(value: unknown): value is number { return Number.isSafeInteger(value) && (value as number) > 0; }
