export type LoadState = "idle" | "loading" | "ready" | "empty" | "permission" | "network" | "error";

export type Project = {
  id: string;
  org_id: string;
  name: string;
  slug: string;
  status: string;
  created_by?: string;
};

export type Readiness = {
  project_id: string;
  status: string;
  can_deploy: boolean;
  next_action?: string;
};

export type NodeRecord = {
  id: string;
  name: string;
  role: string;
  status: string;
  public_host?: string;
  provider?: string;
  region?: string;
  cpu_cores?: number;
  memory_mb?: number;
  disk_total_gb?: number;
  k3s_role?: string;
  k3s_status?: string;
  agent_id?: string;
  agent_version?: string;
  last_seen_at?: string;
};

export type ServiceRecord = {
  id: string;
  name: string;
  type: string;
  status: string;
  source_type: string;
  repo_url?: string;
  image?: string;
  branch?: string;
  git_sha?: string;
  build_method?: string;
  build_context?: string;
  dockerfile?: string;
  manifest_path?: string;
  watch_paths?: string[];
  container_port?: number;
  health_path?: string;
  replicas?: number;
  namespace?: string;
  configuration?: ServiceConfiguration;
};

export type RepositoryEvidence = { path: string; kind: string; reason: string; confidence: "high" | "medium" | "low" };
export type DetectedApplication = { source_key: string; key: string; name: string; root: string; port?: number; environment?: Record<string,string>; capacity?: { replicas?: number; cpu_milli?: number; memory_bytes?: number; cpu_limit_milli?: number; memory_limit_bytes?: number }; exposure?: { mode?: string; hostname?: string; path?: string; automatic?: boolean }; build: { context: string; dockerfile_path?: string; strategy: string; platform: string; image?: string }; confidence: string; reason: string; evidence: RepositoryEvidence[] };
export type DetectedResource = { logical_name: string; type: string; managed: boolean; required: boolean; persistence?: { persistent: boolean; size_bytes?: number; policy_ref?: string }; settings?: Record<string,string>; recommendation?: string; confidence: string; reason: string; evidence: RepositoryEvidence[] };
export type DependencyVerification = { type: string; path?: string; expected_status?: number };
export type DetectedDependency = { from: string; to: string; protocol: string; strategy?: string; path?: string; required: boolean; injections?: Array<{ environment_name: string; symbolic_source: string; template?: string }>; verification?: DependencyVerification; confidence: string; reason: string; evidence: RepositoryEvidence[] };
export type DetectedBinding = { from: string; to: string; kind: string; path?: string; confidence: string; reason: string; evidence: RepositoryEvidence[] };
export type AnalysisIssue = { code: string; message: string; path?: string; resolution?: string; blocking: boolean };
export type AnalysisScope = { application_roots: string[]; exclude_paths: string[] };
export type EvidenceCoverage = { candidates_found: number; candidates_selected: number; files_inspected: number; bytes_inspected: number };
export type ApplicationEnvironmentReview = { application_source_key: string; no_environment_required: boolean };
export type DeploymentPlan = {
  schema_version: "opsi.deployment_plan/v3"; hash: string;
  source: { repository_id: number; installation_id: number; repository: string; selected_ref: string; commit_sha: string };
  applications: DetectedApplication[]; resources: DetectedResource[]; dependencies: DetectedDependency[]; bindings: DetectedBinding[];
  secrets: Array<{ name: string; application_key: string; environment_name: string; generated: boolean; secret_ref?: string; revision?: number; display: string; confidence: string; reason: string; evidence: RepositoryEvidence[] }>;
  application_environment_reviews?: ApplicationEnvironmentReview[];
  issues: AnalysisIssue[];
  analysis_scope: AnalysisScope; analysis_scope_hash: string; evidence_coverage: EvidenceCoverage; truncation_reason?: string;
  target: { environment_id: string; runtime_id?: string; node_id?: string; hostname?: string; exposure: string; public_routes?: "automatic" | "manual"; cpu_milli?: number; memory_bytes?: number; cpu_limit_milli?: number; memory_limit_bytes?: number };
  authority_revisions: { source_commit_sha: string; repository_updated_at?: string; topology_revision?: number; topology_hash?: string; policy_revision?: number; policy_hash?: string; resource_revision?: number; resource_hash?: string };
  failure_policy: { fail_fast: boolean; rollback_known_good: boolean; retain_persistent_data: boolean; max_attempts: number };
};
export type DeploymentRunState = "analyzing" | "awaiting_input" | "awaiting_approval" | "provisioning" | "building" | "preflighting" | "awaiting_warning_ack" | "deploying" | "verifying" | "succeeded" | "stale" | "failed" | "rolling_back" | "cleaning_up" | "rolled_back" | "cancelled";
export type DeploymentRun = {
  schema_version: "opsi.deployment_run/v3"; id: string; project_id: string; created_by: string; state: DeploymentRunState;
  plan: DeploymentPlan; analysis: { authority?: string; issues?: AnalysisIssue[]; scope?: AnalysisScope; scope_hash?: string; evidence_coverage?: EvidenceCoverage; files_inspected?: number; bytes_inspected?: number; truncated?: boolean; truncation_reason?: string };
  approval?: { actor: string; plan_hash: string; approved_at: string };
  warning_acknowledgement?: { actor: string; preflight_hash: string; acknowledged_at: string };
  preflight_hash?: string; preflight_warnings?: string[];
  authority_refs: { checkpoints?: Array<{ kind: string; id: string; revision?: number; state_hash?: string; step: DeploymentRunState }> };
  failure?: { step: DeploymentRunState; code: string; message: string; next_action?: string; retryable: boolean };
  public_route_failures?: Array<{ service_key: string; message: string }>;
  attempt: number; revision: number; created_at: string; updated_at: string; finished_at?: string;
};
export type RepositoryExportPreview = { run_id: string; run_revision: number; plan_hash: string; source_sha: string; repository_id: number; target_branch: string; path: string; yaml: string; diff: string; preview_hash: string; export_enabled: boolean; disabled_reason?: string };
export type RepositoryExportResult = { branch: string; commit_sha: string; pull_request_number: number; pull_request_url: string; reused: boolean };
export type WorkloadSecretMetadata = { id: string; reference: string; project_id: string; service_id: string; logical_name: string; revision: number; status: string; updated_at: string };
export type DeploymentRunEvent = { id: string; project_id: string; run_id: string; state: DeploymentRunState; level: string; message: string; metadata?: Record<string, unknown>; created_at: string };
export type DeploymentRunResult = {
  run_id: string; state: DeploymentRunState; source_sha: string; public_url?: string;
  applications: Array<{ service_key: string; service_id: string; build_record_id: string; build_digest: string; build_log_url?: string; deployment_job_id?: string; deployment_status?: string; container_port?: number; application_image_id?: string; available_replicas?: number; readiness_evidence_hash?: string; digest_matches_image_id: boolean; public_url?: string }>;
  public_endpoints?: Array<{ service_key: string; service_id: string; port: number; hostname: string; url: string; status: "publishing" | "ready" | "failed" | "manual_preserved"; message?: string }>;
  public_hostname?: PublicHostnameAllocation;
  verifications: Array<{ id: string; dependency_logical_name: string; overall_status: string; provider_health: { status: string; provider_kind: string; safe_evidence?: Record<string,string> }; connection: { status: string; protocol?: string; latency_ms?: number }; consumer_assertion: { status: string; assertion_path?: string; status_code?: number; expected_code?: number } }>;
  capacity: Array<{ runtime_id: string; source: string; reserved_cpu_millicores: number; reserved_memory_bytes: number; assigned_cpu_millicores: number; assigned_memory_bytes: number; requested_cpu_millicores: number; requested_memory_bytes: number; available_cpu_millicores?: number; available_memory_bytes?: number; unknown_capacity: boolean; oversubscribed: boolean }>;
};

export type PublicHostnameStatus = "reserved" | "provisioning" | "active" | "release_pending" | "failed" | "released";
export type PublicHostnameAllocation = {
  id: string; hostname: string; owner_user_id: string; project_id: string; environment_id: string; runtime_id?: string;
  target_ip?: string; cloudflare_record_id?: string; status: PublicHostnameStatus; publication_error_code?: string; publication_error?: string;
  created_at: string; updated_at: string; released_at?: string;
};
export type PublicHostnameQuota = { used: number; limit: number; remaining: number; allocations: PublicHostnameAllocation[]; project_allocations?: PublicHostnameAllocation[] };

export type EnvironmentVariable = { name: string; value: string };

export type ServiceBinding = {
  kind: "internal_http" | "browser_http";
  target_service_id: string;
  target_service_key: string;
  env_prefix?: string;
  env_name?: string;
  path?: string;
};

export type PublicRouteIntent = { hostname: string; path: string };

export type ServiceResourceBinding = {
  logical_name: string;
  binding_id: string;
};

export type DependencyInjectionMapping = {
  env_name: string;
  symbolic_source: string;
  template?: string;
};

export type DependencyVerificationContract = {
  type: string; // "consumer_http"
  path: string; // relative path e.g. "/health/dependencies/database"
  expected_status: number; // e.g. 200
};

export type ApplicationDependency = {
  logical_name: string;
  target_kind: "managed_resource" | "application" | string;
  target_identity: string;
  protocol: "postgres" | "redis" | "http" | string;
  strategy?: "same_origin" | "internal_http" | "public_http" | string;
  access_context?: "browser" | "server" | string;
  path?: string;
  required: boolean;
  injection_phase: "runtime" | "build" | string;
  injection_mappings?: DependencyInjectionMapping[];
  verification_contract?: DependencyVerificationContract;
};

export type ServiceConfigurationDraft = {
  schema_version?: "opsi.service_configuration/v1";
  environment?: EnvironmentVariable[];
  public_route?: PublicRouteIntent;
  bindings?: ServiceBinding[];
  resource_bindings?: ServiceResourceBinding[];
  dependencies?: ApplicationDependency[];
};

export type ServiceConfiguration = ServiceConfigurationDraft & {
  schema_version: "opsi.service_configuration/v1";
  revision: number;
  state_hash: string;
  applied_by?: string;
  applied_at?: string;
};

export type GeneratedEnvironment = { name: string; value: string; binding: number };
export type ServiceConfigurationPreview = { configuration: ServiceConfigurationDraft; generated_environment?: GeneratedEnvironment[]; current_revision: number; current_state_hash: string; draft_state_hash: string };
export type ServiceConfigurationValidation = { valid: boolean; issues?: { code: string; field?: string; message: string }[] };
export type ServiceConfigurationChange = { kind: "connection" | "dependency" | "resource_binding" | "generated_environment" | "public_route" | "user_environment"; action: string; name?: string; before?: string; after?: string };
export type ServiceConfigurationDiff = { changes: ServiceConfigurationChange[] };
export type ProposalReviewAudit = { proposal_hash: string; reviewed_payload_hash: string; proposer_origin?: "mcp_client" };
export type ProposalReviewStatus = "review_required" | "approved" | "rejected" | "stale" | "expired" | "applied" | "apply_failed";
export type ProposalReview = {
  id: string; project_id: string; environment_id: string; application_id: string;
  kind: "service_configuration" | "source_patch"; status: ProposalReviewStatus;
  proposal_hash: string; analysis_inputs_hash: string; source_commit?: string; application_root?: string;
  normalized_payload: unknown; reviewed_payload_hash: string;
  expected_configuration_revision?: number; expected_configuration_state_hash?: string;
  created_by?: string; created_at: string; expires_at: string; approved_by?: string; approved_at?: string;
  rejected_by?: string; rejected_at?: string; applied_at?: string; resulting_configuration_revision?: number;
};
export type ProposalReviewCreateRequest = {
  environment_id: string; kind: "service_configuration"; analysis_inputs_hash: string;
  configuration_draft?: ServiceConfigurationDraft;
};
export type ServiceConfigurationApplyResult = { configuration: ServiceConfiguration; reused: boolean };

export type DependencyRealizationProjection = {
  env_name: string;
  symbolic_source: string;
  template?: string;
  injection_phase: string;
  conflict: boolean;
  conflict_reason?: string;
};

export type DependencyRealizationPlanItem = {
  logical_name: string;
  target_kind: string;
  target_identity: string;
  target_display_name?: string;
  protocol: string;
  strategy?: string;
  access_context?: string;
  required: boolean;
  injection_phase: string;
  binding_action: "create" | "reuse" | "noop" | "migration_required" | string;
  existing_binding_id?: string;
  projections: DependencyRealizationProjection[];
  status: string;
  message?: string;
};

export type DependencyReviewResult = {
  dependencies: DependencyRealizationPlanItem[];
  realized: DependencyRealizationPlanItem[];
};

export type GitHubInstallation = {
  installation_id: number;
  account_login?: string;
  status: string;
  suspended?: boolean;
};

export type GitHubRepository = {
  repository_id: number;
  installation_id: number;
  owner_login?: string;
  name?: string;
  full_name: string;
  archived?: boolean;
  disabled?: boolean;
  default_branch?: string;
  status: string;
  claim_status: "available" | "active" | "conflict" | string;
  claimed_project_id?: string;
};

export type GitHubBinding = {
  id: string;
  project_id: string;
  service_id: string;
  repository_id: number;
  installation_id: number;
  service_key: string;
  config_path: string;
  selected_ref: string;
  application_root: string;
  build_context: string;
  build_strategy: "auto" | "dockerfile" | "buildpack";
  dockerfile_path?: string;
  status: string;
};

export type BuildJob = {
  id: string;
  project_id: string;
  environment_id: string;
  application_id: string;
  source: {
    binding_id: string;
    binding_updated_at: string;
    github_installation_id: number;
    repository_id: number;
    repository_owner_id: number;
    repository_full_name: string;
    selected_ref: string;
    resolved_commit_sha: string;
    application_root: string;
    build_context: string;
  };
  requested_build_strategy: "auto" | "dockerfile" | "buildpack";
  resolved_build_strategy: "dockerfile" | "buildpack" | "";
  dockerfile_path?: string;
  status: "pending" | "ready" | "running" | "succeeded" | "failed" | "cancelled";
  failure_code?: string;
  failure_message_redacted?: string;
  failure_cause?: string;
  build_record_id?: string;
  completed_at?: string;
  created_by: string;
  created_at: string;
  updated_at: string;
};

export type BuildRecord = {
  schema_version: "opsi.build_record/v1";
  id: string;
  project_id: string;
  repository_id: number;
  repository_owner_id: number;
  active_binding_id: string;
  service_id: string;
  service_key: string;
  created_at: string;
  workload: {
    issuer: string;
    subject: string;
    repository_id: number;
    repository_owner_id: number;
    ref: string;
    sha: string;
    event_name: string;
    workflow: string;
    workflow_ref: string;
    job_workflow_ref?: string;
    run_id: number;
    run_attempt: number;
  };
  build: {
    config_hash: string;
    plan_hash?: string;
    platform: string;
    oci_repository: string;
    oci_digest: string;
    provenance_digest?: string;
    build_job_id?: string;
    build_strategy?: "dockerfile" | "buildpack";
    builder_identity?: string;
    builder_version?: string;
    builder?: {
      pack_version?: string;
      builder_image?: string;
      builder_image_digest?: string;
      run_image?: string;
      run_image_digest?: string;
      lifecycle_version?: string;
      buildpacks?: Array<{ id: string; version: string }>;
      processes?: Array<{ type: string; command?: string[]; arguments?: string[]; direct?: boolean; default?: boolean }>;
    };
    media_type?: string;
    status: string;
  };
};

export type BuildRecordList = { records: BuildRecord[]; next_cursor?: string };

export type TopologyAssignment = {
  service_key: string;
  environment_id: string;
  runtime_id: string;
  replicas: number;
  cpu_request_millicores: number;
  memory_request_bytes: number;
  cpu_limit_millicores?: number;
  memory_limit_bytes?: number;
  exposure: { mode: "none" | "internal" | "public" };
  rationale?: { summary?: string };
};

export type ResourceBudget = { cpu_millicores: number; memory_bytes: number };
export type BudgetProjection = {
  real_capacity: ResourceBudget;
  system_reserve: ResourceBudget;
  existing_workloads: ResourceBudget;
  planned_managed: ResourceBudget;
  available_for_run: ResourceBudget;
  remaining_after_proposal: ResourceBudget;
};
export type ApplicationResourceValues = { cpu_request_milli: number; cpu_limit_milli: number; memory_request_bytes: number; memory_limit_bytes: number };
export type ApplicationRecommendation = { key: string; name: string; replicas: number; current: ApplicationResourceValues; proposed: ApplicationResourceValues };
export type TargetCapacityInfo = { cpu_millicores: number; memory_bytes: number; source: string; heartbeat_age_seconds: number; heartbeat_fresh: boolean };
export type RecommendationBasis = {
  run_revision: number;
  plan_hash: string;
  topology_revision: number;
  topology_hash: string;
  capacity_state_hash: string;
  basis_hash: string;
  observed_at: string;
};
export type ResourceRecommendation = {
  eligible: boolean;
  runtime_id?: string;
  node_id?: string;
  target_capacity: TargetCapacityInfo;
  budget_projection: BudgetProjection;
  basis: RecommendationBasis;
  applications: ApplicationRecommendation[];
  warnings?: string[];
  reason?: string;
};

export type TopologyDraft = { schema_version: "opsi.topology_plan/v1"; project_id: string; assignments: TopologyAssignment[] };
export type TopologyPlan = TopologyDraft & { id: string; revision: number; state_hash: string; plan_hash: string; created_by: string; applied_by: string; created_at: string; applied_at: string };
export type TopologyPreview = { draft: TopologyDraft; plan_hash: string; state_hash: string };
export type TopologyCapacity = {
  runtime_id: string; node_id?: string; agent_id?: string; source: string; heartbeat_age_seconds?: number; heartbeat_fresh: boolean;
  cpu_capacity_millicores?: number; memory_capacity_bytes?: number; reserved_cpu_millicores: number; reserved_memory_bytes: number;
  assigned_cpu_millicores: number; assigned_memory_bytes: number; requested_cpu_millicores: number; requested_memory_bytes: number;
  available_cpu_millicores?: number; available_memory_bytes?: number; unknown_capacity: boolean; unknown_capacity_policy_override: boolean; oversubscribed: boolean;
};
export type TopologyValidation = { schema_version: string; project_id: string; plan_hash: string; valid: boolean; runtimes: Array<{ runtime_id: string; eligible: boolean; capacity: TopologyCapacity; issues: Array<{ code: string; message: string }> }>; issues: Array<{ code: string; message: string; service_key?: string; runtime_id?: string }>; validated_at: string };
export type TopologyDiff = { project_id: string; current_revision: number; current_hash?: string; proposed_hash: string; changes: Array<{ service_key: string; change: string; before?: TopologyAssignment; after?: TopologyAssignment }> };
export type PlacementFacts = {
  project_id: string;
  environments: Array<{ id: string; project_id: string; name: string; type: string; status: string }>;
  runtimes: Array<{ id: string; project_id: string; environment_id: string; name: string; type: string; status: string }>;
  nodes: Array<{ id: string; project_id: string; runtime_id: string; status: string; cpu_cores?: number; memory_mb?: number; last_seen_at?: string }>;
  agents: Array<{ id: string; project_id: string; runtime_id: string; node_id: string; status: string; capabilities: Record<string, unknown>; last_seen_at?: string }>;
  services: Array<{ id: string; project_id: string; key: string }>;
  resources?: Array<{ id: string; project_id: string; environment_id: string; name: string; kind: "managed_service" | "external_resource"; type: string; lifecycle: string; runtime_id?: string; version?: string; replicas?: number; cpu_millicores?: number; memory_bytes?: number }>;
};

export type DeploymentPolicyDraft = {
  schema_version: "opsi.deployment_policy/v1"; project_id: string; repository_id: number; service_keys: string[]; workflow_refs: string[]; job_workflow_refs?: string[];
  allowed_events: string[]; allowed_git_refs: string[]; environment_id: string; allowed_runtime_ids: string[]; allowed_oci_repositories: string[]; allowed_oci_prefixes?: string[];
  allowed_platforms: string[]; allowed_config_hashes: string[]; allowed_build_plan_hashes: string[]; allow_unknown_capacity: boolean; enabled: boolean;
  automatic_main?: boolean;
  preview?: { enabled: boolean; hostname_suffix?: string; ttl_seconds?: number; max_replicas?: number; cpu?: string; memory?: string };
};

export type PreviewSpec = { namespace: string; hostname: string; repository_id: number; repository_owner_id: number; pr_number: number; head_sha: string; service_key: string; cpu: string; memory: string; max_replicas: number; created_at: string; expires_at: string };
export type DeploymentPolicy = { schema_version: string; id: string; revision: number; state_hash: string; policy_hash: string; policy: DeploymentPolicyDraft; created_by: string; applied_by: string; created_at: string; applied_at: string };
export type DeploymentPolicyPreview = { policy: DeploymentPolicyDraft; policy_hash: string; state_hash: string };
export type DeploymentPolicyApplyResult = { policy: DeploymentPolicy; reused: boolean };

export type DeploymentJob = {
	 schema_version?: string;
	 mode?: string;
	 id: string;
	 project_id?: string;
	 environment_id?: string;
	 runtime_id?: string;
	 service_id: string;
	 status: string;
	 action?: string;
	 spec_hash?: string;
	 attempt_count?: number;
	 max_attempts?: number;
	 retry_after?: string;
	 reused?: boolean;
	 started_at?: string;
	 finished_at?: string;
	 updated_at?: string;
	 deployment_plan_hash?: string;
  manifest_hash?: string;
  intent_hash?: string;
  deployment_intent?: unknown;
  previous_revision_ref?: string;
  rollback_eligible?: boolean;
  rollback_blocked_reason?: string;
  agent_id?: string;
  node_id?: string;
  failure_code?: string;
	 failure_message_redacted?: string;
	 terminal_result?: {
		schema_version: string;
		status: string;
		spec_hash: string;
		application_image: string;
		application_image_id: string;
		namespace: string;
		deployment_name: string;
		service_name: string;
		available_replicas: number;
		failure_code?: string;
		failure_message_redacted?: string;
		rollout_id?: string;
		rollout_state?: string;
		intent_hash?: string;
		state_hash?: string;
		workload_spec_hash?: string;
		exposure_spec_hash?: string;
		desired_digest?: string;
		current_digest?: string;
		previous_digest?: string;
		known_good_id?: string;
		known_good_hash?: string;
		readiness_evidence_hash?: string;
	 };
	 base_deployment_id?: string;
	 rollout_state?: string;
	 rollout_state_hash?: string;
	 desired_digest?: string;
	 current_digest?: string;
	 previous_digest?: string;
	 known_good_id?: string;
	 known_good_hash?: string;
	 readiness_evidence_hash?: string;
	 exposure_spec?: ExposureSpec;
  requested_by?: string;
  warning_acknowledgements?: string[];
	 created_at: string;
	 snapshot?: {
		project_id: string;
		image: { repository: string; digest: string; reference: string };
		authority: { build_record: BuildRecord; topology_plan_id: string; topology_revision: number; topology_hash?: string; service_configuration_revision?: number; service_configuration_state_hash?: string; deployment_policy_id: string; deployment_policy_revision: number; deployment_policy_hash?: string; expected_preflight_hash?: string; runtime_id: string; node_id: string; agent_id: string };
		workload: WorkloadSpec;
		spec_hash: string;
		preview?: PreviewSpec;
	};
};

export type ExposureSpec = {
	schema_version: "opsi.exposure_spec/v1";
	project_id: string;
	environment_id: string;
	runtime_id: string;
	service_key: string;
	deployment_job_id: string;
	hostname: string;
	path: string;
	service_port: number;
	tls: { mode: "disabled" | "secret_ref"; secret_ref?: string };
	metadata?: { display_name?: string; rationale?: string };
	spec_hash: string;
};

export async function hashExposure(spec: Omit<ExposureSpec, "spec_hash">) {
	const data = new TextEncoder().encode(JSON.stringify({ ...spec, spec_hash: "" }));
	const digest = await crypto.subtle.digest("SHA-256", data);
	return Array.from(new Uint8Array(digest), (value) => value.toString(16).padStart(2, "0")).join("");
}

export type ExposureMutationRequest = {
	schema_version: "opsi.exposure_mutation/v1";
	base_deployment_job_id: string;
	expected_state_hash?: string;
	exposure: ExposureSpec;
};

export type ExposurePreview = {
	schema_version: string;
	base_deployment_job_id: string;
	current?: ExposureSpec;
	desired: ExposureSpec;
	changes: string[];
	state_hash: string;
	eligible: boolean;
	decision_code: string;
	message: string;
	resolved_at: string;
};

export type WorkloadSpec = {
	schema_version: "opsi.workload_spec/v1";
	service_key: string;
	replicas: number;
	application_container_name: "app";
	container_port: number;
	readiness_probe?: { path: string; port: number; initial_delay_seconds: number; period_seconds: number; timeout_seconds: number; failure_threshold: number };
	liveness_probe?: { path: string; port: number; initial_delay_seconds: number; period_seconds: number; timeout_seconds: number; failure_threshold: number };
	resources: { requests: { cpu: string; memory: string }; limits: { cpu: string; memory: string } };
	termination_grace_period_seconds: number;
	environment?: Array<{ name: string; value: string }>;
	secret_references?: Array<{ env_name: string; secret_id: string }>;
	registry_pull_credential?: { provider: string; credential_id: string; registry: string };
	exposure: { mode: "none" | "internal" };
};

export type PreflightCheck = {
  id: string;
  code: string;
  severity: "PASS" | "WARN" | "BLOCK";
  scope_kind: string;
  scope_id: string;
  dependency_logical_name?: string;
  target_safe_id?: string;
  message: string;
  remediation_code?: string;
  safe_evidence?: Record<string, string>;
};

export type PreflightResult = {
  status: "PASS" | "PASS_WITH_WARNINGS" | "BLOCKED";
  checks: PreflightCheck[];
  authority_fingerprint: string;
  preflight_hash: string;
  generated_at: string;
};

export type DeploymentPreview = {
	schema_version: string;
	snapshot: NonNullable<DeploymentJob["snapshot"]>;
	current?: DeploymentJob["snapshot"];
	changes: string[];
	eligible: boolean;
	decision_code: string;
	message: string;
	preflight?: PreflightResult;
	resolved_at: string;
};

export type ProviderHealthLayer = {
  status: "HEALTHY" | "UNHEALTHY" | "PENDING" | string;
  provider_kind: string;
  provider_id: string;
  safe_evidence?: Record<string, string>;
  failure_code?: string;
  message?: string;
};

export type ContractResolutionLayer = {
  status: "RESOLVED" | "INVALID" | string;
  binding_id?: string;
  protocol?: string;
  injection_complete: boolean;
  failure_code?: string;
  message?: string;
};

export type ConnectionLayer = {
  status: "VERIFIED" | "FAILED" | "NOT_SUPPORTED" | "NOT_CONFIGURED" | string;
  protocol?: string;
  latency_ms?: number;
  failure_code?: string;
  message?: string;
};

export type ConsumerHealthLayer = {
  status: "HEALTHY" | "UNHEALTHY" | string;
  ready_pods: number;
  total_pods: number;
  failure_code?: string;
  message?: string;
};

export type ConsumerAssertionLayer = {
  status: "VERIFIED" | "FAILED" | "NOT_CONFIGURED" | "NOT_SUPPORTED" | string;
  assertion_path?: string;
  status_code?: number;
  expected_code?: number;
  failure_code?: string;
  message?: string;
};

export type VerificationRun = {
  schema_version: "opsi.verification/v1";
  id: string;
  project_id: string;
  environment_id: string;
  consumer_application_id: string;
  dependency_logical_name: string;
  deployment_job_id: string;
  config_revision: number;
  target_binding_id?: string;
  source_commit_sha?: string;
  staleness_hash: string;
  provider_health: ProviderHealthLayer;
  contract_resolution: ContractResolutionLayer;
  connection: ConnectionLayer;
  consumer_health: ConsumerHealthLayer;
  consumer_assertion: ConsumerAssertionLayer;
  overall_status: "VERIFIED" | "PARTIALLY_VERIFIED" | "FAILED" | "STALE" | "NOT_RUN" | string;
  failure_code?: string;
  triggered_by: string;
  started_at: string;
  completed_at?: string;
};

export type DepProbeEvidence = {
  status_code?: number;
  latency_ms?: number;
  message?: string;
};

export type VerifyDependencyRequest = {
  dependency_logical_name: string;
  deployment_job_id: string;
  consumer_contract?: DependencyVerificationContract;
  observed_status_code?: number;
  probe_result?: DepProbeEvidence;
};

export type VerifyDependencyResponse = {
  run: VerificationRun;
};

export type SourceRiskFinding = {
  finding_id: string;
  rule_id: string;
  severity: "INFO" | "WARN" | string;
  confidence: "HIGH" | "MEDIUM" | "LOW" | string;
  category: string;
  dependency_logical_name?: string;
  file: string;
  line: number;
  column?: number;
  safe_evidence: string;
  remediation_code?: string;
};

export type SourceRiskEnvReference = {
  env_key: string;
  file: string;
  line: number;
};

export type SourceRiskReport = {
  scanner_version: string;
  application_id: string;
  project_id: string;
  repository_id: number;
  commit_sha: string;
  application_root: string;
  build_job_id?: string;
  analysis_status: "complete" | "failed" | "unavailable" | string;
  findings: SourceRiskFinding[];
  env_references: SourceRiskEnvReference[];
  files_scanned: number;
  bytes_scanned: number;
  truncated: boolean;
  report_hash: string;
};

export type TimelineEvent = {
  schema_version?: string;
  id: string;
  deployment_id?: string;
  step: string;
  message_redacted: string;
  progress_percent: number;
  attempt?: number;
  request_id?: string;
  created_at: string;
};

export type AgentDiagnosticError = {
  node_id?: string;
  agent_id?: string;
  endpoint?: string;
  code: string;
  message_redacted: string;
  actionable_cause?: string;
};

export type TelemetryCoverage = {
  status: "connected" | "partial" | "unavailable";
  expected_agents: number;
  successful_agents: number;
  failed_agents: number;
  errors?: AgentDiagnosticError[];
  observed_at: string;
};

export type ProjectAgentConnection = {
  project_id: string;
  status: "connected" | "partial" | "unavailable";
  expected_agents: number;
  successful_agents: number;
  failed_agents: number;
  errors?: AgentDiagnosticError[];
  observed_at: string;
};

export type TelemetrySummary = {
  project_id: string;
  since_unix: number;
  chunk_count: number;
  record_count: number;
  start_unix: number;
  end_unix: number;
  done: boolean;
  source: "agent";
  payload_policy: string;
  health?: string;
  metric_count?: number;
  log_count?: number;
  error_count?: number;
  service_count?: number;
  coverage?: TelemetryCoverage;
  services?: TelemetryServiceStatus[];
};

export type TelemetryLogEntry = {
  service_id?: string;
  pod_id?: string;
  namespace?: string;
  level: string;
  message: string;
  fingerprint: string;
  observed_unix: number;
};

export type TelemetryServiceStatus = {
  service_id: string;
  health: string;
  pod_count: number;
  ready_pods: number;
  cpu_cores?: number;
  memory_bytes?: number;
  restart_count?: number;
  recent_error_count?: number;
  last_seen_unix?: number;
};

export type TelemetryQueryResponse = {
  project_id: string;
  source: "agent";
  payload_policy: string;
  summary?: {
    since_unix: number;
    end_unix: number;
    metric_count: number;
    log_count: number;
    error_count: number;
    service_count: number;
    health: string;
  };
  services?: TelemetryServiceStatus[];
  logs?: TelemetryLogEntry[];
  next_cursor?: string;
  coverage?: TelemetryCoverage;
};

export type SecretResult = {
  status: string;
  source: "agent";
  project_id: string;
  service_id: string;
  name: string;
  namespace?: string;
  username?: string;
  password?: string;
  ttl_seconds?: number;
  reveal_expires_at?: string;
};

export type IncidentResponse = {
  incident_id: string;
  project_id: string;
  node_id?: string;
  service_id?: string;
  pod_id?: string;
  status: string;
  severity?: string;
  anomaly_type?: string;
  created_at_unix?: number;
  resolved_at_unix?: number;
  mttr_seconds?: number;
};

export type IncidentResult = {
  status?: string;
  source: "agent";
  payload_policy: string;
  incident: IncidentResponse;
};

export type IncidentListResult = {
  source: "agent";
  payload_policy: string;
  incidents: IncidentResponse[];
  coverage?: TelemetryCoverage;
};

export type IncidentEvidence = {
  schema_version: string;
  identity: IncidentResponse;
  generated_at_unix: number;
  observation_window: { start_unix: number; end_unix: number };
  deployment: { desired_digest?: string; previous_digest?: string; observed_digest?: string; restored_digest?: string };
  rollout: { rollout_id?: string; state?: string; failure_code?: string; readiness_hash?: string; event_correlation?: string[] };
  timeline?: Array<{ observed_at_unix: number; source: string; kind: string; detail?: string; untrusted_content: boolean }>;
  pods?: Array<{ namespace?: string; pod_id: string; node_id?: string; ready_containers: number; total_containers: number; restart_count: number; observed_digest?: string }>;
  kubernetes_events?: Array<{ observed_at_unix: number; namespace?: string; object_kind?: string; object_name?: string; type?: string; reason?: string; message?: string; untrusted_content: boolean }>;
  log_fingerprints?: Array<{ fingerprint: string; level?: string; count: number; first_observed_unix: number; last_observed_unix: number; excerpt?: string; untrusted_content: boolean }>;
  audit_references?: Array<{ audit_id: string; action: string; resource_type: string; resource_id: string; result: string; created_at_unix: number }>;
  coverage: Array<{ source: string; status: string; reason_code?: string; item_count: number; truncated: boolean }>;
  truncations?: Array<{ section: string; omitted_items: number; utf8_safe: boolean }>;
  content_sha256: string;
};

export type SSHHostKeyTrust = {
  id: string;
  project_id: string;
  host: string;
  port: number;
  algorithm: string;
  fingerprint: string;
  status: "active" | "superseded";
  created_at: string;
  superseded_at?: string;
};

export type SSHHostKeyObservation = {
  id: string;
  probe_id: string;
  project_id: string;
  public_host: string;
  ssh_port: number;
  resolved_ip: string;
  algorithm: string;
  fingerprint: string;
  trust_state: "first_seen" | "matched" | "changed";
  previous_fingerprint?: string;
  status: "pending" | "confirmed" | "consumed" | "expired";
  expires_at: string;
  created_at: string;
};

export type BootstrapSession = {
  id: string;
  status: string;
  public_host?: string;
  resolved_ip?: string;
  role: string;
  auth_method?: string;
  ssh_host_key_trust_id?: string;
  bootstrap_command?: string;
  attempt_count?: number;
  max_attempts?: number;
  last_failure_code?: string;
  last_failure_message_redacted?: string;
  checkpoint?: {
    plan_version: string;
    next_step_index: number;
    last_completed_step?: string;
  };
  created_at: string;
};
export type AuditEvent = {
  id: string;
  actor_user_id?: string;
  actor_type: string;
  action: string;
  resource_type: string;
  resource_id: string;
  result: string;
  metadata_redacted?: Record<string, unknown>;
  created_at: string;
};

export type SupportSignal = {
  name: string;
  status: string;
  value: string;
  target: string;
  detail?: string;
};

export type SupportAlert = {
  id: string;
  severity: string;
  status: string;
  title: string;
  resource_id?: string;
  runbook_id: string;
};

export type SupportAlertRule = {
  id: string;
  severity: string;
  title: string;
  metric: string;
  runbook_id: string;
};

export type SupportRunbook = {
  id: string;
  title: string;
  symptoms: string;
  impact: string;
  dashboard_query: string;
  immediate_mitigation: string;
  long_term_fix: string;
  customer_communication: string;
  escalation_path: string;
};

export type GrafanaSeries = {
  name: string;
  status: string;
  value: number;
  points?: number[];
};

export type GrafanaPanel = {
  id: string;
  title: string;
  kind: string;
  unit: string;
  query: string;
  description?: string;
  series: GrafanaSeries[];
};

export type GrafanaDashboard = {
  title: string;
  datasource: string;
  refresh: string;
  panels: GrafanaPanel[];
};

export type ProductionGate = {
  name: string;
  passed: boolean;
  detail: string;
};

export type BreakGlassPolicy = {
  time_limited: boolean;
  approval_required: boolean;
  reason_required: boolean;
  audited: boolean;
  secret_reveal_by_default: boolean;
  owner_notification: string;
};

export type SupportSummary = {
  generated_at: string;
  readiness: Readiness;
  counts: {
    nodes: number;
    healthy_nodes: number;
    services: number;
    deployment_jobs: number;
    failed_deployments: number;
    bootstrap_sessions: number;
    open_bootstrap_jobs: number;
    audit_events: number;
  };
  dashboard: GrafanaDashboard;
  signals: SupportSignal[];
  active_alerts: SupportAlert[];
  configured_alerts: SupportAlertRule[];
  production_gates: ProductionGate[];
  break_glass_policy: BreakGlassPolicy;
  runbooks: SupportRunbook[];
  recent_request_ids?: string[];
};

export type NodeDiagnostics = {
  node?: NodeRecord;
  open_bootstrap_events?: TimelineEvent[];
  recent_deployment_jobs?: DeploymentJob[];
};

export type ConsoleState = {
  status: LoadState;
  message: string;
  projects: Project[];
  project: Project | null;
  readiness: Readiness | null;
  nodes: NodeRecord[];
  services: ServiceRecord[];
  deployments: DeploymentJob[];
  sessions: BootstrapSession[];
  bootstrapCommand: string;
  bootstrapCommandSessionID: string;
  bootstrapEvents: TimelineEvent[];
  bootstrapEventsSessionID: string;
  deploymentEvents: TimelineEvent[];
  audit: AuditEvent[];
  support: SupportSummary | null;
  secretReveal: SecretResult | null;
  totpSetup: { secret: string; uri: string; ttl_seconds: number } | null;
  incidents: IncidentResponse[];
  nodeDetail: NodeDiagnostics | null;
  serviceDetail: ServiceRecord | null;
  busy: string;
};

export type ResourceKind = "application" | "managed_service" | "external_resource";
export type ResourceLifecycle = "unplaced" | "planned" | "provisioning" | "ready" | "updating" | "degraded" | "failed" | "deleting" | "unknown" | "configured";

export type ManagedResourceSpec = {
  type: "postgres" | "redis" | "nats" | string;
  version?: string;
  profile?: string;
  replicas: number;
  cpu_millicores: number;
  memory_bytes: number;
  storage: { persistent: boolean; size_bytes?: number; policy_ref?: string };
  service_config?: Record<string, string>;
  credential_refs?: Array<{ secret_id: string; key?: string }>;
  connection_policy: { mode: string };
};

export type ManagedResourceEvidence = {
  observed_spec_hash?: string;
  workload_ready?: boolean;
  pod_ready?: boolean;
  service_ready?: boolean;
  secret_ready?: boolean;
  auth_ready?: boolean;
  storage_ready?: boolean;
  volume_mounted?: boolean;
  image?: string;
  image_id?: string;
  available_replicas?: number;
  namespace?: string;
  pvc_name?: string;
  pvc_uid?: string;
  pv_name?: string;
  pv_uid?: string;
  storage_class?: string;
  reclaim_policy?: string;
  requested_bytes?: number;
  actual_storage?: string;
  storage_hash?: string;
  storage_retained?: boolean;
  deleted?: boolean;
  observed_at?: string;
};

export type Resource = {
  schema_version?: string;
  id: string;
  project_id: string;
  environment_id: string;
  name: string;
  kind: ResourceKind;
  provider?: string;
  type: string;
  lifecycle: ResourceLifecycle;
  managed?: ManagedResourceSpec;
  external?: Record<string, unknown>;
  internal_name?: string;
  runtime?: {
    spec: {
      schema_version: string;
      resource_id: string;
      project_id: string;
      environment_id: string;
      resource_type: string;
      profile: string;
      version: string;
      image: string;
      assignment: { runtime_id: string; node_id: string; agent_id: string };
      replicas: number;
      cpu_millicores: number;
      memory_bytes: number;
      ports: Array<{ name: string; port: number; protocol: string }>;
      storage: { persistent: boolean; size_bytes?: number; policy_ref?: string };
      connection: { protocol: string; host: string; port: number; service_name: string; database?: string; url?: string };
      configuration_hash: string;
      topology_revision: number;
      topology_hash: string;
      spec_hash: string;
    };
    evidence?: ManagedResourceEvidence;
    failure_code?: string;
    failure_message?: string;
    delete_actor?: string;
  };
  created_by: string;
  created_at: string;
  updated_at: string;
};

export type CreateResourceRequest = {
  environment_id: string;
  name: string;
  kind: ResourceKind;
  provider?: string;
  type: string;
  managed?: ManagedResourceSpec;
  external?: Record<string, unknown>;
};

export type UpdateResourceRequest = {
  managed?: ManagedResourceSpec;
  external?: Record<string, unknown>;
};

export type ResourceTypeDefinition = {
  type: string;
  display_name: string;
  support_tier: string;
  stateful: boolean;
  default_port: number;
  protocols: string[];
  required_config: string[];
  optional_config: string[];
  credential_keys: string[];
  generated_values: Array<{ name: string; sensitivity: "non_secret" | "secret" }>;
  storage: { supported: boolean; required: boolean };
  provisioning: { implemented: boolean; profiles: Array<{ name: string; versions: Array<{ version: string; image: string }> }> };
};

export type ResourceBinding = {
  schema_version?: string;
  id: string;
  project_id: string;
  environment_id: string;
  source: { kind: "application" | string; id: string };
  target: { kind: "managed_service" | string; id: string };
  protocol: string;
  logical_name: string;
  lifecycle: string;
  credential_id?: string;
  role_name?: string;
  database?: string;
  failure_code?: string;
  runtime_refs?: Array<{ name: string; sensitivity: "non_secret" | "secret"; value?: string; secret_ref?: { secret_id: string; key?: string } }>;
  created_at: string;
  updated_at: string;
};

export type CreateResourceBindingRequest = {
  environment_id: string;
  source: { kind: "application" | string; id: string };
  target: { kind: "managed_service" | "external_resource" | string; id: string };
  protocol: string;
  logical_name: string;
  credential_ref?: { secret_id: string; key?: string };
};

export type RetainedStorage = {
  schema_version?: string;
  id: string;
  original_resource_id: string;
  project_id: string;
  environment_id: string;
  resource_type: string;
  resource_name: string;
  namespace: string;
  pvc_name: string;
  pvc_uid: string;
  pv_name: string;
  pv_uid?: string;
  storage_class: string;
  reclaim_policy: string;
  requested_bytes: number;
  actual_size: string;
  storage_hash: string;
  assignment: { runtime_id: string; node_id: string; agent_id: string };
  lifecycle: "retained" | "destroying" | "destroyed" | "destroy_failed" | "unknown";
  revision: number;
  original_created_by?: string;
  retained_by?: string;
  retained_at: string;
  destroy_requested_by?: string;
  destroy_requested_at?: string;
  destroyed_at?: string;
  failure_code?: string;
  failure_message_redacted?: string;
};

export type RetainedStorageReview = {
  retained_storage_id: string;
  original_resource_id: string;
  resource_name: string;
  pvc_name: string;
  pvc_uid: string;
  pv_name: string;
  pv_uid?: string;
  storage_class: string;
  reclaim_policy: string;
  requested_bytes: number;
  actual_size: string;
  storage_hash: string;
  retained_at: string;
  revision: number;
  active_resource: boolean;
  active_binding: boolean;
  warning?: string;
  review_token: string;
  reviewed_at: string;
};

export type DestroyRetainedStorageRequest = {
  review_token: string;
};

export type Backup = {
  schema_version: string;
  id: string;
  project_id: string;
  environment_id: string;
  source_resource_id: string;
  source_node_id: string;
  resource_type: string;
  backup_type: string;
  source_database: string;
  source_postgres_version?: string;
  source_profile?: string;
  source_image?: string;
  source_spec_revision: number;
  source_spec_hash: string;
  source_pvc_name: string;
  source_pvc_uid: string;
  source_pv_name?: string;
  source_pv_uid?: string;
  source_storage_hash: string;
  format: string;
  dump_options: string[];
  lifecycle: "queued" | "leased" | "running" | "succeeded" | "failed";
  store_id: string;
  object_key: string;
  object_etag?: string;
  object_version_id?: string;
  artifact_size?: number;
  sha256?: string;
  pg_dump_version?: string;
  archive_verified: boolean;
  requested_by: string;
  requested_at: string;
  created_at: string;
  leased_at?: string;
  started_at?: string;
  completed_at?: string;
  failure_code?: string;
  failure_message_redacted?: string;
  attempt_count: number;
};

export type RestoreReview = {
  schema_version: string;
  id: string;
  project_id: string;
  environment_id: string;
  backup_id: string;
  backup_created_at: string;
  backup_artifact_sha256: string;
  backup_revision: string;
  source_resource_id: string;
  source_postgres_version: string;
  artifact_size: number;
  target_resource_id: string;
  target_node_id: string;
  target_postgres_version: string;
  target_database: string;
  target_database_oid?: string;
  target_lifecycle: string;
  target_spec_revision: number;
  target_spec_hash: string;
  target_pvc_name: string;
  target_pvc_uid: string;
  target_pv_name?: string;
  target_pv_uid?: string;
  target_storage_hash: string;
  pristine: boolean;
  objects: { schemas: number; tables: number; sequences: number; indexes: number; functions: number };
  pristine_evidence_hash?: string;
  warning?: string;
  lifecycle: "queued" | "leased" | "succeeded" | "failed";
  requested_by: string;
  requested_at: string;
  reviewed_at?: string;
  failure_code?: string;
  failure_message_redacted?: string;
  attempt_count: number;
};

export type Restore = {
  schema_version: string;
  id: string;
  project_id: string;
  environment_id: string;
  review_id: string;
  backup_id: string;
  backup_revision: string;
  source_resource_id: string;
  target_resource_id: string;
  target_node_id: string;
  artifact_sha256: string;
  artifact_size: number;
  source_postgres_version: string;
  target_postgres_version: string;
  source_profile: string;
  source_image: string;
  target_profile: string;
  target_image: string;
  source_spec_revision: number;
  source_spec_hash: string;
  source_pvc_uid: string;
  target_spec_revision: number;
  target_spec_hash: string;
  target_database: string;
  target_database_oid: string;
  target_pvc_name: string;
  target_pvc_uid: string;
  target_storage_hash: string;
  pristine_evidence_hash: string;
  restore_options: string[];
  lifecycle: "queued" | "leased" | "running" | "verifying" | "succeeded" | "failed";
  requested_by: string;
  requested_at: string;
  created_at: string;
  leased_at?: string;
  started_at?: string;
  verifying_at?: string;
  completed_at?: string;
  failure_code?: string;
  failure_message_redacted?: string;
  attempt_count: number;
  restored_objects?: { schemas: number; tables: number; sequences: number; indexes: number; functions: number };
};

export type ApplicationCutoverReview = {
  schema_version: string;
  id: string;
  project_id: string;
  environment_id: string;
  application_id: string;
  source_binding_id: string;
  source_resource_id: string;
  target_resource_id: string;
  target_binding_id: string;
  application_config_revision: number;
  application_config_hash: string;
  source_binding_revision: string;
  target_binding_revision: string;
  source_resource_revision: number;
  source_resource_spec_hash: string;
  target_resource_revision: number;
  target_resource_spec_hash: string;
  target_restore_id: string;
  target_restore_revision: string;
  backup_id: string;
  backup_completed_at?: string;
  restore_completed_at?: string;
  backup_age_seconds: number;
  validation_summary: {
    source_sql_preflight: string;
    target_sql_preflight: string;
    target_role_attributes: string;
    source_binding_ready: boolean;
    target_binding_ready: boolean;
    target_restore_ready: boolean;
    target_pvc_uid?: string;
    target_pv_uid?: string;
    target_storage_hash?: string;
  };
  warnings: string[];
  lifecycle: "queued" | "leased" | "succeeded" | "failed";
  requested_by: string;
  requested_at: string;
  reviewed_at?: string;
  failure_code?: string;
  failure_message_redacted?: string;
  attempt_count: number;
  target_node_id?: string;
  evidence_hash?: string;
};

export type ApplicationCutover = {
  schema_version: string;
  id: string;
  project_id: string;
  environment_id: string;
  application_id: string;
  cutover_review_id: string;
  source_binding_id: string;
  target_binding_id: string;
  source_resource_id: string;
  target_resource_id: string;
  reviewed_application_config_revision: number;
  reviewed_application_config_hash: string;
  pre_cutover_application_config_revision: number;
  pre_cutover_application_config_hash: string;
  resulting_application_config_revision: number;
  resulting_application_config_hash: string;
  deployment_job_id?: string;
  lifecycle: "queued" | "validating" | "applying" | "deploying" | "verifying" | "succeeded" | "failed";
  requested_by: string;
  requested_at: string;
  applied_at?: string;
  completed_at?: string;
  updated_at: string;
  target_node_id?: string;
  failure_code?: string;
  failure_message_redacted?: string;
  verification_summary: {
    source_sql_preflight: string;
    target_sql_preflight: string;
    target_role_attributes: string;
    deployment_ready: boolean;
    workload_ready: boolean;
    target_db_connected: boolean;
    restored_data_verified: boolean;
    target_only_marker_present: boolean;
    source_only_marker_absent: boolean;
    post_cutover_target_written: boolean;
    source_rollback_preserved: boolean;
  };
  evidence_hash?: string;
};

export type ApplicationCutoverRollback = {
  schema_version: string;
  id: string;
  project_id: string;
  environment_id: string;
  application_id: string;
  cutover_id: string;
  source_binding_id: string;
  target_binding_id: string;
  source_resource_id: string;
  target_resource_id: string;
  current_application_config_revision: number;
  current_application_config_hash: string;
  original_pre_cutover_application_config_revision: number;
  original_pre_cutover_application_config_hash: string;
  resulting_application_config_revision: number;
  resulting_application_config_hash: string;
  deployment_job_id?: string;
  lifecycle: "queued" | "validating" | "applying" | "deploying" | "verifying" | "succeeded" | "failed";
  requested_by?: string;
  requested_at: string;
  applied_at?: string;
  completed_at?: string;
  updated_at: string;
  target_node_id?: string;
  failure_code?: string;
  failure_message_redacted?: string;
  warnings?: string[];
  verification_summary?: {
    source_sql_preflight: string;
    target_sql_preflight?: string;
    source_role_attributes: string;
    deployment_ready: boolean;
    workload_ready: boolean;
    source_db_connected: boolean;
    source_marker_present: boolean;
    target_marker_absent: boolean;
    post_rollback_source_written: boolean;
    target_authority_preserved: boolean;
  };
  evidence_hash?: string;
};

export type ApplicationCutoverFinalization = {
  schema_version: string;
  id: string;
  project_id: string;
  environment_id: string;
  application_id: string;
  cutover_id: string;
  source_binding_id: string;
  target_binding_id: string;
  source_resource_id: string;
  target_resource_id: string;
  application_config_revision: number;
  application_config_hash: string;
  cutover_evidence_hash: string;
  lifecycle: "queued" | "validating" | "revoking_source_binding" | "verifying" | "succeeded" | "failed";
  requested_by: string;
  requested_at: string;
  completed_at?: string;
  updated_at: string;
  target_node_id?: string;
  failure_code?: string;
  failure_message_redacted?: string;
  verification_summary: {
    target_sql_preflight: string;
    target_role_attributes: string;
    target_db_connected: boolean;
    target_only_marker_present: boolean;
    post_cutover_marker_present: boolean;
    source_marker_absent: boolean;
    source_binding_revoked: boolean;
    source_credential_rejected: boolean;
    source_resource_retained: boolean;
    post_finalize_target_written: boolean;
  };
  evidence_hash?: string;
};
