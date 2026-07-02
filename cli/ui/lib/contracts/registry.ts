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
  container_port?: number;
  health_path?: string;
  replicas?: number;
  namespace?: string;
};

export type DeploymentJob = {
  id: string;
  service_id: string;
  status: string;
  deployment_plan_hash?: string;
  manifest_hash?: string;
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

export type BootstrapSession = {
  id: string;
  status: string;
  public_host?: string;
  role: string;
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
  signals: SupportSignal[];
  active_alerts: SupportAlert[];
  configured_alerts: SupportAlertRule[];
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
  nodeDetail: NodeDiagnostics | null;
  serviceDetail: ServiceRecord | null;
  busy: string;
};
