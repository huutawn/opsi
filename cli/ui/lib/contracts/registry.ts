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
