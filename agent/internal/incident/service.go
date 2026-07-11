package incident

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/opsi-dev/opsi/agent/internal/secret"
	"github.com/opsi-dev/opsi/agent/internal/telemetry"
)

const StatusResolved = "resolved"

type Store interface {
	GetIncident(ctx context.Context, projectID, incidentID string) (*telemetry.IncidentRecord, error)
	ListIncidents(ctx context.Context, projectID, status string, limit int) ([]telemetry.IncidentRecord, error)
	ResolveIncident(ctx context.Context, projectID, incidentID string, resolved time.Time) (*telemetry.IncidentRecord, error)
}

type Service struct {
	Store Store
	Audit secret.AuditSink
	Auth  secret.AuthVerifier
	Now   func() time.Time
}

type IncidentRequest struct {
	ProjectID  string `json:"project_id"`
	IncidentID string `json:"incident_id"`
	UserID     string `json:"user_id"`
	Role       string `json:"role"`
	PAT        string `json:"pat,omitempty"`
}

type ListRequest struct {
	ProjectID string `json:"project_id"`
	Status    string `json:"status,omitempty"`
	Limit     int    `json:"limit,omitempty"`
	UserID    string `json:"user_id"`
	Role      string `json:"role"`
	PAT       string `json:"pat,omitempty"`
}

type ResolveRequest struct {
	ProjectID  string `json:"project_id"`
	IncidentID string `json:"incident_id"`
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

func (s *Service) List(ctx context.Context, req ListRequest) ([]telemetry.IncidentRecord, error) {
	auth, err := s.authorize(ctx, secret.AuthContext{ProjectID: req.ProjectID, UserID: req.UserID, Role: secret.Role(req.Role), PAT: req.PAT})
	if err != nil {
		return nil, err
	}
	if !canRead(auth.Role) {
		return nil, errors.New("permission denied")
	}
	if auth.ProjectID == "" || auth.UserID == "" {
		return nil, errors.New("project_id and user_id are required")
	}
	return s.Store.ListIncidents(ctx, auth.ProjectID, strings.TrimSpace(req.Status), req.Limit)
}

func (s *Service) Get(ctx context.Context, req IncidentRequest) (*telemetry.IncidentRecord, error) {
	auth, err := s.authorize(ctx, secret.AuthContext{ProjectID: req.ProjectID, UserID: req.UserID, Role: secret.Role(req.Role), PAT: req.PAT})
	if err != nil {
		return nil, err
	}
	if !canRead(auth.Role) {
		return nil, errors.New("permission denied")
	}
	rec, err := s.Store.GetIncident(ctx, auth.ProjectID, req.IncidentID)
	if err != nil || rec == nil {
		return nil, firstErr(err, errors.New("incident not found"))
	}
	return rec, nil
}

func (s *Service) Resolve(ctx context.Context, req ResolveRequest) (*telemetry.IncidentRecord, error) {
	auth, err := s.authorize(ctx, secret.AuthContext{ProjectID: req.ProjectID, UserID: req.UserID, Role: secret.Role(req.Role), PAT: req.PAT})
	if err != nil {
		return nil, err
	}
	if !canResolve(auth.Role) {
		return nil, errors.New("permission denied")
	}
	rec, err := s.Store.ResolveIncident(ctx, auth.ProjectID, req.IncidentID, s.now())
	_ = s.audit(ctx, auth, "incident.resolve", req.IncidentID, result(err), "")
	return rec, err
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

func canResolve(role secret.Role) bool {
	return role == secret.RoleOwner || role == secret.RoleDeveloper
}

func canRead(role secret.Role) bool {
	return role == secret.RoleOwner || role == secret.RoleDeveloper || role == secret.RoleViewer
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

func result(err error) string {
	if err != nil {
		return "failed"
	}
	return "success"
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
