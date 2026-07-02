package cloudrunner

import (
	"errors"
	"strings"

	"github.com/opsi-dev/opsi/agent/internal/cloudrelay"
	"github.com/opsi-dev/opsi/agent/internal/deploy"
)

func ResultFromRecord(record deploy.Record, err error, lease cloudrelay.DeploymentLease) cloudrelay.DeploymentResult {
	result := cloudrelay.DeploymentResult{
		Status:                "failed",
		FinalRevisionRef:      firstNonEmpty(record.ImageTag, lease.Deployment.ManifestHash),
		RollbackEligible:      lease.Deployment.PreviousRevisionRef != "",
		RollbackBlockedReason: "",
	}
	switch record.Status {
	case deploy.StatusSuccess:
		result.Status = "succeeded"
	case deploy.StatusRolledBack:
		result.Status = "rolled_back"
		result.RollbackEligible = false
	case deploy.StatusFailedAfterRollback:
		result.FailureCode = "ROLLBACK_FAILED"
	case deploy.StatusFailed:
		result.FailureCode = "DEPLOY_FAILED"
	}
	if !result.RollbackEligible && result.RollbackBlockedReason == "" {
		result.RollbackBlockedReason = "no previous successful revision"
	}
	if err != nil {
		result.FailureMessageRedacted = err.Error()
		if result.FailureCode == "" {
			result.FailureCode = failureCode(err)
		}
	} else if record.Error != "" {
		result.FailureMessageRedacted = record.Error
	}
	return result
}

func failureCode(err error) string {
	switch {
	case errors.Is(err, errImageSourceUnsupported):
		return "IMAGE_SOURCE_UNSUPPORTED"
	case err == nil:
		return ""
	case strings.Contains(err.Error(), "required"):
		return "DEPLOYMENT_REQUEST_INVALID"
	default:
		return "DEPLOY_FAILED"
	}
}
