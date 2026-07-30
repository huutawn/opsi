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
  TopologyDraft,
  TopologyPlan,
  TopologyPreview,
  TopologyValidation,
  TopologyDiff,
  PlacementFacts,
  DeploymentPolicyDraft,
  DeploymentPolicy,
  DeploymentPolicyPreview,
  DeploymentPolicyApplyResult,
  DeploymentPreview,
  ExposureMutationRequest,
  ExposurePreview,
  WorkloadSpec,
} from "@/lib/contracts/registry";

type RequestOptions = RequestInit & { write?: boolean; idempotencyKey?: string };
const responseLimit = 2 * 1024 * 1024;

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

export type LocalSettings = {
  version: string;
  revision: string;
  go_version: string;
  cloud_authority: string;
  cloud_configured: boolean;
  agent_configured: boolean;
  agent_tls_pinned: boolean;
  config_selected: boolean;
  ui_assets: string;
  backend_gaps: Array<{ capability: string; status: string; roadmap: string }>;
};

export class LocalAPIError extends Error {
  status = 0;
  code = "LOCAL_REQUEST_FAILED";
  requestID = "";
  nextAction = "Retry after checking Local backend connectivity.";
  retryable = false;
}

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

  logout(projectID?: string, idempotencyKey?: string) {
    this.localSession = "";
    return this.call<{ authenticated: false }>("/api/local/session/logout", {
      method: "POST",
      write: true,
      idempotencyKey,
      body: JSON.stringify({ project_id: projectID ?? "" }),
    });
  }

  rotatePAT(projectID?: string, idempotencyKey?: string) {
    return this.call<{ rotated: boolean; revoked_old: boolean }>("/api/local/session/token/rotate", {
      method: "POST",
      write: true,
      idempotencyKey,
      body: JSON.stringify({ project_id: projectID ?? "" }),
    });
  }

  revokePAT(projectID?: string, idempotencyKey?: string) {
    this.localSession = "";
    return this.call<{ authenticated: false; revoked: boolean }>("/api/local/session/token/revoke", {
      method: "POST",
      write: true,
      idempotencyKey,
      body: JSON.stringify({ project_id: projectID ?? "" }),
    });
  }

  switchProject(projectID: string, idempotencyKey?: string) {
    return this.call<LocalSessionStatus>("/api/local/session/project", {
      method: "POST",
      write: true,
      idempotencyKey,
      body: JSON.stringify({ project_id: projectID }),
    });
  }

  settings() {
    return this.call<LocalSettings>("/api/local/settings");
  }

  async projects(orgID: string) {
    return this.call<{ projects: Project[] }>(`/api/local/projects?org_id=${encodeURIComponent(orgID)}`);
  }

  createProject(orgID: string, body: { name: FormDataEntryValue | null; slug: FormDataEntryValue | null }, idempotencyKey?: string) {
    return this.call<Project>(`/api/local/projects?org_id=${encodeURIComponent(orgID)}`, {
      method: "POST",
      write: true,
      idempotencyKey,
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

  nodeAction(projectID: string, nodeID: string, action: "offline" | "drain" | "remove", idempotencyKey?: string) {
    return this.call<NodeRecord>(`/api/local/projects/${projectID}/nodes/${nodeID}/${action}`, {
      method: "POST",
      write: true,
      idempotencyKey,
      body: action === "remove" ? JSON.stringify({ confirm_remove: true }) : "{}",
    });
  }

  createBootstrap(projectID: string, body: Record<string, unknown>, idempotencyKey?: string) {
    return this.call<BootstrapSession>(`/api/local/projects/${projectID}/bootstrap-sessions`, {
      method: "POST",
      write: true,
      idempotencyKey,
      body: JSON.stringify(body),
    });
  }

  retryBootstrap(projectID: string, sessionID: string, idempotencyKey: string) {
    return this.call<BootstrapSession>(`/api/local/projects/${projectID}/bootstrap-sessions/${sessionID}/retry`, {
      method: "POST",
      write: true,
      idempotencyKey,
      body: "{}",
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

  placementFacts(projectID: string) { return this.call<PlacementFacts>(`/api/local/projects/${projectID}/topology/facts`); }
  topology(projectID: string) { return this.call<TopologyPlan>(`/api/local/projects/${projectID}/topology`); }
  topologyPlan(projectID: string, draft: TopologyDraft) { return this.call<TopologyPreview>(`/api/local/projects/${projectID}/topology/plan`, { method: "POST", body: JSON.stringify({ draft }) }); }
  topologyValidate(projectID: string, draft: TopologyDraft, policyID = "") { return this.call<TopologyValidation>(`/api/local/projects/${projectID}/topology/validate`, { method: "POST", body: JSON.stringify({ draft, policy_id: policyID }) }); }
  topologyDiff(projectID: string, draft: TopologyDraft) { return this.call<TopologyDiff>(`/api/local/projects/${projectID}/topology/diff`, { method: "POST", body: JSON.stringify({ draft }) }); }
  topologyApply(projectID: string, body: { draft: TopologyDraft; expected_revision: number; expected_state_hash: string; policy_id?: string }, idempotencyKey: string) { return this.call<{ plan: TopologyPlan; reused: boolean }>(`/api/local/projects/${projectID}/topology/apply`, { method: "POST", write: true, idempotencyKey, body: JSON.stringify(body) }); }
  deploymentPolicies(projectID: string) { return this.call<{ policies: DeploymentPolicy[] }>(`/api/local/projects/${projectID}/deployment-policies`); }
  deploymentPolicyPreview(projectID: string, policy: DeploymentPolicyDraft) { return this.call<DeploymentPolicyPreview>(`/api/local/projects/${projectID}/deployment-policies/preview`, { method: "POST", body: JSON.stringify(policy) }); }
  deploymentPolicyDiff(projectID: string, body: { policy_id?: string; policy: DeploymentPolicyDraft; expected_revision?: number; expected_state_hash?: string }) { return this.call<{ policy_id?: string; current_revision: number; current_hash?: string; proposed_hash: string; changes: unknown[] }>(`/api/local/projects/${projectID}/deployment-policies/diff`, { method: "POST", body: JSON.stringify(body) }); }
  deploymentPolicyApply(projectID: string, body: { policy_id?: string; policy: DeploymentPolicyDraft; expected_revision: number; expected_state_hash: string }, idempotencyKey: string) { return this.call<DeploymentPolicyApplyResult>(`/api/local/projects/${projectID}/deployment-policies/apply`, { method: "POST", write: true, idempotencyKey, body: JSON.stringify(body) }); }
  disableDeploymentPolicy(projectID: string, policyID: string, body: { expected_revision: number; expected_state_hash: string }, idempotencyKey: string) { return this.call<DeploymentPolicyApplyResult>(`/api/local/projects/${projectID}/deployment-policies/${encodeURIComponent(policyID)}/disable`, { method: "POST", write: true, idempotencyKey, body: JSON.stringify(body) }); }

  createService(projectID: string, body: Record<string, unknown>, idempotencyKey?: string) {
    return this.call<ServiceRecord>(`/api/local/projects/${projectID}/services`, {
      method: "POST",
      write: true,
      idempotencyKey,
      body: JSON.stringify(body),
    });
  }

  githubInstallations(projectID: string) {
    return this.call<{ installations: GitHubInstallation[] }>(`/api/local/projects/${projectID}/github/installations`);
  }

  startGitHubInstallationClaim(projectID: string, installationID: number, idempotencyKey?: string) {
    return this.call<{ authorization_url: string; status: string; expires_at: string }>(
      `/api/local/projects/${projectID}/github/installations/${installationID}/claim/start`,
      { method: "POST", write: true, idempotencyKey, body: "{}" },
    );
  }

  githubRepositories(projectID: string) {
    return this.call<{ repositories: GitHubRepository[] }>(`/api/local/projects/${projectID}/github/repositories`);
  }

  claimGitHubRepository(projectID: string, repositoryID: number, idempotencyKey?: string) {
    return this.call<{ repository_id: number; project_id: string; status: string }>(
      `/api/local/projects/${projectID}/github/repositories/${repositoryID}/claim`,
      { method: "POST", write: true, idempotencyKey, body: "{}" },
    );
  }

  releaseGitHubRepository(projectID: string, repositoryID: number, idempotencyKey?: string) {
    return this.call<{ released: boolean }>(`/api/local/projects/${projectID}/github/repositories/${repositoryID}/claim`, {
      method: "DELETE",
      write: true,
      idempotencyKey,
    });
  }

  githubBindings(projectID: string) {
    return this.call<{ bindings: GitHubBinding[] }>(`/api/local/projects/${projectID}/github/bindings`);
  }

  createGitHubBinding(projectID: string, body: { service_id: string; repository_id: number; service_key: string; config_path: string }, idempotencyKey?: string) {
    return this.call<GitHubBinding>(`/api/local/projects/${projectID}/github/bindings`, {
      method: "POST",
      write: true,
      idempotencyKey,
      body: JSON.stringify(body),
    });
  }

  removeGitHubBinding(projectID: string, bindingID: string, idempotencyKey?: string) {
    return this.call<{ removed: boolean }>(`/api/local/projects/${projectID}/github/bindings/${encodeURIComponent(bindingID)}`, {
      method: "DELETE",
      write: true,
      idempotencyKey,
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

  deploymentPreview(projectID: string, body: { schema_version: string; build_record_id: string; environment_id: string; workload: WorkloadSpec }) {
    return this.call<DeploymentPreview>(`/api/local/projects/${projectID}/deployments/preview`, {
      method: "POST",
      body: JSON.stringify(body),
    });
  }

  deploymentDiff(projectID: string, body: { schema_version: string; build_record_id: string; environment_id: string; workload: WorkloadSpec }) {
    return this.call<DeploymentPreview>(`/api/local/projects/${projectID}/deployments/diff`, {
      method: "POST",
      body: JSON.stringify(body),
    });
  }

  deploymentApply(projectID: string, body: { schema_version: string; build_record_id: string; environment_id: string; workload: WorkloadSpec }, idempotencyKey: string) {
    return this.call<DeploymentJob>(`/api/local/projects/${projectID}/deployments`, {
      method: "POST",
      write: true,
      idempotencyKey,
      body: JSON.stringify(body),
    });
  }

  rollback(projectID: string, deploymentID: string, idempotencyKey: string) {
    return this.call<DeploymentJob>(`/api/local/projects/${projectID}/deployments/${deploymentID}/rollback`, {
      method: "POST",
      write: true,
      idempotencyKey,
      body: "{}",
    });
  }

  deployments(projectID: string) {
    return this.call<{ deployments: DeploymentJob[] }>(`/api/local/projects/${projectID}/deployments`);
  }

  deployment(projectID: string, deploymentID: string) {
    return this.call<DeploymentJob>(`/api/local/projects/${projectID}/deployments/${encodeURIComponent(deploymentID)}`);
  }

  deploymentEvents(projectID: string, deploymentID: string) {
    return this.call<{ events: TimelineEvent[] }>(`/api/local/projects/${projectID}/deployments/${deploymentID}/events`);
  }

  deploymentCancel(projectID: string, deploymentID: string, idempotencyKey: string) {
    return this.call<DeploymentJob>(`/api/local/projects/${projectID}/deployments/${encodeURIComponent(deploymentID)}/cancel`, { method: "POST", write: true, idempotencyKey, body: "{}" });
  }

  deploymentRetry(projectID: string, deploymentID: string, idempotencyKey: string) {
    return this.call<DeploymentJob>(`/api/local/projects/${projectID}/deployments/${encodeURIComponent(deploymentID)}/retry`, { method: "POST", write: true, idempotencyKey, body: "{}" });
  }

  previewCleanup(projectID: string, deploymentID: string, idempotencyKey: string, reason = "manual") {
    return this.call<DeploymentJob>(`/api/local/projects/${projectID}/deployments/${encodeURIComponent(deploymentID)}/cleanup`, { method: "POST", write: true, idempotencyKey, body: JSON.stringify({ deployment_id: deploymentID, reason }) });
  }

  exposurePreview(projectID: string, body: ExposureMutationRequest) {
    return this.call<ExposurePreview>(`/api/local/projects/${projectID}/exposures/preview`, { method: "POST", body: JSON.stringify(body) });
  }

  exposureDiff(projectID: string, body: ExposureMutationRequest) {
    return this.call<ExposurePreview>(`/api/local/projects/${projectID}/exposures/diff`, { method: "POST", body: JSON.stringify(body) });
  }

  exposureApply(projectID: string, body: ExposureMutationRequest, idempotencyKey: string) {
    return this.call<DeploymentJob>(`/api/local/projects/${projectID}/exposures`, { method: "POST", write: true, idempotencyKey, body: JSON.stringify(body) });
  }

  exposures(projectID: string) {
    return this.call<{ exposures: DeploymentJob[] }>(`/api/local/projects/${projectID}/exposures`);
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

  setupTOTP(projectID: string, idempotencyKey?: string) {
    return this.call<{ status: string; project_id: string; secret: string; uri: string; ttl_seconds: number }>(
      `/api/local/projects/${projectID}/secrets/setup-totp`,
      { method: "POST", write: true, idempotencyKey, body: "{}" },
    );
  }

  createSecret(projectID: string, body: Record<string, unknown>, idempotencyKey?: string) {
    return this.call<SecretResult>(`/api/local/projects/${projectID}/secrets`, {
      method: "POST",
      write: true,
      idempotencyKey,
      body: JSON.stringify(body),
    });
  }

  revealSecret(projectID: string, name: string, body: Record<string, unknown>, idempotencyKey?: string) {
    return this.call<SecretResult>(`/api/local/projects/${projectID}/secrets/${encodeURIComponent(name)}/reveal`, {
      method: "POST",
      write: true,
      idempotencyKey,
      body: JSON.stringify({ ...body, reveal: true }),
    });
  }

  rotateSecret(projectID: string, name: string, body: Record<string, unknown>, idempotencyKey?: string) {
    return this.call<SecretResult>(`/api/local/projects/${projectID}/secrets/${encodeURIComponent(name)}/rotate`, {
      method: "POST",
      write: true,
      idempotencyKey,
      body: JSON.stringify(body),
    });
  }

  incidents(projectID: string, status = "") {
	const query = new URLSearchParams();
	if (status) query.set("status", status);
	const suffix = query.toString() ? `?${query.toString()}` : "";
	return this.call<IncidentListResult>(`/api/local/projects/${projectID}/incidents${suffix}`);
  }

  incident(projectID: string, incidentID: string) {
	return this.call<IncidentResult>(`/api/local/projects/${projectID}/incidents/${encodeURIComponent(incidentID)}`);
  }

  resolveIncident(projectID: string, incidentID: string, idempotencyKey?: string) {
	return this.call<IncidentResult>(`/api/local/projects/${projectID}/incidents/${encodeURIComponent(incidentID)}/resolve`, {
	  method: "POST",
	  write: true,
	  idempotencyKey,
	  body: JSON.stringify({}),
	});
  }

  private async call<T>(path: string, init: RequestOptions = {}) {
    if (!path.startsWith("/api/local/")) throw new Error("LocalClient only accepts relative /api/local routes");
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
    const timeout = AbortSignal.timeout(30_000);
    const signal = requestInit.signal ? AbortSignal.any([requestInit.signal, timeout]) : timeout;
    const res = await fetch(path, { ...requestInit, headers, signal });
    const text = await readBoundedText(res);
    let data: Record<string, unknown> = {};
    try {
      data = text ? JSON.parse(text) : {};
    } catch {
      throw Object.assign(new LocalAPIError("Local backend returned invalid JSON"), { status: res.status });
    }
    if (!res.ok) {
      const payload = (data.error ?? data) as Record<string, unknown>;
      const error = new LocalAPIError(String(payload.message ?? "request failed"));
      Object.assign(error, {
        status: res.status,
        code: String(payload.code ?? "LOCAL_REQUEST_FAILED"),
        requestID: String(payload.request_id ?? res.headers.get("X-Request-ID") ?? ""),
        nextAction: String(payload.next_action ?? "Retry after checking Local backend connectivity."),
        retryable: Boolean(payload.retryable),
      });
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

async function readBoundedText(response: Response) {
  const declared = Number(response.headers.get("content-length") || 0);
  if (declared > responseLimit) throw new LocalAPIError("Local backend response exceeded 2 MiB");
  if (!response.body) return "";
  const reader = response.body.getReader();
  const chunks: Uint8Array[] = [];
  let size = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    size += value.byteLength;
    if (size > responseLimit) {
      await reader.cancel();
      throw new LocalAPIError("Local backend response exceeded 2 MiB");
    }
    chunks.push(value);
  }
  const body = new Uint8Array(size);
  let offset = 0;
  for (const chunk of chunks) {
    body.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return new TextDecoder().decode(body);
}
