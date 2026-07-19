import type {
  AuditEvent,
  BuildRecord,
  BuildRecordList,
  BootstrapSession,
  DeploymentJob,
  GitHubBinding,
  GitHubInstallation,
  GitHubRepository,
  NodeDiagnostics,
  NodeRecord,
  Project,
  Readiness,
  ServiceRecord,
  SecretResult,
  IncidentResult,
  IncidentListResult,
  SupportSummary,
  TelemetryQueryResponse,
  TelemetrySummary,
  TimelineEvent,
} from "@/lib/contracts/registry";

type RequestOptions = RequestInit & { write?: boolean; idempotencyKey?: string };
export type LocalSessionStatus = {
  authenticated: boolean;
  cloud_connected: "ok" | "failed" | "unknown";
  agent_connected: "ok" | "failed" | "unknown";
  token_status?: string;
  local_session?: string;
  org_id?: string;
  project_id?: string;
  capabilities?: string[];
};

export type RepositoryCDService = {
  key: string;
  build: { context: string; dockerfile: string; platform: string };
  watch_paths: string[];
  shared_paths: string[];
  dependencies: string[];
  deploy: {
    production: { enabled: boolean; branches: string[] };
    preview: { enabled: boolean; pull_requests: boolean };
  };
};

export type RepositoryCDConfig = { version: 2; services: RepositoryCDService[] };

export type RepositoryMutationPreview = {
  config: RepositoryCDConfig;
  migrated_v1: boolean;
  files: Array<{ path: string; action: "created" | "updated" | "unchanged"; old_sha256?: string; new_sha256: string }>;
  config_hash: string;
  config_yaml: string;
  workflow_yaml: string;
  config_diff: string;
  workflow_diff: string;
  preview_hash: string;
};

export type RepositoryMutationApplyResult = RepositoryMutationPreview & { reused: boolean };

export type RepositoryCDPlan = {
  schema_version: string;
  base: string;
  head: string;
  event: "initial" | "push" | "pull_request" | "merge";
  config_hash: string;
  plan_hash: string;
  full_build: boolean;
  affected_service_keys: string[];
  reason_codes: string[];
  services: Array<{ key: string; reasons: Array<{ code: string; explanation: string; path?: string; dependency?: string }> }>;
  explanation: string;
};

export class LocalClient {
  private localSession = "";

  async session(projectID?: string) {
    const query = projectID ? `?verify=1&project_id=${encodeURIComponent(projectID)}` : "?verify=1";
    return this.call<LocalSessionStatus>(`/api/local/session${query}`);
  }

  startLogin(projectID?: string) {
    return this.call<{ auth_url: string; status: string }>("/api/local/session/login/start", {
      method: "POST",
      body: JSON.stringify({ project_id: projectID ?? "" }),
    });
  }

  logout(projectID?: string) {
    this.localSession = "";
    return this.call<{ authenticated: false }>("/api/local/session/logout", {
      method: "POST",
      write: true,
      body: JSON.stringify({ project_id: projectID ?? "" }),
    });
  }

  rotatePAT(projectID?: string) {
    return this.call<{ rotated: boolean; revoked_old: boolean }>("/api/local/session/token/rotate", {
      method: "POST",
      write: true,
      body: JSON.stringify({ project_id: projectID ?? "" }),
    });
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

  buildRecords(projectID: string, filters: { serviceKey?: string; repositoryID?: string; sha?: string; status?: string; cursor?: string } = {}) {
    const query = new URLSearchParams({ limit: "50" });
    if (filters.serviceKey) query.set("service_key", filters.serviceKey);
    if (filters.repositoryID) query.set("repository_id", filters.repositoryID);
    if (filters.sha) query.set("sha", filters.sha);
    if (filters.status) query.set("status", filters.status);
    if (filters.cursor) query.set("cursor", filters.cursor);
    return this.call<BuildRecordList>(`/api/local/projects/${projectID}/build-records?${query}`);
  }

  buildRecord(projectID: string, recordID: string) {
    return this.call<BuildRecord>(`/api/local/projects/${projectID}/build-records/${encodeURIComponent(recordID)}`);
  }

  createService(projectID: string, body: Record<string, unknown>) {
    return this.call<ServiceRecord>(`/api/local/projects/${projectID}/services`, {
      method: "POST",
      write: true,
      body: JSON.stringify(body),
    });
  }

  githubInstallations(projectID: string) {
    return this.call<{ installations: GitHubInstallation[] }>(`/api/local/projects/${projectID}/github/installations`);
  }

  startGitHubInstallationClaim(projectID: string, installationID: number) {
    return this.call<{ authorization_url: string; status: string; expires_at: string }>(
      `/api/local/projects/${projectID}/github/installations/${installationID}/claim/start`,
      { method: "POST", write: true, body: "{}" },
    );
  }

  githubRepositories(projectID: string) {
    return this.call<{ repositories: GitHubRepository[] }>(`/api/local/projects/${projectID}/github/repositories`);
  }

  claimGitHubRepository(projectID: string, repositoryID: number) {
    return this.call<{ repository_id: number; project_id: string; status: string }>(
      `/api/local/projects/${projectID}/github/repositories/${repositoryID}/claim`,
      { method: "POST", write: true, body: "{}" },
    );
  }

  releaseGitHubRepository(projectID: string, repositoryID: number) {
    return this.call<{ released: boolean }>(`/api/local/projects/${projectID}/github/repositories/${repositoryID}/claim`, {
      method: "DELETE",
      write: true,
    });
  }

  githubBindings(projectID: string) {
    return this.call<{ bindings: GitHubBinding[] }>(`/api/local/projects/${projectID}/github/bindings`);
  }

  createGitHubBinding(projectID: string, body: { service_id: string; repository_id: number; service_key: string; config_path: string }) {
    return this.call<GitHubBinding>(`/api/local/projects/${projectID}/github/bindings`, {
      method: "POST",
      write: true,
      body: JSON.stringify(body),
    });
  }

  removeGitHubBinding(projectID: string, bindingID: string) {
    return this.call<{ removed: boolean }>(`/api/local/projects/${projectID}/github/bindings/${encodeURIComponent(bindingID)}`, {
      method: "DELETE",
      write: true,
    });
  }

  repositoryCDConfig() {
    return this.call<{ config: RepositoryCDConfig; migrated_v1: boolean; config_hash: string }>("/api/local/repository/config");
  }

  previewRepositoryMutation(service: RepositoryCDService) {
    return this.call<RepositoryMutationPreview>("/api/local/repository/config/preview", {
      method: "POST",
      body: JSON.stringify({ service }),
    });
  }

  applyRepositoryMutation(service: RepositoryCDService, previewHash: string, idempotencyKey: string) {
    return this.call<RepositoryMutationApplyResult>("/api/local/repository/apply", {
      method: "POST",
      write: true,
      idempotencyKey,
      body: JSON.stringify({ service, confirm: true, preview_hash: previewHash }),
    });
  }

  previewRepositoryPlan(body: { event: RepositoryCDPlan["event"]; base: string; head: string }) {
    return this.call<RepositoryCDPlan>("/api/local/repository/plan/preview", {
      method: "POST",
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

  incidents(projectID: string, userID: string, role: string, status = "") {
    const query = new URLSearchParams({ user_id: userID, role });
    if (status) query.set("status", status);
    return this.call<IncidentListResult>(`/api/local/projects/${projectID}/incidents?${query}`);
  }

  incident(projectID: string, incidentID: string, userID: string, role: string) {
    const query = new URLSearchParams({ user_id: userID, role });
    return this.call<IncidentResult>(`/api/local/projects/${projectID}/incidents/${encodeURIComponent(incidentID)}?${query}`);
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
      headers.set("Idempotency-Key", init.idempotencyKey ?? crypto.randomUUID());
      headers.set("X-Local-Session", await this.getLocalSession());
    }

    const requestInit = { ...init };
    delete requestInit.write;
    delete requestInit.idempotencyKey;
    const res = await fetch(path, { ...requestInit, headers });
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
