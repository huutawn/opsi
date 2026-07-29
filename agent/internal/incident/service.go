package incident

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/opsi-dev/opsi/agent/internal/secret"
	"github.com/opsi-dev/opsi/agent/internal/telemetry"
	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
)

const StatusResolved = "resolved"

var ErrApprovalRequired = errors.New("ACTION_APPROVAL_REQUIRED")

type Store interface {
	GetIncident(ctx context.Context, projectID, incidentID string) (*telemetry.IncidentRecord, error)
	ListIncidents(ctx context.Context, projectID, status string, limit int) ([]telemetry.IncidentRecord, error)
	ResolveIncident(ctx context.Context, projectID, incidentID string, resolved time.Time) (*telemetry.IncidentRecord, error)
}

type Service struct {
	Store           Store
	Audit           secret.AuditSink
	Auth            secret.AuthVerifier
	Now             func() time.Time
	EvidenceBuilder IncidentContextBuilder
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

func (s *Service) GetEvidence(ctx context.Context, req IncidentRequest) (*agentv1.IncidentEvidence, error) {
	auth, err := s.authorize(ctx, secret.AuthContext{ProjectID: req.ProjectID, UserID: req.UserID, Role: secret.Role(req.Role), PAT: req.PAT})
	if err != nil {
		return nil, err
	}
	if !canRead(auth.Role) {
		return nil, errors.New("permission denied")
	}
	record, err := s.Store.GetIncident(ctx, auth.ProjectID, req.IncidentID)
	if err != nil || record == nil {
		return nil, firstErr(err, errors.New("incident not found"))
	}
	if record.EvidenceJSON != "" || record.EvidenceSHA256 != "" || !record.EvidenceGeneratedAt.IsZero() {
		return verifyPersistedIncidentEvidence(record)
	}
	persistence, ok := s.Store.(incidentEvidencePersistence)
	if !ok {
		return nil, ErrEvidenceUnavailable
	}
	builder := s.EvidenceBuilder
	if builder.Store == nil {
		builder.Store = s.Store
	}
	if builder.Now == nil {
		builder.Now = s.Now
	}
	evidence, err := builder.Build(ctx, *record)
	if err != nil {
		return nil, err
	}
	body, err := EncodeIncidentEvidence(evidence)
	if err != nil {
		return nil, err
	}
	if err := persistence.PersistIncidentEvidence(ctx, auth.ProjectID, req.IncidentID, string(body), evidence.ContentSHA256, time.Unix(evidence.GeneratedAtUnix, 0).UTC()); err != nil {
		return nil, ErrEvidenceUnavailable
	}
	persisted, err := s.Store.GetIncident(ctx, auth.ProjectID, req.IncidentID)
	if err != nil || persisted == nil {
		return nil, ErrEvidenceUnavailable
	}
	return verifyPersistedIncidentEvidence(persisted)
}

func verifyPersistedIncidentEvidence(record *telemetry.IncidentRecord) (*agentv1.IncidentEvidence, error) {
	if record == nil || record.EvidenceJSON == "" || record.EvidenceSHA256 == "" || record.EvidenceGeneratedAt.IsZero() {
		return nil, ErrEvidenceCorrupt
	}
	evidence, err := VerifyIncidentEvidence([]byte(record.EvidenceJSON), record.EvidenceSHA256)
	if err != nil || evidence.GeneratedAtUnix != record.EvidenceGeneratedAt.Unix() {
		return nil, ErrEvidenceCorrupt
	}
	return evidence, nil
}

func (s *Service) Resolve(ctx context.Context, req ResolveRequest) (*telemetry.IncidentRecord, error) {
	auth, err := s.authorize(ctx, secret.AuthContext{ProjectID: req.ProjectID, UserID: req.UserID, Role: secret.Role(req.Role), PAT: req.PAT})
	if err != nil {
		return nil, err
	}
	if !canResolve(auth.Role) {
		return nil, errors.New("permission denied")
	}
	return nil, ErrApprovalRequired
}

func (s *Service) ResolveApproved(ctx context.Context, req ResolveRequest) (*telemetry.IncidentRecord, error) {
	if req.ProjectID == "" || req.IncidentID == "" || req.UserID == "" || !canResolve(secret.Role(req.Role)) {
		return nil, errors.New("approved incident identity is invalid")
	}
	auth := secret.AuthContext{ProjectID: req.ProjectID, UserID: req.UserID, Role: secret.Role(req.Role)}
	rec, err := s.Store.ResolveIncident(ctx, req.ProjectID, req.IncidentID, s.now())
	_ = s.audit(ctx, auth, "incident.resolve.approved", req.IncidentID, result(err), "")
	return rec, err
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
