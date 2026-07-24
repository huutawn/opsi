package cloudrunner

import (
	"errors"

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

func resultFromRollout(intent deploymentv1.RolloutIntent, record deploymentv1.RolloutRecord, reconcileErr error, lease cloudrelay.DeploymentLease) cloudrelay.DeploymentResult {
	if record.Intent.RolloutID == "" {
		record = preMutationFailureRecord(intent, reconcileErr)
	}
	result := cloudrelay.DeploymentResult{SchemaVersion: deploymentv1.ResultSchemaVersion, LeaseToken: lease.LeaseToken, Status: record.State, IntentHash: record.Intent.IntentHash}
	agentResult := &deploymentv1.AgentResult{SchemaVersion: deploymentv1.ResultSchemaVersion, Status: record.State, RolloutID: record.Intent.RolloutID, RolloutState: record.State, IntentHash: record.Intent.IntentHash, StateHash: record.StateHash, SpecHash: record.Intent.Desired.WorkloadSpecHash, WorkloadSpecHash: record.Intent.Desired.WorkloadSpecHash, ExposureSpecHash: record.Intent.Desired.ExposureSpecHash, DesiredDigest: record.Intent.Desired.Image.Digest, PreviousDigest: record.Intent.PreviousDigest, Resources: append([]deploymentv1.ResourceIdentity(nil), record.Resources...), Attempt: record.Intent.Attempt}
	if record.Evidence != nil {
		agentResult.ReadinessEvidenceHash, _ = record.Evidence.Hash()
	}
	if record.Error != nil {
		failure := deploymentv1.NewRolloutError(record.Error.Code, deploy.RedactSensitive(record.Error.Message), record.Error.Retryable)
		agentResult.FailureCode = failure.Code
		agentResult.FailureMessageRedacted = failure.Message
	} else if reconcileErr != nil {
		failure := rolloutFailure(reconcileErr, deploymentv1.RolloutCodeRuntimeFailed)
		agentResult.FailureCode = failure.Code
		agentResult.FailureMessageRedacted = failure.Message
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
	case deploymentv1.RolloutStateFailed:
		result.Status = deploymentv1.StateFailed
		if agentResult.FailureCode != deploymentv1.RolloutCodeNoKnownGood {
			agentResult.CurrentDigest = record.Intent.PreviousDigest
			agentResult.KnownGoodID = record.Intent.PreviousKnownGoodID
			agentResult.KnownGoodHash = record.Intent.PreviousKnownGoodHash
			agentResult.ReadinessEvidenceHash = ""
			agentResult.Resources = nil
		}
	default:
		result.Status = deploymentv1.StateFailed
	}
	result.FailureCode = agentResult.FailureCode
	result.FailureMessageRedacted = agentResult.FailureMessageRedacted
	result.RolloutResult = agentResult
	return result
}

func preMutationFailureRecord(intent deploymentv1.RolloutIntent, err error) deploymentv1.RolloutRecord {
	failure := rolloutFailure(err, deploymentv1.RolloutCodePreflightFailed)
	terminalAt := intent.CreatedAt
	record := deploymentv1.RolloutRecord{SchemaVersion: deploymentv1.RolloutRecordVersion, Intent: intent, State: deploymentv1.RolloutStateFailed, Version: 1, Error: failure, CreatedAt: intent.CreatedAt, UpdatedAt: intent.CreatedAt, TerminalAt: &terminalAt}
	record.StateHash, _ = record.CalculateStateHash()
	return record
}

func rolloutFailure(err error, fallbackCode string) *deploymentv1.RolloutError {
	var typed *deploymentv1.RolloutError
	if errors.As(err, &typed) && typed.Code != "" {
		return deploymentv1.NewRolloutError(typed.Code, deploy.RedactSensitive(typed.Message), typed.Retryable)
	}
	message := "rollout reconciliation failed"
	if err != nil {
		message = err.Error()
	}
	return deploymentv1.NewRolloutError(fallbackCode, deploy.RedactSensitive(message), false)
}
