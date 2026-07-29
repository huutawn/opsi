package actionv1

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	SchemaVersion   = "action.v1"
	MaxChallengeTTL = 5 * time.Minute
	MaxPlanTTL      = 10 * time.Minute
	MaxJSONBytes    = 1 << 20
	MaxReplicas     = 100
)

type ActionKind string

const (
	ActionRestartWorkload  ActionKind = "restart_workload"
	ActionScaleWorkload    ActionKind = "scale_workload"
	ActionGatewayReconcile ActionKind = "gateway_reconcile"
	ActionIncidentResolve  ActionKind = "incident_resolve"
)

func (k ActionKind) Valid() bool {
	switch k {
	case ActionRestartWorkload, ActionScaleWorkload, ActionGatewayReconcile, ActionIncidentResolve:
		return true
	default:
		return false
	}
}

type RiskClass string

const (
	RiskR1 RiskClass = "R1"
	RiskR2 RiskClass = "R2"
	RiskR3 RiskClass = "R3"
	RiskR4 RiskClass = "R4"
)

type ActionStatus string

const (
	StatusPlanned     ActionStatus = "planned"
	StatusPreflighted ActionStatus = "preflighted"
	StatusApproved    ActionStatus = "approved"
	StatusExecuting   ActionStatus = "executing"
	StatusSucceeded   ActionStatus = "succeeded"
	StatusFailed      ActionStatus = "failed"
	StatusRejected    ActionStatus = "rejected"
)

func (s ActionStatus) Terminal() bool {
	return s == StatusSucceeded || s == StatusFailed || s == StatusRejected
}

type FailureCode string

const (
	FailureInvalid                FailureCode = "ACTION_INVALID"
	FailureApprovalRequired       FailureCode = "ACTION_APPROVAL_REQUIRED"
	FailureR4Prohibited           FailureCode = "ACTION_R4_PROHIBITED"
	FailureChallengeExpired       FailureCode = "ACTION_CHALLENGE_EXPIRED"
	FailureStateStale             FailureCode = "ACTION_STATE_STALE"
	FailureWrongProject           FailureCode = "ACTION_WRONG_PROJECT"
	FailureWrongUser              FailureCode = "ACTION_WRONG_USER"
	FailureWrongDevice            FailureCode = "ACTION_WRONG_DEVICE"
	FailureDeviceRevoked          FailureCode = "ACTION_DEVICE_REVOKED"
	FailureSignatureInvalid       FailureCode = "ACTION_SIGNATURE_INVALID"
	FailureNonceReplay            FailureCode = "ACTION_NONCE_REPLAY"
	FailureReplayConflict         FailureCode = "ACTION_REPLAY_CONFLICT"
	FailureTargetLocked           FailureCode = "ACTION_TARGET_LOCKED"
	FailureDeviceUnavailable      FailureCode = "ACTION_DEVICE_RESOLVER_UNAVAILABLE"
	FailureExecution              FailureCode = "ACTION_EXECUTION_FAILED"
	FailurePostCheck              FailureCode = "ACTION_POST_CHECK_FAILED"
	FailureInterrupted            FailureCode = "ACTION_EXECUTION_INTERRUPTED"
	FailureInterruptedPreMutation FailureCode = "ACTION_EXECUTION_INTERRUPTED_PRE_MUTATION"
)

type TrustedOrigin string

const OriginManualCLI TrustedOrigin = "manual_cli"

type TargetIdentity struct {
	ProjectID     string `json:"project_id"`
	NodeID        string `json:"node_id"`
	ServiceID     string `json:"service_id"`
	EnvironmentID string `json:"environment_id,omitempty"`
	RuntimeID     string `json:"runtime_id,omitempty"`
}

func (t TargetIdentity) Key() string {
	return strings.Join([]string{t.ProjectID, t.NodeID, t.ServiceID, t.EnvironmentID, t.RuntimeID}, "\x00")
}

func (t TargetIdentity) Validate() error {
	if strings.TrimSpace(t.ProjectID) == "" || strings.TrimSpace(t.NodeID) == "" || strings.TrimSpace(t.ServiceID) == "" {
		return errors.New("project_id, node_id, and service_id are required")
	}
	return nil
}

func (t TargetIdentity) validateRuntime() error {
	if strings.TrimSpace(t.EnvironmentID) == "" || strings.TrimSpace(t.RuntimeID) == "" {
		return errors.New("environment_id and runtime_id are required for Kubernetes actions")
	}
	return nil
}

type RestartWorkloadParameters struct{}
type ScaleWorkloadParameters struct {
	Replicas int32 `json:"replicas"`
}
type GatewayReconcileParameters struct{}
type IncidentResolveParameters struct {
	IncidentID string `json:"incident_id"`
}

type ActionParameters struct {
	RestartWorkload  *RestartWorkloadParameters  `json:"restart_workload,omitempty"`
	ScaleWorkload    *ScaleWorkloadParameters    `json:"scale_workload,omitempty"`
	GatewayReconcile *GatewayReconcileParameters `json:"gateway_reconcile,omitempty"`
	IncidentResolve  *IncidentResolveParameters  `json:"incident_resolve,omitempty"`
}

func (p ActionParameters) Kind() ActionKind {
	switch {
	case p.RestartWorkload != nil:
		return ActionRestartWorkload
	case p.ScaleWorkload != nil:
		return ActionScaleWorkload
	case p.GatewayReconcile != nil:
		return ActionGatewayReconcile
	case p.IncidentResolve != nil:
		return ActionIncidentResolve
	default:
		return ""
	}
}

func (p ActionParameters) Validate() error {
	count := 0
	if p.RestartWorkload != nil {
		count++
	}
	if p.ScaleWorkload != nil {
		count++
		if p.ScaleWorkload.Replicas < 0 || p.ScaleWorkload.Replicas > MaxReplicas {
			return fmt.Errorf("replicas must be between 0 and %d", MaxReplicas)
		}
	}
	if p.GatewayReconcile != nil {
		count++
	}
	if p.IncidentResolve != nil {
		count++
		if strings.TrimSpace(p.IncidentResolve.IncidentID) == "" {
			return errors.New("incident_id is required")
		}
	}
	if count != 1 {
		return errors.New("exactly one typed action parameter is required")
	}
	return nil
}

type Condition struct {
	Code    string `json:"code"`
	Summary string `json:"summary"`
}

type PreflightRequest struct {
	SchemaVersion string           `json:"schema_version"`
	ProjectID     string           `json:"project_id"`
	NodeID        string           `json:"node_id"`
	ServiceID     string           `json:"service_id"`
	Target        TargetIdentity   `json:"target"`
	Kind          ActionKind       `json:"kind"`
	Parameters    ActionParameters `json:"parameters"`
}

func (r PreflightRequest) Validate() error {
	if r.SchemaVersion != SchemaVersion {
		return errors.New("unsupported schema_version")
	}
	if err := r.Target.Validate(); err != nil {
		return err
	}
	if r.ProjectID != r.Target.ProjectID || r.NodeID != r.Target.NodeID || r.ServiceID != r.Target.ServiceID {
		return errors.New("request and target identity mismatch")
	}
	if !r.Kind.Valid() || r.Parameters.Kind() != r.Kind {
		return errors.New("action kind and typed parameters mismatch")
	}
	if r.Kind != ActionIncidentResolve {
		if err := r.Target.validateRuntime(); err != nil {
			return err
		}
	}
	return r.Parameters.Validate()
}

type ActionPlan struct {
	SchemaVersion    string           `json:"schema_version"`
	ID               string           `json:"id"`
	ProjectID        string           `json:"project_id"`
	NodeID           string           `json:"node_id"`
	ServiceID        string           `json:"service_id"`
	Target           TargetIdentity   `json:"target"`
	Kind             ActionKind       `json:"kind"`
	Parameters       ActionParameters `json:"parameters"`
	Origin           TrustedOrigin    `json:"origin"`
	RequestedBy      string           `json:"requested_by"`
	Risk             RiskClass        `json:"risk"`
	Preconditions    []Condition      `json:"preconditions,omitempty"`
	Postconditions   []Condition      `json:"postconditions,omitempty"`
	CurrentStateHash string           `json:"current_state_hash"`
	PlanHash         string           `json:"plan_hash"`
	IssuedAt         time.Time        `json:"issued_at"`
	ExpiresAt        time.Time        `json:"expires_at"`
}

func (p ActionPlan) Validate(now time.Time) error {
	if p.SchemaVersion != SchemaVersion || p.ID == "" || p.RequestedBy == "" || p.Origin != OriginManualCLI {
		return errors.New("invalid action plan authority")
	}
	if err := p.Target.Validate(); err != nil {
		return err
	}
	if p.ProjectID != p.Target.ProjectID || p.NodeID != p.Target.NodeID || p.ServiceID != p.Target.ServiceID {
		return errors.New("plan target identity mismatch")
	}
	if !p.Kind.Valid() || p.Parameters.Kind() != p.Kind {
		return errors.New("plan kind and parameters mismatch")
	}
	if p.Kind != ActionIncidentResolve {
		if err := p.Target.validateRuntime(); err != nil {
			return err
		}
	}
	if err := p.Parameters.Validate(); err != nil {
		return err
	}
	if p.Risk == RiskR4 || (p.Risk != RiskR1 && p.Risk != RiskR2 && p.Risk != RiskR3) {
		return errors.New("invalid or prohibited risk")
	}
	if p.CurrentStateHash == "" || p.ExpiresAt.After(p.IssuedAt.Add(MaxPlanTTL)) || !p.ExpiresAt.After(p.IssuedAt) || !now.Before(p.ExpiresAt) {
		return errors.New("invalid or expired action plan")
	}
	return nil
}

type ApprovalChallenge struct {
	SchemaVersion      string         `json:"schema_version"`
	ID                 string         `json:"id"`
	ActionID           string         `json:"action_id"`
	ProjectID          string         `json:"project_id"`
	PlanHash           string         `json:"plan_hash"`
	StateHash          string         `json:"state_hash"`
	Nonce              string         `json:"nonce"`
	Risk               RiskClass      `json:"risk,omitempty"`
	Target             TargetIdentity `json:"target,omitempty"`
	Summary            string         `json:"summary,omitempty"`
	Preconditions      []Condition    `json:"preconditions,omitempty"`
	ConfirmationPhrase string         `json:"confirmation_phrase,omitempty"`
	IssuedAt           time.Time      `json:"issued_at"`
	ExpiresAt          time.Time      `json:"expires_at"`
}

func (c ApprovalChallenge) Validate(now time.Time) error {
	if c.SchemaVersion != SchemaVersion || c.ID == "" || c.ActionID == "" || c.ProjectID == "" || c.PlanHash == "" || c.StateHash == "" || c.Nonce == "" {
		return errors.New("incomplete approval challenge")
	}
	if c.ExpiresAt.After(c.IssuedAt.Add(MaxChallengeTTL)) || !c.ExpiresAt.After(c.IssuedAt) || !now.Before(c.ExpiresAt) {
		return errors.New("approval challenge expired or TTL is invalid")
	}
	return nil
}

type ApprovalGrant struct {
	SchemaVersion string    `json:"schema_version"`
	ChallengeID   string    `json:"challenge_id"`
	ActionID      string    `json:"action_id"`
	ProjectID     string    `json:"project_id"`
	DeviceID      string    `json:"device_id"`
	PlanHash      string    `json:"plan_hash"`
	StateHash     string    `json:"state_hash"`
	Nonce         string    `json:"nonce"`
	IssuedAt      time.Time `json:"issued_at"`
	ExpiresAt     time.Time `json:"expires_at"`
	Signature     []byte    `json:"signature"`
}

type ActionPreflight struct {
	SchemaVersion string            `json:"schema_version"`
	Plan          ActionPlan        `json:"plan"`
	CurrentState  CurrentState      `json:"current_state"`
	Challenge     ApprovalChallenge `json:"challenge"`
	Summary       string            `json:"summary"`
}

type WorkloadState struct {
	UID                string `json:"uid"`
	ResourceVersion    string `json:"resource_version"`
	Generation         int64  `json:"generation"`
	ObservedGeneration int64  `json:"observed_generation"`
	DesiredReplicas    int32  `json:"desired_replicas"`
	ObservedReplicas   int32  `json:"observed_replicas"`
	ReadyReplicas      int32  `json:"ready_replicas"`
	RestartToken       string `json:"restart_token,omitempty"`
}

type GatewayState struct {
	UID              string `json:"uid,omitempty"`
	ResourceVersion  string `json:"resource_version,omitempty"`
	Generation       int64  `json:"generation"`
	SpecHash         string `json:"spec_hash,omitempty"`
	BackendServiceID string `json:"backend_service_id,omitempty"`
	Owned            bool   `json:"owned"`
}

type IncidentState struct {
	IncidentID string `json:"incident_id"`
	Status     string `json:"status"`
}

type CurrentState struct {
	SchemaVersion string         `json:"schema_version"`
	ProjectID     string         `json:"project_id"`
	Target        TargetIdentity `json:"target"`
	Workload      *WorkloadState `json:"workload,omitempty"`
	Gateway       *GatewayState  `json:"gateway,omitempty"`
	Incident      *IncidentState `json:"incident,omitempty"`
	StateHash     string         `json:"state_hash,omitempty"`
}

type ActionResult struct {
	SchemaVersion string       `json:"schema_version"`
	ActionID      string       `json:"action_id"`
	ChallengeID   string       `json:"challenge_id,omitempty"`
	ProjectID     string       `json:"project_id,omitempty"`
	Status        ActionStatus `json:"status"`
	FailureCode   FailureCode  `json:"failure_code,omitempty"`
	Message       string       `json:"message,omitempty"`
	ApprovedBy    string       `json:"approved_by,omitempty"`
	DeviceID      string       `json:"device_id,omitempty"`
	StartedAt     time.Time    `json:"started_at,omitempty"`
	FinishedAt    time.Time    `json:"finished_at,omitempty"`
	PostStateHash string       `json:"post_state_hash,omitempty"`
}

func (r ActionResult) Validate() error {
	if r.SchemaVersion != SchemaVersion || r.ActionID == "" {
		return errors.New("invalid action result")
	}
	if !r.Status.Terminal() {
		return errors.New("action result is not terminal")
	}
	if (r.Status == StatusFailed || r.Status == StatusRejected) && r.FailureCode == "" {
		return errors.New("terminal failure requires failure_code")
	}
	return nil
}

type ActionEvent struct {
	SchemaVersion string       `json:"schema_version"`
	ID            string       `json:"id"`
	ActionID      string       `json:"action_id"`
	ProjectID     string       `json:"project_id"`
	Status        ActionStatus `json:"status"`
	FailureCode   FailureCode  `json:"failure_code,omitempty"`
	Actor         string       `json:"actor,omitempty"`
	CreatedAt     time.Time    `json:"created_at"`
}

type DeviceStatus string

const (
	DeviceActive  DeviceStatus = "active"
	DeviceRevoked DeviceStatus = "revoked"
)

type ActionDevice struct {
	SchemaVersion     string       `json:"schema_version"`
	ID                string       `json:"id"`
	ProjectID         string       `json:"project_id"`
	OwnerPrincipal    string       `json:"owner_principal"`
	DisplayName       string       `json:"display_name"`
	PublicKey         []byte       `json:"public_key,omitempty"`
	FingerprintSHA256 string       `json:"fingerprint_sha256"`
	Status            DeviceStatus `json:"status"`
	TrustedActor      string       `json:"trusted_actor"`
	IdempotencyKey    string       `json:"idempotency_key,omitempty"`
	CreatedAt         time.Time    `json:"created_at"`
	RevokedAt         *time.Time   `json:"revoked_at,omitempty"`
}

type CatalogRequest struct{}
type CatalogAction struct {
	Kind    ActionKind `json:"kind"`
	Risk    RiskClass  `json:"risk"`
	Summary string     `json:"summary,omitempty"`
}
type CatalogResponse struct {
	SchemaVersion string          `json:"schema_version"`
	Actions       []CatalogAction `json:"actions"`
}
type ChallengeRequest struct {
	ProjectID   string `json:"project_id"`
	ChallengeID string `json:"challenge_id"`
}
type ExecuteRequest struct {
	ProjectID   string        `json:"project_id"`
	ChallengeID string        `json:"challenge_id"`
	Grant       ApprovalGrant `json:"grant"`
}
type StatusRequest struct {
	ProjectID string `json:"project_id"`
	ActionID  string `json:"action_id"`
}
