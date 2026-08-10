package buildjob

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	ExecutorProviderGitHubActions = "github_actions"
	RunnerOIDCAudience            = "opsi-build"
	runnerLeaseTTL                = 10 * time.Minute

	DispatchStateDispatching = "dispatching"
	DispatchStateDispatched  = "dispatched"
	DispatchStateRejected    = "dispatch_rejected"
	DispatchStateClaimed     = "claimed"
)

type ExecutorConfig struct {
	Owner      string `json:"owner"`
	Repository string `json:"repository"`
	Workflow   string `json:"workflow"`
	Ref        string `json:"ref"`
}

func (c ExecutorConfig) Available() bool {
	return c.Owner != "" && c.Repository != "" && c.Workflow != "" && c.Ref != ""
}

func (c ExecutorConfig) Empty() bool {
	return c.Owner == "" && c.Repository == "" && c.Workflow == "" && c.Ref == ""
}

func (c ExecutorConfig) Validate() error {
	refName := strings.TrimPrefix(strings.TrimPrefix(c.Ref, "refs/heads/"), "refs/tags/")
	if !validGitHubName(c.Owner) || !validGitHubName(c.Repository) || !strings.HasPrefix(c.Workflow, ".github/workflows/") || !(strings.HasSuffix(c.Workflow, ".yml") || strings.HasSuffix(c.Workflow, ".yaml")) || !canonicalRepositoryPath(c.Workflow, false) || refName == c.Ref || refName == "" || len(c.Ref) > 512 || strings.TrimSpace(c.Ref) != c.Ref || strings.IndexFunc(c.Ref, unicode.IsControl) >= 0 {
		return invalid("EXECUTOR_CONFIG_INVALID", "Build executor configuration is invalid.", "executor_config")
	}
	return nil
}

func (c ExecutorConfig) RepositoryFullName() string { return c.Owner + "/" + c.Repository }
func (c ExecutorConfig) WorkflowRef() string {
	return c.RepositoryFullName() + "/" + c.Workflow + "@" + c.Ref
}
func (c ExecutorConfig) DispatchRef() string {
	if value := strings.TrimPrefix(c.Ref, "refs/heads/"); value != c.Ref {
		return value
	}
	return strings.TrimPrefix(c.Ref, "refs/tags/")
}

type DispatchAttempt struct {
	Provider       string     `json:"provider"`
	AttemptID      string     `json:"attempt_id"`
	BuildJobID     string     `json:"build_job_id"`
	Workflow       string     `json:"workflow"`
	WorkflowRef    string     `json:"workflow_ref"`
	ExecutorRef    string     `json:"executor_ref"`
	RunID          uint64     `json:"run_id,omitempty"`
	RunAttempt     uint32     `json:"run_attempt,omitempty"`
	RunURL         string     `json:"run_url,omitempty"`
	DispatchedAt   time.Time  `json:"dispatched_at"`
	ClaimedAt      *time.Time `json:"claimed_at,omitempty"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	LastState      string     `json:"last_factual_state"`
	FailureCode    string     `json:"failure_code,omitempty"`
	LeaseExpiresAt time.Time  `json:"-"`
	LeaseHash      []byte     `json:"-"`
}

type DispatchFacts struct {
	RunID      uint64
	RunAttempt uint32
	RunURL     string
}

type Dispatcher interface {
	DispatchWorkflow(context.Context, ExecutorConfig, string, string) (DispatchFacts, error)
}

type RunnerIdentity struct {
	Repository  string
	WorkflowRef string
	Ref         string
	EventName   string
	RunID       uint64
	RunAttempt  uint32
}

type RunnerLease struct {
	Token     string    `json:"lease_token"`
	ExpiresAt time.Time `json:"expires_at"`
	JobID     string    `json:"build_job_id"`
	AttemptID string    `json:"attempt_id"`
	RunID     uint64    `json:"run_id"`
}

type BuildSpec struct {
	BuildJobID            string `json:"build_job_id"`
	Repository            string `json:"repository"`
	RepositoryID          int64  `json:"repository_id"`
	RepositoryOwnerID     int64  `json:"repository_owner_id"`
	GitHubInstallationID  int64  `json:"github_installation_id"`
	ResolvedCommitSHA     string `json:"resolved_commit_sha"`
	ApplicationRoot       string `json:"application_root"`
	BuildContext          string `json:"build_context"`
	ResolvedBuildStrategy string `json:"resolved_build_strategy"`
	DockerfilePath        string `json:"dockerfile_path"`
}

func (s Service) Dispatch(ctx context.Context, projectID, applicationID, jobID string) (DispatchAttempt, error) {
	if s.Store == nil || s.Dispatcher == nil || !s.Executor.Available() {
		return DispatchAttempt{}, Error{Code: "EXECUTOR_CONFIG_UNAVAILABLE", Status: 503, Message: "Build executor configuration is unavailable.", Cause: "executor_config"}
	}
	if err := s.Executor.Validate(); err != nil {
		return DispatchAttempt{}, err
	}
	attemptID, err := s.newOpaqueID("bja-")
	if err != nil {
		return DispatchAttempt{}, unavailable()
	}
	now := s.clock()
	attempt := DispatchAttempt{Provider: ExecutorProviderGitHubActions, AttemptID: attemptID, BuildJobID: jobID, Workflow: s.Executor.Workflow, WorkflowRef: s.Executor.WorkflowRef(), ExecutorRef: s.Executor.Ref, DispatchedAt: now, LastState: DispatchStateDispatching}
	if err := s.Store.ReserveDispatch(ctx, projectID, applicationID, attempt); err != nil {
		return DispatchAttempt{}, err
	}
	facts, err := s.Dispatcher.DispatchWorkflow(ctx, s.Executor, jobID, attemptID)
	if err != nil {
		_ = s.Store.RejectDispatch(ctx, attemptID, "EXECUTOR_DISPATCH_REJECTED", s.clock())
		return DispatchAttempt{}, Error{Code: "EXECUTOR_DISPATCH_REJECTED", Status: 502, Message: "GitHub Actions rejected the executor dispatch.", Cause: "executor_dispatch"}
	}
	attempt, err = s.Store.CompleteDispatch(ctx, attemptID, facts, s.clock())
	if err != nil {
		return DispatchAttempt{}, err
	}
	return attempt, nil
}

func (s Service) Claim(ctx context.Context, jobID, attemptID string, identity RunnerIdentity) (RunnerLease, error) {
	if s.Store == nil || !s.Executor.Available() {
		return RunnerLease{}, Error{Code: "EXECUTOR_CONFIG_UNAVAILABLE", Status: 503, Message: "Build executor configuration is unavailable.", Cause: "executor_config"}
	}
	if !validOpaqueID(jobID) || !validOpaqueID(attemptID) || identity.RunID == 0 || identity.RunAttempt == 0 {
		return RunnerLease{}, invalid("RUNNER_CLAIM_INVALID", "Runner claim is invalid.", "runner_claim")
	}
	if err := s.Executor.Validate(); err != nil {
		return RunnerLease{}, err
	}
	if identity.Repository != s.Executor.RepositoryFullName() {
		return RunnerLease{}, Error{Code: "OIDC_WRONG_REPOSITORY", Status: 403, Message: "OIDC repository is not trusted for build execution.", Cause: "oidc_repository"}
	}
	if identity.WorkflowRef != s.Executor.WorkflowRef() || identity.Ref != s.Executor.Ref {
		return RunnerLease{}, Error{Code: "OIDC_WRONG_WORKFLOW_REF", Status: 403, Message: "OIDC workflow or ref is not trusted for build execution.", Cause: "oidc_workflow"}
	}
	if identity.EventName != "workflow_dispatch" {
		return RunnerLease{}, Error{Code: "OIDC_WRONG_EVENT", Status: 403, Message: "OIDC event is not a workflow dispatch.", Cause: "oidc_event"}
	}
	token, hash, err := s.newLeaseCredential()
	if err != nil {
		return RunnerLease{}, unavailable()
	}
	now := s.clock()
	expiresAt := now.Add(runnerLeaseTTL)
	if err := s.Store.ClaimDispatch(ctx, jobID, attemptID, identity, hash, expiresAt, now); err != nil {
		return RunnerLease{}, err
	}
	return RunnerLease{Token: token, ExpiresAt: expiresAt, JobID: jobID, AttemptID: attemptID, RunID: identity.RunID}, nil
}

func (s Service) BuildSpec(ctx context.Context, jobID, token string) (BuildSpec, error) {
	if s.Store == nil || !validOpaqueID(jobID) || token == "" || len(token) > 256 {
		return BuildSpec{}, Error{Code: "RUNNER_LEASE_INVALID", Status: 401, Message: "Runner lease is invalid.", Cause: "runner_lease"}
	}
	hash := sha256.Sum256([]byte(token))
	return s.Store.GetBuildSpec(ctx, jobID, hash[:], s.clock())
}

func validateDispatchableJob(job Job) error {
	if job.Status != StatusReady {
		return Error{Code: "BUILD_JOB_NOT_READY", Status: 409, Message: "BuildJob is not ready for dispatch.", Cause: "build_job_status"}
	}
	if job.ResolvedBuildStrategy != StrategyDockerfile || job.DockerfilePath == "" {
		return Error{Code: "BUILD_STRATEGY_NOT_DISPATCHABLE", Status: 409, Message: "Only resolved Dockerfile BuildJobs can be dispatched.", Cause: "build_strategy"}
	}
	if !validOpaqueID(job.ProjectID) || !validOpaqueID(job.ApplicationID) || !validOpaqueID(job.EnvironmentID) || !validOpaqueID(job.Source.BindingID) || job.Source.BindingUpdatedAt.IsZero() || job.Source.InstallationID <= 0 || job.Source.RepositoryID <= 0 || job.Source.RepositoryOwnerID <= 0 || !validRepositoryFullName(job.Source.RepositoryFullName) || !validSHA40(job.Source.ResolvedCommitSHA) || !canonicalRepositoryPath(job.Source.ApplicationRoot, true) || !canonicalRepositoryPath(job.Source.BuildContext, true) || !canonicalRepositoryPath(job.DockerfilePath, false) {
		return Error{Code: "BUILD_JOB_SNAPSHOT_INVALID", Status: 409, Message: "BuildJob immutable snapshot is invalid.", Cause: "build_job_snapshot"}
	}
	return nil
}

func buildSpec(job Job) BuildSpec {
	return BuildSpec{BuildJobID: job.ID, Repository: job.Source.RepositoryFullName, RepositoryID: job.Source.RepositoryID, RepositoryOwnerID: job.Source.RepositoryOwnerID, GitHubInstallationID: job.Source.InstallationID, ResolvedCommitSHA: job.Source.ResolvedCommitSHA, ApplicationRoot: job.Source.ApplicationRoot, BuildContext: job.Source.BuildContext, ResolvedBuildStrategy: job.ResolvedBuildStrategy, DockerfilePath: job.DockerfilePath}
}

func uintString(value uint64) string { return strconv.FormatUint(value, 10) }

func (s Service) newOpaqueID(prefix string) (string, error) {
	var raw [16]byte
	if _, err := io.ReadFull(s.random(), raw[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(raw[:]), nil
}

func (s Service) newLeaseCredential() (string, []byte, error) {
	var raw [32]byte
	if _, err := io.ReadFull(s.random(), raw[:]); err != nil {
		return "", nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	hash := sha256.Sum256([]byte(token))
	return token, hash[:], nil
}

func (s Service) random() io.Reader {
	if s.Random != nil {
		return s.Random
	}
	return rand.Reader
}
