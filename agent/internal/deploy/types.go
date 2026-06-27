package deploy

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/opsi-dev/opsi/agent/internal/config"
	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
)

const (
	PhaseQueued   = "queued"
	PhaseCloning  = "cloning"
	PhaseBuilding = "building"
	PhaseApplying = "applying"
	PhaseWatching = "watching"
	PhaseSuccess  = "success"
	PhaseRollback = "rollback"
	PhaseFailed   = "failed"

	StatusQueued              = "queued"
	StatusRunning             = "running"
	StatusSuccess             = "success"
	StatusRolledBack          = "rolled_back"
	StatusFailed              = "failed"
	StatusFailedAfterRollback = "failed_after_rollback"

	DefaultTerminationGracePeriodSeconds = 30
	DefaultResourceRequestsJSON          = `{"cpu":"100m","memory":"128Mi"}`
	DefaultResourceLimitsJSON            = `{"cpu":"500m","memory":"512Mi"}`
)

type Request struct {
	ProjectID                     string
	ServiceID                     string
	ServiceName                   string
	ServiceType                   string
	Service                       string
	RepoURL                       string
	Branch                        string
	GitSHA                        string
	Namespace                     string
	BuildContext                  string
	Dockerfile                    string
	ManifestPath                  string
	WatchPaths                    []string
	TerminationGracePeriodSeconds int
	ResourceRequestsJSON          string
	ResourceLimitsJSON            string
	IngressEnabled                bool
	Registry                      string
	ImageTag                      string
	TriggeredBy                   string
	DependsOn                     []ServiceDependency
}

type ServiceDependency struct {
	Name            string
	EnvPrefix       string
	ExposeAsDefault bool
}

type Record struct {
	DeployID       string
	ProjectID      string
	ServiceID      string
	ServiceName    string
	StartedAt      time.Time
	FinishedAt     time.Time
	GitSHA         string
	ImageTag       string
	Status         string
	Duration       time.Duration
	Error          string
	TriggeredBy    string
	MigrationRan   bool
	RollbackSafe   bool
	RollbackReason string
}

type ServiceRecord struct {
	ID                            string
	ProjectID                     string
	Name                          string
	Type                          string
	Namespace                     string
	RepoURL                       string
	Branch                        string
	BuildContext                  string
	Dockerfile                    string
	ManifestPath                  string
	WatchPaths                    []string
	TerminationGracePeriodSeconds int
	ResourceRequestsJSON          string
	ResourceLimitsJSON            string
	CurrentImage                  string
	Health                        string
	UpdatedAt                     time.Time
}

func ServiceRecordFromRequest(req Request) ServiceRecord {
	return ServiceRecord{
		ID:                            req.ServiceID,
		ProjectID:                     req.ProjectID,
		Name:                          req.ServiceName,
		Type:                          firstNonEmpty(req.ServiceType, "custom"),
		Namespace:                     req.Namespace,
		RepoURL:                       req.RepoURL,
		Branch:                        req.Branch,
		BuildContext:                  req.BuildContext,
		Dockerfile:                    req.Dockerfile,
		ManifestPath:                  req.ManifestPath,
		WatchPaths:                    req.WatchPaths,
		TerminationGracePeriodSeconds: req.TerminationGracePeriodSeconds,
		ResourceRequestsJSON:          req.ResourceRequestsJSON,
		ResourceLimitsJSON:            req.ResourceLimitsJSON,
		CurrentImage:                  req.ImageTag,
		Health:                        "deploying",
		UpdatedAt:                     time.Now().UTC(),
	}
}

type WebhookEvent struct {
	ProjectID   string
	ServiceID   string
	ServiceName string
	ServiceType string
	RepoURL     string
	Ref         string
	After       string
	Branch      string
	TriggeredBy string
	Body        []byte
	Signature   string
	Modified    []string
}

type githubPushPayload struct {
	Commits []struct {
		Modified []string `json:"modified"`
		Added    []string `json:"added"`
		Removed  []string `json:"removed"`
	} `json:"commits"`
}

func RequestFromContract(in *agentv1.DeployRequest, cfg config.DeploymentConfig) (Request, error) {
	if in == nil {
		return Request{}, errors.New("deploy request is required")
	}
	req := Request{
		ProjectID:                     firstNonEmpty(in.ProjectID, cfg.ProjectID),
		ServiceID:                     firstNonEmpty(in.ServiceID, cfg.ServiceID),
		ServiceName:                   firstNonEmpty(in.ServiceName, cfg.ServiceName),
		ServiceType:                   firstNonEmpty(in.ServiceType, cfg.ServiceType, "custom"),
		RepoURL:                       firstNonEmpty(in.RepoURL, cfg.RepoURL),
		Branch:                        firstNonEmpty(in.Branch, cfg.Branch),
		GitSHA:                        in.GitSHA,
		Namespace:                     firstNonEmpty(in.Namespace, cfg.Namespace),
		BuildContext:                  firstNonEmpty(in.BuildContext, cfg.BuildContext),
		Dockerfile:                    firstNonEmpty(in.Dockerfile, cfg.Dockerfile),
		ManifestPath:                  firstNonEmpty(in.ManifestPath, cfg.ManifestPath),
		WatchPaths:                    firstNonEmptySlice(in.WatchPaths, cfg.WatchPaths),
		TerminationGracePeriodSeconds: firstNonZeroInt(int(in.TerminationGracePeriodSeconds), cfg.TerminationGracePeriodSeconds, DefaultTerminationGracePeriodSeconds),
		ResourceRequestsJSON:          firstNonEmpty(in.ResourceRequestsJSON, resourceJSON(cfg.ResourceRequests, DefaultResourceRequestsJSON)),
		ResourceLimitsJSON:            firstNonEmpty(in.ResourceLimitsJSON, resourceJSON(cfg.ResourceLimits, DefaultResourceLimitsJSON)),
		IngressEnabled:                in.IngressEnabled || cfg.IngressEnabled,
		Registry:                      firstNonEmpty(in.Registry, cfg.Registry),
		ImageTag:                      in.ImageTag,
		TriggeredBy:                   firstNonEmpty(in.TriggeredBy, "cli"),
		DependsOn:                     dependenciesFromContract(in.DependsOn),
	}
	req.Service = req.ServiceName
	if req.BuildContext == "" {
		req.BuildContext = "."
	}
	if req.Dockerfile == "" {
		req.Dockerfile = "Dockerfile"
	}
	if req.Namespace == "" {
		req.Namespace = "default"
	}
	if req.ImageTag == "" && req.ProjectID != "" && req.ServiceName != "" && req.GitSHA != "" {
		req.ImageTag = imageTag(req.Registry, req.ProjectID, req.ServiceName, req.GitSHA)
	}
	req = req.WithDefaults()
	return req, req.Validate()
}

func RequestFromWebhook(event WebhookEvent, cfg config.DeploymentConfig) (Request, error) {
	branch := firstNonEmpty(event.Branch, branchFromRef(event.Ref), cfg.Branch)
	req := Request{
		ProjectID:                     firstNonEmpty(event.ProjectID, cfg.ProjectID),
		ServiceID:                     firstNonEmpty(event.ServiceID, cfg.ServiceID),
		ServiceName:                   firstNonEmpty(event.ServiceName, cfg.ServiceName),
		ServiceType:                   firstNonEmpty(event.ServiceType, cfg.ServiceType, "custom"),
		RepoURL:                       firstNonEmpty(event.RepoURL, cfg.RepoURL),
		Branch:                        branch,
		GitSHA:                        event.After,
		Namespace:                     firstNonEmpty(cfg.Namespace, "default"),
		BuildContext:                  firstNonEmpty(cfg.BuildContext, "."),
		Dockerfile:                    firstNonEmpty(cfg.Dockerfile, "Dockerfile"),
		ManifestPath:                  cfg.ManifestPath,
		WatchPaths:                    cfg.WatchPaths,
		TerminationGracePeriodSeconds: firstNonZeroInt(cfg.TerminationGracePeriodSeconds, DefaultTerminationGracePeriodSeconds),
		ResourceRequestsJSON:          resourceJSON(cfg.ResourceRequests, DefaultResourceRequestsJSON),
		ResourceLimitsJSON:            resourceJSON(cfg.ResourceLimits, DefaultResourceLimitsJSON),
		IngressEnabled:                cfg.IngressEnabled,
		Registry:                      cfg.Registry,
		TriggeredBy:                   firstNonEmpty(event.TriggeredBy, "webhook"),
	}
	req.Service = req.ServiceName
	if req.ImageTag == "" && req.ProjectID != "" && req.ServiceName != "" && req.GitSHA != "" {
		req.ImageTag = imageTag(req.Registry, req.ProjectID, req.ServiceName, req.GitSHA)
	}
	req = req.WithDefaults()
	return req, req.Validate()
}

func (r Request) Validate() error {
	if r.ProjectID == "" {
		return errors.New("project_id is required")
	}
	if !safeID(r.ProjectID) {
		return errors.New("project_id must be a safe project identifier")
	}
	if r.ServiceID == "" {
		return errors.New("service_id is required")
	}
	if !safeID(r.ServiceID) {
		return errors.New("service_id must be a safe service identifier")
	}
	if r.ServiceName == "" {
		return errors.New("service_name is required")
	}
	if r.RepoURL == "" {
		return errors.New("repo_url is required")
	}
	if r.GitSHA == "" {
		return errors.New("git_sha is required")
	}
	if r.ManifestPath == "" {
		return errors.New("manifest_path is required")
	}
	if strings.Contains(r.ServiceName, "/") || strings.Contains(r.ServiceName, " ") {
		return fmt.Errorf("service_name must be a Kubernetes deployment name")
	}
	if r.Service != "" && r.Service != r.ServiceName {
		return fmt.Errorf("service must match service_name")
	}
	seen := map[string]bool{}
	for _, dep := range r.DependsOn {
		if !safeKubernetesName(dep.Name) {
			return fmt.Errorf("depends_on name %q must be a Kubernetes-safe service name", dep.Name)
		}
		if seen[dep.Name] {
			return fmt.Errorf("depends_on contains duplicate service %q", dep.Name)
		}
		seen[dep.Name] = true
	}
	return nil
}

func (r Request) WithDefaults() Request {
	if r.BuildContext == "" {
		r.BuildContext = "."
	}
	if r.Dockerfile == "" {
		r.Dockerfile = "Dockerfile"
	}
	if r.Namespace == "" {
		r.Namespace = "default"
	}
	if r.TerminationGracePeriodSeconds == 0 {
		r.TerminationGracePeriodSeconds = DefaultTerminationGracePeriodSeconds
	}
	if r.ResourceRequestsJSON == "" {
		r.ResourceRequestsJSON = DefaultResourceRequestsJSON
	}
	if r.ResourceLimitsJSON == "" {
		r.ResourceLimitsJSON = DefaultResourceLimitsJSON
	}
	return r
}

func ShouldDeploy(event WebhookEvent, cfg config.DeploymentConfig) bool {
	branch := firstNonEmpty(event.Branch, branchFromRef(event.Ref))
	if branch == "" || branch != cfg.Branch {
		return false
	}
	return ChangedFilesMatchWatchPaths(event.ChangedFiles(), cfg.WatchPaths)
}

func (e WebhookEvent) ChangedFiles() []string {
	if len(e.Modified) > 0 {
		return e.Modified
	}
	var payload githubPushPayload
	if len(e.Body) == 0 || json.Unmarshal(e.Body, &payload) != nil {
		return nil
	}
	seen := map[string]bool{}
	var files []string
	for _, commit := range payload.Commits {
		for _, group := range [][]string{commit.Modified, commit.Added, commit.Removed} {
			for _, file := range group {
				if file == "" || seen[file] {
					continue
				}
				seen[file] = true
				files = append(files, file)
			}
		}
	}
	return files
}

func ChangedFilesMatchWatchPaths(files, watchPaths []string) bool {
	if len(watchPaths) == 0 || len(files) == 0 {
		return true
	}
	for _, file := range files {
		for _, pattern := range watchPaths {
			if globMatch(pattern, file) {
				return true
			}
		}
	}
	return false
}

func globMatch(pattern, file string) bool {
	pattern = strings.TrimSpace(filepath.ToSlash(pattern))
	file = filepath.ToSlash(file)
	if pattern == "" {
		return false
	}
	if strings.HasSuffix(pattern, "/**") {
		return strings.HasPrefix(file, strings.TrimSuffix(pattern, "**"))
	}
	matched, err := path.Match(pattern, file)
	return err == nil && matched
}

func VerifyGitHubSignature(secret string, body []byte, header string) bool {
	if secret == "" || len(body) == 0 || header == "" {
		return false
	}
	if strings.HasPrefix(header, "sha256=") {
		return verifyMAC(secret, body, strings.TrimPrefix(header, "sha256="), sha256.New)
	}
	if strings.HasPrefix(header, "sha1=") {
		return verifyMAC(secret, body, strings.TrimPrefix(header, "sha1="), sha1.New)
	}
	return false
}

func branchFromRef(ref string) string {
	return strings.TrimPrefix(ref, "refs/heads/")
}

func imageTag(registry, projectID, service, sha string) string {
	short := sha
	if len(short) > 12 {
		short = short[:12]
	}
	name := projectID + "/" + service + ":" + short
	if registry == "" {
		return name
	}
	return strings.TrimRight(registry, "/") + "/" + name
}

func safeID(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && value == filepath.Clean(value) && !strings.ContainsAny(value, `/\\ `) && value != "." && value != ".."
}

func safeKubernetesName(value string) bool {
	return regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`).MatchString(value) && len(value) <= 63
}

func dependenciesFromContract(in []agentv1.ServiceDependency) []ServiceDependency {
	if len(in) == 0 {
		return nil
	}
	out := make([]ServiceDependency, 0, len(in))
	for _, dep := range in {
		out = append(out, ServiceDependency{
			Name:            strings.TrimSpace(dep.Name),
			EnvPrefix:       strings.TrimSpace(dep.EnvPrefix),
			ExposeAsDefault: dep.ExposeAsDefault,
		})
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstNonEmptySlice(values ...[]string) []string {
	for _, value := range values {
		if len(value) > 0 {
			return value
		}
	}
	return nil
}

func firstNonZeroInt(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func resourceJSON(values map[string]string, fallback string) string {
	if len(values) == 0 {
		return fallback
	}
	data, err := json.Marshal(values)
	if err != nil {
		return fallback
	}
	return string(data)
}

func verifyMAC(secret string, body []byte, sig string, hash func() hash.Hash) bool {
	decoded, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	mac := hmac.New(hash, []byte(secret))
	_, _ = mac.Write(body)
	return hmac.Equal(decoded, mac.Sum(nil))
}
