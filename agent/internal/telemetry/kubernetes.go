package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

type KubernetesCollector struct {
	ProjectID    string
	NodeID       string
	KubectlPath  string
	Runner       CommandRunner
	LogTailLines int
	LogSince     time.Duration
	Metrics      Collector
	LogWatch     bool
	Fallback     Collector
	Now          func() time.Time
}

func (c KubernetesCollector) Collect(ctx context.Context) ([]MetricRecord, []LogRecord, error) {
	pods, err := c.listPods(ctx)
	if err != nil {
		if c.Fallback != nil {
			return c.Fallback.Collect(ctx)
		}
		return nil, nil, err
	}
	var metrics []MetricRecord
	var metricsErr error
	if c.Metrics != nil {
		metrics, _, metricsErr = c.Metrics.Collect(ctx)
	}
	if len(metrics) == 0 {
		metrics, metricsErr = c.collectTop(ctx, pods)
	}
	if metricsErr != nil && len(metrics) == 0 && c.Fallback != nil {
		fallbackMetrics, fallbackLogs, fallbackErr := c.Fallback.Collect(ctx)
		if fallbackErr != nil {
			return nil, nil, fallbackErr
		}
		metrics = append(metrics, fallbackMetrics...)
		_ = fallbackLogs
	}
	logs := c.collectLogs(ctx, pods)
	return metrics, logs, nil
}

func (c KubernetesCollector) listPods(ctx context.Context) (map[string]podMeta, error) {
	out, err := c.run(ctx, "get", "pods", "-A", "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("kubectl get pods: %w", err)
	}
	var payload struct {
		Items []struct {
			Metadata struct {
				Name      string            `json:"name"`
				Namespace string            `json:"namespace"`
				Labels    map[string]string `json:"labels"`
			} `json:"metadata"`
			Spec struct {
				NodeName string `json:"nodeName"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return nil, fmt.Errorf("parse pod list: %w", err)
	}
	items := map[string]podMeta{}
	for _, item := range payload.Items {
		projectID := firstLabel(item.Metadata.Labels, "opsi.dev/project-id", "opsi.project_id", "project_id")
		if projectID == "" {
			projectID = c.ProjectID
		}
		serviceID := firstLabel(item.Metadata.Labels, "opsi.dev/service-id", "opsi.service_id", "service_id", "app.kubernetes.io/name", "app")
		meta := podMeta{
			ProjectID: projectID,
			NodeID:    firstNonEmpty(item.Spec.NodeName, c.NodeID),
			ServiceID: serviceID,
			PodID:     item.Metadata.Name,
			Namespace: item.Metadata.Namespace,
		}
		items[item.Metadata.Namespace+"/"+item.Metadata.Name] = meta
	}
	return items, nil
}

func (c KubernetesCollector) collectTop(ctx context.Context, pods map[string]podMeta) ([]MetricRecord, error) {
	out, err := c.run(ctx, "top", "pods", "-A", "--containers", "--no-headers")
	if err != nil {
		return nil, fmt.Errorf("kubectl top pods: %w", err)
	}
	now := c.now()
	var records []MetricRecord
	for _, raw := range strings.Split(string(out), "\n") {
		fields := strings.Fields(raw)
		if len(fields) < 5 {
			continue
		}
		meta, ok := pods[fields[0]+"/"+fields[1]]
		if !ok || meta.ProjectID == "" {
			continue
		}
		cpu, err := parseKubernetesCPU(fields[len(fields)-2])
		if err == nil {
			records = append(records, MetricRecord{ProjectID: meta.ProjectID, NodeID: meta.NodeID, ServiceID: meta.ServiceID, PodID: meta.PodID, Name: "pod.cpu", Value: cpu, Unit: "cores", ObservedAt: now})
		}
		memory, err := parseKubernetesMemory(fields[len(fields)-1])
		if err == nil {
			records = append(records, MetricRecord{ProjectID: meta.ProjectID, NodeID: meta.NodeID, ServiceID: meta.ServiceID, PodID: meta.PodID, Name: "pod.memory", Value: memory, Unit: "bytes", ObservedAt: now})
		}
	}
	return records, nil
}

func (c KubernetesCollector) collectLogs(ctx context.Context, pods map[string]podMeta) []LogRecord {
	var records []LogRecord
	tail := c.LogTailLines
	if tail <= 0 {
		tail = 50
	}
	since := c.LogSince
	if since <= 0 {
		since = time.Minute
	}
	for _, meta := range pods {
		if meta.ProjectID == "" || meta.PodID == "" || meta.Namespace == "" {
			continue
		}
		args := []string{"logs", "-n", meta.Namespace, meta.PodID, "--all-containers=true", "--tail", strconv.Itoa(tail), "--since", since.String(), "--timestamps"}
		logCtx := ctx
		cancel := func() {}
		if c.LogWatch {
			sinceTime := c.now().Add(-since).Format(time.RFC3339)
			args = []string{"logs", "-n", meta.Namespace, meta.PodID, "--all-containers=true", "--since-time", sinceTime, "--timestamps", "--follow"}
			logCtx, cancel = context.WithTimeout(ctx, 2*time.Second)
		}
		out, err := c.run(logCtx, args...)
		cancel()
		if err != nil {
			continue
		}
		records = append(records, parsePodLogs(meta, out)...)
	}
	return records
}

func parsePodLogs(meta podMeta, out []byte) []LogRecord {
	var records []LogRecord
	for _, line := range bytes.Split(out, []byte{'\n'}) {
		message := strings.TrimSpace(string(line))
		if message == "" {
			continue
		}
		observed := time.Now().UTC()
		if len(message) >= len(time.RFC3339) {
			parts := strings.SplitN(message, " ", 2)
			if ts, err := time.Parse(time.RFC3339Nano, parts[0]); err == nil {
				observed = ts.UTC()
				if len(parts) == 2 {
					message = parts[1]
				}
			}
		}
		records = append(records, LogRecord{ProjectID: meta.ProjectID, NodeID: meta.NodeID, ServiceID: meta.ServiceID, PodID: meta.PodID, Namespace: meta.Namespace, Level: inferLogLevel(message), Message: message, Unread: true, ObservedAt: observed})
	}
	return records
}

func (c KubernetesCollector) run(ctx context.Context, args ...string) ([]byte, error) {
	runner := c.Runner
	if runner == nil {
		runner = ExecRunner{}
	}
	return runner.Run(ctx, firstNonEmpty(c.KubectlPath, "kubectl"), args...)
}

func (c KubernetesCollector) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

type podMeta struct {
	ProjectID string
	NodeID    string
	ServiceID string
	PodID     string
	Namespace string
}

func parseKubernetesCPU(raw string) (float64, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasSuffix(raw, "m") {
		milli, err := strconv.ParseFloat(strings.TrimSuffix(raw, "m"), 64)
		return milli / 1000, err
	}
	return strconv.ParseFloat(raw, 64)
}

func parseKubernetesMemory(raw string) (float64, error) {
	raw = strings.TrimSpace(raw)
	multipliers := map[string]float64{"Ki": 1024, "Mi": 1024 * 1024, "Gi": 1024 * 1024 * 1024, "Ti": 1024 * 1024 * 1024 * 1024, "K": 1000, "M": 1000 * 1000, "G": 1000 * 1000 * 1000}
	for suffix, multiplier := range multipliers {
		if strings.HasSuffix(raw, suffix) {
			value, err := strconv.ParseFloat(strings.TrimSuffix(raw, suffix), 64)
			return value * multiplier, err
		}
	}
	return strconv.ParseFloat(raw, 64)
}

func inferLogLevel(message string) string {
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "error") || strings.Contains(lower, "failed") || strings.Contains(lower, "panic"):
		return "error"
	case strings.Contains(lower, "warn"):
		return "warn"
	default:
		return "info"
	}
}

func firstLabel(labels map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := labels[key]; value != "" {
			return value
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
