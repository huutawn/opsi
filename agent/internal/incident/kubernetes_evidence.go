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
}

func (s KubernetesEvidenceSource) Read(ctx context.Context, projectID, serviceID, nodeID, podID string) (KubernetesEvidenceResult, error) {
	podsJSON, _, err := s.run(ctx, "get", "pods", "-A", "-o", "json")
	if err != nil {
		return KubernetesEvidenceResult{}, errors.New("kubernetes pods unavailable")
	}
	var podsPayload struct {
		Items []struct {
			Metadata struct {
				Name      string            `json:"name"`
				Namespace string            `json:"namespace"`
				Labels    map[string]string `json:"labels"`
			} `json:"metadata"`
			Spec struct {
				NodeName string `json:"nodeName"`
			} `json:"spec"`
			Status struct {
				ContainerStatuses []struct {
					Ready        bool   `json:"ready"`
					RestartCount int32  `json:"restartCount"`
					ImageID      string `json:"imageID"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := decodeBoundedJSON(podsJSON, &podsPayload); err != nil {
		return KubernetesEvidenceResult{}, errors.New("kubernetes pods response is invalid")
	}
	result := KubernetesEvidenceResult{}
	podNames := map[string]bool{}
	for _, item := range podsPayload.Items {
		if firstEvidenceLabel(item.Metadata.Labels, "opsi.dev/project-id", "opsi.project_id", "project_id") != projectID {
			continue
		}
		if serviceID != "" && firstEvidenceLabel(item.Metadata.Labels, "opsi.dev/service-id", "opsi.service_id", "service_id", "app.kubernetes.io/name", "app") != serviceID {
			continue
		}
		if nodeID != "" && item.Spec.NodeName != nodeID {
			continue
		}
		if podID != "" && item.Metadata.Name != podID {
			continue
		}
		pod := agentv1.IncidentPodEvidence{Namespace: safeEvidenceText(item.Metadata.Namespace, 256), PodID: safeEvidenceText(item.Metadata.Name, 256), NodeID: safeEvidenceText(item.Spec.NodeName, 256)}
		for _, container := range item.Status.ContainerStatuses {
			pod.TotalContainers++
			if container.Ready {
				pod.ReadyContainers++
			}
			pod.RestartCount += container.RestartCount
			if digest := imageDigest(container.ImageID); digest != "" && (pod.ObservedDigest == "" || digest < pod.ObservedDigest) {
				pod.ObservedDigest = digest
			}
		}
		if pod.ObservedDigest != "" && (result.ObservedDigest == "" || pod.ObservedDigest < result.ObservedDigest) {
			result.ObservedDigest = pod.ObservedDigest
		}
		result.Pods = append(result.Pods, pod)
		podNames[item.Metadata.Name] = true
	}
	sort.Slice(result.Pods, func(i, j int) bool {
		return result.Pods[i].Namespace+"\x00"+result.Pods[i].PodID < result.Pods[j].Namespace+"\x00"+result.Pods[j].PodID
	})

	eventsJSON, _, err := s.run(ctx, "get", "events", "-A", "-o", "json")
	if err != nil {
		return result, errors.New("kubernetes events unavailable")
	}
	var eventsPayload struct {
		Items []struct {
			Metadata struct {
				Namespace         string `json:"namespace"`
				CreationTimestamp string `json:"creationTimestamp"`
			} `json:"metadata"`
			InvolvedObject struct {
				Kind string `json:"kind"`
				Name string `json:"name"`
			} `json:"involvedObject"`
			Type          string `json:"type"`
			Reason        string `json:"reason"`
			Message       string `json:"message"`
			EventTime     string `json:"eventTime"`
			LastTimestamp string `json:"lastTimestamp"`
		} `json:"items"`
	}
	if err := decodeBoundedJSON(eventsJSON, &eventsPayload); err != nil {
		return result, errors.New("kubernetes events response is invalid")
	}
	for _, item := range eventsPayload.Items {
		if !podNames[item.InvolvedObject.Name] && item.InvolvedObject.Name != serviceID {
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

func (s KubernetesEvidenceSource) run(ctx context.Context, args ...string) ([]byte, []byte, error) {
	if len(args) != 5 || args[0] != "get" || (args[1] != "pods" && args[1] != "events") || args[2] != "-A" || args[3] != "-o" || args[4] != "json" {
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
