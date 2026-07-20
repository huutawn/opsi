package cloudrunner

import (
	"errors"
	"strings"

	"github.com/opsi-dev/opsi/agent/internal/cloudrelay"
	"github.com/opsi-dev/opsi/agent/internal/deploy"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
)

func ResultFromRecord(record deploy.Record, err error, lease cloudrelay.DeploymentLease) cloudrelay.DeploymentResult {
	result := cloudrelay.DeploymentResult{
		SchemaVersion:         deploymentv1.ResultSchemaVersion,
		Status:                "failed",
		LeaseToken:            lease.LeaseToken,
		FinalRevisionRef:      firstNonEmpty(record.ImageTag, lease.Deployment.ManifestHash),
		IntentHash:            firstNonEmpty(lease.Deployment.IntentHash, intentHashFromLease(lease)),
		RollbackEligible:      lease.Deployment.PreviousRevisionRef != "",
		RollbackBlockedReason: "",
		SpecHash:              record.SpecHash,
		ApplicationImage:      record.ImageTag,
		ApplicationImageID:    record.ImageID,
		Namespace:             record.Namespace,
		DeploymentName:        record.DeploymentName,
		ServiceName:           record.KubernetesServiceName,
		AvailableReplicas:     record.AvailableReplicas,
	}
	if lease.Command != nil {
		result.SpecHash = firstNonEmpty(result.SpecHash, lease.Command.SpecHash)
		result.ApplicationImage = firstNonEmpty(result.ApplicationImage, lease.Command.Image.Reference)
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
		result.FailureMessageRedacted = deploy.RedactSensitive(err.Error())
		if result.FailureCode == "" {
			result.FailureCode = failureCode(err)
		}
	} else if record.Error != "" {
		result.FailureMessageRedacted = deploy.RedactSensitive(record.Error)
	}
	return result
}

func intentHashFromLease(lease cloudrelay.DeploymentLease) string {
	if lease.Deployment.DeploymentIntent == nil {
		return ""
	}
	return lease.Deployment.DeploymentIntent.Review.IntentHash
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
