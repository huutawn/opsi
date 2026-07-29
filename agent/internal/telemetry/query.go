package telemetry

import (
	"context"
	"sort"
	"time"

	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
)

func BuildQueryResponse(ctx context.Context, store Store, req *agentv1.TelemetryQueryRequest, now time.Time) (*agentv1.TelemetryQueryResponse, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	since := time.Unix(req.SinceUnix, 0).UTC()
	records, err := store.SyncRecords(ctx, req.ProjectID, since, now, resourceIDs(req.ServiceID))
	if err != nil {
		return nil, err
	}
	limit := int(req.Limit)
	if limit <= 0 {
		limit = 100
	}
	if limit > 200 {
		limit = 200
	}
	resp := &agentv1.TelemetryQueryResponse{
		ProjectID:     req.ProjectID,
		Source:        "agent",
		PayloadPolicy: "raw logs and raw metric streams remain Agent-local; browser responses are redacted summaries/windows",
	}
	stats := newQueryStats()
	logCount := 0
	for _, rec := range records {
		stats.observe(rec)
		if req.IncludeLogs && rec.Log != nil {
			logCount++
			if len(resp.Logs) < limit {
				resp.Logs = append(resp.Logs, logEntry(rec.Log))
				resp.NextCursor = rec.ObservedAt.Format(time.RFC3339Nano)
			}
		}
	}
	if req.IncludeSummary {
		resp.Summary = stats.summary(req.SinceUnix, now.Unix())
	}
	if req.IncludeServices {
		resp.Services = stats.services()
	}
	if logCount <= len(resp.Logs) {
		resp.NextCursor = ""
	}
	return resp, nil
}

func resourceIDs(serviceID string) []string {
	if serviceID == "" {
		return nil
	}
	return []string{serviceID}
}

type queryStats struct {
	metricCount int32
	logCount    int32
	errorCount  int32
	servicesMap map[string]*serviceStats
}

type serviceStats struct {
	id          string
	pods        map[string]bool
	readyPods   map[string]bool
	cpu         float64
	memory      float64
	restarts    int32
	errors      int32
	lastSeen    int64
	metricNames map[string]bool
}

func newQueryStats() *queryStats {
	return &queryStats{servicesMap: map[string]*serviceStats{}}
}

func (s *queryStats) observe(rec SyncRecord) {
	switch {
	case rec.Metric != nil:
		s.metricCount++
		s.service(rec.Metric.ServiceID).metric(rec.Metric)
	case rec.Log != nil:
		s.logCount++
		if rec.Log.Level == "error" {
			s.errorCount++
			s.service(rec.Log.ServiceID).errors++
		}
		s.service(rec.Log.ServiceID).seen(rec.Log.ObservedAt)
	case rec.Incident != nil && rec.Incident.Status != "resolved":
		s.errorCount++
		s.service(rec.Incident.ServiceID).errors++
	}
}

func (s *queryStats) service(id string) *serviceStats {
	if id == "" {
		id = "unscoped"
	}
	if s.servicesMap[id] == nil {
		s.servicesMap[id] = &serviceStats{id: id, pods: map[string]bool{}, readyPods: map[string]bool{}, metricNames: map[string]bool{}}
	}
	return s.servicesMap[id]
}

func (s *serviceStats) metric(rec *MetricRecord) {
	if rec.PodID != "" {
		s.pods[rec.PodID] = true
	}
	s.metricNames[rec.Name] = true
	s.seen(rec.ObservedAt)
	switch rec.Name {
	case "pod.cpu":
		s.cpu += rec.Value
	case "pod.memory":
		s.memory += rec.Value
	case "pod.ready":
		if rec.Value > 0 && rec.PodID != "" {
			s.readyPods[rec.PodID] = true
		}
	case "pod.restart_count":
		s.restarts += int32(rec.Value)
	}
}

func (s *serviceStats) seen(at time.Time) {
	if unix := at.Unix(); unix > s.lastSeen {
		s.lastSeen = unix
	}
}

func (s *queryStats) summary(sinceUnix, endUnix int64) *agentv1.TelemetryRuntimeSummary {
	health := "unknown"
	services := s.servicesList()
	if len(services) > 0 {
		health = "healthy"
		for _, service := range services {
			if service.Health != "healthy" {
				health = "degraded"
				break
			}
		}
	}
	return &agentv1.TelemetryRuntimeSummary{
		SinceUnix:    sinceUnix,
		EndUnix:      endUnix,
		MetricCount:  s.metricCount,
		LogCount:     s.logCount,
		ErrorCount:   s.errorCount,
		ServiceCount: int32(len(services)),
		Health:       health,
	}
}

func (s *queryStats) services() []agentv1.TelemetryServiceStatus {
	return s.servicesList()
}

func (s *queryStats) servicesList() []agentv1.TelemetryServiceStatus {
	out := make([]agentv1.TelemetryServiceStatus, 0, len(s.servicesMap))
	for _, service := range s.servicesMap {
		health := "unknown"
		switch {
		case service.errors > 0 || (len(service.pods) > 0 && len(service.readyPods) < len(service.pods)):
			health = "degraded"
		case len(service.pods) > 0:
			health = "healthy"
		}
		out = append(out, agentv1.TelemetryServiceStatus{
			ServiceID:        service.id,
			Health:           health,
			PodCount:         int32(len(service.pods)),
			ReadyPods:        int32(len(service.readyPods)),
			CPUCores:         service.cpu,
			MemoryBytes:      service.memory,
			RestartCount:     service.restarts,
			RecentErrorCount: service.errors,
			LastSeenUnix:     service.lastSeen,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ServiceID < out[j].ServiceID })
	return out
}

func logEntry(rec *LogRecord) agentv1.TelemetryLogEntry {
	return agentv1.TelemetryLogEntry{
		ServiceID:    rec.ServiceID,
		PodID:        rec.PodID,
		Namespace:    rec.Namespace,
		Level:        rec.Level,
		Message:      RedactSensitiveText(rec.Message),
		Fingerprint:  rec.Fingerprint,
		ObservedUnix: rec.ObservedAt.Unix(),
	}
}
