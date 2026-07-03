package incident

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/opsi-dev/opsi/agent/internal/secret"
	"github.com/opsi-dev/opsi/agent/internal/telemetry"
)

const (
	StatusAnalyzed      = "analyzed"
	StatusActionPending = "action_pending"
	StatusResolving     = "resolving"
	StatusResolved      = "resolved"
)

type Store interface {
	GetIncident(ctx context.Context, projectID, incidentID string) (*telemetry.IncidentRecord, error)
	UpdateIncidentRCA(ctx context.Context, projectID, incidentID, status, rcaResult string, updated time.Time) (*telemetry.IncidentRecord, error)
	AppendIncidentAction(ctx context.Context, projectID, incidentID, status, mitigationActions string, updated time.Time) (*telemetry.IncidentRecord, error)
	ResolveIncident(ctx context.Context, projectID, incidentID string, resolved time.Time) (*telemetry.IncidentRecord, error)
}

type Service struct {
	Store       Store
	Audit       secret.AuditSink
	Auth        secret.AuthVerifier
	Cloud       AnalyzerClient
	KubectlPath string
	Namespace   string
	DryRun      bool
	Exec        func(context.Context, string, ...string) error
	Now         func() time.Time
}

type AnalyzeRequest struct {
	ProjectID  string `json:"project_id"`
	IncidentID string `json:"incident_id"`
	UserID     string `json:"user_id"`
	Role       string `json:"role"`
	PAT        string `json:"pat,omitempty"`
}

type ActionRequest struct {
	ProjectID  string `json:"project_id"`
	IncidentID string `json:"incident_id"`
	ActionID   string `json:"action_id"`
	ActionHash string `json:"action_hash,omitempty"`
	UserID     string `json:"user_id"`
	Role       string `json:"role"`
	PAT        string `json:"pat,omitempty"`
}

type IncidentContext struct {
	SchemaVersion  string                    `json:"schema_version"`
	IncidentID     string                    `json:"incident_id"`
	ProjectID      string                    `json:"project_id"`
	NodeID         string                    `json:"node_id,omitempty"`
	ServiceID      string                    `json:"service_id,omitempty"`
	PodID          string                    `json:"pod_id,omitempty"`
	AnomalyType    string                    `json:"anomaly_type"`
	Severity       string                    `json:"severity"`
	Status         string                    `json:"status"`
	Metric         map[string]any            `json:"metric,omitempty"`
	LogPattern     map[string]any            `json:"log_pattern,omitempty"`
	MetricSnapshot map[string]map[string]any `json:"metric_snapshot,omitempty"`
	LogPatterns    []map[string]any          `json:"log_patterns,omitempty"`
	Sanitization   map[string]any            `json:"sanitization,omitempty"`
	CreatedAtUnix  int64                     `json:"created_at_unix"`
}

type RCA struct {
	SchemaVersion       string   `json:"schema_version"`
	IncidentID          string   `json:"incident_id"`
	RootCause           string   `json:"root_cause"`
	Confidence          float64  `json:"confidence"`
	ContributingFactors []string `json:"contributing_factors"`
	RecommendedActions  []Action `json:"recommended_actions"`
}

type Action struct {
	ID           string            `json:"id"`
	Type         string            `json:"type"`
	Description  string            `json:"description"`
	RollbackSafe bool              `json:"rollback_safe,omitempty"`
	Params       map[string]string `json:"params,omitempty"`
	ActionHash   string            `json:"action_hash,omitempty"`
}

type ActionResult struct {
	ID         string    `json:"id"`
	Type       string    `json:"type"`
	ApprovedBy string    `json:"approved_by"`
	Status     string    `json:"status"`
	Error      string    `json:"error,omitempty"`
	ExecutedAt time.Time `json:"executed_at"`
}

type AnalyzerClient interface {
	Analyze(ctx context.Context, req IncidentContext) (RCA, error)
}

type HTTPAnalyzerClient struct {
	Endpoint string
	Client   *http.Client
}

func (c HTTPAnalyzerClient) Analyze(ctx context.Context, req IncidentContext) (RCA, error) {
	endpoint := strings.TrimRight(c.Endpoint, "/")
	if endpoint == "" {
		return fallbackRCA(req), nil
	}
	body, _ := json.Marshal(req)
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/v1/ai/incidents/analyze", bytes.NewReader(body))
	if err != nil {
		return fallbackRCA(req), nil
	}
	httpReq.Header.Set("content-type", "application/json")
	resp, err := client.Do(httpReq)
	if err != nil {
		return fallbackRCA(req), nil
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fallbackRCA(req), nil
	}
	var out RCA
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fallbackRCA(req), nil
	}
	if err := ValidateRCA(req, out); err != nil {
		return fallbackRCA(req), nil
	}
	return out, nil
}

func (s *Service) Analyze(ctx context.Context, req AnalyzeRequest) (*telemetry.IncidentRecord, RCA, error) {
	auth, err := s.authorize(ctx, secret.AuthContext{ProjectID: req.ProjectID, UserID: req.UserID, Role: secret.Role(req.Role), PAT: req.PAT})
	if err != nil {
		return nil, RCA{}, err
	}
	if !canApprove(auth.Role) {
		_ = s.audit(ctx, auth, "incident.analyze", req.IncidentID, "denied", "rbac")
		return nil, RCA{}, errors.New("permission denied")
	}
	rec, err := s.Store.GetIncident(ctx, req.ProjectID, req.IncidentID)
	if err != nil || rec == nil {
		return nil, RCA{}, firstErr(err, errors.New("incident not found"))
	}
	ictx, err := (IncidentContextBuilder{Store: s.Store, Window: 5 * time.Minute}).Build(ctx, *rec)
	if err != nil {
		return nil, RCA{}, err
	}
	client := s.Cloud
	if client == nil {
		client = HTTPAnalyzerClient{}
	}
	rca, err := client.Analyze(ctx, ictx)
	if err != nil {
		return nil, RCA{}, err
	}
	if err := ValidateRCA(ictx, rca); err != nil {
		return nil, RCA{}, err
	}
	rca = withActionHashes(rca)
	data, _ := json.Marshal(rca)
	updated, err := s.Store.UpdateIncidentRCA(ctx, req.ProjectID, req.IncidentID, StatusActionPending, string(data), s.now())
	_ = s.audit(ctx, auth, "incident.analyze", req.IncidentID, result(err), "")
	return updated, rca, err
}

func (s *Service) Approve(ctx context.Context, req ActionRequest) (*telemetry.IncidentRecord, RCA, error) {
	auth, err := s.authorize(ctx, secret.AuthContext{ProjectID: req.ProjectID, UserID: req.UserID, Role: secret.Role(req.Role), PAT: req.PAT})
	if err != nil {
		return nil, RCA{}, err
	}
	if !canApprove(auth.Role) {
		_ = s.audit(ctx, auth, "incident.action.approve", req.IncidentID, "denied", "rbac")
		return nil, RCA{}, errors.New("permission denied")
	}
	rec, err := s.Store.GetIncident(ctx, req.ProjectID, req.IncidentID)
	if err != nil || rec == nil {
		return nil, RCA{}, firstErr(err, errors.New("incident not found"))
	}
	var rca RCA
	if err := json.Unmarshal([]byte(rec.RCAResult), &rca); err != nil {
		return nil, RCA{}, errors.New("incident has no valid rca")
	}
	ictx, err := SanitizeIncidentContext(*rec)
	if err != nil {
		return nil, RCA{}, err
	}
	if err := ValidateRCA(ictx, rca); err != nil {
		return nil, RCA{}, err
	}
	action, ok := findAction(rca, req.ActionID)
	if !ok {
		return nil, RCA{}, errors.New("action not found")
	}
	if action.Type == "rollback" && !action.RollbackSafe {
		return nil, RCA{}, errors.New("rollback is not safe")
	}
	if req.ActionHash != "" && req.ActionHash != hashAction(action) {
		return nil, RCA{}, errors.New("stale action: reload RCA before approving")
	}
	_ = s.audit(ctx, auth, "incident.action.approve", req.IncidentID, "success", action.Type)
	execErr := s.execute(ctx, *rec, action)
	if execErr == nil {
		execErr = s.verify(ctx, *rec, action)
	}
	out := appendResult(rec.MitigationActions, ActionResult{ID: action.ID, Type: action.Type, ApprovedBy: auth.UserID, Status: result(execErr), Error: errString(execErr), ExecutedAt: s.now()})
	updated, err := s.Store.AppendIncidentAction(ctx, req.ProjectID, req.IncidentID, StatusResolving, out, s.now())
	_ = s.audit(ctx, auth, "incident.action.execute", req.IncidentID, result(execErr), action.Type)
	if execErr != nil {
		return updated, rca, execErr
	}
	return updated, rca, err
}

func (s *Service) Resolve(ctx context.Context, req ActionRequest) (*telemetry.IncidentRecord, RCA, error) {
	auth, err := s.authorize(ctx, secret.AuthContext{ProjectID: req.ProjectID, UserID: req.UserID, Role: secret.Role(req.Role), PAT: req.PAT})
	if err != nil {
		return nil, RCA{}, err
	}
	if !canApprove(auth.Role) {
		return nil, RCA{}, errors.New("permission denied")
	}
	rec, err := s.Store.ResolveIncident(ctx, req.ProjectID, req.IncidentID, s.now())
	_ = s.audit(ctx, auth, "incident.resolve", req.IncidentID, result(err), "")
	return rec, rcaFromIncident(rec), err
}

func SanitizeIncidentContext(rec telemetry.IncidentRecord) (IncidentContext, error) {
	out := IncidentContext{SchemaVersion: "opsi.incident_context.v1", IncidentID: rec.ID, ProjectID: rec.ProjectID, NodeID: rec.NodeID, ServiceID: rec.ServiceID, PodID: rec.PodID, AnomalyType: rec.AnomalyType, Severity: rec.Severity, Status: rec.Status, CreatedAtUnix: rec.CreatedAt.Unix()}
	var raw map[string]any
	if rec.ContextJSON != "" && rec.ContextJSON != "{}" {
		if err := json.Unmarshal([]byte(rec.ContextJSON), &raw); err != nil {
			return out, err
		}
	}
	out.Metric = pickMap(raw, "metric", "metric_snapshot", "resource_trend")
	out.LogPattern = pickMap(raw, "log_fingerprint", "fingerprint", "pattern")
	data, _ := json.Marshal(out)
	if secretLike(string(data)) {
		return out, errors.New("sanitized incident context still contains sensitive data")
	}
	return out, nil
}

func ValidateRCA(ctx IncidentContext, r RCA) error {
	if r.SchemaVersion != "opsi.rca.v1" || r.IncidentID != ctx.IncidentID || r.RootCause == "" || r.Confidence < 0 || r.Confidence > 1 {
		return errors.New("invalid rca")
	}
	if len(r.RecommendedActions) == 0 || len(r.RecommendedActions) > 5 {
		return errors.New("invalid recommended_actions")
	}
	seen := map[string]bool{}
	for _, a := range r.RecommendedActions {
		if a.ID == "" || seen[a.ID] || !allowedAction(a.Type) {
			return errors.New("invalid action")
		}
		seen[a.ID] = true
		if a.Params["service_id"] != "" && a.Params["service_id"] != ctx.ServiceID {
			return errors.New("action target mismatch")
		}
	}
	return nil
}

func (s *Service) execute(ctx context.Context, rec telemetry.IncidentRecord, action Action) error {
	if s.DryRun {
		return nil
	}
	kubectl := s.KubectlPath
	if kubectl == "" {
		kubectl = "kubectl"
	}
	namespace := first(action.Params["namespace"], s.Namespace, "default")
	serviceID := first(action.Params["service_id"], rec.ServiceID)
	switch action.Type {
	case "restart_pod":
		return s.run(ctx, kubectl, "-n", namespace, "rollout", "restart", "deployment/"+serviceID)
	case "scale_replicas":
		replicas := first(action.Params["replicas"], "2")
		return s.run(ctx, kubectl, "-n", namespace, "scale", "deployment/"+serviceID, "--replicas="+replicas)
	case "rate_limit_ingress":
		return s.run(ctx, kubectl, "-n", namespace, "annotate", "ingress/"+serviceID, "nginx.ingress.kubernetes.io/limit-rps="+first(action.Params["rps"], "10"), "--overwrite")
	case "increase_resource_limits":
		return s.run(ctx, kubectl, "-n", namespace, "set", "resources", "deployment/"+serviceID, "--limits=cpu="+first(action.Params["cpu"], "1000m")+",memory="+first(action.Params["memory"], "1Gi"))
	case "rollback":
		return s.run(ctx, kubectl, "-n", namespace, "rollout", "undo", "deployment/"+serviceID)
	default:
		return errors.New("action not allowed")
	}
}

func (s *Service) run(ctx context.Context, name string, args ...string) error {
	if s.Exec != nil {
		return s.Exec(ctx, name, args...)
	}
	return exec.CommandContext(ctx, name, args...).Run()
}

func fallbackRCA(ctx IncidentContext) RCA {
	rca := RCA{SchemaVersion: "opsi.rca.v1", IncidentID: ctx.IncidentID, RootCause: "Local anomaly detected: " + first(ctx.AnomalyType, "unknown"), Confidence: 0.62, ContributingFactors: []string{"metric anomaly", "recent service health degradation"}, RecommendedActions: []Action{{ID: "scale-replicas", Type: "scale_replicas", Description: "Scale service replicas to reduce pressure", RollbackSafe: true, Params: map[string]string{"service_id": ctx.ServiceID, "replicas": "2"}}, {ID: "restart-pod", Type: "restart_pod", Description: "Restart deployment pods", RollbackSafe: true, Params: map[string]string{"service_id": ctx.ServiceID}}}}
	return withActionHashes(rca)
}

func (s *Service) verify(ctx context.Context, rec telemetry.IncidentRecord, action Action) error {
	if s.DryRun {
		return nil
	}
	kubectl := first(s.KubectlPath, "kubectl")
	namespace := first(action.Params["namespace"], s.Namespace, "default")
	serviceID := first(action.Params["service_id"], rec.ServiceID)
	switch action.Type {
	case "restart_pod", "scale_replicas", "rollback", "increase_resource_limits":
		return s.run(ctx, kubectl, "-n", namespace, "rollout", "status", "deployment/"+serviceID, "--timeout=60s")
	case "rate_limit_ingress":
		return s.run(ctx, kubectl, "-n", namespace, "get", "ingress/"+serviceID, "-o", "jsonpath={.metadata.annotations.nginx\\.ingress\\.kubernetes\\.io/limit-rps}")
	default:
		return nil
	}
}

func withActionHashes(r RCA) RCA {
	for i := range r.RecommendedActions {
		r.RecommendedActions[i].ActionHash = hashAction(r.RecommendedActions[i])
	}
	return r
}

func hashAction(action Action) string {
	action.ActionHash = ""
	data, _ := json.Marshal(action)
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func canApprove(role secret.Role) bool {
	return role == secret.RoleOwner || role == secret.RoleDeveloper
}
func allowedAction(t string) bool {
	return t == "restart_pod" || t == "scale_replicas" || t == "rate_limit_ingress" || t == "rollback" || t == "increase_resource_limits"
}
func (s *Service) authorize(ctx context.Context, auth secret.AuthContext) (secret.AuthContext, error) {
	if s.Auth == nil {
		return auth, nil
	}
	return s.Auth.VerifyAuth(ctx, auth)
}
func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
func (s *Service) audit(ctx context.Context, auth secret.AuthContext, action, resourceID, res, reason string) error {
	if s.Audit == nil {
		return nil
	}
	meta := "{}"
	if reason != "" {
		meta = fmt.Sprintf(`{"reason":%q}`, reason)
	}
	return s.Audit.InsertAudit(ctx, secret.AuditRecord{ID: newID(), ProjectID: auth.ProjectID, Actor: auth.UserID, Action: action, ResourceType: "incident", ResourceID: resourceID, Result: res, MetadataJSON: meta, CreatedAt: s.now()})
}
func rcaFromIncident(rec *telemetry.IncidentRecord) RCA {
	var r RCA
	if rec != nil {
		_ = json.Unmarshal([]byte(rec.RCAResult), &r)
	}
	return r
}
func findAction(r RCA, id string) (Action, bool) {
	for _, a := range r.RecommendedActions {
		if a.ID == id {
			return a, true
		}
	}
	return Action{}, false
}
func appendResult(raw string, item ActionResult) string {
	var items []ActionResult
	_ = json.Unmarshal([]byte(first(raw, "[]")), &items)
	items = append(items, item)
	data, _ := json.Marshal(items)
	return string(data)
}
func pickMap(raw map[string]any, keys ...string) map[string]any {
	out := map[string]any{}
	for _, key := range keys {
		if v, ok := raw[key]; ok {
			out[key] = v
		}
	}
	return out
}
func secretLike(s string) bool {
	return regexp.MustCompile(`(?i)(password|secret|token|authorization|kubeconfig|private_key|otp|pat)`).MatchString(s)
}
func first(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
func result(err error) string {
	if err != nil {
		return "failed"
	}
	return "success"
}
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
func firstErr(err, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}
func newID() string {
	var data [16]byte
	if _, err := rand.Read(data[:]); err == nil {
		return hex.EncodeToString(data[:])
	}
	return fmt.Sprintf("audit-%d", time.Now().UnixNano())
}
