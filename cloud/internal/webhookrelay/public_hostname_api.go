package webhookrelay

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/auth"
	"github.com/opsi-dev/opsi/cloud/internal/cloudflare"
	"github.com/opsi-dev/opsi/cloud/internal/deploymentworkflow"
	"github.com/opsi-dev/opsi/cloud/internal/publichostname"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
)

func (s *Server) handlePublicHostnameAPI(w http.ResponseWriter, r *http.Request, projectID string, parts []string, principal auth.VerifyResult) bool {
	if len(parts) < 3 || parts[2] != "public-hostnames" {
		return false
	}
	if len(parts) == 3 && r.Method == http.MethodGet {
		if !s.requireRole(w, r, principal, projectID, "public_hostname", projectID, "owner", "admin", "developer", "viewer", "support") {
			return true
		}
		quota, err := s.PublicHostnames.Quota(r.Context(), principal.UserID)
		projectAllocations, projectErr := s.PublicHostnames.ProjectAllocations(r.Context(), projectID)
		if err == nil {
			err = projectErr
		}
		writeRegistryResult(w, r, struct {
			publichostname.Quota
			ProjectAllocations []publichostname.Allocation `json:"project_allocations"`
		}{Quota: quota, ProjectAllocations: projectAllocations}, err, http.StatusOK)
		return true
	}
	if len(parts) != 5 || r.Method != http.MethodPost || parts[4] != "release" && parts[4] != "retry" {
		return false
	}
	allocationID := parts[3]
	if !requireWriteHeaders(w, r) || !s.requireRole(w, r, principal, projectID, "public_hostname", allocationID, "owner", "admin", "developer") {
		return true
	}
	allocation, err := s.PublicHostnames.Get(r.Context(), allocationID)
	if err != nil || allocation.ProjectID != projectID {
		if errors.Is(err, publichostname.ErrNotFound) || err == nil {
			err = registry.ErrNotFound
		}
		writeRegistryFailure(w, r, err)
		return true
	}
	if parts[4] == "release" {
		allocation, err = s.releasePublicHostname(r.Context(), allocation)
	} else {
		allocation, err = s.retryPublicHostname(r.Context(), allocation)
	}
	writeRegistryResult(w, r, allocation, err, http.StatusOK)
	return true
}

func (s *Server) publishPublicHostname(ctx context.Context, allocation publichostname.Allocation, targetIP string) (publichostname.Allocation, error) {
	if parsed := net.ParseIP(targetIP); parsed == nil || parsed.To4() == nil {
		_, _ = s.PublicHostnames.PublicationFailed(ctx, allocation.ID, "", "PUBLIC_TARGET_IPV4_INVALID", "The verified target node does not have a public IPv4 address.")
		return allocation, deploymentworkflow.Error{Code: "PUBLIC_TARGET_IPV4_INVALID", Status: http.StatusConflict, Message: "The verified target node does not have a public IPv4 address.", NextAction: "Correct the target node public_host and retry publication."}
	}
	allocation, err := s.PublicHostnames.Provisioning(ctx, allocation.ID, targetIP)
	if err != nil {
		return allocation, err
	}
	if s.Cloudflare == nil {
		allocation, _ = s.PublicHostnames.PublicationFailed(ctx, allocation.ID, targetIP, "CLOUDFLARE_NOT_CONFIGURED", "Cloudflare publication is not configured.")
		return allocation, deploymentworkflow.Error{Code: "CLOUDFLARE_NOT_CONFIGURED", Status: http.StatusServiceUnavailable, Message: "Cloudflare publication is not configured.", NextAction: "Configure the scoped Cloudflare API token and retry publication."}
	}
	record, err := s.Cloudflare.ReconcileARecord(ctx, allocation.Hostname, targetIP, allocation.ID)
	if err != nil {
		code := "CLOUDFLARE_DNS_FAILED"
		message := "Cloudflare could not publish the exact DNS record."
		if errors.Is(err, cloudflare.ErrDNSConflict) {
			code, message = "CLOUDFLARE_DNS_CONFLICT", "A DNS record with this hostname exists but is not owned by this Opsi allocation."
		}
		allocation, _ = s.PublicHostnames.PublicationFailed(ctx, allocation.ID, targetIP, code, message)
		return allocation, deploymentworkflow.Error{Code: code, Status: http.StatusConflict, Message: message, NextAction: "Resolve the DNS conflict or Cloudflare error, then retry publication."}
	}
	return s.PublicHostnames.Active(ctx, allocation.ID, targetIP, record.ID)
}

func (s *Server) releasePublicHostname(ctx context.Context, allocation publichostname.Allocation) (publichostname.Allocation, error) {
	if allocation.Status == publichostname.StatusReleased {
		return allocation, nil
	}
	allocation, err := s.PublicHostnames.ReleasePending(ctx, allocation.ID)
	if err != nil {
		return allocation, err
	}
	if s.Cloudflare == nil && allocation.CloudflareRecordID != "" {
		return allocation, deploymentworkflow.Error{Code: "CLOUDFLARE_NOT_CONFIGURED", Status: http.StatusServiceUnavailable, Message: "Cloudflare is unavailable; the hostname remains release pending."}
	}
	if s.Cloudflare != nil {
		if err := s.Cloudflare.DeleteARecord(ctx, allocation.CloudflareRecordID, allocation.Hostname, allocation.ID); err != nil {
			return allocation, deploymentworkflow.Error{Code: "CLOUDFLARE_DNS_RELEASE_FAILED", Status: http.StatusServiceUnavailable, Message: "The owned DNS record could not be deleted; the hostname remains release pending.", NextAction: "Retry release after Cloudflare is available."}
		}
	}
	return s.PublicHostnames.Released(ctx, allocation.ID)
}

func (s *Server) retryPublicHostname(ctx context.Context, allocation publichostname.Allocation) (publichostname.Allocation, error) {
	if allocation.Status == publichostname.StatusActive || allocation.Status == publichostname.StatusReleased {
		return allocation, nil
	}
	if allocation.Status == publichostname.StatusReleasePending {
		return s.releasePublicHostname(ctx, allocation)
	}
	if allocation.TargetIP == "" {
		return allocation, deploymentworkflow.Error{Code: "PUBLIC_TARGET_IPV4_PENDING", Status: http.StatusConflict, Message: "Publication is waiting for a verified target IPv4 address.", NextAction: "Wait for workload verification before retrying publication."}
	}
	return s.publishPublicHostname(ctx, allocation, allocation.TargetIP)
}

func (s *Server) ensureAutomaticHostnamePublished(ctx context.Context, run deploymentworkflow.Run) error {
	if run.Plan.Target.Exposure != "public" || run.Plan.Target.PublicRoutes != deploymentworkflow.PublicRoutesAutomatic || run.Plan.Target.Hostname == "" {
		return nil
	}
	allocation, err := s.PublicHostnames.GetByHostname(ctx, run.Plan.Target.Hostname)
	if errors.Is(err, publichostname.ErrNotFound) {
		allocation, err = s.reservePublicHostname(ctx, run.CreatedBy, run.ProjectID, run.Plan.Target.EnvironmentID, run.Plan.Target.RuntimeID, run.Plan.Target.Hostname)
	}
	if err != nil {
		return err
	}
	targetIP, err := s.verifiedTargetIPv4(run)
	if err != nil {
		return err
	}
	_, err = s.publishPublicHostname(ctx, allocation, targetIP)
	return err
}

func (s *Server) verifiedTargetIPv4(run deploymentworkflow.Run) (string, error) {
	nodes, err := s.Registry.ListNodes(run.ProjectID)
	if err != nil {
		return "", err
	}
	for _, node := range nodes {
		if run.Plan.Target.NodeID != "" && node.ID != run.Plan.Target.NodeID {
			continue
		}
		if node.EnvironmentID != run.Plan.Target.EnvironmentID || node.RuntimeID != run.Plan.Target.RuntimeID || node.ProjectID != run.ProjectID || node.Status != registry.NodeHealthy {
			continue
		}
		if parsed := net.ParseIP(node.PublicHost); parsed != nil && parsed.To4() != nil {
			return parsed.To4().String(), nil
		}
	}
	return "", deploymentworkflow.Error{Code: "PUBLIC_TARGET_IPV4_INVALID", Status: http.StatusConflict, Message: "The verified deployment node has no valid public IPv4 address.", NextAction: "Correct node public_host and retry publication."}
}

func (s *Server) verifiedDeploymentIPv4(projectID, deploymentID string) (string, error) {
	reader, ok := s.Registry.(immutableDeploymentReader)
	if !ok {
		return "", deploymentworkflow.Error{Code: "DEPLOYMENT_UNAVAILABLE", Status: http.StatusServiceUnavailable, Message: "Deployment authority is unavailable."}
	}
	job, err := reader.GetDeployment(projectID, deploymentID)
	if err != nil {
		return "", err
	}
	if job.Status != deploymentv1.StateSucceeded || job.TerminalResult == nil || job.TerminalResult.AvailableReplicas < 1 {
		return "", deploymentworkflow.Error{Code: "PUBLICATION_WORKLOAD_NOT_VERIFIED", Status: http.StatusConflict, Message: "Public hostname publication requires a verified successful workload.", NextAction: "Wait for workload verification before publishing the route."}
	}
	nodes, err := s.Registry.ListNodes(projectID)
	if err != nil {
		return "", err
	}
	for _, node := range nodes {
		if node.ID == job.NodeID && node.ProjectID == projectID && node.RuntimeID == job.RuntimeID && node.Status == registry.NodeHealthy {
			if parsed := net.ParseIP(node.PublicHost); parsed != nil && parsed.To4() != nil {
				return parsed.To4().String(), nil
			}
		}
	}
	return "", deploymentworkflow.Error{Code: "PUBLIC_TARGET_IPV4_INVALID", Status: http.StatusConflict, Message: "The verified deployment node has no valid public IPv4 address.", NextAction: "Correct node public_host and retry publication."}
}

func (s *Server) RunPublicHostnameReconciler(ctx context.Context) {
	if s.Cloudflare == nil {
		return
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	reconcile := func() {
		items, err := s.PublicHostnames.Pending(ctx, 100)
		if err == nil {
			for _, allocation := range items {
				if allocation.Status == publichostname.StatusReleasePending {
					_, _ = s.releasePublicHostname(ctx, allocation)
				} else if allocation.TargetIP != "" {
					_, _ = s.publishPublicHostname(ctx, allocation, allocation.TargetIP)
				}
			}
		}
		_ = s.Cloudflare.ReconcileZoneRules(ctx)
	}
	reconcile()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reconcile()
		}
	}
}
