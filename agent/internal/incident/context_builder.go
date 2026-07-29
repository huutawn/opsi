package incident

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/opsi-dev/opsi/agent/internal/telemetry"
	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
)

type IncidentContextBuilder struct {
	Store      interface{}
	Rollouts   rolloutEvidenceSource
	Kubernetes kubernetesEvidenceSource
	Window     time.Duration
	Now        func() time.Time
}

func (b IncidentContextBuilder) Build(ctx context.Context, record telemetry.IncidentRecord) (*agentv1.IncidentEvidence, error) {
	generatedAt := time.Now().UTC()
	if b.Now != nil {
		generatedAt = b.Now().UTC()
	}
	window := b.Window
	if window <= 0 {
		window = 5 * time.Minute
	}
	if window > MaxEvidenceSourceWindow {
		window = MaxEvidenceSourceWindow
	}
	center := record.CreatedAt.UTC()
	if center.IsZero() {
		center = time.Unix(0, 0).UTC()
	}
	since, until := center.Add(-window), center.Add(window)
	evidence := &agentv1.IncidentEvidence{
		SchemaVersion: IncidentEvidenceSchemaVersion,
		Identity: agentv1.IncidentEvidenceIdentity{
			IncidentID: safeEvidenceText(record.ID, 256), ProjectID: safeEvidenceText(record.ProjectID, 256),
			ServiceID: safeEvidenceText(record.ServiceID, 256), NodeID: safeEvidenceText(record.NodeID, 256), PodID: safeEvidenceText(record.PodID, 256),
			AnomalyType: safeEvidenceText(record.AnomalyType, 256), Severity: safeEvidenceText(record.Severity, 64), Status: safeEvidenceText(record.Status, 64),
		},
		GeneratedAtUnix: generatedAt.Unix(), ObservationWindow: agentv1.IncidentEvidenceWindow{StartUnix: since.Unix(), EndUnix: until.Unix()},
		Coverage: []agentv1.IncidentSourceCoverage{{Source: "incident", Status: "available", ItemCount: 1}},
		Timeline: []agentv1.IncidentTimelineEntry{{ObservedAtUnix: center.Unix(), Source: "incident", Kind: "incident_created", Detail: safeEvidenceText(record.AnomalyType, 256)}},
	}
	readLegacyDeploymentFacts(record.ContextJSON, &evidence.Deployment)
	b.readTelemetry(ctx, record, since, until, evidence)
	b.readRollout(ctx, record, since, until, evidence)
	b.readKubernetes(ctx, record, since, until, evidence)
	b.readAudit(ctx, record, since, until, evidence)
	canonicalizeEvidence(evidence)
	evidence.Timeline = limitEvidenceSection(evidence.Timeline, MaxTimelineEntries, "timeline", evidence)
	evidence.KubernetesEvents = limitEvidenceSection(evidence.KubernetesEvents, MaxKubernetesEventEntries, "kubernetes", evidence)
	evidence.LogFingerprints = limitEvidenceSection(evidence.LogFingerprints, MaxLogFingerprintGroups, "telemetry", evidence)
	evidence.AuditReferences = limitEvidenceSection(evidence.AuditReferences, MaxAuditReferences, "audit", evidence)
	canonicalizeEvidence(evidence)
	if _, err := EncodeIncidentEvidence(evidence); err != nil {
		return nil, err
	}
	return evidence, nil
}

func (b IncidentContextBuilder) readTelemetry(ctx context.Context, record telemetry.IncidentRecord, since, until time.Time, evidence *agentv1.IncidentEvidence) {
	source, ok := b.Store.(telemetryEvidenceSource)
	if !ok {
		evidence.Coverage = append(evidence.Coverage, unavailableCoverage("telemetry", "TELEMETRY_SOURCE_UNAVAILABLE"))
		return
	}
	projection, err := source.IncidentEvidenceTelemetry(ctx, record.ProjectID, record.NodeID, record.ServiceID, record.PodID, since, until, MaxLogFingerprintGroups+1)
	if err != nil {
		evidence.Coverage = append(evidence.Coverage, unavailableCoverage("telemetry", "TELEMETRY_QUERY_FAILED"))
		return
	}
	for _, group := range projection.LogGroups {
		evidence.LogFingerprints = append(evidence.LogFingerprints, agentv1.IncidentLogFingerprint{Fingerprint: safeEvidenceText(group.Fingerprint, 128), Level: safeEvidenceText(group.Level, 32), Count: group.Count, FirstObservedUnix: group.FirstAt.Unix(), LastObservedUnix: group.LastAt.Unix(), Excerpt: safeEvidenceText(group.Excerpt, MaxRedactedExcerptBytes), UntrustedContent: true})
	}
	for _, pod := range projection.Pods {
		ready := int32(0)
		if pod.Ready {
			ready = 1
		}
		evidence.Pods = append(evidence.Pods, agentv1.IncidentPodEvidence{PodID: safeEvidenceText(pod.PodID, 256), NodeID: safeEvidenceText(pod.NodeID, 256), ReadyContainers: ready, TotalContainers: 1, RestartCount: pod.RestartCount})
	}
	coverage := agentv1.IncidentSourceCoverage{Source: "telemetry", Status: "available", ItemCount: int32(projection.TotalLogGroups + projection.TotalPods)}
	if projection.TotalLogGroups > MaxLogFingerprintGroups {
		evidence.LogFingerprints = evidence.LogFingerprints[:MaxLogFingerprintGroups]
		evidence.Truncations = append(evidence.Truncations, agentv1.IncidentTruncation{Section: "telemetry", OmittedItems: int32(projection.TotalLogGroups - MaxLogFingerprintGroups), UTF8Safe: true})
		coverage.Status, coverage.Truncated = "truncated", true
	}
	if projection.TotalPods > maxEvidencePodEntries {
		evidence.Pods = evidence.Pods[:maxEvidencePodEntries]
		evidence.Truncations = append(evidence.Truncations, agentv1.IncidentTruncation{Section: "pods", OmittedItems: int32(projection.TotalPods - maxEvidencePodEntries), UTF8Safe: true})
		coverage.Status, coverage.Truncated = "truncated", true
	}
	evidence.Coverage = append(evidence.Coverage, coverage)
}

func (b IncidentContextBuilder) readRollout(ctx context.Context, record telemetry.IncidentRecord, since, until time.Time, evidence *agentv1.IncidentEvidence) {
	if b.Rollouts == nil {
		evidence.Coverage = append(evidence.Coverage, unavailableCoverage("rollout", "ROLLOUT_SOURCE_UNAVAILABLE"))
		return
	}
	projection, err := b.Rollouts.ReadIncidentEvidence(ctx, record.ProjectID, record.ServiceID, since, until)
	if err != nil {
		evidence.Coverage = append(evidence.Coverage, unavailableCoverage("rollout", "ROLLOUT_QUERY_FAILED"))
		return
	}
	if projection == nil {
		evidence.Coverage = append(evidence.Coverage, unavailableCoverage("rollout", "ROLLOUT_NOT_FOUND"))
		return
	}
	evidence.Deployment.DesiredDigest = safeEvidenceText(projection.DesiredDigest, 256)
	evidence.Deployment.PreviousDigest = safeEvidenceText(projection.PreviousDigest, 256)
	evidence.Deployment.RestoredDigest = safeEvidenceText(projection.RestoredDigest, 256)
	evidence.Rollout.RolloutID = safeEvidenceText(projection.RolloutID, 256)
	evidence.Rollout.State = safeEvidenceText(projection.State, 64)
	evidence.Rollout.FailureCode = safeEvidenceText(projection.FailureCode, 128)
	evidence.Rollout.ReadinessHash = safeEvidenceText(projection.ReadinessHash, 128)
	for _, event := range projection.Events {
		correlation := fmt.Sprintf("%s/%d/%s", projection.RolloutID, event.Version, event.StateHash)
		evidence.Rollout.EventCorrelation = append(evidence.Rollout.EventCorrelation, safeEvidenceText(correlation, 512))
		evidence.Timeline = append(evidence.Timeline, agentv1.IncidentTimelineEntry{ObservedAtUnix: event.CreatedAt.Unix(), Source: "rollout", Kind: safeEvidenceText(event.State, 64), Detail: safeEvidenceText(event.StateHash, 128)})
	}
	sort.Strings(evidence.Rollout.EventCorrelation)
	coverage := agentv1.IncidentSourceCoverage{Source: "rollout", Status: "available", ItemCount: int32(projection.TotalEvents + 1)}
	if projection.TotalEvents > MaxTimelineEntries {
		evidence.Rollout.EventCorrelation = evidence.Rollout.EventCorrelation[:MaxTimelineEntries]
		evidence.Truncations = append(evidence.Truncations, agentv1.IncidentTruncation{Section: "rollout", OmittedItems: int32(projection.TotalEvents - MaxTimelineEntries), UTF8Safe: true})
		coverage.Status, coverage.Truncated = "truncated", true
	}
	evidence.Coverage = append(evidence.Coverage, coverage)
}

func (b IncidentContextBuilder) readKubernetes(ctx context.Context, record telemetry.IncidentRecord, since, until time.Time, evidence *agentv1.IncidentEvidence) {
	if b.Kubernetes == nil {
		evidence.Coverage = append(evidence.Coverage, unavailableCoverage("kubernetes", "KUBERNETES_SOURCE_UNAVAILABLE"))
		return
	}
	result, err := b.Kubernetes.Read(ctx, record.ProjectID, record.ServiceID, record.NodeID, record.PodID)
	if result.ObservedDigest != "" {
		evidence.Deployment.ObservedDigest = safeEvidenceText(result.ObservedDigest, 256)
	}
	if len(result.Pods) > 0 {
		pods := make([]agentv1.IncidentPodEvidence, 0, len(result.Pods))
		for _, pod := range result.Pods {
			pod.Namespace = safeEvidenceText(pod.Namespace, 256)
			pod.PodID = safeEvidenceText(pod.PodID, 256)
			pod.NodeID = safeEvidenceText(pod.NodeID, 256)
			pod.ObservedDigest = safeEvidenceText(pod.ObservedDigest, 256)
			pods = append(pods, pod)
		}
		evidence.Pods = mergePods(evidence.Pods, pods)
	}
	for _, event := range result.Events {
		if event.ObservedAtUnix < since.Unix() || event.ObservedAtUnix > until.Unix() {
			continue
		}
		event.Namespace = safeEvidenceText(event.Namespace, 256)
		event.ObjectKind = safeEvidenceText(event.ObjectKind, 128)
		event.ObjectName = safeEvidenceText(event.ObjectName, 256)
		event.Type = safeEvidenceText(event.Type, 64)
		event.Reason = safeEvidenceText(event.Reason, 256)
		event.Message = safeEvidenceText(event.Message, MaxRedactedExcerptBytes)
		event.UntrustedContent = true
		evidence.KubernetesEvents = append(evidence.KubernetesEvents, event)
		evidence.Timeline = append(evidence.Timeline, agentv1.IncidentTimelineEntry{ObservedAtUnix: event.ObservedAtUnix, Source: "kubernetes", Kind: event.Reason, Detail: event.Message, UntrustedContent: true})
	}
	if err != nil {
		evidence.Coverage = append(evidence.Coverage, agentv1.IncidentSourceCoverage{Source: "kubernetes", Status: "unavailable", ReasonCode: "KUBERNETES_QUERY_FAILED", ItemCount: int32(len(result.Pods) + len(result.Events))})
		return
	}
	status := firstNonEmptyEvidence(result.CoverageStatus, "available")
	evidence.Coverage = append(evidence.Coverage, agentv1.IncidentSourceCoverage{Source: "kubernetes", Status: status, ReasonCode: result.ReasonCode, ItemCount: int32(len(result.Pods) + len(result.Events)), Truncated: status == "partial"})
}

func (b IncidentContextBuilder) readAudit(ctx context.Context, record telemetry.IncidentRecord, since, until time.Time, evidence *agentv1.IncidentEvidence) {
	source, ok := b.Store.(auditEvidenceSource)
	if !ok {
		evidence.Coverage = append(evidence.Coverage, unavailableCoverage("audit", "AUDIT_SOURCE_UNAVAILABLE"))
		return
	}
	records, total, err := source.EvidenceAuditRecords(ctx, record.ProjectID, record.ID, record.ServiceID, since, until, MaxAuditReferences+1)
	if err != nil {
		evidence.Coverage = append(evidence.Coverage, unavailableCoverage("audit", "AUDIT_QUERY_FAILED"))
		return
	}
	for _, item := range records {
		evidence.AuditReferences = append(evidence.AuditReferences, agentv1.IncidentAuditReference{
			AuditID: safeEvidenceText(item.ID, 256), Action: safeEvidenceText(item.Action, 256), ResourceType: safeEvidenceText(item.ResourceType, 128),
			ResourceID: safeEvidenceText(item.ResourceID, 256), Result: safeEvidenceText(item.Result, 64), CreatedAtUnix: item.CreatedAt.Unix(),
		})
	}
	coverage := agentv1.IncidentSourceCoverage{Source: "audit", Status: "available", ItemCount: int32(total)}
	if total > MaxAuditReferences {
		evidence.AuditReferences = evidence.AuditReferences[:MaxAuditReferences]
		evidence.Truncations = append(evidence.Truncations, agentv1.IncidentTruncation{Section: "audit", OmittedItems: int32(total - MaxAuditReferences), UTF8Safe: true})
		coverage.Status, coverage.Truncated = "truncated", true
	}
	evidence.Coverage = append(evidence.Coverage, coverage)
}

func unavailableCoverage(source, reason string) agentv1.IncidentSourceCoverage {
	return agentv1.IncidentSourceCoverage{Source: source, Status: "unavailable", ReasonCode: reason}
}

func readLegacyDeploymentFacts(raw string, target *agentv1.IncidentDeploymentEvidence) {
	var facts struct {
		DesiredDigest  string `json:"desired_digest"`
		PreviousDigest string `json:"previous_digest"`
		ObservedDigest string `json:"observed_digest"`
		RestoredDigest string `json:"restored_digest"`
	}
	if json.Unmarshal([]byte(raw), &facts) != nil {
		return
	}
	target.DesiredDigest = safeEvidenceText(facts.DesiredDigest, 256)
	target.PreviousDigest = safeEvidenceText(facts.PreviousDigest, 256)
	target.ObservedDigest = safeEvidenceText(facts.ObservedDigest, 256)
	target.RestoredDigest = safeEvidenceText(facts.RestoredDigest, 256)
}

func mergePods(existing, observed []agentv1.IncidentPodEvidence) []agentv1.IncidentPodEvidence {
	byID := map[string]agentv1.IncidentPodEvidence{}
	for _, pod := range existing {
		byID[pod.Namespace+"\x00"+pod.PodID] = pod
	}
	for _, pod := range observed {
		byID[pod.Namespace+"\x00"+pod.PodID] = pod
	}
	result := make([]agentv1.IncidentPodEvidence, 0, len(byID))
	for _, pod := range byID {
		result = append(result, pod)
	}
	return result
}
