package cloudrunner

import (
	"errors"
	"strings"

	"github.com/opsi-dev/opsi/agent/internal/cloudrelay"
	"github.com/opsi-dev/opsi/agent/internal/config"
	"github.com/opsi-dev/opsi/agent/internal/deploy"
)

var errImageSourceUnsupported = errors.New("image source deploy is not supported by the Agent runner yet")

func RequestFromLease(lease cloudrelay.DeploymentLease, cfg config.DeploymentConfig) (deploy.Request, error) {
	if lease.Service.SourceType == "image" {
		return deploy.Request{}, errImageSourceUnsupported
	}
	req := deploy.Request{
		ProjectID:                     cfg.ProjectID,
		ServiceID:                     firstNonEmpty(lease.Service.ID, cfg.ServiceID),
		ServiceName:                   firstNonEmpty(lease.Service.Name, cfg.ServiceName),
		ServiceType:                   firstNonEmpty(lease.Service.Type, cfg.ServiceType, "application"),
		RepoURL:                       firstNonEmpty(lease.Service.RepoURL, cfg.RepoURL),
		Branch:                        firstNonEmpty(lease.Service.Branch, cfg.Branch),
		GitSHA:                        lease.Service.GitSHA,
		Namespace:                     firstNonEmpty(lease.Service.Namespace, cfg.Namespace),
		BuildContext:                  firstNonEmpty(cfg.BuildContext, "."),
		Dockerfile:                    firstNonEmpty(cfg.Dockerfile, "Dockerfile"),
		ManifestPath:                  cfg.ManifestPath,
		WatchPaths:                    cfg.WatchPaths,
		TerminationGracePeriodSeconds: firstNonZeroInt(cfg.TerminationGracePeriodSeconds, deploy.DefaultTerminationGracePeriodSeconds),
		ResourceRequestsJSON:          deploy.DefaultResourceRequestsJSON,
		ResourceLimitsJSON:            deploy.DefaultResourceLimitsJSON,
		IngressEnabled:                cfg.IngressEnabled,
		Registry:                      cfg.Registry,
		TriggeredBy:                   "cloud",
	}
	req.ProjectID = firstNonEmpty(cfg.ProjectID, req.ProjectID)
	req.Service = req.ServiceName
	if req.ImageTag == "" && req.ProjectID != "" && req.ServiceName != "" && req.GitSHA != "" {
		req.ImageTag = imageTag(req.Registry, req.ProjectID, req.ServiceName, req.GitSHA)
	}
	req = req.WithDefaults()
	return req, req.Validate()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstNonZeroInt(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
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
