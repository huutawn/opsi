import type {
  AuditEvent,
  BootstrapSession,
  DeploymentJob,
  NodeDiagnostics,
  NodeRecord,
  Project,
  Readiness,
  ServiceRecord,
  SecretResult,
  IncidentResult,
  SupportSummary,
  TelemetryQueryResponse,
  TelemetrySummary,
  TimelineEvent,
} from "@/lib/contracts/registry";

type RequestOptions = RequestInit & { write?: boolean };

export class LocalClient {
  private localSession = "";

  async session() {
    return this.call<{ local_session?: string }>("/api/local/session");
  }

  async projects(orgID: string) {
    return this.call<{ projects: Project[] }>(`/api/local/projects?org_id=${encodeURIComponent(orgID)}`);
  }

  createProject(orgID: string, body: { name: FormDataEntryValue | null; slug: FormDataEntryValue | null }) {
    return this.call<Project>(`/api/local/projects?org_id=${encodeURIComponent(orgID)}`, {
      method: "POST",
      write: true,
      body: JSON.stringify(body),
    });
  }

  readiness(projectID: string) {
    return this.call<Readiness>(`/api/local/projects/${projectID}/readiness`);
  }

  nodes(projectID: string) {
    return this.call<NodeRecord[]>(`/api/local/projects/${projectID}/nodes`);
  }

  node(projectID: string, nodeID: string) {
    return this.call<NodeDiagnostics>(`/api/local/projects/${projectID}/nodes/${nodeID}`);
  }

  nodeAction(projectID: string, nodeID: string, action: "drain" | "remove") {
    return this.call<NodeRecord>(`/api/local/projects/${projectID}/nodes/${nodeID}/${action}`, { method: "POST", write: true });
  }

  createBootstrap(projectID: string, body: Record<string, unknown>) {
    return this.call<BootstrapSession>(`/api/local/projects/${projectID}/bootstrap-sessions`, {
      method: "POST",
      write: true,
      body: JSON.stringify(body),
    });
  }

  bootstrapSessions(projectID: string) {
    return this.call<{ sessions: BootstrapSession[] }>(`/api/local/projects/${projectID}/bootstrap-sessions`);
  }

  bootstrapEvents(projectID: string, sessionID: string) {
    return this.call<TimelineEvent[]>(`/api/local/projects/${projectID}/bootstrap-sessions/${sessionID}/events`);
  }

  services(projectID: string) {
    return this.call<{ services: ServiceRecord[] }>(`/api/local/projects/${projectID}/services`);
  }

  createService(projectID: string, body: Record<string, unknown>) {
    return this.call<ServiceRecord>(`/api/local/projects/${projectID}/services`, {
      method: "POST",
      write: true,
      body: JSON.stringify(body),
    });
  }

  deploy(projectID: string, serviceID: string) {
    return this.call<DeploymentJob>(`/api/local/projects/${projectID}/services/${serviceID}/deployments`, {
      method: "POST",
      write: true,
      body: JSON.stringify({ requested_by: "cli-ui" }),
    });
  }

  rollback(projectID: string, deploymentID: string) {
    return this.call<DeploymentJob>(`/api/local/projects/${projectID}/deployments/${deploymentID}/rollback`, {
      method: "POST",
      write: true,
      body: JSON.stringify({ requested_by: "cli-ui" }),
    });
  }

  deployments(projectID: string) {
    return this.call<{ deployments: DeploymentJob[] }>(`/api/local/projects/${projectID}/deployments`);
  }

  deploymentEvents(projectID: string, deploymentID: string) {
    return this.call<{ events: TimelineEvent[] }>(`/api/local/projects/${projectID}/deployments/${deploymentID}/events`);
  }

  audit(projectID: string) {
    return this.call<{ events: AuditEvent[] }>(`/api/local/projects/${projectID}/audit`);
  }

  support(projectID: string) {
    return this.call<SupportSummary>(`/api/local/projects/${projectID}/support`);
  }

  telemetrySummary(projectID: string, sinceUnix = 0) {
    return this.call<TelemetrySummary>(
      `/api/local/projects/${projectID}/telemetry/summary?since_unix=${encodeURIComponent(String(sinceUnix))}`,
    );
  }

  telemetryService(projectID: string, serviceID: string, sinceUnix = 0) {
    return this.call<TelemetryQueryResponse>(
      `/api/local/projects/${projectID}/telemetry/services/${encodeURIComponent(serviceID)}?since_unix=${encodeURIComponent(String(sinceUnix))}`,
    );
  }

  logs(projectID: string, params: { serviceID?: string; cursor?: string; limit?: number } = {}) {
    const query = new URLSearchParams();
    if (params.serviceID) query.set("service_id", params.serviceID);
    if (params.cursor) query.set("cursor", params.cursor);
    if (params.limit) query.set("limit", String(params.limit));
    const suffix = query.toString() ? `?${query.toString()}` : "";
    return this.call<TelemetryQueryResponse>(`/api/local/projects/${projectID}/logs${suffix}`);
  }

  createSecret(projectID: string, body: Record<string, unknown>) {
    return this.call<SecretResult>(`/api/local/projects/${projectID}/secrets`, {
      method: "POST",
      write: true,
      body: JSON.stringify(body),
    });
  }

  revealSecret(projectID: string, name: string, body: Record<string, unknown>) {
    return this.call<SecretResult>(`/api/local/projects/${projectID}/secrets/${encodeURIComponent(name)}/reveal`, {
      method: "POST",
      write: true,
      body: JSON.stringify({ ...body, reveal: true }),
    });
  }

  rotateSecret(projectID: string, name: string, body: Record<string, unknown>) {
    return this.call<SecretResult>(`/api/local/projects/${projectID}/secrets/${encodeURIComponent(name)}/rotate`, {
      method: "POST",
      write: true,
      body: JSON.stringify(body),
    });
  }

  analyzeIncident(projectID: string, incidentID: string, body: Record<string, unknown>) {
    return this.call<IncidentResult>(`/api/local/projects/${projectID}/incidents/${encodeURIComponent(incidentID)}/analyze`, {
      method: "POST",
      write: true,
      body: JSON.stringify(body),
    });
  }

  approveIncident(projectID: string, incidentID: string, actionID: string, body: Record<string, unknown>) {
    return this.call<IncidentResult>(
      `/api/local/projects/${projectID}/incidents/${encodeURIComponent(incidentID)}/actions/${encodeURIComponent(actionID)}/approve`,
      {
        method: "POST",
        write: true,
        body: JSON.stringify({ ...body, approved: true }),
      },
    );
  }

  resolveIncident(projectID: string, incidentID: string, body: Record<string, unknown>) {
    return this.call<IncidentResult>(`/api/local/projects/${projectID}/incidents/${encodeURIComponent(incidentID)}/resolve`, {
      method: "POST",
      write: true,
      body: JSON.stringify(body),
    });
  }

  private async call<T>(path: string, init: RequestOptions = {}) {
    const headers = new Headers(init.headers);
    headers.set("content-type", "application/json");
    headers.set("X-Request-ID", crypto.randomUUID());
    if (init.write) {
      headers.set("Idempotency-Key", crypto.randomUUID());
      headers.set("X-Local-Session", await this.getLocalSession());
    }

    const res = await fetch(path, { ...init, headers });
    const text = await res.text();
    const data = text ? JSON.parse(text) : {};
    if (!res.ok) {
      const payload = data.error ?? data;
      const error = new Error(payload.message ?? payload.error ?? payload.error_code ?? "request failed");
      Object.assign(error, { status: res.status, data });
      throw error;
    }
    return data as T;
  }

  private async getLocalSession() {
    if (this.localSession) return this.localSession;
    const session = await this.session();
    this.localSession = session.local_session ?? "";
    return this.localSession;
  }
}
