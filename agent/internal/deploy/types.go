package deploy

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"path/filepath"
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
)

type Request struct {
	ProjectID    string
	ServiceID    string
	ServiceName  string
	ServiceType  string
	Service      string
	RepoURL      string
	Branch       string
	GitSHA       string
	Namespace    string
	BuildContext string
	Dockerfile   string
	ManifestPath string
	Registry     string
	ImageTag     string
	TriggeredBy  string
}

type Record struct {
	DeployID    string
	ProjectID   string
	ServiceID   string
	ServiceName string
	StartedAt   time.Time
	FinishedAt  time.Time
	GitSHA      string
	ImageTag    string
	Status      string
	Duration    time.Duration
	Error       string
	TriggeredBy string
}

type ServiceRecord struct {
	ID           string
	ProjectID    string
	Name         string
	Type         string
	Namespace    string
	RepoURL      string
	Branch       string
	BuildContext string
	Dockerfile   string
	ManifestPath string
	CurrentImage string
	Health       string
	UpdatedAt    time.Time
}

func ServiceRecordFromRequest(req Request) ServiceRecord {
	return ServiceRecord{
		ID:           req.ServiceID,
		ProjectID:    req.ProjectID,
		Name:         req.ServiceName,
		Type:         firstNonEmpty(req.ServiceType, "custom"),
		Namespace:    req.Namespace,
		RepoURL:      req.RepoURL,
		Branch:       req.Branch,
		BuildContext: req.BuildContext,
		Dockerfile:   req.Dockerfile,
		ManifestPath: req.ManifestPath,
		CurrentImage: req.ImageTag,
		Health:       "deploying",
		UpdatedAt:    time.Now().UTC(),
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
}

func RequestFromContract(in *agentv1.DeployRequest, cfg config.DeploymentConfig) (Request, error) {
	if in == nil {
		return Request{}, errors.New("deploy request is required")
	}
	req := Request{
		ProjectID:    firstNonEmpty(in.ProjectID, cfg.ProjectID),
		ServiceID:    firstNonEmpty(in.ServiceID, cfg.ServiceID),
		ServiceName:  firstNonEmpty(in.ServiceName, cfg.ServiceName),
		ServiceType:  firstNonEmpty(in.ServiceType, cfg.ServiceType, "custom"),
		RepoURL:      firstNonEmpty(in.RepoURL, cfg.RepoURL),
		Branch:       firstNonEmpty(in.Branch, cfg.Branch),
		GitSHA:       in.GitSHA,
		Namespace:    firstNonEmpty(in.Namespace, cfg.Namespace),
		BuildContext: firstNonEmpty(in.BuildContext, cfg.BuildContext),
		Dockerfile:   firstNonEmpty(in.Dockerfile, cfg.Dockerfile),
		ManifestPath: firstNonEmpty(in.ManifestPath, cfg.ManifestPath),
		Registry:     firstNonEmpty(in.Registry, cfg.Registry),
		ImageTag:     in.ImageTag,
		TriggeredBy:  firstNonEmpty(in.TriggeredBy, "cli"),
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
	return req, req.Validate()
}

func RequestFromWebhook(event WebhookEvent, cfg config.DeploymentConfig) (Request, error) {
	branch := firstNonEmpty(event.Branch, branchFromRef(event.Ref), cfg.Branch)
	req := Request{
		ProjectID:    firstNonEmpty(event.ProjectID, cfg.ProjectID),
		ServiceID:    firstNonEmpty(event.ServiceID, cfg.ServiceID),
		ServiceName:  firstNonEmpty(event.ServiceName, cfg.ServiceName),
		ServiceType:  firstNonEmpty(event.ServiceType, cfg.ServiceType, "custom"),
		RepoURL:      firstNonEmpty(event.RepoURL, cfg.RepoURL),
		Branch:       branch,
		GitSHA:       event.After,
		Namespace:    firstNonEmpty(cfg.Namespace, "default"),
		BuildContext: firstNonEmpty(cfg.BuildContext, "."),
		Dockerfile:   firstNonEmpty(cfg.Dockerfile, "Dockerfile"),
		ManifestPath: cfg.ManifestPath,
		Registry:     cfg.Registry,
		TriggeredBy:  firstNonEmpty(event.TriggeredBy, "webhook"),
	}
	req.Service = req.ServiceName
	if req.ImageTag == "" && req.ProjectID != "" && req.ServiceName != "" && req.GitSHA != "" {
		req.ImageTag = imageTag(req.Registry, req.ProjectID, req.ServiceName, req.GitSHA)
	}
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
	return nil
}

func ShouldDeploy(event WebhookEvent, cfg config.DeploymentConfig) bool {
	branch := firstNonEmpty(event.Branch, branchFromRef(event.Ref))
	return branch != "" && branch == cfg.Branch
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
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
