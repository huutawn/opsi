import type {
  AuditEvent,
  BuildJob,
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
  ServiceConfiguration,
  ServiceConfigurationDraft,
  ServiceConfigurationPreview,
  ServiceConfigurationValidation,
  ServiceConfigurationDiff,
  ServiceConfigurationApplyResult,
  ProposalReviewAudit,
  ProposalReview,
  ProposalReviewCreateRequest,
  DependencyReviewResult,
  PreflightResult,
  VerifyDependencyRequest,
  VerifyDependencyResponse,
  SourceRiskReport,
  SecretResult,
  IncidentResult,
  IncidentListResult,
  IncidentEvidence,
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
  Resource,
  CreateResourceRequest,
  UpdateResourceRequest,
  ResourceTypeDefinition,
  ResourceBinding,
  CreateResourceBindingRequest,
  RetainedStorage,
  RetainedStorageReview,
  Backup,
  RestoreReview,
  Restore,
  ApplicationCutoverReview,
  ApplicationCutover,
  ApplicationCutoverRollback,
  ApplicationCutoverFinalization,
  DeploymentRun,
  DeploymentRunEvent,
  DeploymentRunResult,
  DeploymentPlan,
	WorkloadSecretMetadata,
} from "@/lib/contracts/registry";

type RequestOptions = RequestInit & { write?: boolean; idempotencyKey?: string };
const responseLimit = 2 * 1024 * 1024;

export type ResolvedDeploymentRequest = {
  schema_version: "opsi.deployment_job/v1";
  build_record_id: string;
  environment_id: string;
  expected_topology_revision?: number;
  expected_topology_hash?: string;
  expected_configuration_revision?: number;
  expected_configuration_state_hash?: string;
  expected_deployment_policy_revision?: number;
  expected_deployment_policy_hash?: string;
  expected_preflight_hash?: string;
  warning_acknowledgements?: string[];
  deployment_batch?: string[];
  workload?: WorkloadSpec;
};

export type LocalSessionStatus = {
  authenticated: boolean;
  cloud_connected: "ok" | "failed" | "unknown";
  agent_connected: "ok" | "failed" | "unknown" | "not connected";
  token_status?: string;
  local_session?: string;
  org_id?: string;
  project_id?: string;
  role?: string;
  user_id?: string;
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

export type SelectableProject = {
  id: string;
  name: string;
  slug?: string;
  role: string;
};

export type SelectionResponse = {
  selection_id: string;
  projects: SelectableProject[];
};

export class LocalClient {
  private localSession = "";

  async session(projectID?: string) {
    const query = projectID ? `?verify=1&project_id=${encodeURIComponent(projectID)}` : "?verify=1";
    return this.call<LocalSessionStatus>(`/api/local/session${query}`);
  }

  getSelectableProjects(selectionID: string) {
    return this.call<SelectionResponse>(`/api/local/session/selection?selection_id=${encodeURIComponent(selectionID)}`);
  }

  selectProject(selectionID: string, projectID: string) {
    return this.call<{ authenticated: boolean; session: LocalSessionStatus }>("/api/local/session/select-project", {
      method: "POST",
      body: JSON.stringify({ selection_id: selectionID, project_id: projectID }),
    });
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

  async nodes(projectID: string) {
    const response = await this.call<{ nodes: NodeRecord[] }>(`/api/local/projects/${projectID}/nodes`);
    return response.nodes;
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

  serviceConfiguration(projectID: string, serviceID: string) { return this.call<ServiceConfiguration>(`/api/local/projects/${projectID}/services/${encodeURIComponent(serviceID)}/configuration`); }
  serviceConfigurationPreview(projectID: string, serviceID: string, draft: ServiceConfigurationDraft) { return this.call<ServiceConfigurationPreview>(`/api/local/projects/${projectID}/services/${encodeURIComponent(serviceID)}/configuration/preview`, { method: "POST", body: JSON.stringify(draft) }); }
  serviceConfigurationValidate(projectID: string, serviceID: string, draft: ServiceConfigurationDraft) { return this.call<ServiceConfigurationValidation>(`/api/local/projects/${projectID}/services/${encodeURIComponent(serviceID)}/configuration/validate`, { method: "POST", body: JSON.stringify(draft) }); }
  serviceConfigurationDiff(projectID: string, serviceID: string, draft: ServiceConfigurationDraft) { return this.call<ServiceConfigurationDiff>(`/api/local/projects/${projectID}/services/${encodeURIComponent(serviceID)}/configuration/diff`, { method: "POST", body: JSON.stringify(draft) }); }
  serviceConfigurationApply(projectID: string, serviceID: string, body: { draft: ServiceConfigurationDraft; expected_revision: number; expected_state_hash: string; proposal_review?: ProposalReviewAudit }, idempotencyKey: string) { return this.call<ServiceConfigurationApplyResult>(`/api/local/projects/${projectID}/services/${encodeURIComponent(serviceID)}/configuration/apply`, { method: "POST", write: true, idempotencyKey, body: JSON.stringify(body) }); }
  proposalReviews(projectID: string, serviceID: string) { return this.call<{ reviews: ProposalReview[] }>(`/api/local/projects/${projectID}/services/${encodeURIComponent(serviceID)}/proposal-reviews`); }
  proposalReview(projectID: string, reviewID: string) { return this.call<ProposalReview>(`/api/local/projects/${projectID}/proposal-reviews/${encodeURIComponent(reviewID)}`); }
  createProposalReview(projectID: string, serviceID: string, request: ProposalReviewCreateRequest, idempotencyKey: string) { return this.call<ProposalReview>(`/api/local/projects/${projectID}/services/${encodeURIComponent(serviceID)}/proposal-reviews`, { method: "POST", write: true, idempotencyKey, body: JSON.stringify(request) }); }
  approveProposalReview(projectID: string, reviewID: string, idempotencyKey: string) { return this.call<ProposalReview>(`/api/local/projects/${projectID}/proposal-reviews/${encodeURIComponent(reviewID)}/approve`, { method: "POST", write: true, idempotencyKey, body: "{}" }); }
  rejectProposalReview(projectID: string, reviewID: string, idempotencyKey: string) { return this.call<ProposalReview>(`/api/local/projects/${projectID}/proposal-reviews/${encodeURIComponent(reviewID)}/reject`, { method: "POST", write: true, idempotencyKey, body: "{}" }); }
  applyProposalReview(projectID: string, reviewID: string, idempotencyKey: string) { return this.call<ProposalReview>(`/api/local/projects/${projectID}/proposal-reviews/${encodeURIComponent(reviewID)}/apply`, { method: "POST", write: true, idempotencyKey, body: "{}" }); }

  dependenciesReview(projectID: string, serviceID: string) {
    return this.call<DependencyReviewResult>(`/api/local/projects/${projectID}/services/${encodeURIComponent(serviceID)}/dependencies/review`, {
      method: "POST",
      body: "{}",
    });
  }

  dependenciesApply(projectID: string, serviceID: string, idempotencyKey: string) {
    return this.call<{ status: string; realized: number }>(`/api/local/projects/${projectID}/services/${encodeURIComponent(serviceID)}/dependencies/apply`, {
      method: "POST",
      write: true,
      idempotencyKey,
      body: "{}",
    });
  }

  sourceRiskReport(projectID: string, applicationID: string) {
    return this.call<SourceRiskReport>(`/api/local/projects/${projectID}/applications/${encodeURIComponent(applicationID)}/source-risk-report`);
  }

  sourceRiskReportByID(projectID: string, reportID: string) {
    return this.call<SourceRiskReport>(`/api/local/projects/${projectID}/source-risk-reports/${encodeURIComponent(reportID)}`);
  }

  verifyDependency(projectID: string, body: VerifyDependencyRequest, applicationID?: string, environmentID?: string, idempotencyKey?: string) {
    const query = new URLSearchParams();
    if (applicationID) query.set("application_id", applicationID);
    if (environmentID) query.set("environment_id", environmentID);
    const suffix = query.toString() ? `?${query.toString()}` : "";
    return this.call<VerifyDependencyResponse>(`/api/local/projects/${projectID}/dependencies/verify${suffix}`, {
      method: "POST",
      write: true,
      idempotencyKey: idempotencyKey || crypto.randomUUID(),
      body: JSON.stringify(body),
    });
  }

  dependencyVerification(projectID: string, dependencyLogicalName: string, applicationID?: string, environmentID?: string) {
    const query = new URLSearchParams();
    if (applicationID) query.set("application_id", applicationID);
    if (environmentID) query.set("environment_id", environmentID);
    const suffix = query.toString() ? `?${query.toString()}` : "";
    return this.call<VerifyDependencyResponse>(`/api/local/projects/${projectID}/dependencies/${encodeURIComponent(dependencyLogicalName)}/verification${suffix}`);
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

  createBuildJob(projectID: string, applicationID: string, idempotencyKey: string) {
    return this.call<BuildJob>(`/api/local/projects/${projectID}/applications/${encodeURIComponent(applicationID)}/build-jobs`, { method: "POST", write: true, idempotencyKey, body: "{}" });
  }

  buildJobs(projectID: string, applicationID: string) {
    return this.call<{ build_jobs: BuildJob[] }>(`/api/local/projects/${projectID}/applications/${encodeURIComponent(applicationID)}/build-jobs?limit=50`);
  }

  buildJob(projectID: string, applicationID: string, buildJobID: string) {
    return this.call<BuildJob>(`/api/local/projects/${projectID}/applications/${encodeURIComponent(applicationID)}/build-jobs/${encodeURIComponent(buildJobID)}`);
  }

  dispatchBuildJob(projectID: string, applicationID: string, buildJobID: string, idempotencyKey: string) {
    return this.call<{ attempt_id: string; last_state: string }>(`/api/local/projects/${projectID}/applications/${encodeURIComponent(applicationID)}/build-jobs/${encodeURIComponent(buildJobID)}/dispatch`, {
      method: "POST",
      write: true,
      idempotencyKey,
      body: "{}",
    });
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

  startGitHubInstallationDiscovery(projectID: string, idempotencyKey?: string) {
    return this.call<{ authorization_url: string; status: string; expires_at: string }>(
      `/api/local/projects/${projectID}/github/installations/discover/start`,
      { method: "POST", write: true, idempotencyKey, body: "{}" },
    );
  }

  githubInstallationDiscovery(projectID: string) {
    return this.call<{ installations: GitHubInstallation[] }>(`/api/local/projects/${projectID}/github/installations/discover`);
  }

  githubRepositories(projectID: string) {
    return this.call<{ repositories: GitHubRepository[] }>(`/api/local/projects/${projectID}/github/repositories`);
  }

  deploymentRuns(projectID: string) {
    return this.call<{ deployment_runs: DeploymentRun[] }>(`/api/local/projects/${projectID}/deployment-runs?limit=50`);
  }

  deploymentRun(projectID: string, runID: string) {
    return this.call<DeploymentRun>(`/api/local/projects/${projectID}/deployment-runs/${encodeURIComponent(runID)}`);
  }

  deploymentRunEvents(projectID: string, runID: string) {
    return this.call<{ events: DeploymentRunEvent[] }>(`/api/local/projects/${projectID}/deployment-runs/${encodeURIComponent(runID)}/events`);
  }

  deploymentRunResult(projectID: string, runID: string) {
    return this.call<DeploymentRunResult>(`/api/local/projects/${projectID}/deployment-runs/${encodeURIComponent(runID)}/result`);
  }

  createDeploymentRun(projectID: string, body: { repository_id: number; selected_ref: string; target: { hostname?: string } }, idempotencyKey: string) {
    return this.call<{ deployment_run: DeploymentRun; reused: boolean }>(`/api/local/projects/${projectID}/deployment-runs`, { method: "POST", write: true, idempotencyKey, body: JSON.stringify(body) });
  }

  updateDeploymentPlan(projectID: string, runID: string, revision: number, expectedPlanHash: string, plan: DeploymentPlan, idempotencyKey: string) {
	return this.call<DeploymentRun>(`/api/local/projects/${projectID}/deployment-runs/${encodeURIComponent(runID)}/plan`, { method: "PUT", write: true, idempotencyKey, headers: { "If-Match": `"${revision}"` }, body: JSON.stringify({ expected_plan_hash: expectedPlanHash, plan }) });
  }

  deploymentRunAction(projectID: string, runID: string, action: "analyze" | "approve" | "acknowledge" | "retry" | "cancel", body: Record<string, unknown>, idempotencyKey: string) {
    return this.call<DeploymentRun>(`/api/local/projects/${projectID}/deployment-runs/${encodeURIComponent(runID)}/${action}`, { method: "POST", write: true, idempotencyKey, body: JSON.stringify(body) });
  }

	workloadSecrets(projectID: string, applicationID: string) {
		return this.call<{ workload_secrets: WorkloadSecretMetadata[] }>(`/api/local/projects/${projectID}/applications/${encodeURIComponent(applicationID)}/workload-secrets`);
	}

	upsertWorkloadSecret(projectID: string, applicationID: string, logicalName: string, value: string, idempotencyKey: string) {
		return this.call<{ workload_secret: WorkloadSecretMetadata; reused: boolean }>(`/api/local/projects/${projectID}/applications/${encodeURIComponent(applicationID)}/workload-secrets`, { method: "PUT", write: true, idempotencyKey, body: JSON.stringify({ logical_name: logicalName, value }) });
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

  createGitHubBinding(projectID: string, body: { service_id: string; repository_id: number; service_key: string; config_path: string; selected_ref?: string; application_root?: string; build_context?: string; build_strategy?: "auto" | "dockerfile" | "buildpack"; dockerfile_path?: string }, idempotencyKey?: string) {
    return this.call<GitHubBinding>(`/api/local/projects/${projectID}/github/bindings`, {
      method: "POST",
      write: true,
      idempotencyKey,
      body: JSON.stringify(body),
    });
  }

  updateGitHubBinding(projectID: string, bindingID: string, body: Pick<GitHubBinding, "selected_ref" | "application_root" | "build_context" | "build_strategy" | "dockerfile_path">, idempotencyKey?: string) {
    return this.call<GitHubBinding>(`/api/local/projects/${projectID}/github/bindings/${encodeURIComponent(bindingID)}`, {
      method: "PUT",
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

  deploymentPreflight(projectID: string, body: ResolvedDeploymentRequest) {
    return this.call<PreflightResult>(`/api/local/projects/${projectID}/deployments/preflight`, {
      method: "POST",
      body: JSON.stringify(body),
    });
  }

  deploymentPreview(projectID: string, body: ResolvedDeploymentRequest) {
    return this.call<DeploymentPreview>(`/api/local/projects/${projectID}/deployments/preview`, {
      method: "POST",
      body: JSON.stringify(body),
    });
  }

  deploymentDiff(projectID: string, body: ResolvedDeploymentRequest) {
    return this.call<DeploymentPreview>(`/api/local/projects/${projectID}/deployments/diff`, {
      method: "POST",
      body: JSON.stringify(body),
    });
  }

  deploymentApply(projectID: string, body: ResolvedDeploymentRequest, idempotencyKey: string) {
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

  incidentEvidence(projectID: string, incidentID: string) {
	return this.call<IncidentEvidence>(`/api/local/projects/${projectID}/incidents/${encodeURIComponent(incidentID)}/evidence`);
  }

  async resources(projectID: string, environmentID?: string) {
    const query = environmentID ? `?environment_id=${encodeURIComponent(environmentID)}` : "";
    const response = await this.call<{ resources: Resource[] }>(`/api/local/projects/${projectID}/resources${query}`);
    return response.resources ?? [];
  }

  resource(projectID: string, resourceID: string) {
    return this.call<Resource>(`/api/local/projects/${projectID}/resources/${encodeURIComponent(resourceID)}`);
  }

  createResource(projectID: string, body: CreateResourceRequest, idempotencyKey?: string) {
    return this.call<{ resource: Resource; reused: boolean }>(`/api/local/projects/${projectID}/resources`, {
      method: "POST",
      write: true,
      idempotencyKey,
      body: JSON.stringify(body),
    });
  }

  updateResource(projectID: string, resourceID: string, body: UpdateResourceRequest, idempotencyKey?: string) {
    return this.call<Resource>(`/api/local/projects/${projectID}/resources/${encodeURIComponent(resourceID)}`, {
      method: "PUT",
      write: true,
      idempotencyKey,
      body: JSON.stringify(body),
    });
  }

  deleteResource(projectID: string, resourceID: string, idempotencyKey?: string) {
    return this.call<Resource>(`/api/local/projects/${projectID}/resources/${encodeURIComponent(resourceID)}`, {
      method: "DELETE",
      write: true,
      idempotencyKey,
    });
  }

  async resourceTypes(projectID: string) {
    const response = await this.call<{ resource_types: ResourceTypeDefinition[] }>(`/api/local/projects/${projectID}/resource-types`);
    return response.resource_types ?? [];
  }

  async resourceBindings(projectID: string, environmentID?: string) {
    const query = environmentID ? `?environment_id=${encodeURIComponent(environmentID)}` : "";
    const response = await this.call<{ bindings: ResourceBinding[] }>(`/api/local/projects/${projectID}/resource-bindings${query}`);
    return response.bindings ?? [];
  }

  createResourceBinding(projectID: string, body: CreateResourceBindingRequest, idempotencyKey?: string) {
    return this.call<{ binding: ResourceBinding; reused: boolean }>(`/api/local/projects/${projectID}/resource-bindings`, {
      method: "POST",
      write: true,
      idempotencyKey,
      body: JSON.stringify(body),
    });
  }

  deleteResourceBinding(projectID: string, bindingID: string, idempotencyKey?: string) {
    return this.call<ResourceBinding>(`/api/local/projects/${projectID}/resource-bindings/${encodeURIComponent(bindingID)}`, {
      method: "DELETE",
      write: true,
      idempotencyKey,
    });
  }

  async retainedStorages(projectID: string, environmentID?: string) {
    const query = environmentID ? `?environment_id=${encodeURIComponent(environmentID)}` : "";
    const response = await this.call<{ retained_storages: RetainedStorage[] }>(`/api/local/projects/${projectID}/retained-storages${query}`);
    return response.retained_storages ?? [];
  }

  retainedStorage(projectID: string, id: string) {
    return this.call<RetainedStorage>(`/api/local/projects/${projectID}/retained-storages/${encodeURIComponent(id)}`);
  }

  reviewRetainedStorageDestroy(projectID: string, id: string, idempotencyKey?: string) {
    return this.call<{ review: RetainedStorageReview }>(`/api/local/projects/${projectID}/retained-storages/${encodeURIComponent(id)}/review`, {
      method: "POST",
      write: true,
      idempotencyKey,
      body: "{}",
    });
  }

  destroyRetainedStorage(projectID: string, id: string, reviewToken: string, idempotencyKey?: string) {
    return this.call<{ retained_storage: RetainedStorage; reused: boolean }>(`/api/local/projects/${projectID}/retained-storages/${encodeURIComponent(id)}/destroy`, {
      method: "POST",
      write: true,
      idempotencyKey,
      body: JSON.stringify({ review_token: reviewToken }),
    });
  }

  async backups(projectID: string, resourceID?: string) {
    const path = resourceID
      ? `/api/local/projects/${projectID}/resources/${encodeURIComponent(resourceID)}/backups`
      : `/api/local/projects/${projectID}/backups`;
    const response = await this.call<{ backups: Backup[] }>(path);
    return response.backups ?? [];
  }

  backup(projectID: string, backupID: string) {
    return this.call<Backup>(`/api/local/projects/${projectID}/backups/${encodeURIComponent(backupID)}`);
  }

  createBackup(projectID: string, resourceID: string, idempotencyKey?: string) {
    return this.call<{ backup: Backup; reused: boolean }>(`/api/local/projects/${projectID}/resources/${encodeURIComponent(resourceID)}/backups`, {
      method: "POST",
      write: true,
      idempotencyKey,
      body: "{}",
    });
  }

  reviewRestore(projectID: string, backupID: string, targetResourceID: string, idempotencyKey?: string) {
    return this.call<{ review: RestoreReview }>(`/api/local/projects/${projectID}/backups/${encodeURIComponent(backupID)}/restore-review`, {
      method: "POST",
      write: true,
      idempotencyKey,
      body: JSON.stringify({ target_resource_id: targetResourceID }),
    });
  }

  restoreReview(projectID: string, reviewID: string) {
    return this.call<RestoreReview>(`/api/local/projects/${projectID}/restore-reviews/${encodeURIComponent(reviewID)}`);
  }

  async restores(projectID: string, backupID?: string, targetResourceID?: string) {
    const query = new URLSearchParams();
    if (backupID) query.set("backup_id", backupID);
    if (targetResourceID) query.set("target_resource_id", targetResourceID);
    const suffix = query.toString() ? `?${query.toString()}` : "";
    const response = await this.call<{ restores: Restore[] }>(`/api/local/projects/${projectID}/restores${suffix}`);
    return response.restores ?? [];
  }

  restore(projectID: string, restoreID: string) {
    return this.call<Restore>(`/api/local/projects/${projectID}/restores/${encodeURIComponent(restoreID)}`);
  }

  createRestore(projectID: string, backupID: string, targetResourceID: string, reviewID: string, idempotencyKey?: string) {
    return this.call<{ restore: Restore; reused: boolean }>(`/api/local/projects/${projectID}/backups/${encodeURIComponent(backupID)}/restores`, {
      method: "POST",
      write: true,
      idempotencyKey,
      body: JSON.stringify({ target_resource_id: targetResourceID, review_id: reviewID }),
    });
  }

  async cutoverReviews(projectID: string, applicationID?: string) {
    const path = applicationID
      ? `/api/local/projects/${projectID}/applications/${encodeURIComponent(applicationID)}/cutover-reviews`
      : `/api/local/projects/${projectID}/cutover-reviews`;
    const response = await this.call<{ cutover_reviews?: ApplicationCutoverReview[]; reviews?: ApplicationCutoverReview[] }>(path);
    return response.cutover_reviews ?? response.reviews ?? [];
  }

  cutoverReview(projectID: string, reviewID: string) {
    return this.call<{ cutover_review?: ApplicationCutoverReview; review?: ApplicationCutoverReview }>(`/api/local/projects/${projectID}/cutover-reviews/${encodeURIComponent(reviewID)}`);
  }

  createCutoverReview(projectID: string, applicationID: string, targetBindingID: string, sourceBindingID?: string, idempotencyKey?: string) {
    return this.call<{ cutover_review?: ApplicationCutoverReview; review?: ApplicationCutoverReview; reused: boolean }>(`/api/local/projects/${projectID}/applications/${encodeURIComponent(applicationID)}/cutover-reviews`, {
      method: "POST",
      write: true,
      idempotencyKey,
      body: JSON.stringify({ target_binding_id: targetBindingID, source_binding_id: sourceBindingID || "" }),
    });
  }

  async cutovers(projectID: string, applicationID?: string) {
    const path = applicationID
      ? `/api/local/projects/${projectID}/applications/${encodeURIComponent(applicationID)}/cutovers`
      : `/api/local/projects/${projectID}/cutovers`;
    const response = await this.call<{ cutovers?: ApplicationCutover[]; application_cutovers?: ApplicationCutover[] }>(path);
    return response.cutovers ?? response.application_cutovers ?? [];
  }

  cutover(projectID: string, cutoverID: string) {
    return this.call<{ cutover?: ApplicationCutover; application_cutover?: ApplicationCutover }>(`/api/local/projects/${projectID}/cutovers/${encodeURIComponent(cutoverID)}`);
  }

  applyCutover(projectID: string, applicationID: string, cutoverReviewID: string, idempotencyKey?: string) {
    return this.call<{ cutover?: ApplicationCutover; application_cutover?: ApplicationCutover; reused: boolean }>(`/api/local/projects/${projectID}/applications/${encodeURIComponent(applicationID)}/cutovers`, {
      method: "POST",
      write: true,
      idempotencyKey,
      body: JSON.stringify({ cutover_review_id: cutoverReviewID }),
    });
  }

  async cutoverRollbacks(projectID: string, applicationID?: string) {
    const query = applicationID ? `?application_id=${encodeURIComponent(applicationID)}` : "";
    const response = await this.call<{ rollbacks?: ApplicationCutoverRollback[]; cutover_rollbacks?: ApplicationCutoverRollback[] }>(`/api/local/projects/${projectID}/cutover-rollbacks${query}`);
    return response.rollbacks ?? response.cutover_rollbacks ?? [];
  }

  cutoverRollback(projectID: string, rollbackID: string) {
    return this.call<{ rollback?: ApplicationCutoverRollback; cutover_rollback?: ApplicationCutoverRollback }>(`/api/local/projects/${projectID}/cutover-rollbacks/${encodeURIComponent(rollbackID)}`);
  }

  applyCutoverRollback(projectID: string, applicationID: string, cutoverID: string, idempotencyKey?: string) {
    return this.call<{ rollback?: ApplicationCutoverRollback; cutover_rollback?: ApplicationCutoverRollback; reused: boolean }>(`/api/local/projects/${projectID}/applications/${encodeURIComponent(applicationID)}/cutovers/${encodeURIComponent(cutoverID)}/rollbacks`, {
      method: "POST",
      write: true,
      idempotencyKey,
      body: "{}",
    });
  }

  async cutoverFinalizations(projectID: string, applicationID?: string) {
    const query = applicationID ? `?application_id=${encodeURIComponent(applicationID)}` : "";
    const response = await this.call<{ finalizations?: ApplicationCutoverFinalization[]; application_cutover_finalizations?: ApplicationCutoverFinalization[] }>(`/api/local/projects/${projectID}/cutover-finalizations${query}`);
    return response.finalizations ?? response.application_cutover_finalizations ?? [];
  }

  cutoverFinalization(projectID: string, finalizationID: string) {
    return this.call<{ finalization?: ApplicationCutoverFinalization; application_cutover_finalization?: ApplicationCutoverFinalization }>(`/api/local/projects/${projectID}/cutover-finalizations/${encodeURIComponent(finalizationID)}`);
  }

  applyCutoverFinalization(projectID: string, applicationID: string, cutoverID: string, idempotencyKey?: string) {
    return this.call<{ finalization?: ApplicationCutoverFinalization; application_cutover_finalization?: ApplicationCutoverFinalization; reused: boolean }>(`/api/local/projects/${projectID}/applications/${encodeURIComponent(applicationID)}/cutovers/${encodeURIComponent(cutoverID)}/finalize`, {
      method: "POST",
      write: true,
      idempotencyKey,
      body: JSON.stringify({ cutover_id: cutoverID }),
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
