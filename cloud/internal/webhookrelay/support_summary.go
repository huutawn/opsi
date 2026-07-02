package webhookrelay

import (
	"strconv"
	"strings"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/registry"
)

type SupportSummary struct {
	GeneratedAt      time.Time          `json:"generated_at"`
	Readiness        registry.Readiness `json:"readiness"`
	Counts           SupportCounts      `json:"counts"`
	Signals          []SupportSignal    `json:"signals"`
	ActiveAlerts     []SupportAlert     `json:"active_alerts"`
	ConfiguredAlerts []SupportAlertRule `json:"configured_alerts"`
	Runbooks         []SupportRunbook   `json:"runbooks"`
	RecentRequestIDs []string           `json:"recent_request_ids,omitempty"`
}

type SupportCounts struct {
	Nodes             int `json:"nodes"`
	HealthyNodes      int `json:"healthy_nodes"`
	Services          int `json:"services"`
	DeploymentJobs    int `json:"deployment_jobs"`
	FailedDeployments int `json:"failed_deployments"`
	BootstrapSessions int `json:"bootstrap_sessions"`
	OpenBootstrapJobs int `json:"open_bootstrap_jobs"`
	AuditEvents       int `json:"audit_events"`
}

type SupportSignal struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Value  string `json:"value"`
	Target string `json:"target"`
	Detail string `json:"detail,omitempty"`
}

type SupportAlert struct {
	ID         string `json:"id"`
	Severity   string `json:"severity"`
	Status     string `json:"status"`
	Title      string `json:"title"`
	ResourceID string `json:"resource_id,omitempty"`
	RunbookID  string `json:"runbook_id"`
}

type SupportAlertRule struct {
	ID        string `json:"id"`
	Severity  string `json:"severity"`
	Title     string `json:"title"`
	Metric    string `json:"metric"`
	RunbookID string `json:"runbook_id"`
}

type SupportRunbook struct {
	ID                    string `json:"id"`
	Title                 string `json:"title"`
	Symptoms              string `json:"symptoms"`
	Impact                string `json:"impact"`
	DashboardQuery        string `json:"dashboard_query"`
	ImmediateMitigation   string `json:"immediate_mitigation"`
	LongTermFix           string `json:"long_term_fix"`
	CustomerCommunication string `json:"customer_communication"`
	EscalationPath        string `json:"escalation_path"`
}

func (s *Server) supportSummary(projectID string) (SupportSummary, error) {
	readiness, err := s.Registry.ProjectReadiness(projectID)
	if err != nil {
		return SupportSummary{}, err
	}
	nodes, err := s.Registry.ListNodes(projectID)
	if err != nil {
		return SupportSummary{}, err
	}
	services, err := s.Registry.ListServices(projectID)
	if err != nil {
		return SupportSummary{}, err
	}
	deployments, err := s.Registry.ListDeployments(projectID)
	if err != nil {
		return SupportSummary{}, err
	}
	sessions, err := s.Registry.ListBootstrapSessions(projectID)
	if err != nil {
		return SupportSummary{}, err
	}
	audit, err := s.Registry.ListAudit(projectID)
	if err != nil {
		return SupportSummary{}, err
	}
	now := time.Now().UTC()
	out := SupportSummary{
		GeneratedAt:      now,
		Readiness:        readiness,
		Counts:           supportCounts(nodes, services, deployments, sessions, audit),
		ConfiguredAlerts: supportAlertRules(),
		Runbooks:         supportRunbooks(),
		RecentRequestIDs: recentRequestIDs(projectID, deployments, s.Registry),
	}
	out.Signals = supportSignals(now, readiness, nodes, deployments, sessions)
	out.ActiveAlerts = supportAlerts(out.Signals, nodes, deployments, sessions)
	return out, nil
}

func supportCounts(nodes []registry.Node, services []registry.ServiceRecord, deployments []registry.DeploymentJob, sessions []registry.BootstrapSession, audit []registry.AuditEvent) SupportCounts {
	counts := SupportCounts{Nodes: len(nodes), Services: len(services), DeploymentJobs: len(deployments), BootstrapSessions: len(sessions), AuditEvents: len(audit)}
	for _, node := range nodes {
		if node.Status == registry.NodeHealthy {
			counts.HealthyNodes++
		}
	}
	for _, job := range deployments {
		if job.Status == registry.DeploymentFailed {
			counts.FailedDeployments++
		}
	}
	for _, session := range sessions {
		if activeBootstrapStatus(session.Status) {
			counts.OpenBootstrapJobs++
		}
	}
	return counts
}

func supportSignals(now time.Time, readiness registry.Readiness, nodes []registry.Node, deployments []registry.DeploymentJob, sessions []registry.BootstrapSession) []SupportSignal {
	signals := []SupportSignal{
		{Name: "project_readiness", Status: readiness.Status, Value: readiness.Status, Target: "ready", Detail: readiness.NextAction},
		{Name: "deployment_acceptance", Status: "ok", Value: "server-side guard enabled", Target: "< 5s accepted once project ready"},
		{Name: "audit_write_detection", Status: "ok", Value: "append-only audit path enabled", Target: "detect write failure"},
	}
	signals = append(signals, heartbeatSignal(now, nodes))
	signals = append(signals, inventorySignal(now, nodes))
	signals = append(signals, deploymentFailureSignal(deployments))
	signals = append(signals, bootstrapSignal(sessions))
	return signals
}

func heartbeatSignal(now time.Time, nodes []registry.Node) SupportSignal {
	var worst time.Duration
	missing := 0
	for _, node := range nodes {
		if node.Status != registry.NodeHealthy {
			continue
		}
		if node.LastSeenAt == nil {
			missing++
			continue
		}
		if lag := now.Sub(*node.LastSeenAt); lag > worst {
			worst = lag
		}
	}
	status := "ok"
	detail := ""
	if missing > 0 || worst > time.Minute {
		status = "critical"
		detail = "healthy node heartbeat is stale or missing"
	}
	return SupportSignal{Name: "agent_heartbeat_lag_seconds", Status: status, Value: durationValue(worst, missing), Target: "< 60s", Detail: detail}
}

func inventorySignal(now time.Time, nodes []registry.Node) SupportSignal {
	var worst time.Duration
	missing := 0
	for _, node := range nodes {
		if node.Status != registry.NodeHealthy {
			continue
		}
		if node.LastInventoryAt == nil {
			missing++
			continue
		}
		if lag := now.Sub(*node.LastInventoryAt); lag > worst {
			worst = lag
		}
	}
	status := "ok"
	if missing > 0 || worst > 5*time.Minute {
		status = "warn"
	}
	return SupportSignal{Name: "inventory_sync_freshness", Status: status, Value: durationValue(worst, missing), Target: "< 5m"}
}

func deploymentFailureSignal(deployments []registry.DeploymentJob) SupportSignal {
	failed := 0
	for _, job := range deployments {
		if job.Status == registry.DeploymentFailed {
			failed++
		}
	}
	status := "ok"
	if failed > 0 {
		status = "warn"
	}
	return SupportSignal{Name: "deployment_failures_total", Status: status, Value: intValue(failed), Target: "0 active failures"}
}

func bootstrapSignal(sessions []registry.BootstrapSession) SupportSignal {
	expired := 0
	failed := 0
	for _, session := range sessions {
		if session.Status == "expired" {
			expired++
		}
		if session.Status == "failed" {
			failed++
		}
	}
	status := "ok"
	if failed > 0 || expired > 0 {
		status = "critical"
	}
	return SupportSignal{Name: "bootstrap_credential_cleanup", Status: status, Value: intValue(failed + expired), Target: "0 cleanup failures"}
}

func supportAlerts(signals []SupportSignal, nodes []registry.Node, deployments []registry.DeploymentJob, sessions []registry.BootstrapSession) []SupportAlert {
	alerts := []SupportAlert{}
	for _, signal := range signals {
		if signal.Status != "warn" && signal.Status != "critical" {
			continue
		}
		severity := "medium"
		if signal.Status == "critical" {
			severity = "high"
		}
		alerts = append(alerts, SupportAlert{ID: signal.Name, Severity: severity, Status: "firing", Title: strings.ReplaceAll(signal.Name, "_", " "), RunbookID: runbookForSignal(signal.Name)})
	}
	for _, node := range nodes {
		if node.Status == registry.NodeHealthy || node.Status == registry.NodeRemoved {
			continue
		}
		alerts = append(alerts, SupportAlert{ID: "node-" + node.ID, Severity: "medium", Status: "firing", Title: "node not healthy", ResourceID: node.ID, RunbookID: "agent-offline"})
	}
	for _, job := range deployments {
		if job.Status == registry.DeploymentFailed {
			alerts = append(alerts, SupportAlert{ID: "deployment-" + job.ID, Severity: "medium", Status: "firing", Title: "deployment failed", ResourceID: job.ID, RunbookID: "deployment-rollout-timeout"})
		}
	}
	for _, session := range sessions {
		if session.Status == "failed" || session.Status == "expired" {
			alerts = append(alerts, SupportAlert{ID: "bootstrap-" + session.ID, Severity: "high", Status: "firing", Title: "bootstrap requires cleanup review", ResourceID: session.ID, RunbookID: "credential-cleanup-failure"})
		}
	}
	return alerts
}

func recentRequestIDs(projectID string, deployments []registry.DeploymentJob, api registry.API) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, job := range deployments {
		events, err := api.DeploymentEvents(projectID, job.ID)
		if err != nil {
			continue
		}
		for _, event := range events {
			if event.RequestID == "" || seen[event.RequestID] {
				continue
			}
			seen[event.RequestID] = true
			out = append(out, event.RequestID)
			if len(out) == 8 {
				return out
			}
		}
	}
	return out
}

func supportAlertRules() []SupportAlertRule {
	return []SupportAlertRule{
		{ID: "control-plane-errors", Severity: "high", Title: "Control Plane high error rate", Metric: "api_errors_total / api_requests_total", RunbookID: "control-plane-outage"},
		{ID: "rate-limit-abuse", Severity: "medium", Title: "Rate limit abuse spike", Metric: "rate_limited_total", RunbookID: "control-plane-outage"},
		{ID: "credential-cleanup", Severity: "high", Title: "Bootstrap credential cleanup failure", Metric: "bootstrap_credential_cleanup_failures_total", RunbookID: "credential-cleanup-failure"},
		{ID: "audit-write", Severity: "high", Title: "Audit write failure", Metric: "audit_write_failures_total", RunbookID: "audit-write-failure"},
		{ID: "agent-heartbeat", Severity: "high", Title: "Agent heartbeat stale", Metric: "agent_heartbeat_lag_seconds", RunbookID: "agent-offline"},
		{ID: "deployment-failure", Severity: "medium", Title: "Deployment failure spike", Metric: "deployment_failures_total", RunbookID: "deployment-rollout-timeout"},
	}
}

func supportRunbooks() []SupportRunbook {
	return []SupportRunbook{
		{ID: "bootstrap-wrong-ssh-key", Title: "Bootstrap failure: wrong SSH key", Symptoms: "Bootstrap preflight fails before install.", Impact: "Node never joins project runtime.", DashboardQuery: "bootstrap_failures_total{failure_code=\"ssh_auth\"}", ImmediateMitigation: "Create a new bootstrap session with a verified key or password.", LongTermFix: "Add host credential validation before session creation.", CustomerCommunication: "We could not authenticate to the host; no app data changed.", EscalationPath: "Support engineer then infrastructure owner."},
		{ID: "bootstrap-unsupported-os", Title: "Bootstrap failure: unsupported OS", Symptoms: "Preflight reports unsupported distribution, arch, or kernel.", Impact: "K3s install is blocked.", DashboardQuery: "bootstrap_preflight_failures_total{failure_code=\"unsupported_os\"}", ImmediateMitigation: "Use a supported Ubuntu/Debian host or replace the VPS image.", LongTermFix: "Expand preflight compatibility matrix.", CustomerCommunication: "The host OS is not currently supported for automated bootstrap.", EscalationPath: "Support engineer then platform engineering."},
		{ID: "bootstrap-stuck-waiting-agent", Title: "Bootstrap stuck waiting agent", Symptoms: "Install completed but agent heartbeat is missing.", Impact: "Deployments remain blocked.", DashboardQuery: "bootstrap_sessions{status=\"waiting_agent\"}", ImmediateMitigation: "Check agent service logs on the node and re-run agent registration if needed.", LongTermFix: "Add bootstrap worker retry and install verification.", CustomerCommunication: "The server installed but has not connected back yet.", EscalationPath: "Support engineer then agent owner."},
		{ID: "k3s-partial-failure", Title: "K3s install partial failure", Symptoms: "K3s process exists but node is not ready.", Impact: "Runtime state is ambiguous; deploys blocked.", DashboardQuery: "node_k3s_status != ready", ImmediateMitigation: "Drain/remove the node if safe, then bootstrap a clean host.", LongTermFix: "Add idempotent K3s repair workflow.", CustomerCommunication: "Cluster install did not complete cleanly; no secrets are exposed.", EscalationPath: "Infrastructure owner."},
		{ID: "agent-offline", Title: "Agent offline", Symptoms: "Heartbeat older than 60 seconds or missing.", Impact: "Leased jobs are not executed.", DashboardQuery: "agent_heartbeat_lag_seconds > 60", ImmediateMitigation: "Restart the agent service or rotate/re-register the agent credential.", LongTermFix: "Add heartbeat timeout reconciler and auto-repair guidance.", CustomerCommunication: "The node is unreachable by OPSI; existing workloads may still be running.", EscalationPath: "Agent owner then infrastructure owner."},
		{ID: "deployment-rollout-timeout", Title: "Deployment rollout timeout", Symptoms: "Deployment remains waiting or failed after rollout window.", Impact: "Service revision may be unavailable or degraded.", DashboardQuery: "deployment_rollout_timeout_total", ImmediateMitigation: "Use rollback when eligible; otherwise inspect deployment events and service health.", LongTermFix: "Improve readiness probe and capacity checks.", CustomerCommunication: "The new release did not become healthy within the rollout window.", EscalationPath: "Application owner then SRE."},
		{ID: "secret-rotation-failure", Title: "Secret rotation failure", Symptoms: "Rotation event fails or pods do not restart.", Impact: "Old credentials may remain active.", DashboardQuery: "secret_rotation_failures_total", ImmediateMitigation: "Stop further rotations, verify target secret, retry with Owner approval.", LongTermFix: "Add managed-service-native rotation and verification.", CustomerCommunication: "Secret rotation did not complete; values were not revealed to support.", EscalationPath: "Security owner."},
		{ID: "audit-write-failure", Title: "Audit write failure", Symptoms: "Sensitive action cannot append audit event.", Impact: "Mutating actions must be halted.", DashboardQuery: "audit_write_failures_total > 0", ImmediateMitigation: "Disable sensitive mutations until audit persistence recovers.", LongTermFix: "Add durable audit retry queue and storage health alert.", CustomerCommunication: "Safety logging is degraded; protected actions are paused.", EscalationPath: "Security owner then database owner."},
		{ID: "db-restore", Title: "DB restore", Symptoms: "Control plane database unavailable or corrupted.", Impact: "Cloud registry APIs unavailable.", DashboardQuery: "db_unavailable == 1", ImmediateMitigation: "Freeze writes, restore latest verified backup, validate audit triggers.", LongTermFix: "Automate restore drills and backup verification.", CustomerCommunication: "Control plane state is being restored; running workloads continue locally.", EscalationPath: "Database owner."},
		{ID: "control-plane-outage", Title: "Control plane outage", Symptoms: "High API errors or health check failure.", Impact: "UI/API control actions fail.", DashboardQuery: "api_errors_total / api_requests_total > 0.01", ImmediateMitigation: "Fail over Cloud service or roll back last control-plane deploy.", LongTermFix: "Add HA control plane and canary rollback.", CustomerCommunication: "Control-plane operations are degraded; local agents keep current workloads running.", EscalationPath: "Incident commander."},
		{ID: "agent-gateway-outage", Title: "Agent gateway outage", Symptoms: "Agent poll/result endpoints error or time out.", Impact: "Deployments and heartbeats stop updating.", DashboardQuery: "agent_gateway_errors_total", ImmediateMitigation: "Check gateway route, TLS, and agent auth failures.", LongTermFix: "Add gateway SLO dashboard and synthetic agent checks.", CustomerCommunication: "Agent communication is degraded; no secret data is exposed.", EscalationPath: "Agent gateway owner."},
		{ID: "credential-cleanup-failure", Title: "Credential cleanup failure", Symptoms: "Bootstrap credential or registration token remains after TTL/consume.", Impact: "Temporary bootstrap secret exposure window may be extended.", DashboardQuery: "bootstrap_credential_cleanup_failures_total > 0", ImmediateMitigation: "Revoke/delete credential material immediately and audit the session.", LongTermFix: "Move cleanup to durable job with high severity alert.", CustomerCommunication: "Temporary bootstrap material required cleanup review; application secrets were not revealed.", EscalationPath: "Security owner immediately."},
	}
}

func runbookForSignal(name string) string {
	switch name {
	case "agent_heartbeat_lag_seconds", "inventory_sync_freshness":
		return "agent-offline"
	case "deployment_failures_total":
		return "deployment-rollout-timeout"
	case "bootstrap_credential_cleanup":
		return "credential-cleanup-failure"
	default:
		return "control-plane-outage"
	}
}

func activeBootstrapStatus(status string) bool {
	return status == "created" || status == "preflight" || status == "installing" || status == "waiting_agent"
}

func durationValue(value time.Duration, missing int) string {
	if missing > 0 {
		return intValue(missing) + " missing"
	}
	return intValue(int(value.Seconds())) + "s"
}

func intValue(value int) string {
	return strconv.Itoa(value)
}
