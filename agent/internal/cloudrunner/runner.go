package cloudrunner

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/opsi-dev/opsi/agent/internal/cloudrelay"
	"github.com/opsi-dev/opsi/agent/internal/deploy"
	"github.com/opsi-dev/opsi/agent/internal/nodelifecycle"
	backupv1 "github.com/opsi-dev/opsi/contracts/go/backupv1"
	cutoverv1 "github.com/opsi-dev/opsi/contracts/go/cutoverv1"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
	restorev1 "github.com/opsi-dev/opsi/contracts/go/restorev1"
)

type CloudClient interface {
	PollJob(context.Context, string, time.Duration) (*cloudrelay.JobLease, error)
	CompleteDeployment(context.Context, string, string, cloudrelay.DeploymentResult) error
	ProgressDeployment(context.Context, string, string, deploymentv1.Progress) error
	CompleteNodeLifecycle(context.Context, string, string, cloudrelay.NodeLifecycleResult) error
	CompleteManagedResource(context.Context, string, string, cloudrelay.ManagedResourceResult) error
	CompleteRetainedStorage(context.Context, string, string, cloudrelay.RetainedStorageResult) error
	CompleteBackup(context.Context, string, string, backupv1.Result) error
	Heartbeat(context.Context, string, cloudrelay.Heartbeat) error
}

type DeployEngine interface {
	ReconcileRollout(context.Context, deploymentv1.RolloutIntent, deploy.ProgressFunc) (deploymentv1.RolloutRecord, error)
	ReconcilePending(context.Context, deploy.ProgressFunc) ([]deploymentv1.RolloutRecord, error)
}

type RegistryPullSecretEnsurer interface {
	Ensure(context.Context, deploymentv1.AgentCommand) error
}

type WorkloadSecretEnsurer interface {
	Ensure(context.Context, deploymentv1.AgentCommand) error
}

type ConnectionState struct {
	connected atomic.Bool
}

func (s *ConnectionState) SetConnected(connected bool) {
	if s != nil {
		s.connected.Store(connected)
	}
}

func (s *ConnectionState) Connected() bool {
	return s != nil && s.connected.Load()
}

type Runner struct {
	Client              CloudClient
	Engine              DeployEngine
	RegistryPullSecrets RegistryPullSecretEnsurer
	WorkloadSecrets     WorkloadSecretEnsurer
	NodeLifecycle       NodeLifecycleExecutor
	ManagedResources    ManagedResourceReconciler
	Backups             BackupExecutor
	Restores            RestoreExecutor
	Cutovers            CutoverExecutor
	DepVerifier         DepVerificationExecutor
	BackupHeartbeat     time.Duration
	NodeID              string
	Version             string
	PollInterval        time.Duration
	LongPollWait        time.Duration
	HeartbeatInterval   time.Duration
	HealthProbe         HealthProbe
	ConnectionState     *ConnectionState
	Logger              *slog.Logger
}

type ManagedResourceReconciler interface {
	Reconcile(context.Context, cloudrelay.ManagedResourceLease) cloudrelay.ManagedResourceResult
	ReconcileRetainedStorage(context.Context, cloudrelay.RetainedStorageLease) cloudrelay.RetainedStorageResult
}

type BackupExecutor interface {
	Execute(context.Context, backupv1.Lease) backupv1.Result
}

type RestoreExecutor interface {
	Review(context.Context, restorev1.ReviewLease) restorev1.ReviewResult
	Execute(context.Context, restorev1.Lease) restorev1.Result
}

type CutoverExecutor interface {
	Review(context.Context, cutoverv1.ReviewLease) cutoverv1.ReviewResult
}

type restoreCloudClient interface {
	CompleteRestoreReview(context.Context, string, string, restorev1.ReviewResult) error
	CompleteRestore(context.Context, string, string, restorev1.Result) error
}

type cutoverCloudClient interface {
	CompleteCutoverReview(context.Context, string, string, cutoverv1.ReviewResult) error
}

type DepVerificationExecutor interface {
	Verify(context.Context, cloudrelay.DepVerificationLease) cloudrelay.DepVerificationResult
}

type depVerificationCloudClient interface {
	CompleteDepVerification(context.Context, string, string, cloudrelay.DepVerificationResult) error
}

type NodeLifecycleExecutor interface {
	Execute(context.Context, nodelifecycle.Request) nodelifecycle.Result
}

const rolloutReconcileAttempts = 2
const defaultBackupHeartbeat = time.Minute

func (r Runner) Run(ctx context.Context) error {
	if r.Client == nil || r.Engine == nil {
		return errors.New("cloud runner client and engine are required")
	}
	if r.PollInterval <= 0 {
		r.PollInterval = 2 * time.Second
	}
	if r.LongPollWait <= 0 {
		r.LongPollWait = 30 * time.Second
	}
	if r.HeartbeatInterval <= 0 {
		r.HeartbeatInterval = 30 * time.Second
	}
	recoveryCtx, cancelRecovery := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	if _, err := r.Engine.ReconcilePending(recoveryCtx, nil); err != nil {
		r.log().Warn("pending rollout reconciliation failed", "error", err)
	}
	if cleaner, ok := r.DepVerifier.(interface{ CleanupStaleProbes(context.Context) error }); ok {
		if err := cleaner.CleanupStaleProbes(recoveryCtx); err != nil {
			r.log().Warn("stale probe cleanup failed", "error", err)
		}
	}
	cancelRecovery()
	r.sendHeartbeat(ctx)
	go r.heartbeatLoop(ctx)
	return r.jobLoop(ctx)
}

func (r Runner) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(r.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.sendHeartbeat(ctx)
		}
	}
}

func (r Runner) sendHeartbeat(ctx context.Context) {
	health := ProbeRuntime(ctx, r.HealthProbe)
	err := r.Client.Heartbeat(ctx, r.NodeID, cloudrelay.Heartbeat{
		Version:      r.Version,
		NodeReady:    health.NodeReady,
		K3SStatus:    health.K3SStatus,
		Capacity:     health.Capacity,
		Capabilities: map[string]any{"deploy": health.NodeReady && r.Engine != nil, "node_lifecycle": r.NodeLifecycle != nil, "managed_resources": health.NodeReady && r.ManagedResources != nil, "postgres_logical_backup": health.NodeReady && r.Backups != nil, "postgres_logical_restore": health.NodeReady && r.Restores != nil, "dep_verification": health.NodeReady && r.DepVerifier != nil},
	})
	if err != nil {
		r.ConnectionState.SetConnected(false)
		r.log().Warn("cloud heartbeat failed", "error", err)
		return
	}
	r.ConnectionState.SetConnected(true)
}

func (r Runner) jobLoop(ctx context.Context) error {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
		lease, err := r.Client.PollJob(ctx, r.NodeID, r.LongPollWait)
		if err != nil {
			r.ConnectionState.SetConnected(false)
			r.log().Warn("cloud job poll failed", "error", err)
			timer.Reset(r.PollInterval)
			continue
		}
		r.ConnectionState.SetConnected(true)
		if lease != nil && lease.Deployment != nil {
			r.handleLease(ctx, *lease.Deployment)
		}
		if lease != nil && lease.NodeLifecycle != nil {
			r.handleNodeLifecycle(ctx, *lease.NodeLifecycle)
		}
		if lease != nil && lease.ManagedResource != nil {
			r.handleManagedResource(ctx, *lease.ManagedResource)
		}
		if lease != nil && lease.RetainedStorage != nil {
			r.handleRetainedStorage(ctx, *lease.RetainedStorage)
		}
		if lease != nil && lease.Backup != nil {
			r.handleBackup(ctx, *lease.Backup)
		}
		if lease != nil && lease.RestoreReview != nil {
			r.handleRestoreReview(ctx, *lease.RestoreReview)
		}
		if lease != nil && lease.Restore != nil {
			r.handleRestore(ctx, *lease.Restore)
		}
		if lease != nil && lease.CutoverReview != nil {
			r.handleCutoverReview(ctx, *lease.CutoverReview)
		}
		if lease != nil && lease.DepVerification != nil {
			r.handleDepVerification(ctx, *lease.DepVerification)
		}
		timer.Reset(r.PollInterval)
	}
}

func (r Runner) handleDepVerification(ctx context.Context, lease cloudrelay.DepVerificationLease) {
	result := cloudrelay.DepVerificationResult{
		ID:                   lease.ID,
		LeaseToken:           lease.LeaseToken,
		ConnectionStatus:     "NOT_SUPPORTED",
		ConsumerHealthStatus: "UNHEALTHY",
		AssertionStatus:      "NOT_CONFIGURED",
		FailureCode:          "DEP_VERIFIER_UNAVAILABLE",
		FailureMessage:       "dependency verifier is not configured",
	}
	if r.DepVerifier != nil {
		result = r.DepVerifier.Verify(ctx, lease)
	}
	depClient, ok := r.Client.(depVerificationCloudClient)
	if !ok {
		return
	}
	for attempt := 0; attempt < 3; attempt++ {
		err := depClient.CompleteDepVerification(ctx, r.NodeID, lease.ID, result)
		if err == nil {
			return
		}
		r.log().Warn("cloud dep verification result report failed", "lease_id", lease.ID, "attempt", attempt+1, "error", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(attempt+1) * time.Second):
		}
	}
}

func (r Runner) handleCutoverReview(ctx context.Context, lease cutoverv1.ReviewLease) {
	client, ok := r.Client.(cutoverCloudClient)
	if !ok {
		return
	}
	result := cutoverv1.ReviewResult{
		Status:                 cutoverv1.ReviewFailed,
		LeaseToken:             lease.LeaseToken,
		FailureCode:            cutoverv1.FailureTargetInvalid,
		FailureMessageRedacted: "cutover executor is unavailable",
	}
	if r.Cutovers != nil {
		result = r.Cutovers.Review(ctx, lease)
	}
	for attempt := 0; attempt < 3; attempt++ {
		if err := client.CompleteCutoverReview(ctx, r.NodeID, lease.Review.ID, result); err == nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(attempt+1) * time.Second):
		}
	}
}

func (r Runner) handleRestoreReview(ctx context.Context, lease restorev1.ReviewLease) {
	client, ok := r.Client.(restoreCloudClient)
	if !ok {
		return
	}
	result := restorev1.ReviewResult{Status: restorev1.ReviewFailed, LeaseToken: lease.LeaseToken, FailureCode: restorev1.FailureTargetStateUnknown, FailureMessageRedacted: "restore executor is unavailable"}
	if r.Restores != nil {
		result = r.Restores.Review(ctx, lease)
	}
	for attempt := 0; attempt < 3; attempt++ {
		if err := client.CompleteRestoreReview(ctx, r.NodeID, lease.Review.ID, result); err == nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(attempt+1) * time.Second):
		}
	}
}

func (r Runner) handleRestore(ctx context.Context, lease restorev1.Lease) {
	client, ok := r.Client.(restoreCloudClient)
	if !ok {
		return
	}
	if err := client.CompleteRestore(ctx, r.NodeID, lease.Restore.ID, restorev1.Result{Status: restorev1.LifecycleRunning, LeaseToken: lease.LeaseToken}); err != nil {
		return
	}
	executionCtx, cancel := context.WithCancel(ctx)
	heartbeat := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(defaultBackupHeartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-executionCtx.Done():
				heartbeat <- nil
				return
			case <-ticker.C:
				if err := client.CompleteRestore(executionCtx, r.NodeID, lease.Restore.ID, restorev1.Result{Status: restorev1.LifecycleRunning, LeaseToken: lease.LeaseToken}); err != nil {
					if executionCtx.Err() != nil {
						heartbeat <- nil
					} else {
						heartbeat <- err
						cancel()
					}
					return
				}
			}
		}
	}()
	result := restorev1.Result{Status: restorev1.LifecycleFailed, LeaseToken: lease.LeaseToken, FailureCode: restorev1.FailureExecution, FailureMessageRedacted: "restore executor is unavailable"}
	if r.Restores != nil {
		result = r.Restores.Execute(executionCtx, lease)
	}
	cancel()
	if err := <-heartbeat; err != nil {
		return
	}
	for attempt := 0; attempt < 3; attempt++ {
		if err := client.CompleteRestore(ctx, r.NodeID, lease.Restore.ID, result); err == nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(attempt+1) * time.Second):
		}
	}
}

func (r Runner) handleBackup(ctx context.Context, lease backupv1.Lease) {
	if err := r.Client.CompleteBackup(ctx, r.NodeID, lease.Backup.ID, backupv1.Result{Status: backupv1.LifecycleRunning, LeaseToken: lease.LeaseToken}); err != nil {
		return
	}
	executionCtx, cancel := context.WithCancel(ctx)
	heartbeat := make(chan error, 1)
	go func() {
		interval := r.BackupHeartbeat
		if interval <= 0 {
			interval = defaultBackupHeartbeat
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-executionCtx.Done():
				heartbeat <- nil
				return
			case <-ticker.C:
				if err := r.Client.CompleteBackup(executionCtx, r.NodeID, lease.Backup.ID, backupv1.Result{Status: backupv1.LifecycleRunning, LeaseToken: lease.LeaseToken}); err != nil {
					if executionCtx.Err() != nil {
						heartbeat <- nil
						return
					}
					heartbeat <- err
					cancel()
					return
				}
			}
		}
	}()
	result := backupv1.Result{Status: backupv1.LifecycleFailed, LeaseToken: lease.LeaseToken, FailureCode: backupv1.FailureDumpFailed, FailureMessageRedacted: "backup executor is unavailable"}
	if r.Backups != nil {
		result = r.Backups.Execute(executionCtx, lease)
	}
	cancel()
	if err := <-heartbeat; err != nil {
		return
	}
	for attempt := 0; attempt < 3; attempt++ {
		if err := r.Client.CompleteBackup(ctx, r.NodeID, lease.Backup.ID, result); err == nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(attempt+1) * time.Second):
		}
	}
}

func (r Runner) handleRetainedStorage(ctx context.Context, lease cloudrelay.RetainedStorageLease) {
	result := cloudrelay.RetainedStorageResult{Status: "failed", LeaseToken: lease.LeaseToken, FailureCode: resourcev1.FailureStorageDestroyFailed, FailureMessageRedacted: "retained storage reconciler is unavailable"}
	if r.ManagedResources != nil {
		result = r.ManagedResources.ReconcileRetainedStorage(ctx, lease)
	}
	for attempt := 0; attempt < 3; attempt++ {
		if err := r.Client.CompleteRetainedStorage(ctx, r.NodeID, lease.Spec.RetainedStorageID, result); err == nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(attempt+1) * time.Second):
		}
	}
}

func (r Runner) handleManagedResource(ctx context.Context, lease cloudrelay.ManagedResourceLease) {
	result := cloudrelay.ManagedResourceResult{Status: "failed", LeaseToken: lease.LeaseToken, FailureCode: "MANAGED_RESOURCE_APPLY_FAILED", FailureMessageRedacted: "managed resource reconciler is unavailable"}
	if r.ManagedResources != nil {
		result = r.ManagedResources.Reconcile(ctx, lease)
	}
	for attempt := 0; attempt < 3; attempt++ {
		if err := r.Client.CompleteManagedResource(ctx, r.NodeID, lease.Spec.ResourceID, result); err == nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(attempt+1) * time.Second):
		}
	}
}

func (r Runner) handleNodeLifecycle(ctx context.Context, lease cloudrelay.NodeLifecycleLease) {
	result := r.executeNodeLifecycle(ctx, lease)
	for attempt := 0; attempt < 3; attempt++ {
		err := r.Client.CompleteNodeLifecycle(ctx, r.NodeID, lease.ID, result)
		if err == nil {
			return
		}
		r.log().Warn("cloud node lifecycle result report failed", "job_id", lease.ID, "attempt", attempt+1, "error", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(attempt+1) * time.Second):
		}
	}
}

func (r Runner) handleLease(ctx context.Context, lease cloudrelay.DeploymentLease) {
	result, terminal := r.execute(ctx, lease)
	if !terminal {
		return
	}
	for attempt := 0; attempt < 3; attempt++ {
		err := r.Client.CompleteDeployment(ctx, r.NodeID, lease.Deployment.ID, result)
		if err == nil {
			return
		}
		r.log().Warn("cloud deployment result report failed", "deployment_id", lease.Deployment.ID, "attempt", attempt+1, "error", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(attempt+1) * time.Second):
		}
	}
}

func (r Runner) executeNodeLifecycle(ctx context.Context, lease cloudrelay.NodeLifecycleLease) cloudrelay.NodeLifecycleResult {
	if r.NodeLifecycle == nil {
		return cloudrelay.NodeLifecycleResult{Status: "unsupported", LeaseToken: lease.LeaseToken, FailureCode: "NODE_LIFECYCLE_UNSUPPORTED", FailureMessageRedacted: "node lifecycle executor is not configured"}
	}
	result := r.NodeLifecycle.Execute(ctx, nodelifecycle.Request{
		Action:         lease.Action,
		TargetNodeID:   lease.TargetNodeID,
		TargetNodeName: lease.TargetName,
		ConfirmRemove:  lease.ConfirmRemove,
	})
	return cloudrelay.NodeLifecycleResult{Status: result.Status, LeaseToken: lease.LeaseToken, FailureCode: result.FailureCode, FailureMessageRedacted: result.FailureMessageRedacted, Verified: result.Verified}
}

func (r Runner) execute(ctx context.Context, lease cloudrelay.DeploymentLease) (cloudrelay.DeploymentResult, bool) {
	if lease.Command == nil || lease.Command.Rollout == nil {
		return deploymentFailure(lease, "LEGACY_DEPLOYMENT_RETIRED", "deployment commands without RolloutIntent are retired"), true
	}
	return r.executeRollout(ctx, lease)
}

func (r Runner) executeRollout(ctx context.Context, lease cloudrelay.DeploymentLease) (cloudrelay.DeploymentResult, bool) {
	intent, err := RolloutIntentFromLease(lease, r.NodeID)
	if err != nil {
		return deploymentFailure(lease, "ROLLOUT_COMMAND_INVALID", err.Error()), true
	}
	if lease.Command.Workload.RegistryPullCredential != nil {
		if r.RegistryPullSecrets == nil {
			return deploymentFailure(lease, deploymentv1.RolloutCodeRegistryCredentialUnavailable, "registry pull secret delivery is unavailable"), true
		}
		if err := r.RegistryPullSecrets.Ensure(ctx, *lease.Command); err != nil {
			failure := rolloutFailure(err, deploymentv1.RolloutCodeRegistryCredentialUnavailable)
			return deploymentFailure(lease, failure.Code, failure.Message), true
		}
	}
	if len(lease.Command.Workload.SecretReferences) > 0 {
		if r.WorkloadSecrets == nil {
			return deploymentFailure(lease, resourcev1.FailureBindingSecretMaterialization, "workload secret delivery is unavailable"), true
		}
		if err := r.WorkloadSecrets.Ensure(ctx, *lease.Command); err != nil {
			return deploymentFailure(lease, resourcev1.FailureBindingSecretMaterialization, err.Error()), true
		}
	}
	var progressMu sync.Mutex
	var latest *deploymentv1.Progress
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				progressMu.Lock()
				if latest != nil {
					copy := *latest
					progressMu.Unlock()
					if err := r.Client.ProgressDeployment(ctx, r.NodeID, lease.Deployment.ID, copy); err != nil {
						r.log().Warn("cloud rollout lease heartbeat failed", "deployment_id", lease.Deployment.ID, "error", err)
					}
				} else {
					progressMu.Unlock()
				}
			}
		}
	}()
	defer close(done)
	for attempt := 0; attempt < rolloutReconcileAttempts; attempt++ {
		record, reconcileErr := r.Engine.ReconcileRollout(ctx, intent, func(event *deploy.ProgressEvent) error {
			if event == nil || event.Rollout == nil {
				return nil
			}
			progress := progressFromRollout(*event.Rollout, lease.LeaseToken, event.Percent, event.Message)
			progressMu.Lock()
			latest = &progress
			progressMu.Unlock()
			if err := r.Client.ProgressDeployment(ctx, r.NodeID, lease.Deployment.ID, progress); err != nil {
				r.log().Warn("cloud rollout progress report failed", "deployment_id", lease.Deployment.ID, "state", progress.State, "error", err)
			}
			return nil
		})
		if intent.Operation == deploymentv1.RolloutOperationCleanup && record.State == deploymentv1.RolloutStateCleaned && record.TerminalAt != nil {
			rolloutResult := &deploymentv1.AgentResult{SchemaVersion: deploymentv1.ResultSchemaVersion, Status: deploymentv1.RolloutStateCleaned, Namespace: intent.Desired.Preview.Namespace, RolloutID: intent.RolloutID, RolloutState: record.State, IntentHash: intent.IntentHash, StateHash: record.StateHash, WorkloadSpecHash: intent.Desired.WorkloadSpecHash, ExposureSpecHash: intent.Desired.ExposureSpecHash, DesiredDigest: intent.Desired.Image.Digest, PreviousDigest: intent.PreviousDigest, Attempt: intent.Attempt}
			return cloudrelay.DeploymentResult{SchemaVersion: deploymentv1.ResultSchemaVersion, Status: deploymentv1.RolloutStateCleaned, LeaseToken: lease.LeaseToken, SpecHash: intent.Desired.WorkloadSpecHash, ApplicationImage: intent.Desired.Image.Reference, Namespace: intent.Desired.Preview.Namespace, RolloutResult: rolloutResult}, true
		}
		if result, terminal := resultFromRollout(intent, record, reconcileErr, lease); terminal {
			return result, true
		}
		if ctx.Err() != nil {
			break
		}
	}
	return cloudrelay.DeploymentResult{}, false
}

func deploymentFailure(lease cloudrelay.DeploymentLease, code, message string) cloudrelay.DeploymentResult {
	result := cloudrelay.DeploymentResult{Status: "failed", LeaseToken: lease.LeaseToken, FailureCode: code, FailureMessageRedacted: deploy.RedactSensitive(message), RollbackEligible: false}
	if lease.Command != nil {
		result.SchemaVersion = deploymentv1.ResultSchemaVersion
		result.SpecHash = lease.Command.SpecHash
		result.ApplicationImage = lease.Command.Image.Reference
	}
	return result
}

func (r Runner) log() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.Default()
}
