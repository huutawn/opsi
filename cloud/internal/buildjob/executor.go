package buildjob

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/opsi-dev/opsi/cloud/internal/sourcescanner"
	buildrecordv1 "github.com/opsi-dev/opsi/contracts/go/buildrecordv1"
)

const (
	ExecutorProviderGitHubActions = "github_actions"
	RunnerOIDCAudience            = "opsi-build"
	runnerLeaseTTL                = 10 * time.Minute

	DispatchStateDispatching = "dispatching"
	DispatchStateDispatched  = "dispatched"
	DispatchStateRejected    = "dispatch_rejected"
	DispatchStateClaimed     = "claimed"
	DispatchStateSucceeded   = "succeeded"
	DispatchStateFailed      = "failed"
	DispatchStateCancelled   = "cancelled"
)

var ociDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type ExecutorConfig struct {
	Owner      string `json:"owner"`
	Repository string `json:"repository"`
	Workflow   string `json:"workflow"`
	Ref        string `json:"ref"`
}

type RegistryConfig struct {
	Host             string `json:"host"`
	Namespace        string `json:"namespace"`
	RepositoryPrefix string `json:"repository_prefix"`
	Visibility       string `json:"visibility"`
}

type PublicationTarget struct {
	Host       string `json:"host"`
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
}

func (c RegistryConfig) Empty() bool {
	return c.Host == "" && c.Namespace == "" && c.RepositoryPrefix == "" && c.Visibility == ""
}

func (c RegistryConfig) Validate() error {
	if !validOCIHost(c.Host) || !validOCIPath(c.Namespace) || !validOCIPath(c.RepositoryPrefix) || c.Visibility != "private" {
		return invalid("REGISTRY_CONFIG_INVALID", "Build registry configuration is invalid.", "registry_config")
	}
	return nil
}

func (c RegistryConfig) Target(applicationID, jobID string) PublicationTarget {
	applicationHash := sha256.Sum256([]byte(applicationID))
	jobHash := sha256.Sum256([]byte(jobID))
	repository := c.Host + "/" + c.Namespace + "/" + c.RepositoryPrefix + "/app-" + hex.EncodeToString(applicationHash[:12])
	return PublicationTarget{Host: c.Host, Repository: repository, Tag: "job-" + hex.EncodeToString(jobHash[:12])}
}

func (t PublicationTarget) TagReference() string                 { return t.Repository + ":" + t.Tag }
func (t PublicationTarget) DigestReference(digest string) string { return t.Repository + "@" + digest }
func (t PublicationTarget) Empty() bool                          { return t.Host == "" && t.Repository == "" && t.Tag == "" }
func (t PublicationTarget) Validate() error {
	if !validOCIHost(t.Host) || !strings.HasPrefix(t.Repository, t.Host+"/") || !validOCIPath(strings.TrimPrefix(t.Repository, t.Host+"/")) || !validOCITag(t.Tag) {
		return invalid("REGISTRY_TARGET_INVALID", "Build registry target is invalid.", "registry_target")
	}
	return nil
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

type WorkflowCanceller interface {
	CancelWorkflow(context.Context, ExecutorConfig, uint64) error
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
	Token      string    `json:"lease_token"`
	ExpiresAt  time.Time `json:"expires_at"`
	JobID      string    `json:"build_job_id"`
	AttemptID  string    `json:"attempt_id"`
	RunID      uint64    `json:"run_id"`
	RunAttempt uint32    `json:"run_attempt"`
}

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// SourceScanContext carries advisory dependency data for the source risk scanner.
// It is NOT part of the immutable build authority and does NOT affect Validate().
// Scanner uses this to perform dependency-aware analysis only.
type SourceScanContext struct {
	ProjectID     string `json:"project_id"`
	ApplicationID string `json:"application_id"`
	// ScanDependenciesJSON is the JSON-encoded []serviceconfigurationv1.ApplicationDependency
	// for env key correlation. Optional — if empty, env correlation is skipped.
	ScanDependenciesJSON []byte `json:"scan_dependencies_json,omitempty"`
}

type BuildSpec struct {
	BuildJobID            string            `json:"build_job_id"`
	Repository            string            `json:"repository"`
	RepositoryID          int64             `json:"repository_id"`
	RepositoryOwnerID     int64             `json:"repository_owner_id"`
	GitHubInstallationID  int64             `json:"github_installation_id"`
	ResolvedCommitSHA     string            `json:"resolved_commit_sha"`
	ApplicationRoot       string            `json:"application_root"`
	BuildContext          string            `json:"build_context"`
	ResolvedBuildStrategy string            `json:"resolved_build_strategy"`
	DockerfilePath        string            `json:"dockerfile_path"`
	Publication           PublicationTarget `json:"publication"`
	BuildEnvironment      map[string]string `json:"build_environment,omitempty"`
	// ScanContext carries advisory scanner data. Not part of immutable build authority.
	ScanContext *SourceScanContext `json:"scan_context,omitempty"`
}

func (s BuildSpec) Validate() error {
	strategyValid := s.ResolvedBuildStrategy == StrategyDockerfile && canonicalRepositoryPath(s.DockerfilePath, false) || s.ResolvedBuildStrategy == StrategyBuildpack && s.DockerfilePath == ""
	if !validOpaqueID(s.BuildJobID) || !validRepositoryFullName(s.Repository) || s.RepositoryID <= 0 || s.RepositoryOwnerID <= 0 || s.GitHubInstallationID <= 0 || !validSHA40(s.ResolvedCommitSHA) || !canonicalRepositoryPath(s.ApplicationRoot, true) || !canonicalRepositoryPath(s.BuildContext, true) || !strategyValid || !s.Publication.Empty() && s.Publication.Validate() != nil {
		return Error{Code: "BUILD_SPEC_INVALID", Status: 409, Message: "Build Spec is invalid.", Cause: "build_spec"}
	}
	for k, v := range s.BuildEnvironment {
		if !envNamePattern.MatchString(k) || len(v) > 4096 || strings.IndexFunc(v, unicode.IsControl) >= 0 {
			return Error{Code: "BUILD_SPEC_INVALID", Status: 409, Message: "Build Spec environment is invalid.", Cause: "build_environment"}
		}
	}
	return nil
}

type ImageDescriptor struct {
	Digest    string `json:"digest"`
	MediaType string `json:"media_type"`
	Size      int64  `json:"size,omitempty"`
}

type RemoteRegistryEvidence struct {
	Descriptor ImageDescriptor `json:"descriptor"`
	Platform   string          `json:"platform"`
	Manifest   []byte          `json:"manifest"`
	Private    bool            `json:"private"`
}

type ExecutorResult struct {
	Strategy        string                        `json:"strategy"`
	Platform        string                        `json:"platform"`
	BuildKitVersion string                        `json:"buildkit_version"`
	BuildxVersion   string                        `json:"buildx_version"`
	BuilderIdentity string                        `json:"builder_identity"`
	Builder         buildrecordv1.BuilderMetadata `json:"builder,omitempty"`
	StartedAt       time.Time                     `json:"started_at"`
	CompletedAt     time.Time                     `json:"completed_at"`
	BuildDescriptor ImageDescriptor               `json:"build_descriptor"`
	Remote          RemoteRegistryEvidence        `json:"remote"`
}

type RunnerResult struct {
	BuildJobID        string                `json:"build_job_id"`
	AttemptID         string                `json:"attempt_id"`
	RegistryReference string                `json:"registry_reference"`
	Digest            string                `json:"digest"`
	Executor          ExecutorResult        `json:"executor"`
	SourceRiskReport  *sourcescanner.Report `json:"source_risk_report,omitempty"`
}

type RunnerFailure struct {
	BuildJobID string `json:"build_job_id"`
	AttemptID  string `json:"attempt_id"`
	Code       string `json:"failure_code"`
}

type Completion struct {
	Result    RunnerResult
	LeaseHash []byte
	Now       time.Time
}

type CompletionResult struct {
	BuildRecordID string `json:"build_record_id"`
	Digest        string `json:"digest"`
	BuildJobState string `json:"build_job_state"`
	Reused        bool   `json:"reused"`
}

type RunnerAccess struct {
	JobID      string
	AttemptID  string
	RunID      uint64
	RunAttempt uint32
	LeaseHash  []byte
}

type SourceGrant struct {
	BuildJobID           string `json:"build_job_id"`
	Repository           string `json:"repository"`
	RepositoryID         int64  `json:"repository_id"`
	GitHubInstallationID int64  `json:"github_installation_id"`
	ResolvedCommitSHA    string `json:"resolved_commit_sha"`
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
	return RunnerLease{Token: token, ExpiresAt: expiresAt, JobID: jobID, AttemptID: attemptID, RunID: identity.RunID, RunAttempt: identity.RunAttempt}, nil
}

func (s Service) BuildSpec(ctx context.Context, jobID, token string) (BuildSpec, error) {
	if s.Store == nil || !validOpaqueID(jobID) || token == "" || len(token) > 256 || s.Registry.Validate() != nil {
		return BuildSpec{}, Error{Code: "RUNNER_LEASE_INVALID", Status: 401, Message: "Runner lease is invalid.", Cause: "runner_lease"}
	}
	hash := sha256.Sum256([]byte(token))
	job, err := s.Store.GetRunnerJob(ctx, RunnerAccess{JobID: jobID, LeaseHash: hash[:]}, s.clock())
	if err != nil {
		return BuildSpec{}, err
	}
	return buildSpec(job, s.Registry), nil
}

func (s Service) SourceGrant(ctx context.Context, jobID, attemptID string, runID uint64, runAttempt uint32, token string) (SourceGrant, error) {
	if s.Store == nil || s.Sources == nil || !validOpaqueID(jobID) || !validOpaqueID(attemptID) || runID == 0 || runAttempt == 0 || token == "" || len(token) > 256 {
		return SourceGrant{}, Error{Code: "RUNNER_LEASE_INVALID", Status: 401, Message: "Runner lease is invalid.", Cause: "runner_lease"}
	}
	hash := sha256.Sum256([]byte(token))
	job, err := s.Store.GetRunnerJob(ctx, RunnerAccess{JobID: jobID, AttemptID: attemptID, RunID: runID, RunAttempt: runAttempt, LeaseHash: hash[:]}, s.clock())
	if err != nil {
		return SourceGrant{}, err
	}
	current, err := s.Sources.ResolveBuildJobSource(ctx, job.ProjectID, job.ApplicationID)
	if err != nil {
		return SourceGrant{}, Error{Code: "SOURCE_ACCESS_DENIED", Status: 403, Message: "Source access is unavailable for this BuildJob.", Cause: "source_authority"}
	}
	if !sourceMatchesSnapshot(current, job) {
		return SourceGrant{}, Error{Code: "SOURCE_BINDING_MISMATCH", Status: 409, Message: "The current source binding does not match the immutable BuildJob snapshot.", Cause: "source_binding"}
	}
	return SourceGrant{BuildJobID: job.ID, Repository: job.Source.RepositoryFullName, RepositoryID: job.Source.RepositoryID, GitHubInstallationID: job.Source.InstallationID, ResolvedCommitSHA: job.Source.ResolvedCommitSHA}, nil
}

func sourceMatchesSnapshot(source ApplicationSource, job Job) bool {
	return source.ProjectID == job.ProjectID && source.ApplicationID == job.ApplicationID && source.EnvironmentID == job.EnvironmentID &&
		source.BindingID == job.Source.BindingID && source.BindingUpdatedAt.Equal(job.Source.BindingUpdatedAt) &&
		source.InstallationID == job.Source.InstallationID && source.RepositoryID == job.Source.RepositoryID && source.RepositoryOwnerID == job.Source.RepositoryOwnerID &&
		source.RepositoryFullName == job.Source.RepositoryFullName && source.SelectedRef == job.Source.SelectedRef &&
		source.ApplicationRoot == job.Source.ApplicationRoot && source.BuildContext == job.Source.BuildContext &&
		source.BuildStrategy == job.RequestedBuildStrategy && (source.DockerfilePath == job.DockerfilePath || source.DockerfilePath == "" && job.RequestedBuildStrategy == StrategyAuto)
}

func validateDispatchableJob(job Job) error {
	if job.Status != StatusReady {
		return Error{Code: "BUILD_JOB_NOT_READY", Status: 409, Message: "BuildJob is not ready for dispatch.", Cause: "build_job_status"}
	}
	strategyValid := job.ResolvedBuildStrategy == StrategyDockerfile && canonicalRepositoryPath(job.DockerfilePath, false) || job.ResolvedBuildStrategy == StrategyBuildpack && job.DockerfilePath == ""
	if !strategyValid {
		return Error{Code: "BUILD_STRATEGY_NOT_DISPATCHABLE", Status: 409, Message: "BuildJob strategy is not dispatchable.", Cause: "build_strategy"}
	}
	if !validOpaqueID(job.ProjectID) || !validOpaqueID(job.ApplicationID) || !validOpaqueID(job.EnvironmentID) || !validOpaqueID(job.Source.BindingID) || job.Source.BindingUpdatedAt.IsZero() || job.Source.InstallationID <= 0 || job.Source.RepositoryID <= 0 || job.Source.RepositoryOwnerID <= 0 || !validRepositoryFullName(job.Source.RepositoryFullName) || !validSHA40(job.Source.ResolvedCommitSHA) || !canonicalRepositoryPath(job.Source.ApplicationRoot, true) || !canonicalRepositoryPath(job.Source.BuildContext, true) {
		return Error{Code: "BUILD_JOB_SNAPSHOT_INVALID", Status: 409, Message: "BuildJob immutable snapshot is invalid.", Cause: "build_job_snapshot"}
	}
	return nil
}

func buildSpec(job Job, registry RegistryConfig) BuildSpec {
	return BuildSpec{
		BuildJobID:            job.ID,
		Repository:            job.Source.RepositoryFullName,
		RepositoryID:          job.Source.RepositoryID,
		RepositoryOwnerID:     job.Source.RepositoryOwnerID,
		GitHubInstallationID:  job.Source.InstallationID,
		ResolvedCommitSHA:     job.Source.ResolvedCommitSHA,
		ApplicationRoot:       job.Source.ApplicationRoot,
		BuildContext:          job.Source.BuildContext,
		ResolvedBuildStrategy: job.ResolvedBuildStrategy,
		DockerfilePath:        job.DockerfilePath,
		Publication:           registry.Target(job.ApplicationID, job.ID),
		BuildEnvironment:      job.Source.BuildEnvironment,
	}
}

func (s Service) Complete(ctx context.Context, result RunnerResult, token string) (CompletionResult, error) {
	if s.Store == nil || token == "" || len(token) > 256 || s.Registry.Validate() != nil || s.Executor.Validate() != nil {
		return CompletionResult{}, Error{Code: "RUNNER_LEASE_INVALID", Status: 401, Message: "Runner lease is invalid.", Cause: "runner_lease"}
	}
	if err := validateRunnerResult(result); err != nil {
		return CompletionResult{}, err
	}
	hash := sha256.Sum256([]byte(token))
	return s.Store.CompleteRunner(ctx, Completion{Result: result, LeaseHash: hash[:], Now: s.clock()}, s.Registry, s.Executor)
}

func (s Service) Fail(ctx context.Context, failure RunnerFailure, token string) error {
	if s.Store == nil || token == "" || len(token) > 256 || !validOpaqueID(failure.BuildJobID) || !validOpaqueID(failure.AttemptID) || !validRunnerFailure(failure.Code) {
		return Error{Code: "RUNNER_FAILURE_INVALID", Status: 400, Message: "Runner failure is invalid.", Cause: "runner_failure"}
	}
	hash := sha256.Sum256([]byte(token))
	return s.Store.FailRunner(ctx, failure, hash[:], s.clock())
}

func validateRunnerResult(result RunnerResult) error {
	if !validOpaqueID(result.BuildJobID) || !validOpaqueID(result.AttemptID) || !ociDigestPattern.MatchString(result.Digest) || result.RegistryReference == "" || result.Executor.Platform != "linux/amd64" || result.Executor.Strategy != StrategyDockerfile && result.Executor.Strategy != StrategyBuildpack || result.Executor.BuilderIdentity == "" || result.Executor.StartedAt.IsZero() || result.Executor.CompletedAt.Before(result.Executor.StartedAt) || !result.Executor.Remote.Private {
		return invalid("RUNNER_RESULT_INVALID", "Runner result is invalid.", "runner_result")
	}
	if result.Executor.Strategy == StrategyDockerfile && (result.Executor.BuildKitVersion == "" || result.Executor.BuildxVersion == "") || result.Executor.Strategy == StrategyBuildpack && !validBuildpackMetadata(result.Executor.Builder) {
		return invalid("RUNNER_RESULT_INVALID", "Runner builder metadata is invalid.", "runner_result")
	}
	local := result.Executor.BuildDescriptor
	remote := result.Executor.Remote.Descriptor
	if local.Digest != result.Digest || remote.Digest != result.Digest || !ociDigestPattern.MatchString(local.Digest) || local.MediaType == "" || remote.MediaType == "" || local.MediaType != remote.MediaType || local.Size > 0 && remote.Size != local.Size || result.Executor.Remote.Platform != result.Executor.Platform || len(result.Executor.Remote.Manifest) == 0 || len(result.Executor.Remote.Manifest) > 4<<20 {
		return Error{Code: "REGISTRY_DIGEST_MISMATCH", Status: 409, Message: "Registry result does not match build output.", Cause: "registry_digest"}
	}
	sum := sha256.Sum256(result.Executor.Remote.Manifest)
	if "sha256:"+hex.EncodeToString(sum[:]) != remote.Digest {
		return Error{Code: "REGISTRY_DIGEST_MISMATCH", Status: 409, Message: "Remote registry manifest digest does not match the result.", Cause: "registry_digest"}
	}
	var manifest struct {
		MediaType string `json:"mediaType"`
	}
	if json.Unmarshal(result.Executor.Remote.Manifest, &manifest) != nil || manifest.MediaType != remote.MediaType {
		return invalid("RUNNER_RESULT_INVALID", "Remote registry descriptor is invalid.", "registry_descriptor")
	}
	return nil
}

func validBuildpackMetadata(metadata buildrecordv1.BuilderMetadata) bool {
	return metadata.PackVersion != "" && metadata.BuilderImage != "" && ociDigestPattern.MatchString(metadata.BuilderImageDigest) && metadata.RunImage != "" && ociDigestPattern.MatchString(metadata.RunImageDigest) && metadata.LifecycleVersion != "" && len(metadata.Buildpacks) > 0
}

func validRunnerFailure(code string) bool {
	switch code {
	case "USER_BUILD_FAILED", "REGISTRY_AUTH_FAILED", "REGISTRY_PUSH_FAILED", "REGISTRY_DIGEST_MISMATCH", "REGISTRY_ARTIFACT_NOT_FOUND", "EXECUTOR_INFRASTRUCTURE_FAILED",
		"SOURCE_ACCESS_DENIED", "SOURCE_TOKEN_UNAVAILABLE", "BUILDPACK_DETECTION_FAILED", "BUILDPACK_BUILD_FAILED", "BUILDPACK_RUN_IMAGE_UNAVAILABLE", "BUILDPACK_BUILDER_UNAVAILABLE", "BUILDPACK_MONOREPO_UNSUPPORTED", "BUILDPACK_RESULT_INVALID":
		return true
	default:
		return false
	}
}

func validOCIHost(value string) bool {
	if value == "" || value != strings.ToLower(value) || strings.ContainsAny(value, "/@ ") {
		return false
	}
	parts := strings.Split(value, ":")
	if len(parts) > 2 || len(parts) == 2 {
		if _, err := strconv.ParseUint(parts[1], 10, 16); err != nil {
			return false
		}
	}
	for _, part := range strings.Split(parts[0], ".") {
		if part == "" || strings.Trim(part, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" || strings.HasPrefix(part, "-") || strings.HasSuffix(part, "-") {
			return false
		}
	}
	return true
}

func validOCIPath(value string) bool {
	if value == "" || value != strings.ToLower(value) || len(value) > 200 {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || strings.Trim(segment, "abcdefghijklmnopqrstuvwxyz0123456789._-") != "" || strings.HasPrefix(segment, ".") || strings.HasPrefix(segment, "-") || strings.HasSuffix(segment, ".") || strings.HasSuffix(segment, "-") {
			return false
		}
	}
	return true
}

func validOCITag(value string) bool {
	return value != "" && len(value) <= 128 && strings.Trim(value, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_.-") == "" && value[0] != '.' && value[0] != '-'
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
