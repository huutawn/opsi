package deploymentworkflow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/repositoryanalysis"
)

type Error struct {
	Code       string `json:"code"`
	Status     int    `json:"-"`
	Message    string `json:"message"`
	NextAction string `json:"next_action,omitempty"`
}

func (e Error) Error() string { return e.Code }

type Service struct {
	Store  Store
	Now    func() time.Time
	Random io.Reader
}

func (s Service) Create(ctx context.Context, projectID, actor, key string, source Source, target Target) (Run, bool, error) {
	if s.Store == nil || !validID(projectID) || !validID(actor) || !validKey(key) || source.RepositoryID <= 0 || source.InstallationID <= 0 || source.Repository == "" || source.SelectedRef == "" {
		return Run{}, false, invalid("DEPLOYMENT_RUN_REQUEST_INVALID", "Deployment run source or identity is invalid.")
	}
	id, err := s.newID("run-")
	if err != nil {
		return Run{}, false, unavailable()
	}
	now := s.clock()
	if target.Exposure == "public" && target.PublicRoutes == "" {
		target.PublicRoutes = PublicRoutesAutomatic
	}
	run := Run{SchemaVersion: RunSchemaVersion, ID: id, ProjectID: projectID, CreatedBy: actor, State: StateAnalyzing, Plan: Plan{SchemaVersion: PlanSchemaVersion, Source: source, Target: target, FailurePolicy: FailurePolicy{FailFast: true, RollbackKnownGood: true, RetainPersistentData: true, MaxAttempts: 3}}, Revision: 1, CreatedAt: now, UpdatedAt: now}
	event := s.event(run, "info", "Repository analysis queued.", nil)
	return s.Store.Create(ctx, run, event, key)
}

func (s Service) SetAnalysis(ctx context.Context, projectID, runID string, analysis repositoryanalysis.Result, authority AuthorityRevisions, target Target) (Run, error) {
	run, err := s.Get(ctx, projectID, runID)
	if err != nil {
		return Run{}, err
	}
	if run.State != StateAnalyzing && run.State != StateAwaitingInput && run.State != StateAwaitingApproval && run.State != StateStale {
		return Run{}, conflict("DEPLOYMENT_RUN_ANALYSIS_CONFLICT", "Repository can be analyzed only before approval.", "Create a new run for a changed source.")
	}
	if analysis.RepositoryID != run.Plan.Source.RepositoryID || analysis.Repository != run.Plan.Source.Repository || analysis.SelectedRef != run.Plan.Source.SelectedRef || len(analysis.CommitSHA) != 40 {
		return Run{}, invalid("DEPLOYMENT_ANALYSIS_SOURCE_MISMATCH", "Repository analysis does not match the selected source.")
	}
	run.Analysis = analysis
	run.Plan.Source.CommitSHA = analysis.CommitSHA
	run.Plan.Applications = analysis.Applications
	run.Plan.Resources = analysis.Resources
	run.Plan.Dependencies = analysis.Dependencies
	run.Plan.Bindings = analysis.Bindings
	run.Plan.Secrets = analysis.Secrets
	run.Plan.Issues = analysis.Issues
	run.Plan.AnalysisScope = analysis.Scope
	run.Plan.AnalysisScopeHash = analysis.ScopeHash
	run.Plan.EvidenceCoverage = analysis.EvidenceCoverage
	run.Plan.TruncationReason = analysis.TruncationReason
	run.Plan.Authority = authority
	run.Plan.Target = target
	run.State = StateAwaitingApproval
	if analysis.NeedsInput() {
		run.State = StateAwaitingInput
	}
	run.Approval = nil
	run.WarningAcknowledgement = nil
	run.Refs = AuthorityRefs{}
	run.PreflightHash = ""
	run.PreflightWarnings = nil
	run.Failure = nil
	run.PublicRouteFailures = nil
	run.Attempt = 0
	run.RetryAfterAt = nil
	run.FinishedAt = nil
	if err := refreshHash(&run.Plan); err != nil {
		return Run{}, unavailable()
	}
	message := "Repository analysis is ready for review."
	if run.State == StateAwaitingInput {
		message = "Repository analysis requires input before approval."
	}
	return s.save(ctx, run, message, nil)
}

func (s Service) UpdatePlan(ctx context.Context, projectID, runID, actor, expectedHash string, draft Plan) (Run, error) {
	run, err := s.Get(ctx, projectID, runID)
	if err != nil {
		return Run{}, err
	}
	if run.State != StateAwaitingInput && run.State != StateAwaitingApproval {
		return Run{}, conflict("DEPLOYMENT_PLAN_LOCKED", "Approved or running plans are immutable.", "Create a new deployment run.")
	}
	if run.Plan.Hash != expectedHash {
		return Run{}, conflict("DEPLOYMENT_PLAN_STALE", "The draft plan changed after it was loaded.", "Refresh and review the latest plan.")
	}
	if draft.Source != run.Plan.Source {
		return Run{}, invalid("DEPLOYMENT_PLAN_SOURCE_IMMUTABLE", "The exact source commit cannot be edited.")
	}
	draft.SchemaVersion = PlanSchemaVersion
	draft.Authority = run.Plan.Authority
	draft.AnalysisScope = run.Plan.AnalysisScope
	draft.AnalysisScopeHash = run.Plan.AnalysisScopeHash
	draft.EvidenceCoverage = run.Plan.EvidenceCoverage
	draft.TruncationReason = run.Plan.TruncationReason
	draft.Issues = reconcileDraftIssues(draft)
	if err := refreshHash(&draft); err != nil {
		return Run{}, invalid("DEPLOYMENT_PLAN_INVALID", err.Error())
	}
	if err := ValidatePlan(draft); err != nil {
		return Run{}, invalid("DEPLOYMENT_PLAN_INVALID", err.Error())
	}
	run.Plan = draft
	run.Approval = nil
	run.WarningAcknowledgement = nil
	run.PreflightHash = ""
	run.PreflightWarnings = nil
	run.PublicRouteFailures = nil
	run.State = StateAwaitingApproval
	for _, issue := range draft.Issues {
		if issue.Blocking {
			run.State = StateAwaitingInput
			break
		}
	}
	return s.save(ctx, run, "Draft deployment plan updated.", map[string]any{"actor": actor, "plan_hash": draft.Hash})
}

// reconcileDraftIssues removes only detector issues whose exact condition is
// now satisfied by the user-reviewed draft. Repository-integrity issues (for
// example truncation, invalid paths, or symlinks) deliberately survive edits
// and require a fresh safe analysis.
func reconcileDraftIssues(draft Plan) []repositoryanalysis.Issue {
	applications := map[string]bool{}
	validKeys, validPorts, validImages := true, true, true
	for _, application := range draft.Applications {
		if application.Key == "" || applications[application.Key] {
			validKeys = false
		}
		applications[application.Key] = true
		validPorts = validPorts && application.Port > 0 && application.Port <= 65535
		validImages = validImages && (application.Build.Strategy != "image" || application.Build.Image != "")
	}
	validDependencies := true
	for _, dependency := range draft.Dependencies {
		validDependencies = validDependencies && (!dependency.Required || dependency.Protocol == "postgres" || dependency.Protocol == "redis" || dependency.Protocol == "nats" || dependency.Verification != nil)
	}
	validSecrets := true
	for _, secret := range draft.Secrets {
		validSecrets = validSecrets && (secret.Generated || secret.SecretRef != "" && secret.Revision > 0)
	}
	resolved := map[string]bool{
		"APPLICATION_PORT_REQUIRED":             validPorts,
		"APPLICATION_IMAGE_REQUIRED":            validImages,
		"CANONICAL_KEY_COLLISION":               validKeys,
		"CANONICAL_KEY_INVALID":                 validKeys,
		"DEPENDENCY_VERIFICATION_REQUIRED":      validDependencies,
		"EXTERNAL_SECRET_REFERENCE_REQUIRED":    validSecrets,
		"COMPOSE_SECRET_VALUE_AMBIGUOUS":        validSecrets,
		"PUBLIC_HOSTNAME_REQUIRED":              draft.Target.Exposure != "public" || draft.Target.Hostname != "",
		"TARGET_SERVER_REQUIRED":                draft.Target.EnvironmentID != "" && draft.Target.RuntimeID != "",
		"EXPLICIT_COMPOSE_BUILD_CONFLICT":       true,
		"EXPLICIT_COMPOSE_ENVIRONMENT_CONFLICT": true,
		"EXPLICIT_COMPOSE_PORT_CONFLICT":        true,
	}
	var issues []repositoryanalysis.Issue
	if len(draft.Issues) > 0 {
		issues = make([]repositoryanalysis.Issue, 0, len(draft.Issues))
	}
	for _, issue := range draft.Issues {
		if !resolved[issue.Code] {
			issues = append(issues, issue)
		}
	}
	return issues
}

func (s Service) Approve(ctx context.Context, projectID, runID, actor, planHash string, current AuthorityRevisions) (Run, error) {
	run, err := s.Get(ctx, projectID, runID)
	if err != nil {
		return Run{}, err
	}
	if run.State != StateAwaitingApproval {
		return Run{}, conflict("DEPLOYMENT_RUN_NOT_APPROVABLE", "Deployment run is not awaiting approval.", "Resolve all blocking analysis items first.")
	}
	if planHash == "" || run.Plan.Hash != planHash {
		return Run{}, conflict("DEPLOYMENT_PLAN_STALE", "Approval does not match the exact deployment plan.", "Refresh and review the latest plan.")
	}
	if current != run.Plan.Authority {
		return s.markStale(ctx, run, "A plan authority revision changed before approval.")
	}
	if err := ValidatePlan(run.Plan); err != nil {
		return Run{}, invalid("DEPLOYMENT_PLAN_INVALID", err.Error())
	}
	for _, secret := range run.Plan.Secrets {
		if !secret.Generated && (secret.SecretRef == "" || secret.Revision == 0) {
			return Run{}, invalid("DEPLOYMENT_SECRET_REFERENCE_REQUIRED", "Every external secret must resolve to an opaque reference and exact revision before approval.")
		}
	}
	now := s.clock()
	run.Approval = &Approval{Actor: actor, PlanHash: planHash, AuthorityRevisions: current, ApprovedAt: now}
	run.State = StateProvisioning
	run.Attempt++
	run.Failure = nil
	return s.save(ctx, run, "Plan approved; provisioning started.", map[string]any{"actor": actor, "plan_hash": planHash})
}

func (s Service) Acknowledge(ctx context.Context, projectID, runID, actor, preflightHash string) (Run, error) {
	run, err := s.Get(ctx, projectID, runID)
	if err != nil {
		return Run{}, err
	}
	if run.State != StateAwaitingWarningAck || preflightHash == "" || run.PreflightHash != preflightHash {
		return Run{}, conflict("PREFLIGHT_ACKNOWLEDGEMENT_STALE", "Acknowledgement does not match the exact preflight result.", "Refresh and review the latest warnings.")
	}
	now := s.clock()
	run.WarningAcknowledgement = &WarningAcknowledgement{Actor: actor, PreflightHash: preflightHash, AcknowledgedAt: now}
	run.State = StateDeploying
	return s.save(ctx, run, "Preflight warnings acknowledged; deployment started.", map[string]any{"actor": actor, "preflight_hash": preflightHash})
}

func (s Service) Cancel(ctx context.Context, projectID, runID, actor string) (Run, error) {
	run, err := s.Get(ctx, projectID, runID)
	if err != nil {
		return Run{}, err
	}
	if Terminal(run.State) {
		return Run{}, conflict("DEPLOYMENT_RUN_TERMINAL", "Terminal deployment runs cannot be cancelled.", "")
	}
	now := s.clock()
	run.State = StateCancelled
	run.FinishedAt = &now
	return s.save(ctx, run, "Deployment run cancelled.", map[string]any{"actor": actor})
}
func (s Service) Retry(ctx context.Context, projectID, runID, actor string) (Run, error) {
	run, err := s.Get(ctx, projectID, runID)
	if err != nil {
		return Run{}, err
	}
	if run.State != StateFailed || run.Failure == nil || !run.Failure.Retryable {
		return Run{}, conflict("DEPLOYMENT_RUN_NOT_RETRYABLE", "The failed step cannot be retried safely.", run.Failure.NextAction)
	}
	if run.Attempt >= run.Plan.FailurePolicy.MaxAttempts {
		return Run{}, conflict("DEPLOYMENT_RUN_RETRY_LIMIT", "The bounded retry limit has been reached.", "Review the failure and create a new run.")
	}
	run.State = run.Failure.Step
	run.Attempt++
	run.Failure = nil
	run.FinishedAt = nil
	run.RetryAfterAt = nil
	return s.save(ctx, run, "Safe failed step queued for retry.", map[string]any{"actor": actor, "attempt": run.Attempt})
}
func (s Service) Get(ctx context.Context, projectID, runID string) (Run, error) {
	if s.Store == nil {
		return Run{}, unavailable()
	}
	run, err := s.Store.Get(ctx, projectID, runID)
	if errors.Is(err, ErrNotFound) {
		return Run{}, Error{Code: "DEPLOYMENT_RUN_NOT_FOUND", Status: 404, Message: "Deployment run was not found in this project."}
	}
	return run, err
}
func (s Service) List(ctx context.Context, projectID string, limit int) ([]Run, error) {
	if s.Store == nil {
		return nil, unavailable()
	}
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 {
		return nil, invalid("DEPLOYMENT_RUN_LIST_INVALID", "Deployment run list limit is invalid.")
	}
	return s.Store.List(ctx, projectID, limit)
}
func (s Service) Events(ctx context.Context, projectID, runID string) ([]Event, error) {
	if _, err := s.Get(ctx, projectID, runID); err != nil {
		return nil, err
	}
	return s.Store.Events(ctx, projectID, runID)
}
func (s Service) markStale(ctx context.Context, run Run, message string) (Run, error) {
	step := run.State
	run.State = StateStale
	run.Approval = nil
	run.Failure = &Failure{Step: step, Code: "DEPLOYMENT_PLAN_STALE", Message: message, NextAction: "Analyze and review the repository again.", Retryable: false}
	return s.save(ctx, run, message, nil)
}

func (s Service) Invalidate(ctx context.Context, projectID, runID, message string) (Run, error) {
	run, err := s.Get(ctx, projectID, runID)
	if err != nil {
		return Run{}, err
	}
	if Terminal(run.State) {
		return Run{}, conflict("DEPLOYMENT_RUN_TERMINAL", "Terminal deployment runs cannot be invalidated.", "Create a new run.")
	}
	return s.markStale(ctx, run, message)
}
func (s Service) save(ctx context.Context, run Run, message string, metadata map[string]any) (Run, error) {
	expected := run.Revision
	run.UpdatedAt = s.clock()
	event := s.event(run, "info", message, metadata)
	saved, err := s.Store.Save(ctx, run, expected, event)
	if errors.Is(err, ErrConflict) {
		return Run{}, conflict("DEPLOYMENT_RUN_CONCURRENT_UPDATE", "Deployment run changed concurrently.", "Refresh the run.")
	}
	return saved, err
}
func (s Service) event(run Run, level, message string, metadata map[string]any) Event {
	id, _ := s.newID("evt-")
	return Event{ID: id, ProjectID: run.ProjectID, RunID: run.ID, State: run.State, Level: level, Message: message, Metadata: metadata, CreatedAt: s.clock()}
}
func refreshHash(plan *Plan) error {
	hash, err := HashPlan(*plan)
	if err == nil {
		plan.Hash = hash
	}
	return err
}
func (s Service) newID(prefix string) (string, error) {
	r := s.Random
	if r == nil {
		r = rand.Reader
	}
	var raw [16]byte
	if _, err := io.ReadFull(r, raw[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(raw[:]), nil
}
func (s Service) clock() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
func validID(v string) bool {
	return v != "" && len(v) <= 128 && strings.TrimSpace(v) == v && !strings.ContainsAny(v, "/\\\r\n")
}
func validKey(v string) bool             { return validID(v) && len(v) <= 128 }
func invalid(code, message string) Error { return Error{Code: code, Status: 400, Message: message} }
func conflict(code, message, next string) Error {
	return Error{Code: code, Status: 409, Message: message, NextAction: next}
}
func unavailable() Error {
	return Error{Code: "DEPLOYMENT_WORKFLOW_UNAVAILABLE", Status: 503, Message: "Deployment workflow authority is unavailable.", NextAction: "Retry after Cloud connectivity is restored."}
}
