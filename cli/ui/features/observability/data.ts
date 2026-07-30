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

function isIncidentEvidence(value: unknown): value is IncidentEvidence {
  if (!value || typeof value !== "object") return false;
  const evidence = value as Partial<IncidentEvidence>;
  return evidence.schema_version === "opsi.incident_evidence/v1"
    && typeof evidence.content_sha256 === "string" && evidence.content_sha256.length > 0
    && typeof evidence.observation_window?.start_unix === "number" && typeof evidence.observation_window.end_unix === "number"
    && Boolean(evidence.deployment && evidence.rollout)
    && Array.isArray(evidence.coverage);
}
