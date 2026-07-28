package actionplane

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/opsi-dev/opsi/agent/internal/secret"
	actionv1 "github.com/opsi-dev/opsi/contracts/go/actionv1"
	"google.golang.org/grpc/metadata"
)

var (
	ErrAuthentication = errors.New("ActionPlane authentication failed")
	ErrAuthorization  = errors.New("ActionPlane authorization failed")
	ErrWrongProject   = errors.New("ActionPlane project mismatch")
	ErrWrongUser      = errors.New("ActionPlane approval user mismatch")
)

type Principal struct {
	ProjectID string
	UserID    string
	Role      string
}

func AuthenticateFromContext(ctx context.Context, verifier secret.AuthVerifier, projectID string) (Principal, error) {
	if verifier == nil {
		return Principal{}, ErrAuthentication
	}
	metadataValue, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return Principal{}, ErrAuthentication
	}
	values := metadataValue.Get("authorization")
	if len(values) == 0 || !strings.HasPrefix(values[0], "Bearer ") {
		return Principal{}, ErrAuthentication
	}
	verified, err := verifier.VerifyAuth(ctx, secret.AuthContext{ProjectID: projectID, PAT: strings.TrimSpace(strings.TrimPrefix(values[0], "Bearer "))})
	if err != nil {
		return Principal{}, ErrAuthentication
	}
	return Principal{ProjectID: verified.ProjectID, UserID: verified.UserID, Role: string(verified.Role)}, nil
}

type Service struct {
	actionv1.UnimplementedActionServiceServer
	Store        ActionStore
	Runtime      Runtime
	Devices      DeviceResolver
	Authenticate func(context.Context, string) (Principal, error)
	Now          func() time.Time
}

func (s *Service) Catalog(context.Context, *actionv1.CatalogRequest) (*actionv1.CatalogResponse, error) {
	return &actionv1.CatalogResponse{SchemaVersion: actionv1.SchemaVersion, Actions: Catalog()}, nil
}

func (s *Service) Preflight(ctx context.Context, request *actionv1.PreflightRequest) (*actionv1.ActionPreflight, error) {
	if request == nil || request.Validate() != nil || ValidateKind(request.Kind) != nil {
		return nil, fmt.Errorf("%s", actionv1.FailureInvalid)
	}
	principal, err := s.principal(ctx, request.ProjectID)
	if err != nil {
		return nil, err
	}
	if s.Store == nil || s.Runtime == nil {
		return nil, errors.New("ActionPlane is unavailable")
	}
	state, err := s.Runtime.CurrentState(ctx, request.Target, request.Kind, request.Parameters)
	if err != nil {
		return nil, fmt.Errorf("read factual state: %w", err)
	}
	if state.ProjectID != request.ProjectID || state.Target.Key() != request.Target.Key() {
		return nil, ErrWrongProject
	}
	state.StateHash, err = actionv1.StateHash(state)
	if err != nil {
		return nil, err
	}
	risk := RiskFor(request.Kind, request.Parameters, state)
	if err := ValidateRisk(risk); err != nil {
		return nil, fmt.Errorf("%s", actionv1.FailureR4Prohibited)
	}
	now := s.clock()
	actionID, err := randomID()
	if err != nil {
		return nil, err
	}
	challengeID, err := randomID()
	if err != nil {
		return nil, err
	}
	nonce, err := randomID()
	if err != nil {
		return nil, err
	}
	plan := actionv1.ActionPlan{SchemaVersion: actionv1.SchemaVersion, ID: "action-" + actionID, ProjectID: request.ProjectID, NodeID: request.NodeID, ServiceID: request.ServiceID, Target: request.Target, Kind: request.Kind, Parameters: request.Parameters, Origin: actionv1.OriginManualCLI, RequestedBy: principal.UserID, Risk: risk, Preconditions: preconditions(request.Kind), Postconditions: postconditions(request.Kind), CurrentStateHash: state.StateHash, IssuedAt: now, ExpiresAt: now.Add(actionv1.MaxPlanTTL)}
	if err := plan.Validate(now); err != nil { return nil, err }
	plan.PlanHash, err = actionv1.PlanHash(plan)
	if err != nil {
		return nil, err
	}
	challenge := actionv1.ApprovalChallenge{SchemaVersion: actionv1.SchemaVersion, ID: "challenge-" + challengeID, ActionID: plan.ID, ProjectID: plan.ProjectID, PlanHash: plan.PlanHash, StateHash: state.StateHash, Nonce: nonce, Risk: risk, Target: plan.Target, Summary: actionSummary(plan), Preconditions: append([]actionv1.Condition(nil), plan.Preconditions...), ConfirmationPhrase: "APPROVE " + plan.ID, IssuedAt: now, ExpiresAt: now.Add(actionv1.MaxChallengeTTL)}
	if err := challenge.Validate(now); err != nil { return nil, err }
	if err := s.Store.Create(ctx, plan, state, challenge); err != nil {
		return nil, err
	}
	return &actionv1.ActionPreflight{SchemaVersion: actionv1.SchemaVersion, Plan: plan, CurrentState: state, Challenge: challenge, Summary: challenge.Summary}, nil
}

func (s *Service) GetChallenge(ctx context.Context, request *actionv1.ChallengeRequest) (*actionv1.ApprovalChallenge, error) {
	if request == nil {
		return nil, ErrWrongProject
	}
	principal, err := s.principal(ctx, request.ProjectID)
	if err != nil {
		return nil, err
	}
	record, err := s.Store.Load(ctx, request.ProjectID, request.ChallengeID)
	if err != nil {
		return nil, err
	}
	if record.Plan.RequestedBy != principal.UserID {
		return nil, ErrWrongUser
	}
	return &record.Challenge, nil
}

func (s *Service) Execute(ctx context.Context, request *actionv1.ExecuteRequest) (*actionv1.ActionResult, error) {
	if request == nil || strings.TrimSpace(request.ProjectID) == "" || strings.TrimSpace(request.ChallengeID) == "" {
		return nil, ErrWrongProject
	}
	principal, err := s.principal(ctx, request.ProjectID)
	if err != nil {
		return nil, err
	}
	record, err := s.Store.Load(ctx, request.ProjectID, request.ChallengeID)
	if err != nil {
		return nil, err
	}
	if record.Plan.ProjectID != principal.ProjectID {
		return nil, ErrWrongProject
	}
	if record.Plan.RequestedBy != principal.UserID {
		return nil, ErrWrongUser
	}
	grantHash, err := approvalHash(request.Grant)
	if err != nil {
		return nil, err
	}
	if record.Status.Terminal() {
		if record.GrantHash != grantHash {
			return nil, ErrReplayConflict
		}
		return &record.Result, nil
	}
	if failure := validateGrant(record, request.Grant, s.clock()); failure != "" {
		return s.rejectAndPersist(ctx, record, grantHash, failure, "approval grant rejected")
	}
	if s.Devices == nil {
		return s.rejectAndPersist(ctx, record, grantHash, actionv1.FailureDeviceUnavailable, "device resolver unavailable")
	}
	device, err := s.Devices.Resolve(ctx, record.Plan.ProjectID, request.Grant.DeviceID, principal.UserID)
	if err != nil {
		return s.rejectAndPersist(ctx, record, grantHash, actionv1.FailureDeviceUnavailable, "device resolver unavailable")
	}
	if device.ProjectID != record.Plan.ProjectID {
		return s.rejectAndPersist(ctx, record, grantHash, actionv1.FailureWrongProject, "device project mismatch")
	}
	if device.OwnerPrincipal != principal.UserID {
		return nil, ErrWrongUser
	}
	if device.Status != DeviceActive {
		return s.rejectAndPersist(ctx, record, grantHash, actionv1.FailureDeviceRevoked, "device is revoked")
	}
	signingBytes, err := actionv1.ApprovalSigningBytes(record.Challenge, device.ID)
	if err != nil {
		return nil, err
	}
	if len(device.PublicKey) != ed25519.PublicKeySize || !ed25519.Verify(ed25519.PublicKey(device.PublicKey), signingBytes, request.Grant.Signature) {
		return s.rejectAndPersist(ctx, record, grantHash, actionv1.FailureSignatureInvalid, "approval signature is invalid")
	}
	if err := s.Store.MarkApproved(ctx, record.Plan.ProjectID, record.Challenge.ID); err != nil {
		return nil, err
	}
	record.Status = actionv1.StatusApproved
	if err := s.Store.TryLock(ctx, record.Plan.Target.Key(), record.Plan.ID); err != nil {
		if errors.Is(err, ErrActionInProgress) {
			return &actionv1.ActionResult{SchemaVersion: actionv1.SchemaVersion, ActionID: record.Plan.ID, ChallengeID: record.Challenge.ID, ProjectID: record.Plan.ProjectID, Status: actionv1.StatusExecuting}, nil
		}
		record.GrantHash = grantHash
		result := rejection(record, actionv1.FailureTargetLocked, "action target is locked", s.clock())
		if completeErr := s.Store.Complete(ctx, record, *result); completeErr != nil {
			return nil, completeErr
		}
		return result, nil
	}
	current, err := s.Runtime.CurrentState(ctx, record.Plan.Target, record.Plan.Kind, record.Plan.Parameters)
	if err != nil {
		return s.finish(ctx, record, grantHash, actionv1.StatusFailed, actionv1.FailureExecution, "state recheck failed", principal.UserID, device.ID, actionv1.CurrentState{})
	}
	currentHash, err := actionv1.StateHash(current)
	if err != nil {
		return nil, err
	}
	if currentHash != record.Challenge.StateHash {
		return s.finish(ctx, record, grantHash, actionv1.StatusRejected, actionv1.FailureStateStale, "factual state changed after approval", principal.UserID, device.ID, current)
	}
	record, err = s.Store.BeginExecution(ctx, record.Plan.ProjectID, record.Challenge.ID, grantHash)
	if err != nil {
		if errors.Is(err, ErrNonceConsumed) {
			latest, loadErr := s.Store.Load(ctx, record.Plan.ProjectID, record.Challenge.ID)
			if loadErr == nil && latest.Result.ActionID != "" && latest.GrantHash == grantHash {
				return &latest.Result, nil
			}
		}
		return nil, err
	}
	if record.Status.Terminal() {
		return &record.Result, nil
	}
	started := s.clock()
	if err := executeTyped(withApprovedPrincipal(ctx, principal.UserID), s.Runtime, record.Plan); err != nil {
		return s.finishAt(ctx, record, actionv1.StatusFailed, actionv1.FailureExecution, "typed action execution failed", principal.UserID, device.ID, actionv1.CurrentState{}, started)
	}
	post, err := s.Runtime.PostCheck(ctx, record.Plan.Target, record.Plan.Kind, record.Plan.Parameters, current)
	if err != nil {
		return s.finishAt(ctx, record, actionv1.StatusFailed, actionv1.FailurePostCheck, "action post-check failed", principal.UserID, device.ID, post, started)
	}
	return s.finishAt(ctx, record, actionv1.StatusSucceeded, "", "action succeeded", principal.UserID, device.ID, post, started)
}

func (s *Service) Status(ctx context.Context, request *actionv1.StatusRequest) (*actionv1.ActionResult, error) {
	if request == nil {
		return nil, ErrWrongProject
	}
	principal, err := s.principal(ctx, request.ProjectID)
	if err != nil {
		return nil, err
	}
	record, err := s.Store.LoadAction(ctx, request.ProjectID, request.ActionID)
	if err != nil {
		return nil, err
	}
	if record.Plan.RequestedBy != principal.UserID {
		return nil, ErrWrongUser
	}
	if record.Result.ActionID != "" {
		return &record.Result, nil
	}
	return &actionv1.ActionResult{SchemaVersion: actionv1.SchemaVersion, ActionID: record.Plan.ID, ChallengeID: record.Challenge.ID, ProjectID: record.Plan.ProjectID, Status: record.Status}, nil
}

func (s *Service) finish(ctx context.Context, record Record, grantHash string, status actionv1.ActionStatus, failure actionv1.FailureCode, message, approvedBy, deviceID string, post actionv1.CurrentState) (*actionv1.ActionResult, error) {
	record.GrantHash = grantHash
	return s.finishAt(ctx, record, status, failure, message, approvedBy, deviceID, post, s.clock())
}

func (s *Service) finishAt(ctx context.Context, record Record, status actionv1.ActionStatus, failure actionv1.FailureCode, message, approvedBy, deviceID string, post actionv1.CurrentState, started time.Time) (*actionv1.ActionResult, error) {
	postHash := ""
	if post.SchemaVersion != "" {
		postHash, _ = actionv1.StateHash(post)
	}
	result := actionv1.ActionResult{SchemaVersion: actionv1.SchemaVersion, ActionID: record.Plan.ID, ChallengeID: record.Challenge.ID, ProjectID: record.Plan.ProjectID, Status: status, FailureCode: failure, Message: message, ApprovedBy: approvedBy, DeviceID: deviceID, StartedAt: started, FinishedAt: s.clock(), PostStateHash: postHash}
	if err := s.Store.Complete(ctx, record, result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (s *Service) principal(ctx context.Context, projectID string) (Principal, error) {
	if s.Authenticate == nil {
		return Principal{}, ErrAuthentication
	}
	principal, err := s.Authenticate(ctx, projectID)
	if err != nil {
		return Principal{}, ErrAuthentication
	}
	if principal.ProjectID == "" || principal.UserID == "" || principal.ProjectID != projectID {
		return Principal{}, ErrAuthentication
	}
	role := strings.ToLower(principal.Role)
	if role != "owner" && role != "developer" {
		return Principal{}, ErrAuthorization
	}
	return principal, nil
}

func validateGrant(record Record, grant actionv1.ApprovalGrant, now time.Time) actionv1.FailureCode {
	if grant.SchemaVersion != actionv1.SchemaVersion || grant.ChallengeID != record.Challenge.ID || grant.ActionID != record.Plan.ID || grant.DeviceID == "" {
		return actionv1.FailureWrongDevice
	}
	if grant.ProjectID != record.Plan.ProjectID {
		return actionv1.FailureWrongProject
	}
	if grant.PlanHash != record.Plan.PlanHash || grant.StateHash != record.Challenge.StateHash || grant.Nonce != record.Challenge.Nonce {
		return actionv1.FailureReplayConflict
	}
	if !grant.IssuedAt.Equal(record.Challenge.IssuedAt) || !grant.ExpiresAt.Equal(record.Challenge.ExpiresAt) || !now.Before(record.Challenge.ExpiresAt) {
		return actionv1.FailureChallengeExpired
	}
	return ""
}

func rejection(record Record, code actionv1.FailureCode, message string, now time.Time) *actionv1.ActionResult {
	return &actionv1.ActionResult{SchemaVersion: actionv1.SchemaVersion, ActionID: record.Plan.ID, ChallengeID: record.Challenge.ID, ProjectID: record.Plan.ProjectID, Status: actionv1.StatusRejected, FailureCode: code, Message: message, FinishedAt: now}
}

func (s *Service) rejectAndPersist(ctx context.Context, record Record, grantHash string, code actionv1.FailureCode, message string) (*actionv1.ActionResult, error) {
	record.GrantHash = grantHash
	result := rejection(record, code, message, s.clock())
	if err := s.Store.Complete(ctx, record, *result); err != nil {
		return nil, err
	}
	return result, nil
}
func approvalHash(grant actionv1.ApprovalGrant) (string, error) {
	body, err := actionv1.CanonicalJSON(grant)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}
func (s *Service) clock() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
func actionSummary(plan actionv1.ActionPlan) string {
	return fmt.Sprintf("%s %s on %s", plan.Risk, plan.Kind, plan.ServiceID)
}
func preconditions(actionv1.ActionKind) []actionv1.Condition {
	return []actionv1.Condition{{Code: "FACTUAL_STATE_MATCH", Summary: "state must still match preflight"}, {Code: "OPSI_OWNED_TARGET", Summary: "target must remain Opsi-owned"}}
}
func postconditions(kind actionv1.ActionKind) []actionv1.Condition {
	if kind == actionv1.ActionIncidentResolve {
		return []actionv1.Condition{{Code: "INCIDENT_RESOLVED", Summary: "incident is resolved"}}
	}
	return []actionv1.Condition{{Code: "TARGET_READY", Summary: "target passes the typed post-check"}}
}

var _ actionv1.ActionServiceServer = (*Service)(nil)
