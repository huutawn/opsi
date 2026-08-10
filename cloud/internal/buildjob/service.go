package buildjob

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"path"
	"strings"
	"time"
	"unicode"
)

const (
	StatusPending   = "pending"
	StatusReady     = "ready"
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"

	StrategyAuto              = "auto"
	StrategyDockerfile        = "dockerfile"
	StrategyBuildpack         = "buildpack"
	StrategyBuildpackRequired = "buildpack_required"
)

type Error struct {
	Code    string `json:"code"`
	Status  int    `json:"-"`
	Message string `json:"message"`
	Cause   string `json:"cause"`
}

func (e Error) Error() string { return e.Code }

type SourceSnapshot struct {
	BindingID          string    `json:"binding_id"`
	BindingUpdatedAt   time.Time `json:"binding_updated_at"`
	InstallationID     int64     `json:"github_installation_id"`
	RepositoryID       int64     `json:"repository_id"`
	RepositoryOwnerID  int64     `json:"repository_owner_id"`
	RepositoryFullName string    `json:"repository_full_name"`
	SelectedRef        string    `json:"selected_ref"`
	ResolvedCommitSHA  string    `json:"resolved_commit_sha"`
	ApplicationRoot    string    `json:"application_root"`
	BuildContext       string    `json:"build_context"`
}

type ApplicationSource struct {
	ProjectID          string
	EnvironmentID      string
	ApplicationID      string
	BindingID          string
	BindingUpdatedAt   time.Time
	InstallationID     int64
	RepositoryID       int64
	RepositoryOwnerID  int64
	RepositoryFullName string
	SelectedRef        string
	ApplicationRoot    string
	BuildContext       string
	BuildStrategy      string
	DockerfilePath     string
}

type Job struct {
	ID                     string         `json:"id"`
	ProjectID              string         `json:"project_id"`
	EnvironmentID          string         `json:"environment_id"`
	ApplicationID          string         `json:"application_id"`
	Source                 SourceSnapshot `json:"source"`
	RequestedBuildStrategy string         `json:"requested_build_strategy"`
	ResolvedBuildStrategy  string         `json:"resolved_build_strategy"`
	DockerfilePath         string         `json:"dockerfile_path,omitempty"`
	Status                 string         `json:"status"`
	FailureCode            string         `json:"failure_code,omitempty"`
	FailureMessageRedacted string         `json:"failure_message_redacted,omitempty"`
	FailureCause           string         `json:"failure_cause,omitempty"`
	CreatedBy              string         `json:"created_by"`
	CreatedAt              time.Time      `json:"created_at"`
	UpdatedAt              time.Time      `json:"updated_at"`
	IdempotencyKey         string         `json:"-"`
}

type SourceAuthority interface {
	ResolveBuildJobSource(context.Context, string, string) (ApplicationSource, error)
}

type RepositoryAuthority interface {
	ResolveCommit(context.Context, int64, string, string) (string, error)
	RepositoryFileExists(context.Context, int64, string, string, string) (bool, error)
}

type Store interface {
	Create(context.Context, Job) (Job, bool, error)
	Get(context.Context, string, string, string) (Job, error)
	GetByIdempotency(context.Context, string, string, string) (Job, bool, error)
	List(context.Context, string, string, string, int) ([]Job, error)
}

type Service struct {
	Store      Store
	Sources    SourceAuthority
	Repository RepositoryAuthority
	Now        func() time.Time
	NewID      func() (string, error)
}

func (s Service) Create(ctx context.Context, projectID, applicationID, createdBy, idempotencyKey string) (Job, bool, error) {
	if s.Store == nil || s.Sources == nil || s.Repository == nil {
		return Job{}, false, unavailable()
	}
	if !validOpaqueID(projectID) || !validOpaqueID(applicationID) || !validOpaqueID(createdBy) || !validIdempotencyKey(idempotencyKey) {
		return Job{}, false, invalid("BUILD_JOB_REQUEST_INVALID", "project, application, actor, or idempotency key is invalid", "request")
	}
	if current, ok, err := s.Store.GetByIdempotency(ctx, projectID, applicationID, idempotencyKey); err != nil {
		return Job{}, false, err
	} else if ok {
		return current, true, nil
	}

	source, err := s.Sources.ResolveBuildJobSource(ctx, projectID, applicationID)
	if err != nil {
		return Job{}, false, err
	}
	if err := validateSource(source, projectID, applicationID); err != nil {
		return Job{}, false, err
	}
	sha, err := s.Repository.ResolveCommit(ctx, source.InstallationID, source.RepositoryFullName, source.SelectedRef)
	if err != nil {
		return Job{}, false, err
	}
	if !validSHA40(sha) {
		return Job{}, false, Error{Code: "GITHUB_COMMIT_UNRESOLVED", Status: 409, Message: "The selected ref did not resolve to a valid commit.", Cause: "github_commit"}
	}

	resolved, dockerfile, failure, err := resolveStrategy(ctx, s.Repository, source, sha)
	if err != nil {
		return Job{}, false, err
	}
	id, err := s.newID()
	if err != nil {
		return Job{}, false, unavailable()
	}
	now := s.clock()
	job := Job{
		ID: id, ProjectID: projectID, EnvironmentID: source.EnvironmentID, ApplicationID: applicationID,
		Source:                 SourceSnapshot{BindingID: source.BindingID, BindingUpdatedAt: source.BindingUpdatedAt, InstallationID: source.InstallationID, RepositoryID: source.RepositoryID, RepositoryOwnerID: source.RepositoryOwnerID, RepositoryFullName: source.RepositoryFullName, SelectedRef: source.SelectedRef, ResolvedCommitSHA: sha, ApplicationRoot: source.ApplicationRoot, BuildContext: source.BuildContext},
		RequestedBuildStrategy: source.BuildStrategy, ResolvedBuildStrategy: resolved, DockerfilePath: dockerfile,
		Status: StatusReady, CreatedBy: createdBy, CreatedAt: now, UpdatedAt: now, IdempotencyKey: idempotencyKey,
	}
	if failure != nil {
		job.Status = StatusFailed
		job.FailureCode = failure.Code
		job.FailureMessageRedacted = failure.Message
		job.FailureCause = failure.Cause
	}
	return s.Store.Create(ctx, job)
}

func (s Service) Get(ctx context.Context, projectID, applicationID, jobID string) (Job, error) {
	if s.Store == nil || !validOpaqueID(projectID) || !validOpaqueID(applicationID) || !validOpaqueID(jobID) {
		return Job{}, invalid("BUILD_JOB_ID_INVALID", "project, application, or build job is invalid", "request")
	}
	return s.Store.Get(ctx, projectID, applicationID, jobID)
}

func (s Service) List(ctx context.Context, projectID, applicationID, status string, limit int) ([]Job, error) {
	if s.Store == nil || !validOpaqueID(projectID) || !validOpaqueID(applicationID) {
		return nil, invalid("BUILD_JOB_LIST_INVALID", "project or application is invalid", "request")
	}
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 || status != "" && !validStatus(status) {
		return nil, invalid("BUILD_JOB_LIST_INVALID", "status or limit is invalid", "request")
	}
	return s.Store.List(ctx, projectID, applicationID, status, limit)
}

func resolveStrategy(ctx context.Context, repository RepositoryAuthority, source ApplicationSource, sha string) (string, string, *Error, error) {
	if source.BuildStrategy == StrategyBuildpack {
		failure := Error{Code: "BUILD_STRATEGY_NOT_IMPLEMENTED", Status: 422, Message: "Buildpack execution is not implemented.", Cause: "build_strategy"}
		return StrategyBuildpackRequired, "", &failure, nil
	}
	if source.DockerfilePath != "" {
		exists, err := repository.RepositoryFileExists(ctx, source.InstallationID, source.RepositoryFullName, sha, source.DockerfilePath)
		if err != nil {
			return "", "", nil, err
		}
		if !exists {
			return "", "", nil, Error{Code: "DOCKERFILE_NOT_FOUND", Status: 422, Message: "The selected Dockerfile does not exist at the resolved commit.", Cause: "dockerfile"}
		}
		return StrategyDockerfile, source.DockerfilePath, nil, nil
	}
	if source.BuildStrategy == StrategyDockerfile {
		return "", "", nil, Error{Code: "DOCKERFILE_PATH_REQUIRED", Status: 422, Message: "Dockerfile strategy requires an explicit Dockerfile path.", Cause: "dockerfile"}
	}

	// Auto checks only the canonical Dockerfile in application_root and build_context.
	candidates := uniqueStrings(repositoryPath(source.ApplicationRoot, "Dockerfile"), repositoryPath(source.BuildContext, "Dockerfile"))
	found := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		exists, err := repository.RepositoryFileExists(ctx, source.InstallationID, source.RepositoryFullName, sha, candidate)
		if err != nil {
			return "", "", nil, err
		}
		if exists {
			found = append(found, candidate)
		}
	}
	if len(found) > 1 {
		return "", "", nil, Error{Code: "DOCKERFILE_AMBIGUOUS", Status: 422, Message: "Multiple canonical Dockerfiles exist; select one explicitly.", Cause: "dockerfile"}
	}
	if len(found) == 1 {
		return StrategyDockerfile, found[0], nil, nil
	}
	failure := Error{Code: "BUILDPACK_REQUIRED", Status: 422, Message: "No canonical Dockerfile exists at the resolved commit.", Cause: "dockerfile_missing"}
	return StrategyBuildpackRequired, "", &failure, nil
}

func validateSource(source ApplicationSource, projectID, applicationID string) error {
	if source.ProjectID != projectID || source.ApplicationID != applicationID || !validOpaqueID(source.EnvironmentID) || !validOpaqueID(source.BindingID) || source.BindingUpdatedAt.IsZero() || source.InstallationID <= 0 || source.RepositoryID <= 0 || source.RepositoryOwnerID <= 0 || !validRepositoryFullName(source.RepositoryFullName) {
		return Error{Code: "BUILD_SOURCE_INVALID_SCOPE", Status: 409, Message: "The active source binding does not belong to the requested Application.", Cause: "source_scope"}
	}
	if source.SelectedRef == "" || len(source.SelectedRef) > 1024 || strings.TrimSpace(source.SelectedRef) != source.SelectedRef || strings.IndexFunc(source.SelectedRef, unicode.IsControl) >= 0 {
		return Error{Code: "BUILD_SOURCE_INVALID", Status: 409, Message: "The source binding contains an invalid selected ref.", Cause: "source_binding"}
	}
	if !canonicalRepositoryPath(source.ApplicationRoot, true) || !canonicalRepositoryPath(source.BuildContext, true) || source.BuildContext != "." && source.ApplicationRoot != source.BuildContext && !strings.HasPrefix(source.ApplicationRoot, source.BuildContext+"/") {
		return Error{Code: "BUILD_SOURCE_INVALID", Status: 409, Message: "The source binding contains invalid canonical paths.", Cause: "source_binding"}
	}
	if source.BuildStrategy != StrategyAuto && source.BuildStrategy != StrategyDockerfile && source.BuildStrategy != StrategyBuildpack {
		return Error{Code: "BUILD_SOURCE_INVALID", Status: 409, Message: "The source binding contains an invalid build strategy.", Cause: "source_binding"}
	}
	if source.DockerfilePath != "" && !canonicalRepositoryPath(source.DockerfilePath, false) || source.BuildStrategy == StrategyDockerfile && source.DockerfilePath == "" {
		return Error{Code: "BUILD_SOURCE_INVALID", Status: 409, Message: "The source binding contains an invalid Dockerfile path.", Cause: "source_binding"}
	}
	return nil
}

func repositoryPath(root, name string) string {
	if root == "." {
		return name
	}
	return path.Join(root, name)
}

func uniqueStrings(values ...string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func canonicalRepositoryPath(value string, allowDot bool) bool {
	if value == "" || len(value) > 1024 || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") || strings.IndexFunc(value, unicode.IsControl) >= 0 || path.Clean(value) != value {
		return false
	}
	if value == "." {
		return allowDot
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func validRepositoryFullName(value string) bool {
	parts := strings.Split(value, "/")
	return len(parts) == 2 && validGitHubName(parts[0]) && validGitHubName(parts[1])
}

func validGitHubName(value string) bool {
	if value == "" || len(value) > 100 {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || strings.ContainsRune("._-", character)) {
			return false
		}
	}
	return true
}

func validSHA40(value string) bool {
	if len(value) != 40 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validOpaqueID(value string) bool {
	return value != "" && len(value) <= 128 && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n/\\")
}

func validIdempotencyKey(value string) bool {
	if value == "" || len(value) > 128 || strings.TrimSpace(value) != value {
		return false
	}
	return strings.IndexFunc(value, func(character rune) bool { return unicode.IsSpace(character) || unicode.IsControl(character) }) < 0
}

func validStatus(status string) bool {
	switch status {
	case StatusPending, StatusReady, StatusRunning, StatusSucceeded, StatusFailed, StatusCancelled:
		return true
	default:
		return false
	}
}

func invalid(code, message, cause string) error {
	return Error{Code: code, Status: 400, Message: message, Cause: cause}
}

func unavailable() error {
	return Error{Code: "BUILD_JOB_UNAVAILABLE", Status: 503, Message: "BuildJob authority is unavailable.", Cause: "build_job_store"}
}

func (s Service) clock() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s Service) newID() (string, error) {
	if s.NewID != nil {
		return s.NewID()
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "bj-" + hex.EncodeToString(raw[:]), nil
}

func idempotencyScope(projectID, applicationID, key string) string {
	return projectID + "\x00" + applicationID + "\x00" + key
}

func Code(err error) string {
	var typed Error
	if errors.As(err, &typed) {
		return typed.Code
	}
	return ""
}
