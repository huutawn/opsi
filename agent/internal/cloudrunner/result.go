package cloudrunner

import (
	"errors"
	"strings"

	"github.com/opsi-dev/opsi/agent/internal/cloudrelay"
	"github.com/opsi-dev/opsi/agent/internal/deploy"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
)

func progressFromRollout(record deploymentv1.RolloutRecord, leaseToken string, percent int32, message string) deploymentv1.Progress {
	progress := deploymentv1.Progress{SchemaVersion: deploymentv1.EventSchemaVersion, LeaseToken: leaseToken, State: record.State, MessageRedacted: deploy.RedactSensitive(message), ProgressPercent: percent, RolloutID: record.Intent.RolloutID, IntentHash: record.Intent.IntentHash, StateHash: record.StateHash, WorkloadSpecHash: record.Intent.Desired.WorkloadSpecHash, ExposureSpecHash: record.Intent.Desired.ExposureSpecHash, DesiredDigest: record.Intent.Desired.Image.Digest, PreviousDigest: record.Intent.PreviousDigest, Resources: append([]deploymentv1.ResourceIdentity(nil), record.Resources...), Attempt: record.Intent.Attempt}
	if record.Error != nil {
		progress.FailureCode = record.Error.Code
	}
	if record.Evidence != nil {
		progress.ReadinessEvidenceHash, _ = record.Evidence.Hash()
	}
	if record.State == deploymentv1.RolloutStateSucceeded {
		progress.CurrentDigest = record.Intent.Desired.Image.Digest
	} else if record.State == deploymentv1.RolloutStateRolledBack {
		progress.CurrentDigest = record.Intent.PreviousDigest
	}
	return progress
}

func resultFromRollout(record deploymentv1.RolloutRecord, reconcileErr error, lease cloudrelay.DeploymentLease) cloudrelay.DeploymentResult {
	result := cloudrelay.DeploymentResult{SchemaVersion: deploymentv1.ResultSchemaVersion, LeaseToken: lease.LeaseToken, Status: record.State, IntentHash: record.Intent.IntentHash}
	agentResult := &deploymentv1.AgentResult{SchemaVersion: deploymentv1.ResultSchemaVersion, Status: record.State, RolloutID: record.Intent.RolloutID, RolloutState: record.State, IntentHash: record.Intent.IntentHash, StateHash: record.StateHash, SpecHash: record.Intent.Desired.WorkloadSpecHash, WorkloadSpecHash: record.Intent.Desired.WorkloadSpecHash, ExposureSpecHash: record.Intent.Desired.ExposureSpecHash, DesiredDigest: record.Intent.Desired.Image.Digest, PreviousDigest: record.Intent.PreviousDigest, Resources: append([]deploymentv1.ResourceIdentity(nil), record.Resources...), Attempt: record.Intent.Attempt}
	if record.Evidence != nil {
		agentResult.ReadinessEvidenceHash, _ = record.Evidence.Hash()
	}
	switch record.State {
	case deploymentv1.RolloutStateSucceeded:
		result.Status = deploymentv1.StateSucceeded
		agentResult.CurrentDigest = record.Intent.Desired.Image.Digest
		snapshot := deploymentv1.KnownGoodSnapshot{SchemaVersion: deploymentv1.KnownGoodSchemaVersion, ID: record.Intent.RolloutID, Target: record.Intent.Target, Runtime: record.Intent.Desired, Resources: record.Resources, EvidenceHash: agentResult.ReadinessEvidenceHash}
		if record.Evidence != nil {
			snapshot.VerifiedAt = record.Evidence.ObservedAt
		}
		if canonical, err := snapshot.Canonicalize(); err == nil {
			agentResult.KnownGoodID = canonical.ID
			agentResult.KnownGoodHash = canonical.SnapshotHash
		}
	case deploymentv1.RolloutStateRolledBack:
		result.Status = deploymentv1.RolloutStateRolledBack
		agentResult.CurrentDigest = record.Intent.PreviousDigest
		agentResult.KnownGoodID = record.Intent.PreviousKnownGoodID
		agentResult.KnownGoodHash = record.Intent.PreviousKnownGoodHash
	case deploymentv1.RolloutStateRollbackFailed:
		result.Status = deploymentv1.RolloutStateRollbackFailed
	default:
		result.Status = deploymentv1.StateFailed
	}
	if record.Error != nil {
		agentResult.FailureCode = record.Error.Code
		agentResult.FailureMessageRedacted = deploy.RedactSensitive(record.Error.Message)
	} else if reconcileErr != nil {
		agentResult.FailureCode = failureCode(reconcileErr)
		agentResult.FailureMessageRedacted = deploy.RedactSensitive(reconcileErr.Error())
	}
	result.FailureCode = agentResult.FailureCode
	result.FailureMessageRedacted = agentResult.FailureMessageRedacted
	result.RolloutResult = agentResult
	return result
}

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
