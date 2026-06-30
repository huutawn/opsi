import type {
  AuditEvent,
  BootstrapSession,
  DeploymentJob,
  NodeDiagnostics,
  NodeRecord,
  Project,
  Readiness,
  ServiceRecord,
  TimelineEvent,
} from "@/lib/contracts/registry";

type ClientConfig = {
  cloudURL: string;
  pat: string;
};

type RequestOptions = RequestInit & { write?: boolean };

export class RegistryClient {
  constructor(private readonly config: ClientConfig) {}

  async projects(orgID: string) {
    return this.call<{ projects: Project[] }>(`/api/orgs/${encodeURIComponent(orgID)}/projects`);
  }

  createProject(orgID: string, body: { name: FormDataEntryValue | null; slug: FormDataEntryValue | null }) {
    return this.call<Project>(`/api/orgs/${encodeURIComponent(orgID)}/projects`, {
      method: "POST",
      write: true,
      body: JSON.stringify(body),
    });
  }

  readiness(projectID: string) {
    return this.call<Readiness>(`/api/projects/${projectID}/readiness`);
  }

  nodes(projectID: string) {
    return this.call<NodeRecord[]>(`/api/projects/${projectID}/nodes`);
  }

  node(projectID: string, nodeID: string) {
    return this.call<NodeDiagnostics>(`/api/projects/${projectID}/nodes/${nodeID}`);
  }

  nodeAction(projectID: string, nodeID: string, action: "drain" | "remove") {
    return this.call<NodeRecord>(`/api/projects/${projectID}/nodes/${nodeID}/${action}`, { method: "POST", write: true });
  }

  createBootstrap(projectID: string, body: Record<string, unknown>) {
    return this.call<BootstrapSession>(`/api/projects/${projectID}/bootstrap-sessions`, {
      method: "POST",
      write: true,
      body: JSON.stringify(body),
    });
  }

  bootstrapSessions(projectID: string) {
    return this.call<{ sessions: BootstrapSession[] }>(`/api/projects/${projectID}/bootstrap-sessions`);
  }

  bootstrapEvents(projectID: string, sessionID: string) {
    return this.call<TimelineEvent[]>(`/api/projects/${projectID}/bootstrap-sessions/${sessionID}/events`);
  }

  services(projectID: string) {
    return this.call<{ services: ServiceRecord[] }>(`/api/projects/${projectID}/services`);
  }

  createService(projectID: string, body: Record<string, FormDataEntryValue | null>) {
    return this.call<ServiceRecord>(`/api/projects/${projectID}/services`, {
      method: "POST",
      write: true,
      body: JSON.stringify(body),
    });
  }

  deploy(projectID: string, serviceID: string) {
    return this.call<DeploymentJob>(`/api/projects/${projectID}/services/${serviceID}/deployments`, {
      method: "POST",
      write: true,
      body: JSON.stringify({ requested_by: "cli-ui" }),
    });
  }

  deployments(projectID: string) {
    return this.call<{ deployments: DeploymentJob[] }>(`/api/projects/${projectID}/deployments`);
  }

  deploymentEvents(projectID: string, deploymentID: string) {
    return this.call<{ events: TimelineEvent[] }>(`/api/projects/${projectID}/deployments/${deploymentID}/events`);
  }

  audit(projectID: string) {
    return this.call<{ events: AuditEvent[] }>(`/api/projects/${projectID}/audit`);
  }

  private async call<T>(path: string, init: RequestOptions = {}) {
    const headers = new Headers(init.headers);
    headers.set("content-type", "application/json");
    headers.set("X-Request-ID", crypto.randomUUID());
    if (init.write) headers.set("Idempotency-Key", crypto.randomUUID());
    if (this.config.pat.trim()) headers.set("Authorization", `Bearer ${this.config.pat.trim()}`);

    const res = await fetch(`${this.config.cloudURL.replace(/\/$/, "")}${path}`, { ...init, headers });
    const text = await res.text();
    const data = text ? JSON.parse(text) : {};
    if (!res.ok) {
      const error = new Error(data.message ?? data.error ?? data.error_code ?? "request failed");
      Object.assign(error, { status: res.status, data });
      throw error;
    }
    return data as T;
  }
}
