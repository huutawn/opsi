package incident

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"sort"
	"strings"
	"time"

	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
)

const maxKubernetesCommandOutput = 1 << 20

type KubernetesEvidenceRunner interface {
	RunKubectlGet(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error)
}

type ExecKubernetesEvidenceRunner struct{}

func (ExecKubernetesEvidenceRunner) RunKubectlGet(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	stdout, stderr := &boundedBuffer{limit: maxKubernetesCommandOutput}, &boundedBuffer{limit: maxKubernetesCommandOutput}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	if stdout.overflow || stderr.overflow {
		return nil, nil, errors.New("kubectl output exceeded the evidence bound")
	}
	return stdout.Bytes(), stderr.Bytes(), err
}

type KubernetesEvidenceSource struct {
	KubectlPath string
	Runner      KubernetesEvidenceRunner
}

type KubernetesEvidenceResult struct {
	Pods           []agentv1.IncidentPodEvidence
	Events         []agentv1.IncidentKubernetesEvent
	ObservedDigest string
	CoverageStatus string
	ReasonCode     string
}

type kubernetesMetadata struct {
	Name              string            `json:"name"`
	Namespace         string            `json:"namespace"`
	CreationTimestamp string            `json:"creationTimestamp"`
	Labels            map[string]string `json:"labels"`
	OwnerReferences   []struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
	} `json:"ownerReferences"`
}

type kubernetesResource struct {
	Metadata kubernetesMetadata `json:"metadata"`
}

type kubernetesPod struct {
	Metadata kubernetesMetadata `json:"metadata"`
	Spec     struct {
		NodeName string `json:"nodeName"`
	} `json:"spec"`
	Status struct {
		ContainerStatuses []struct {
			Name         string `json:"name"`
			Ready        bool   `json:"ready"`
			RestartCount int32  `json:"restartCount"`
			ImageID      string `json:"imageID"`
		} `json:"containerStatuses"`
	} `json:"status"`
}

type kubernetesEvent struct {
	Metadata       kubernetesMetadata `json:"metadata"`
	InvolvedObject struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
	} `json:"involvedObject"`
	Type          string `json:"type"`
	Reason        string `json:"reason"`
	Message       string `json:"message"`
	EventTime     string `json:"eventTime"`
	LastTimestamp string `json:"lastTimestamp"`
}

func (s KubernetesEvidenceSource) Read(ctx context.Context, projectID, serviceKey, nodeID, podID string) (KubernetesEvidenceResult, error) {
	var podsPayload struct {
		Items []kubernetesPod `json:"items"`
	}
	podsJSON, _, err := s.run(ctx, "get", "pods", "-A", "-o", "json")
	if err != nil {
		return KubernetesEvidenceResult{}, errors.New("kubernetes pods unavailable")
	}
	if err := decodeBoundedJSON(podsJSON, &podsPayload); err != nil {
		return KubernetesEvidenceResult{}, errors.New("kubernetes pods response is invalid")
	}
	result := KubernetesEvidenceResult{}
	resources := map[string][]kubernetesResource{}
	for _, resource := range []string{"deployments", "replicasets", "services"} {
		var payload struct {
			Items []kubernetesResource `json:"items"`
		}
		data, _, runErr := s.run(ctx, "get", resource, "-A", "-o", "json")
		if runErr != nil {
			markKubernetesPartial(&result, "KUBERNETES_RESOURCE_QUERY_PARTIAL")
			continue
		}
		if err := decodeBoundedJSON(data, &payload); err != nil {
			return result, errors.New("kubernetes resource response is invalid")
		}
		resources[resource] = payload.Items
	}
	identities, missingServiceKey := kubernetesResourceIdentities(projectID, serviceKey, nodeID, podID, podsPayload.Items, resources)
	if missingServiceKey {
		markKubernetesPartial(&result, "SERVICE_KEY_LABEL_MISSING")
	}

	applicationDigests := map[string]bool{}
	digestIncomplete := false
	selectedPodCount := 0
	for _, item := range podsPayload.Items {
		if !identities[resourceIdentity(item.Metadata.Namespace, "Pod", item.Metadata.Name)] {
			continue
		}
		selectedPodCount++
		pod := agentv1.IncidentPodEvidence{Namespace: safeEvidenceText(item.Metadata.Namespace, 256), PodID: safeEvidenceText(item.Metadata.Name, 256), NodeID: safeEvidenceText(item.Spec.NodeName, 256)}
		applicationFound := false
		for _, container := range item.Status.ContainerStatuses {
			pod.TotalContainers++
			if container.Ready {
				pod.ReadyContainers++
			}
			pod.RestartCount += container.RestartCount
			if container.Name == deploymentv1.ApplicationContainer {
				applicationFound = true
				pod.ObservedDigest = imageDigest(container.ImageID)
			}
		}
		if !applicationFound || pod.ObservedDigest == "" {
			digestIncomplete = true
		} else {
			applicationDigests[pod.ObservedDigest] = true
		}
		result.Pods = append(result.Pods, pod)
	}
	sort.Slice(result.Pods, func(i, j int) bool {
		return result.Pods[i].Namespace+"\x00"+result.Pods[i].PodID < result.Pods[j].Namespace+"\x00"+result.Pods[j].PodID
	})
	if selectedPodCount == 0 {
		if podID != "" {
			markKubernetesPartial(&result, "KUBERNETES_TARGET_POD_NOT_FOUND")
		} else {
			markKubernetesPartial(&result, "KUBERNETES_PODS_NOT_FOUND")
		}
	}

	if !digestIncomplete && len(applicationDigests) == 1 {
		for digest := range applicationDigests {
			result.ObservedDigest = digest
		}
	} else if digestIncomplete {
		markKubernetesPartial(&result, "APPLICATION_DIGEST_INCOMPLETE")
	} else if len(applicationDigests) > 1 {
		markKubernetesPartial(&result, "MIXED_APPLICATION_DIGESTS")
	}

	var eventsPayload struct {
		Items []kubernetesEvent `json:"items"`
	}
	eventsJSON, _, err := s.run(ctx, "get", "events", "-A", "-o", "json")
	if err != nil {
		markKubernetesPartial(&result, "KUBERNETES_RESOURCE_QUERY_PARTIAL")
		return result, nil
	}
	if err := decodeBoundedJSON(eventsJSON, &eventsPayload); err != nil {
		return result, errors.New("kubernetes events response is invalid")
	}
	for _, item := range eventsPayload.Items {
		if !identities[resourceIdentity(item.Metadata.Namespace, item.InvolvedObject.Kind, item.InvolvedObject.Name)] {
			continue
		}
		observed := firstEvidenceTime(item.EventTime, item.LastTimestamp, item.Metadata.CreationTimestamp)
		result.Events = append(result.Events, agentv1.IncidentKubernetesEvent{
			ObservedAtUnix: observed.Unix(), Namespace: safeEvidenceText(item.Metadata.Namespace, 256),
			ObjectKind: safeEvidenceText(item.InvolvedObject.Kind, 128), ObjectName: safeEvidenceText(item.InvolvedObject.Name, 256),
			Type: safeEvidenceText(item.Type, 64), Reason: safeEvidenceText(item.Reason, 256), Message: safeEvidenceText(item.Message, MaxRedactedExcerptBytes),
			UntrustedContent: true,
		})
	}
	sort.Slice(result.Events, func(i, j int) bool {
		left, right := result.Events[i], result.Events[j]
		if left.ObservedAtUnix != right.ObservedAtUnix {
			return left.ObservedAtUnix < right.ObservedAtUnix
		}
		return left.Namespace+"\x00"+left.ObjectKind+"\x00"+left.ObjectName+"\x00"+left.Type+"\x00"+left.Reason+"\x00"+left.Message <
			right.Namespace+"\x00"+right.ObjectKind+"\x00"+right.ObjectName+"\x00"+right.Type+"\x00"+right.Reason+"\x00"+right.Message
	})
	return result, nil
}

func ownedKubernetesResource(labels map[string]string, projectID, serviceKey string) bool {
	return labels["app.kubernetes.io/managed-by"] == "opsi" && labels["opsi.dev/project"] == projectID && labels["opsi.dev/service"] == serviceKey
}

func kubernetesResourceIdentities(projectID, serviceKey, nodeID, podID string, pods []kubernetesPod, resources map[string][]kubernetesResource) (map[string]bool, bool) {
	identities := map[string]bool{}
	missingServiceKey := false
	for _, item := range resources["deployments"] {
		missingServiceKey = missingServiceKey || missingKubernetesServiceKey(item.Metadata.Labels, projectID)
		if ownedKubernetesResource(item.Metadata.Labels, projectID, serviceKey) {
			identities[resourceIdentity(item.Metadata.Namespace, "Deployment", item.Metadata.Name)] = true
		}
	}
	for _, item := range resources["replicasets"] {
		missingServiceKey = missingServiceKey || missingKubernetesServiceKey(item.Metadata.Labels, projectID)
		if !ownedKubernetesResource(item.Metadata.Labels, projectID, serviceKey) || !hasSelectedOwner(item.Metadata, "Deployment", identities) {
			continue
		}
		identities[resourceIdentity(item.Metadata.Namespace, "ReplicaSet", item.Metadata.Name)] = true
	}
	for _, item := range resources["services"] {
		missingServiceKey = missingServiceKey || missingKubernetesServiceKey(item.Metadata.Labels, projectID)
		if ownedKubernetesResource(item.Metadata.Labels, projectID, serviceKey) {
			identities[resourceIdentity(item.Metadata.Namespace, "Service", item.Metadata.Name)] = true
		}
	}
	for _, item := range pods {
		missingServiceKey = missingServiceKey || missingKubernetesServiceKey(item.Metadata.Labels, projectID)
		if ownedKubernetesResource(item.Metadata.Labels, projectID, serviceKey) &&
			hasSelectedOwner(item.Metadata, "ReplicaSet", identities) &&
			(nodeID == "" || item.Spec.NodeName == nodeID) && (podID == "" || item.Metadata.Name == podID) {
			identities[resourceIdentity(item.Metadata.Namespace, "Pod", item.Metadata.Name)] = true
		}
	}
	return identities, missingServiceKey
}

func missingKubernetesServiceKey(labels map[string]string, projectID string) bool {
	return labels["app.kubernetes.io/managed-by"] == "opsi" && labels["opsi.dev/project"] == projectID && labels["opsi.dev/service"] == ""
}

func hasSelectedOwner(metadata kubernetesMetadata, kind string, identities map[string]bool) bool {
	for _, owner := range metadata.OwnerReferences {
		if owner.Kind == kind && identities[resourceIdentity(metadata.Namespace, kind, owner.Name)] {
			return true
		}
	}
	return false
}

func resourceIdentity(namespace, kind, name string) string {
	return namespace + "\x00" + kind + "\x00" + name
}

func markKubernetesPartial(result *KubernetesEvidenceResult, reason string) {
	result.CoverageStatus = "partial"
	if result.ReasonCode == "" || reason == "APPLICATION_DIGEST_INCOMPLETE" || reason == "MIXED_APPLICATION_DIGESTS" && result.ReasonCode == "KUBERNETES_RESOURCE_QUERY_PARTIAL" {
		result.ReasonCode = reason
	}
}

func (s KubernetesEvidenceSource) run(ctx context.Context, args ...string) ([]byte, []byte, error) {
	if len(args) != 5 || args[0] != "get" || !containsKubernetesResource([]string{"pods", "deployments", "replicasets", "services", "events"}, args[1]) || args[2] != "-A" || args[3] != "-o" || args[4] != "json" {
		return nil, nil, errors.New("kubectl evidence command is not allowed")
	}
	runner := s.Runner
	if runner == nil {
		runner = ExecKubernetesEvidenceRunner{}
	}
	commandCtx, cancel := context.WithTimeout(ctx, MaxKubernetesCommandDuration)
	defer cancel()
	stdout, stderr, err := runner.RunKubectlGet(commandCtx, firstNonEmptyEvidence(s.KubectlPath, "kubectl"), args...)
	if len(stdout) > maxKubernetesCommandOutput || len(stderr) > maxKubernetesCommandOutput {
		return nil, nil, errors.New("kubectl output exceeded the evidence bound")
	}
	_ = safeEvidenceText(string(stderr), MaxRedactedExcerptBytes)
	return stdout, stderr, err
}

func containsKubernetesResource(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

type boundedBuffer struct {
	data     bytes.Buffer
	limit    int
	overflow bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - b.data.Len()
	if remaining > 0 {
		_, _ = b.data.Write(p[:min(len(p), remaining)])
	}
	if len(p) > remaining {
		b.overflow = true
	}
	return len(p), nil
}

func (b *boundedBuffer) Bytes() []byte { return b.data.Bytes() }

func decodeBoundedJSON(data []byte, target any) error {
	if len(data) == 0 || len(data) > maxKubernetesCommandOutput {
		return errors.New("invalid bounded JSON size")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("multiple JSON values")
	}
	return nil
}

func firstEvidenceLabel(labels map[string]string, keys ...string) string {
	for _, key := range keys {
		if labels[key] != "" {
			return labels[key]
		}
	}
	return ""
}

func firstEvidenceTime(values ...string) time.Time {
	for _, value := range values {
		if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
			return parsed.UTC()
		}
	}
	return time.Unix(0, 0).UTC()
}

func imageDigest(imageID string) string {
	if index := strings.LastIndex(imageID, "@sha256:"); index >= 0 {
		return imageID[index+1:]
	}
	if strings.HasPrefix(imageID, "sha256:") {
		return imageID
	}
	return ""
}

func firstNonEmptyEvidence(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
