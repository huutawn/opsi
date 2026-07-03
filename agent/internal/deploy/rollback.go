package deploy

import "strings"

const (
	FailTypeDeployTimeFail        = "deploy_time_fail"
	FailTypeRuntimeCrash          = "runtime_crash"
	FailTypeResourceExhaustion    = "resource_exhaustion"
	FailTypeExternalDependency    = "external_dependency_fail"
	FailTypeUnknown               = "unknown"
	ActionAutoRollback            = "auto_rollback"
	ActionHumanApproveRollback    = "human_approve_rollback"
	ActionScaleThenAlert          = "scale_then_alert_p1"
	ActionCheckExternalDependency = "check_external_dependency"
)

type RollbackDecision struct {
	FailType        string
	RollbackSafe    bool
	Reason          string
	SuggestedAction string
}

type RolloutFailure struct {
	Reason              string
	ReadinessEverPassed bool
	MinutesAfterReady   int
	ContainerReasons    []string
	Events              []string
}

func (e RolloutFailure) Error() string {
	return strings.TrimSpace(strings.Join(append([]string{e.Reason}, append(e.ContainerReasons, e.Events...)...), " "))
}

func ClassifyRolloutFailure(f RolloutFailure) RollbackDecision {
	return ClassifyFailure(f.Error(), f.ReadinessEverPassed, f.MinutesAfterReady)
}

func ClassifyFailure(message string, readinessPassed bool, minutesAfterSuccess int) RollbackDecision {
	lower := strings.ToLower(message)
	if strings.Contains(lower, "oom") || strings.Contains(lower, "cpu throttle") || strings.Contains(lower, "resource") {
		return RollbackDecision{FailType: FailTypeResourceExhaustion, RollbackSafe: false, Reason: "resource exhaustion: scale before rollback", SuggestedAction: ActionScaleThenAlert}
	}
	if strings.Contains(lower, "redis") || strings.Contains(lower, "database") || strings.Contains(lower, "db") || strings.Contains(lower, "connection refused") {
		return RollbackDecision{FailType: FailTypeExternalDependency, RollbackSafe: false, Reason: "external dependency failure: rollback is unlikely to help", SuggestedAction: ActionCheckExternalDependency}
	}
	if readinessPassed || minutesAfterSuccess >= 5 || strings.Contains(lower, "crashloop") || strings.Contains(lower, "runtime") {
		return RollbackDecision{FailType: FailTypeRuntimeCrash, RollbackSafe: false, Reason: "runtime crash after successful deploy: human approval required", SuggestedAction: ActionHumanApproveRollback}
	}
	return RollbackDecision{FailType: FailTypeDeployTimeFail, RollbackSafe: true, Reason: "deploy-time fail: readiness never passed during rollout", SuggestedAction: ActionAutoRollback}
}
