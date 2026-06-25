package telemetry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"time"
)

const (
	IncidentStatusDetecting = "detecting"
	AnomalyCPUSpike         = "cpu_spike"
	AnomalyMemorySpike      = "memory_spike"
)

type IncidentStore interface {
	InsertIncident(ctx context.Context, record IncidentRecord) error
	FindOpenIncident(ctx context.Context, projectID, serviceID, anomalyType string, since time.Time) (*IncidentRecord, error)
}

type Analyzer struct {
	Store             IncidentStore
	ConsecutiveNeeded int
	ZThreshold        float64
	DedupeWindow      time.Duration
	Now               func() time.Time
	states            map[string]*anomalyState
}

type anomalyState struct {
	Count       int
	Mean        float64
	Variance    float64
	EWMA        float64
	Consecutive int
}

type IncidentContext struct {
	ProjectID         string         `json:"project_id"`
	AffectedServices  []string       `json:"affected_services"`
	AffectedNodes     []string       `json:"affected_nodes"`
	AffectedPods      []string       `json:"affected_pods"`
	AnomalyType       string         `json:"anomaly_type"`
	MetricSnapshot    map[string]any `json:"metric_snapshot"`
	LogPatterns       []string       `json:"log_patterns"`
	RecentDeployments []string       `json:"recent_deployments"`
	ResourceUsage     map[string]any `json:"resource_usage"`
}

func (a *Analyzer) ObserveMetric(ctx context.Context, metric MetricRecord) (*IncidentRecord, error) {
	if metric.ProjectID == "" || metric.ServiceID == "" || metric.Name == "" {
		return nil, nil
	}
	anomalyType := anomalyTypeForMetric(metric)
	if anomalyType == "" {
		return nil, nil
	}
	state := a.state(metric.ProjectID + ":" + metric.ServiceID + ":" + metric.Name)
	z := state.update(metric.Value)
	if state.Count < 5 || z < a.threshold() {
		state.Consecutive = 0
		return nil, nil
	}
	state.Consecutive++
	if state.Consecutive < a.consecutiveNeeded() {
		return nil, nil
	}
	now := a.now()
	if a.Store != nil {
		existing, err := a.Store.FindOpenIncident(ctx, metric.ProjectID, metric.ServiceID, anomalyType, now.Add(-a.dedupeWindow()))
		if err != nil || existing != nil {
			return existing, err
		}
	}
	contextJSON, err := incidentContextJSON(metric, anomalyType, z)
	if err != nil {
		return nil, err
	}
	incident := IncidentRecord{
		ID:               newIncidentID(),
		ProjectID:        metric.ProjectID,
		NodeID:           metric.NodeID,
		ServiceID:        metric.ServiceID,
		PodID:            metric.PodID,
		AffectedServices: metric.ServiceID,
		AffectedNodes:    metric.NodeID,
		AffectedPods:     metric.PodID,
		AnomalyType:      anomalyType,
		Severity:         severityFor(metric, anomalyType),
		Status:           IncidentStatusDetecting,
		ContextJSON:      contextJSON,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if a.Store != nil {
		if err := a.Store.InsertIncident(ctx, incident); err != nil {
			return nil, err
		}
	}
	return &incident, nil
}

func (a *Analyzer) state(key string) *anomalyState {
	if a.states == nil {
		a.states = map[string]*anomalyState{}
	}
	if a.states[key] == nil {
		a.states[key] = &anomalyState{}
	}
	return a.states[key]
}

func (s *anomalyState) update(value float64) float64 {
	s.Count++
	if s.Count == 1 {
		s.Mean = value
		s.EWMA = value
		return 0
	}
	delta := value - s.Mean
	s.Mean += delta / float64(s.Count)
	s.Variance += delta * (value - s.Mean)
	s.EWMA = 0.3*value + 0.7*s.EWMA
	stddev := math.Sqrt(s.Variance / math.Max(1, float64(s.Count-1)))
	if stddev == 0 {
		return 0
	}
	return math.Abs((value - s.Mean) / stddev)
}

func (a *Analyzer) threshold() float64 {
	if a.ZThreshold > 0 {
		return a.ZThreshold
	}
	return 1.5
}

func (a *Analyzer) consecutiveNeeded() int {
	if a.ConsecutiveNeeded > 0 {
		return a.ConsecutiveNeeded
	}
	return 3
}

func (a *Analyzer) dedupeWindow() time.Duration {
	if a.DedupeWindow > 0 {
		return a.DedupeWindow
	}
	return 15 * time.Minute
}

func (a *Analyzer) now() time.Time {
	if a.Now != nil {
		return a.Now().UTC()
	}
	return time.Now().UTC()
}

func anomalyTypeForMetric(metric MetricRecord) string {
	switch metric.Name {
	case "cpu", "cpu_pct", "container.cpu", "node.cpu":
		return AnomalyCPUSpike
	case "memory", "ram_mb", "container.memory", "node.memory_available":
		return AnomalyMemorySpike
	default:
		return ""
	}
}

func severityFor(metric MetricRecord, anomalyType string) string {
	if anomalyType == AnomalyCPUSpike && metric.Value >= 95 {
		return "P1"
	}
	return "P2"
}

func incidentContextJSON(metric MetricRecord, anomalyType string, z float64) (string, error) {
	ctx := IncidentContext{
		ProjectID:        metric.ProjectID,
		AffectedServices: compact(metric.ServiceID),
		AffectedNodes:    compact(metric.NodeID),
		AffectedPods:     compact(metric.PodID),
		AnomalyType:      anomalyType,
		MetricSnapshot: map[string]any{
			"name":    metric.Name,
			"value":   metric.Value,
			"unit":    metric.Unit,
			"z_score": math.Round(z*100) / 100,
		},
		LogPatterns:       []string{},
		RecentDeployments: []string{},
		ResourceUsage: map[string]any{
			"service_id": metric.ServiceID,
			"pod_id":     metric.PodID,
		},
	}
	data, err := json.Marshal(ctx)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func compact(values ...string) []string {
	var out []string
	for _, value := range values {
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func newIncidentID() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("inc_%d", time.Now().UnixNano())
	}
	return "inc_" + hex.EncodeToString(raw[:])
}
