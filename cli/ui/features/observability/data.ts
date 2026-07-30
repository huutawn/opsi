"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { LocalClient } from "@/lib/api/local-client";
import type { IncidentEvidence, IncidentResponse, TelemetryLogEntry, TelemetryServiceStatus, TelemetrySummary } from "@/lib/contracts/registry";
import type { ConsoleController } from "@/features/console/types";

export type SourceState = "loading" | "ready" | "empty" | "unavailable" | "partial";
export type SourceStates = { registry: SourceState; telemetry: SourceState; incidents: SourceState; support: SourceState };
export type LogState = { rows: TelemetryLogEntry[]; nextCursor?: string; source: SourceState; error: string };
export type ObservabilityData = {
  summary: TelemetrySummary | null;
  telemetry: TelemetryServiceStatus[];
  incidents: IncidentResponse[];
  selectedIncident: IncidentResponse | null;
  evidence: IncidentEvidence | null;
  evidenceState: SourceState;
  evidenceError: string;
  sources: SourceStates;
  logs: LogState;
  error: string;
};

const emptyLogs: LogState = { rows: [], source: "empty", error: "" };
const emptyData: ObservabilityData = { summary: null, telemetry: [], incidents: [], selectedIncident: null, evidence: null, evidenceState: "empty", evidenceError: "", sources: { registry: "empty", telemetry: "empty", incidents: "empty", support: "empty" }, logs: emptyLogs, error: "" };

export function useObservabilityData(console: ConsoleController) {
  const projectID = console.state.project?.id ?? "";
  const services = console.state.services;
  const client = useMemo(() => new LocalClient(), []);
  const sequence = useRef(0);
  const incidentSequence = useRef(0);
  const logSequence = useRef(0);
  const [data, setData] = useState<ObservabilityData>(() => seedData(console));
  const load = useCallback(async () => {
    if (!projectID) return;
    const current = ++sequence.current;
    setData((previous) => ({ ...previous, error: "", sources: { ...previous.sources, telemetry: previous.summary ? previous.sources.telemetry : "loading", incidents: previous.incidents.length ? previous.sources.incidents : "loading" } }));
    const [summary, serviceResults, incidents] = await Promise.allSettled([
      client.telemetrySummary(projectID),
      Promise.all(services.map((service) => client.telemetryService(projectID, service.id))),
      client.incidents(projectID),
    ]);
    if (current !== sequence.current) return;
    setData((previous) => {
      const telemetry = serviceResults.status === "fulfilled" ? serviceResults.value.flatMap((item) => item.services ?? []) : previous.telemetry;
      const nextSummary = summary.status === "fulfilled" ? summary.value : previous.summary;
      const nextIncidents = incidents.status === "fulfilled" ? incidents.value.incidents ?? [] : previous.incidents;
      return { ...previous, summary: nextSummary, telemetry, incidents: nextIncidents, sources: { ...previous.sources, registry: "ready", telemetry: summary.status === "fulfilled" && serviceResults.status === "fulfilled" ? telemetry.length ? "ready" : "empty" : previous.summary ? "partial" : "unavailable", incidents: incidents.status === "fulfilled" ? nextIncidents.length ? "ready" : "empty" : previous.incidents.length ? "partial" : "unavailable" }, error: summary.status === "rejected" || serviceResults.status === "rejected" || incidents.status === "rejected" ? "A refresh source failed; last factual data is preserved." : "" };
    });
  }, [client, projectID, services]);

  const loadLogs = useCallback(async (params: { serviceID?: string; cursor?: string; level?: string; query?: string } = {}) => {
    if (!projectID) return;
    const current = ++logSequence.current;
    setData((previous) => ({ ...previous, logs: { ...previous.logs, source: previous.logs.rows.length ? previous.logs.source : "loading", error: "" } }));
    try {
      const result = await client.logs(projectID, { serviceID: params.serviceID, cursor: params.cursor, limit: 100 });
      if (current !== logSequence.current) return;
      setData((previous) => ({ ...previous, logs: { rows: params.cursor ? [...previous.logs.rows, ...(result.logs ?? [])] : result.logs ?? [], nextCursor: result.next_cursor, source: result.logs?.length ? "ready" : "empty", error: "" } }));
    } catch (error) {
      if (current !== logSequence.current) return;
      setData((previous) => ({ ...previous, logs: { ...previous.logs, source: previous.logs.rows.length ? "partial" : "unavailable", error: (error as Error).message } }));
    }
  }, [client, projectID]);

  const selectIncident = useCallback(async (incidentID: string) => {
    if (!projectID || !incidentID) return;
    const current = ++incidentSequence.current;
    setData((previous) => ({ ...previous, selectedIncident: previous.incidents.find((item) => item.incident_id === incidentID) ?? null, evidence: null, evidenceState: "loading", evidenceError: "" }));
    const [incident, evidence] = await Promise.allSettled([client.incident(projectID, incidentID), client.incidentEvidence(projectID, incidentID)]);
    if (current !== incidentSequence.current) return;
    const validEvidence = evidence.status === "fulfilled" && isIncidentEvidence(evidence.value);
    setData((previous) => ({ ...previous, selectedIncident: incident.status === "fulfilled" ? incident.value.incident : previous.selectedIncident, evidence: validEvidence ? evidence.value : null, evidenceState: validEvidence ? "ready" : "unavailable", evidenceError: evidence.status === "rejected" ? (evidence.reason as Error).message : validEvidence ? "" : "Incident evidence failed structural validation." }));
  }, [client, projectID]);

  useEffect(() => {
    sequence.current++;
    incidentSequence.current++;
    logSequence.current++;
    queueMicrotask(() => {
      setData(projectID ? seedData(console) : emptyData);
      void load();
    });
  }, [projectID]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    const incidentID = console.route.incident;
    if (incidentID && data.incidents.some((item) => item.incident_id === incidentID) && data.selectedIncident?.incident_id !== incidentID) void selectIncident(incidentID);
  }, [console.route.incident, data.incidents, data.selectedIncident?.incident_id, selectIncident]);

  return { data, load, loadLogs, selectIncident };
}

function seedData(console: ConsoleController): ObservabilityData {
  const foundation = console.state.foundation;
  const incidents = foundation.incidents;
  const support = console.state.support;
  return { ...emptyData, telemetry: foundation.telemetry, incidents, sources: { registry: console.state.services.length ? "ready" : "empty", telemetry: foundation.sources.telemetry === "available" ? foundation.telemetry.length ? "ready" : "empty" : foundation.sources.telemetry === "unavailable" ? "unavailable" : "empty", incidents: foundation.sources.incidents === "available" ? incidents.length ? "ready" : "empty" : foundation.sources.incidents === "unavailable" ? "unavailable" : "empty", support: support ? "ready" : "unavailable" } };
}

export function overallHealth(data: ObservabilityData, services: number, agentAvailable: boolean) {
  if (!services) return "unknown";
  if (!agentAvailable || data.sources.telemetry === "unavailable" || data.sources.incidents === "unavailable") return "unknown";
  if (data.sources.telemetry === "partial" || data.sources.incidents === "partial" || data.telemetry.some((item) => item.ready_pods < item.pod_count || ["degraded", "failed", "critical"].includes(item.health))) return "degraded";
  return data.sources.telemetry === "ready" ? "healthy" : "unknown";
}

export function safeLogMessage(message: string) {
  return /(?:authorization:\s*bearer|password\s*=|token\s*=|private[_ -]?key|secret\s*=)/i.test(message) ? "[message hidden: backend redaction contract violation]" : message;
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
