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
  status: string;
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
  exposure: { mode: "none" | "internal" | "public" };
  rationale?: { summary?: string };
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
};

export type DeploymentPolicyDraft = {
  schema_version: "opsi.deployment_policy/v1"; project_id: string; repository_id: number; service_keys: string[]; workflow_refs: string[]; job_workflow_refs?: string[];
  allowed_events: string[]; allowed_git_refs: string[]; environment_id: string; allowed_runtime_ids: string[]; allowed_oci_repositories: string[]; allowed_oci_prefixes?: string[];
  allowed_platforms: string[]; allowed_config_hashes: string[]; allowed_build_plan_hashes: string[]; allow_unknown_capacity: boolean; enabled: boolean;
};
export type DeploymentPolicy = { schema_version: string; id: string; revision: number; state_hash: string; policy_hash: string; policy: DeploymentPolicyDraft; created_by: string; applied_by: string; created_at: string; applied_at: string };
export type DeploymentPolicyPreview = { policy: DeploymentPolicyDraft; policy_hash: string; state_hash: string };
export type DeploymentPolicyApplyResult = { policy: DeploymentPolicy; reused: boolean };

export type DeploymentJob = {
  id: string;
  service_id: string;
  status: string;
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
  requested_by?: string;
  created_at: string;
};

export type TimelineEvent = {
  id: string;
  deployment_id?: string;
  step: string;
  message_redacted: string;
  progress_percent: number;
  request_id?: string;
  created_at: string;
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
};

export type BootstrapSession = {
  id: string;
  status: string;
  public_host?: string;
  role: string;
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
  bootstrapEvents: TimelineEvent[];
  deploymentEvents: TimelineEvent[];
  audit: AuditEvent[];
  support: SupportSummary | null;
  secretReveal: SecretResult | null;
  incidents: IncidentResponse[];
  incidentDetail: IncidentResponse | null;
  incidentError: string;
  nodeDetail: NodeDiagnostics | null;
  serviceDetail: ServiceRecord | null;
  busy: string;
};
