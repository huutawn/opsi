package incident

import (
	"context"
	"time"

	"github.com/opsi-dev/opsi/agent/internal/telemetry"
)

type IncidentContextBuilder struct {
	Store  interface{}
	Window time.Duration
}

type syncStore interface {
	SyncRecords(ctx context.Context, projectID string, since time.Time, until time.Time, resourceIDs []string) ([]telemetry.SyncRecord, error)
}

func (b IncidentContextBuilder) Build(ctx context.Context, rec telemetry.IncidentRecord) (IncidentContext, error) {
	out, err := SanitizeIncidentContext(rec)
	if err != nil {
		return out, err
	}
	store, ok := b.Store.(syncStore)
	if !ok {
		return out, nil
	}
	window := b.Window
	if window <= 0 {
		window = 5 * time.Minute
	}
	center := rec.CreatedAt
	if center.IsZero() {
		center = time.Now().UTC()
	}
	records, err := store.SyncRecords(ctx, rec.ProjectID, center.Add(-window), center.Add(window), resourceIDs(rec))
	if err != nil {
		return out, err
	}
	metrics := map[string]map[string]any{}
	logCounts := map[string]map[string]any{}
	for _, item := range records {
		if item.Metric != nil {
			metrics[item.Metric.Name] = map[string]any{
				"value":            item.Metric.Value,
				"unit":             item.Metric.Unit,
				"observed_at_unix": item.Metric.ObservedAt.Unix(),
			}
		}
		if item.Log != nil && item.Log.Fingerprint != "" {
			entry := logCounts[item.Log.Fingerprint]
			if entry == nil {
				entry = map[string]any{"fingerprint": item.Log.Fingerprint, "level": item.Log.Level, "count": 0}
				logCounts[item.Log.Fingerprint] = entry
			}
			entry["count"] = entry["count"].(int) + 1
		}
	}
	if len(metrics) > 0 {
		out.MetricSnapshot = metrics
	}
	for _, entry := range logCounts {
		out.LogPatterns = append(out.LogPatterns, entry)
	}
	out.Sanitization = map[string]any{"raw_logs_included": false, "secret_like_removed": true}
	return out, nil
}

func resourceIDs(rec telemetry.IncidentRecord) []string {
	var ids []string
	for _, id := range []string{rec.NodeID, rec.ServiceID, rec.PodID} {
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}
