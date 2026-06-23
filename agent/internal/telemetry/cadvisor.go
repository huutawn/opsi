package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type CAdvisorCollector struct {
	Endpoint  string
	Client    *http.Client
	Timeout   time.Duration
	ProjectID string
	NodeID    string
	Now       func() time.Time
}

func (c CAdvisorCollector) Collect(ctx context.Context) ([]MetricRecord, []LogRecord, error) {
	if strings.TrimSpace(c.Endpoint) == "" {
		return nil, nil, fmt.Errorf("cadvisor endpoint is required")
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.Endpoint, "/")+"/api/v1.3/subcontainers", nil)
	if err != nil {
		return nil, nil, err
	}
	client := c.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil, fmt.Errorf("cadvisor status %d", resp.StatusCode)
	}
	var containers []cadvisorContainer
	if err := json.NewDecoder(resp.Body).Decode(&containers); err != nil {
		return nil, nil, err
	}
	now := c.now()
	var records []MetricRecord
	for _, container := range containers {
		if len(container.Stats) == 0 {
			continue
		}
		projectID := firstLabel(container.Spec.Labels, "opsi.dev/project-id", "opsi.project_id", "project_id")
		if projectID == "" {
			projectID = c.ProjectID
		}
		serviceID := firstLabel(container.Spec.Labels, "opsi.dev/service-id", "opsi.service_id", "service_id", "app.kubernetes.io/name", "app")
		podID := firstLabel(container.Spec.Labels, "io.kubernetes.pod.name", "pod")
		stat := container.Stats[len(container.Stats)-1]
		if stat.Timestamp != "" {
			if parsed, err := time.Parse(time.RFC3339Nano, stat.Timestamp); err == nil {
				now = parsed.UTC()
			}
		}
		base := MetricRecord{ProjectID: projectID, NodeID: c.NodeID, ServiceID: serviceID, PodID: podID, ObservedAt: now}
		if stat.CPU.Usage.Total > 0 {
			record := base
			record.Name = "container.cpu_usage_seconds_total"
			record.Value = float64(stat.CPU.Usage.Total) / 1_000_000_000
			record.Unit = "seconds"
			records = append(records, record)
		}
		if stat.Memory.Usage > 0 {
			record := base
			record.Name = "container.memory_usage"
			record.Value = float64(stat.Memory.Usage)
			record.Unit = "bytes"
			records = append(records, record)
		}
		for _, item := range stat.DiskIO.IoServiceBytes {
			record := base
			record.Name = "container.diskio_" + strings.ToLower(item.StatsType) + "_bytes"
			record.Value = float64(item.Value)
			record.Unit = "bytes"
			records = append(records, record)
		}
		for _, iface := range stat.Network.Interfaces {
			if iface.RxBytes > 0 {
				record := base
				record.Name = "container.network_rx_bytes"
				record.Value = float64(iface.RxBytes)
				record.Unit = "bytes"
				records = append(records, record)
			}
			if iface.TxBytes > 0 {
				record := base
				record.Name = "container.network_tx_bytes"
				record.Value = float64(iface.TxBytes)
				record.Unit = "bytes"
				records = append(records, record)
			}
		}
	}
	return records, nil, nil
}

func (c CAdvisorCollector) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

type cadvisorContainer struct {
	Spec struct {
		Labels map[string]string `json:"labels"`
	} `json:"spec"`
	Stats []struct {
		Timestamp string `json:"timestamp"`
		CPU       struct {
			Usage struct {
				Total uint64 `json:"total"`
			} `json:"usage"`
		} `json:"cpu"`
		Memory struct {
			Usage uint64 `json:"usage"`
		} `json:"memory"`
		DiskIO struct {
			IoServiceBytes []struct {
				StatsType string `json:"stats_type"`
				Value     uint64 `json:"value"`
			} `json:"io_service_bytes"`
		} `json:"diskio"`
		Network struct {
			Interfaces []struct {
				RxBytes uint64 `json:"rx_bytes"`
				TxBytes uint64 `json:"tx_bytes"`
			} `json:"interfaces"`
		} `json:"network"`
	} `json:"stats"`
}
