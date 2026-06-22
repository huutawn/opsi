package secret

import (
	"context"
	"time"
)

type Role string

const (
	RoleOwner     Role = "Owner"
	RoleDeveloper Role = "Developer"
	RoleViewer    Role = "Viewer"
)

type AuthContext struct {
	ProjectID string
	UserID    string
	Role      Role
	PAT       string
	RemoteIP  string
}

type SecretRef struct {
	ProjectID string
	ServiceID string
	Name      string
	Namespace string
}

type SecretValue struct {
	Username string
	Password string
}

type AuditRecord struct {
	ID           string
	ProjectID    string
	Actor        string
	Action       string
	ResourceType string
	ResourceID   string
	Result       string
	MetadataJSON string
	CreatedAt    time.Time
}

type AuditSink interface {
	InsertAudit(ctx context.Context, record AuditRecord) error
}
