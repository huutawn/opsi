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
  TelemetryCoverage,
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

export type SourceState = "loading" | "ready" | "fresh" | "stale" | "partial" | "unavailable" | "empty";
export type SourceDetail = {
  state: SourceState;
  observedAt?: number | string;
  errorRedacted?: string;
  coverage?: TelemetryCoverage;
};

export type SourceStates = {
  registry: SourceState;
  telemetry: SourceState;
  incidents: SourceState;
  support: SourceState;
  nodes: SourceState;
  resources: SourceState;
  deployments: SourceState;
};

export type SourceDetails = {
  registry: SourceDetail;
  telemetry: SourceDetail;
  incidents: SourceDetail;
  support: SourceDetail;
  nodes: SourceDetail;
  resources: SourceDetail;
  deployments: SourceDetail;
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
  sourceDetails: SourceDetails;
  failingSources: string[];
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

const emptyDetails: SourceDetails = {
  registry: { state: "empty" },
  telemetry: { state: "empty" },
  incidents: { state: "empty" },
  support: { state: "empty" },
  nodes: { state: "empty" },
  resources: { state: "empty" },
  deployments: { state: "empty" },
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
  sourceDetails: emptyDetails,
  failingSources: [],
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
  const dataRef = useRef(data);
  dataRef.current = data;

  const load = useCallback(async () => {
    if (!projectID) return;
    const previous = dataRef.current;
    const current = ++sequence.current;
    setData((prev) => ({
      ...prev,
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

    const windowParam = console.route.window || "1h";
    const windowSeconds = windowParam === "24h" ? 86400 : windowParam === "6h" ? 21600 : 3600;
    const sinceUnix = Math.floor(Date.now() / 1000) - windowSeconds;

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
      client.telemetrySummary(projectID, sinceUnix, windowParam),
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

    const failingSources: string[] = [];

    // Registry / services
    let services = console.state.services;
    let registryState: SourceState = "empty";
    let registryError: string | undefined;
    if (servicesRes.status === "fulfilled") {
      services = servicesRes.value.services ?? [];
      registryState = services.length ? "fresh" : "empty";
    } else {
      failingSources.push("Cloud registry");
      registryError = servicesRes.reason instanceof Error ? servicesRes.reason.message : "Registry unavailable";
      if (previous.services.length) {
        services = previous.services;
        registryState = "stale";
      } else {
        registryState = "unavailable";
      }
    }

    // Nodes
    let nodes = console.state.nodes;
    let nodesState: SourceState = "empty";
    let nodesError: string | undefined;
    if (nodesRes.status === "fulfilled") {
      nodes = nodesRes.value ?? [];
      nodesState = nodes.length ? "fresh" : "empty";
    } else {
      failingSources.push("Server nodes");
      nodesError = nodesRes.reason instanceof Error ? nodesRes.reason.message : "Nodes unavailable";
      if (previous.nodes.length) {
        nodes = previous.nodes;
        nodesState = "stale";
      } else {
        nodesState = "unavailable";
      }
    }

    // Telemetry snapshot
    let summary = previous.summary;
    let telemetry: TelemetryServiceStatus[] = previous.telemetry;
    let telemetryState: SourceState = "empty";
    let telemetryError: string | undefined;
    let telemetryCoverage: TelemetryCoverage | undefined;

    if (summaryRes.status === "fulfilled") {
      summary = summaryRes.value;
      telemetryCoverage = summary.coverage;
      telemetry = summary.services ?? [];
      if (telemetryCoverage?.status === "partial") {
        telemetryState = "partial";
      } else if (telemetryCoverage?.status === "unavailable") {
        telemetryState = previous.summary ? "stale" : "unavailable";
      } else {
        telemetryState = telemetry.length || (summary.record_count && summary.record_count > 0) || services.length === 0 ? "fresh" : "empty";
      }
    } else {
      failingSources.push("Agent telemetry");
      telemetryError = summaryRes.reason instanceof Error ? summaryRes.reason.message : "Telemetry unavailable";
      if (summaryRes.reason && typeof summaryRes.reason === "object" && "coverage" in summaryRes.reason) {
        const reasonObj = summaryRes.reason;
        if (reasonObj.coverage && typeof reasonObj.coverage === "object") {
          telemetryCoverage = reasonObj.coverage as TelemetryCoverage;
        }
      }
      if (previous.summary) {
        summary = previous.summary;
        telemetry = previous.telemetry;
        telemetryState = "stale";
      } else {
        summary = null;
        telemetry = [];
        telemetryState = "unavailable";
      }
    }

    // Incidents
    let incidents = console.state.incidents;
    let incidentsState: SourceState = "empty";
    let incidentsError: string | undefined;
    let incidentsCoverage: TelemetryCoverage | undefined;
    if (incidentsRes.status === "fulfilled") {
      incidents = incidentsRes.value.incidents ?? [];
      incidentsCoverage = incidentsRes.value.coverage;
      if (incidentsCoverage?.status === "partial") {
        incidentsState = "partial";
      } else if (incidentsCoverage?.status === "unavailable") {
        incidentsState = previous.incidents.length ? "stale" : "unavailable";
      } else {
        incidentsState = incidents.length ? "fresh" : "empty";
      }
    } else {
      failingSources.push("Incident store");
      incidentsError = incidentsRes.reason instanceof Error ? incidentsRes.reason.message : "Incidents unavailable";
      if (incidentsRes.reason && typeof incidentsRes.reason === "object" && "coverage" in incidentsRes.reason) {
        const reasonObj = incidentsRes.reason;
        if (reasonObj.coverage && typeof reasonObj.coverage === "object") {
          incidentsCoverage = reasonObj.coverage as TelemetryCoverage;
        }
      }
      if (previous.incidents.length) {
        incidents = previous.incidents;
        incidentsState = "stale";
      } else {
        incidents = [];
        incidentsState = "unavailable";
      }
    }
    let resources = previous.resources;
    let resourcesState: SourceState = "empty";
    if (resourcesRes.status === "fulfilled") {
      resources = resourcesRes.value ?? [];
      resourcesState = resources.length ? "fresh" : "empty";
    } else {
      failingSources.push("Managed resources");
      resourcesState = previous.resources.length ? "stale" : "unavailable";
    }

    let deployments = console.state.deployments;
    let deploymentsState: SourceState = "empty";
    if (deploymentsRes.status === "fulfilled") {
      deployments = deploymentsRes.value.deployments ?? [];
      deploymentsState = deployments.length ? "fresh" : "empty";
    } else {
      failingSources.push("Delivery rollouts");
      deployments = previous.deployments;
      deploymentsState = previous.deployments.length ? "stale" : "unavailable";
    }

    const bindings = bindingsRes.status === "fulfilled" ? bindingsRes.value ?? [] : [];
    const exposures = exposuresRes.status === "fulfilled" ? exposuresRes.value.exposures ?? [] : [];
    const placement = placementRes.status === "fulfilled" ? placementRes.value : console.state.foundation.placement;
    const topology = topologyRes.status === "fulfilled" ? topologyRes.value : console.state.foundation.topology;
    const audit = auditRes.status === "fulfilled" ? auditRes.value.events ?? [] : console.state.audit;
    const agentAvailable = telemetryCoverage?.status === "connected" && telemetryState === "fresh";
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

    const sources: SourceStates = {
      registry: registryState,
      telemetry: telemetryState,
      incidents: incidentsState,
      support: console.state.support ? "ready" : "unavailable",
      nodes: nodesState,
      resources: resourcesState,
      deployments: deploymentsState,
    };

    const sourceDetails: SourceDetails = {
      registry: { state: registryState, errorRedacted: registryError, observedAt: Date.now() },
      telemetry: { state: telemetryState, errorRedacted: telemetryError, observedAt: summary?.end_unix || Date.now(), coverage: telemetryCoverage },
      incidents: { state: incidentsState, errorRedacted: incidentsError, observedAt: Date.now(), coverage: incidentsCoverage },
      support: { state: console.state.support ? "ready" : "unavailable", observedAt: Date.now() },
      nodes: { state: nodesState, errorRedacted: nodesError, observedAt: Date.now() },
      resources: { state: resourcesState, observedAt: Date.now() },
      deployments: { state: deploymentsState, observedAt: Date.now() },
    };

    const errorMessage = failingSources.length > 0
      ? `Refresh failed for: ${failingSources.join(", ")}; last factual data is preserved.`
      : "";

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
      sources,
      sourceDetails,
      failingSources,
      error: errorMessage,
    }));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [client, console.route.window, console.session?.agent_connected, console.state.support, environmentID, projectID]);

  const loadLogs = useCallback(
    async (params: { serviceID?: string; cursor?: string; level?: string; query?: string } = {}) => {
      if (!projectID) return { logs: [] };
      const windowParam = console.route.window || "1h";
      const windowSeconds = windowParam === "24h" ? 86400 : windowParam === "6h" ? 21600 : 3600;
      const sinceUnix = Math.floor(Date.now() / 1000) - windowSeconds;
      setData((previous) => ({
        ...previous,
        logs: { ...previous.logs, source: previous.logs.rows.length ? previous.logs.source : "loading", error: "" },
      }));
      try {
        const result = await client.logs(projectID, { serviceID: params.serviceID, cursor: params.cursor, limit: 100, sinceUnix, window: windowParam });
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
    [client, console.route.window, projectID],
  );

  const selectIncident = useCallback(
    async (incidentID: string, nodeID?: string) => {
      if (!projectID || !incidentID) return;
      const current = ++incidentSequence.current;
      setData((previous) => ({
        ...previous,
        selectedIncident: previous.incidents.find((item) => item.incident_id === incidentID && (!nodeID || item.node_id === nodeID)) ?? null,
        evidence: null,
        evidenceState: "loading",
        evidenceError: "",
        sources: { ...previous.sources, incidents: "loading" },
      }));
      const [incidentRes, evidenceRes] = await Promise.allSettled([
        client.incident(projectID, incidentID, nodeID),
        client.incidentEvidence(projectID, incidentID, nodeID),
      ]);
      if (current !== incidentSequence.current) return;
      const incident = incidentRes.status === "fulfilled" ? incidentRes.value.incident : data.incidents.find((item) => item.incident_id === incidentID && (!nodeID || item.node_id === nodeID)) ?? null;
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
            : !validEvidence && evidenceRes.status === "fulfilled"
              ? "Incident evidence payload failed schema validation"
              : "",
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
    const nodeID = console.route.node;
    if (incidentID && data.incidents.some((item) => item.incident_id === incidentID && (!nodeID || item.node_id === nodeID))) {
      if (data.selectedIncident?.incident_id !== incidentID || (nodeID && data.selectedIncident?.node_id !== nodeID)) {
        void selectIncident(incidentID, nodeID);
      }
    }
  }, [console.route.incident, console.route.node, data.incidents, data.selectedIncident?.incident_id, data.selectedIncident?.node_id, selectIncident]);

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
  if (!agentAvailable || data.sources.telemetry === "unavailable" || data.sources.incidents === "unavailable" || data.sources.telemetry === "stale") return "unknown";
  if (data.sources.telemetry === "partial" || data.sources.incidents === "partial" || data.telemetry.some((item) => item.ready_pods < item.pod_count || ["degraded", "failed", "critical"].includes(item.health))) return "degraded";
  return data.sources.telemetry === "fresh" || data.sources.telemetry === "ready" ? "healthy" : "unknown";
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
