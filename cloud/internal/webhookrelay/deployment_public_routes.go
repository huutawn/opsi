package webhookrelay

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/opsi-dev/opsi/cloud/internal/deploymentworkflow"
	"github.com/opsi-dev/opsi/cloud/internal/publichostname"
	"github.com/opsi-dev/opsi/cloud/internal/repositoryanalysis"
	exposurev1 "github.com/opsi-dev/opsi/contracts/go/exposurev1"
	serviceconfigurationv1 "github.com/opsi-dev/opsi/contracts/go/serviceconfigurationv1"
)

var reservedPublicSubdomains = map[string]bool{
	"example": true, "internal": true, "invalid": true, "local": true, "localhost": true,
}

// canonicalPublicHostname owns the public-subdomain boundary. The API accepts
// exactly one user-facing DNS label and persists only the managed FQDN.
func (s *Server) canonicalPublicHostname(label string) (string, error) {
	if s.Config.DeploymentDomain == "" {
		return "", deploymentworkflow.Error{Code: "PUBLIC_DEPLOYMENT_DOMAIN_REQUIRED", Status: http.StatusServiceUnavailable, Message: "Public deployment subdomains are not configured.", NextAction: "Set OPSI_CLOUD_DEPLOYMENT_DOMAIN before publishing a public deployment."}
	}
	if label == "" {
		return "", deploymentworkflow.Error{Code: "PUBLIC_HOSTNAME_REQUIRED", Status: http.StatusBadRequest, Message: "A public subdomain is required.", NextAction: "Enter one DNS label, for example tcip."}
	}
	if label != strings.TrimSpace(label) || strings.Contains(label, ".") {
		return "", deploymentworkflow.Error{Code: "PUBLIC_HOSTNAME_INVALID", Status: http.StatusBadRequest, Message: "Public subdomain must be one DNS label.", NextAction: "Enter only the label before the managed domain suffix."}
	}
	canonicalLabel, err := exposurev1.NormalizeHostname(label)
	if err != nil || reservedPublicSubdomains[canonicalLabel] {
		return "", deploymentworkflow.Error{Code: "PUBLIC_HOSTNAME_INVALID", Status: http.StatusBadRequest, Message: "Public subdomain is invalid or reserved.", NextAction: "Choose a different DNS label."}
	}
	return canonicalLabel + "." + s.Config.DeploymentDomain, nil
}

func (s *Server) canonicalizeNewDeploymentTarget(target *deploymentworkflow.Target) error {
	if target.Exposure != "public" || target.Hostname == "" {
		return nil
	}
	hostname, err := s.canonicalPublicHostname(target.Hostname)
	if err != nil {
		return err
	}
	target.Hostname = hostname
	return nil
}

func (s *Server) canonicalizeUpdatedPlan(draft *deploymentworkflow.Plan, current deploymentworkflow.Plan) error {
	if draft.Target.Exposure == "public" && draft.Target.Hostname != "" && draft.Target.Hostname != current.Target.Hostname {
		hostname, err := s.canonicalPublicHostname(draft.Target.Hostname)
		if err != nil {
			return err
		}
		draft.Target.Hostname = hostname
	}
	for index := range draft.Applications {
		application := &draft.Applications[index]
		if application.Exposure.Automatic {
			continue
		}
		if application.Exposure.Mode != "public" || application.Exposure.Hostname == "" {
			continue
		}
		currentHostname := ""
		if index < len(current.Applications) {
			currentHostname = current.Applications[index].Exposure.Hostname
		}
		if application.Exposure.Hostname == currentHostname {
			continue
		}
		hostname, err := s.canonicalPublicHostname(application.Exposure.Hostname)
		if err != nil {
			return err
		}
		application.Exposure.Hostname = hostname
	}
	return s.applyAutomaticPublicRoutesForPlan(draft)
}

// applyAutomaticPublicRoutes is the only hostname generator for automatic
// routes. It uses the user supplied label as a stable base and never derives a
// hostname from the repository name.
func (s *Server) applyAutomaticPublicRoutes(analysis *repositoryanalysis.Result, target deploymentworkflow.Target) error {
	plan := deploymentworkflow.Plan{Applications: analysis.Applications, Dependencies: analysis.Dependencies, Bindings: analysis.Bindings, Target: target}
	if err := s.applyAutomaticPublicRoutesForPlan(&plan); err != nil {
		return err
	}
	analysis.Applications = plan.Applications
	return nil
}

func (s *Server) applyAutomaticPublicRoutesForPlan(plan *deploymentworkflow.Plan) error {
	if plan.Target.Exposure != "public" || plan.Target.PublicRoutes == deploymentworkflow.PublicRoutesManual {
		for index := range plan.Applications {
			if plan.Applications[index].Exposure.Automatic {
				plan.Applications[index].Exposure = repositoryanalysis.Exposure{Mode: "internal"}
			}
		}
		return nil
	}
	if plan.Target.PublicRoutes == "" {
		plan.Target.PublicRoutes = deploymentworkflow.PublicRoutesAutomatic
	}
	if plan.Target.PublicRoutes != deploymentworkflow.PublicRoutesAutomatic {
		return deploymentworkflow.Error{Code: "PUBLIC_ROUTE_POLICY_INVALID", Status: http.StatusBadRequest, Message: "Public route policy is invalid."}
	}
	suffix := "." + s.Config.DeploymentDomain
	base := strings.TrimSuffix(plan.Target.Hostname, suffix)
	if base == plan.Target.Hostname || base == "" || strings.Contains(base, ".") {
		return deploymentworkflow.Error{Code: "PUBLIC_HOSTNAME_INVALID", Status: http.StatusBadRequest, Message: "Automatic routes need one managed public subdomain label.", NextAction: "Enter one label before the managed domain suffix."}
	}
	rootCandidates := map[string]bool{}
	pathsByApplication := map[string]map[string]bool{}
	addBackendPath := func(applicationKey, rawPath string) error {
		path, err := exposurev1.NormalizePath(rawPath)
		if err != nil || path == "/" {
			return deploymentworkflow.Error{Code: "AUTO_PUBLIC_ROUTE_INVALID", Status: http.StatusBadRequest, Message: "Same-origin backend paths must be valid non-root URL paths.", NextAction: "Assign a unique path such as /api to every browser backend."}
		}
		if pathsByApplication[applicationKey] == nil {
			pathsByApplication[applicationKey] = map[string]bool{}
		}
		pathsByApplication[applicationKey][path] = true
		return nil
	}
	for _, dependency := range plan.Dependencies {
		if dependency.Protocol != "http" {
			continue
		}
		if dependency.Strategy == serviceconfigurationv1.StrategySameOrigin {
			rootCandidates[dependency.From] = true
			if err := addBackendPath(dependency.To, dependency.Path); err != nil {
				return err
			}
		}
		if dependency.Strategy == serviceconfigurationv1.StrategyInternalHTTP && hasApplicationProxyEvidence(dependency) {
			rootCandidates[dependency.From] = true
			proxyPaths := dependency.ProxyPaths
			if len(proxyPaths) == 0 {
				proxyPaths = []string{dependency.Path}
			}
			for _, proxyPath := range proxyPaths {
				if err := addBackendPath(dependency.To, proxyPath); err != nil {
					return err
				}
			}
		}
	}
	for _, binding := range plan.Bindings {
		if binding.Kind != serviceconfigurationv1.BindingBrowserHTTP {
			continue
		}
		rootCandidates[binding.From] = true
		if err := addBackendPath(binding.To, binding.Path); err != nil {
			return err
		}
	}
	if len(rootCandidates) > 1 {
		return deploymentworkflow.Error{Code: "AUTO_PUBLIC_ROOT_AMBIGUOUS", Status: http.StatusUnprocessableEntity, Message: "Automatic routing found more than one browser frontend.", NextAction: "Switch to manual routes or keep one application at the root path."}
	}
	usedPaths := map[string]string{}
	for index := range plan.Applications {
		application := &plan.Applications[index]
		if application.Port < 1 {
			continue
		}
		path := "/" + safeDNSLabel(application.Key)
		additionalPaths := []string(nil)
		if rootCandidates[application.Key] {
			path = "/"
		}
		if candidates := pathsByApplication[application.Key]; len(candidates) > 0 {
			paths := make([]string, 0, len(candidates))
			for candidate := range candidates {
				paths = append(paths, candidate)
			}
			sort.Strings(paths)
			path = paths[0]
			additionalPaths = append(additionalPaths, paths[1:]...)
		}
		for _, candidate := range append([]string{path}, additionalPaths...) {
			if owner := usedPaths[candidate]; owner != "" && owner != application.Key {
				return deploymentworkflow.Error{Code: "AUTO_PUBLIC_PATH_CONFLICT", Status: http.StatusConflict, Message: "Automatic public routes contain a duplicate path.", NextAction: "Assign a unique same-origin path to each backend."}
			}
			usedPaths[candidate] = application.Key
		}
		application.Exposure = repositoryanalysis.Exposure{Mode: "public", Hostname: plan.Target.Hostname, Path: path, AdditionalPaths: additionalPaths, Automatic: true}
	}
	return nil
}

func hasApplicationProxyEvidence(dependency repositoryanalysis.Dependency) bool {
	for _, evidence := range dependency.Evidence {
		if evidence.Kind == "application_proxy" {
			return true
		}
	}
	return false
}

func (s *Server) reservePublicHostname(ctx context.Context, userID, projectID, environmentID, runtimeID, hostname string) (publichostname.Allocation, error) {
	allocation, _, err := s.PublicHostnames.Reserve(ctx, publichostname.ReserveRequest{Hostname: hostname, OwnerUserID: userID, ProjectID: projectID, EnvironmentID: environmentID, RuntimeID: runtimeID})
	return allocation, publicHostnameError(err)
}

func (s *Server) ensurePublicHostnameAvailable(ctx context.Context, hostname, projectID, environmentID string) error {
	allocation, err := s.PublicHostnames.GetByHostname(ctx, hostname)
	if errors.Is(err, publichostname.ErrNotFound) || allocation.Status == publichostname.StatusReleased {
		return nil
	}
	if err != nil {
		return publicHostnameError(err)
	}
	if allocation.ProjectID == projectID && allocation.EnvironmentID == environmentID {
		return nil
	}
	return deploymentworkflow.Error{Code: "PUBLIC_HOSTNAME_UNAVAILABLE", Status: http.StatusConflict, Message: "This public subdomain has already been issued by Opsi.", NextAction: "Choose a different public subdomain."}
}

func publicHostnameError(err error) error {
	if err == nil {
		return nil
	}
	var allocationErr publichostname.Error
	if errors.As(err, &allocationErr) {
		status := http.StatusConflict
		if allocationErr.Code == "PUBLIC_HOSTNAME_RESERVATION_INVALID" {
			status = http.StatusBadRequest
		}
		return deploymentworkflow.Error{Code: allocationErr.Code, Status: status, Message: allocationErr.Message, NextAction: allocationErr.NextAction}
	}
	return err
}
