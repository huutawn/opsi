package telemetry

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

type UptimeStore interface {
	InsertUptimeCheck(ctx context.Context, record UptimeCheckRecord) error
	InsertIncident(ctx context.Context, record IncidentRecord) error
}

type SyntheticChecker struct {
	Store      UptimeStore
	HTTPClient *http.Client
	Now        func() time.Time
}

type SyntheticTarget struct {
	ProjectID     string
	ServiceID     string
	NodeID        string
	PublicURL     string
	InternalReady bool
}

func (c SyntheticChecker) Check(ctx context.Context, target SyntheticTarget) (*IncidentRecord, error) {
	if target.ProjectID == "" || target.ServiceID == "" || target.PublicURL == "" {
		return nil, fmt.Errorf("project_id, service_id and public_url are required")
	}
	now := c.now()
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	start := now
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.PublicURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	latency := int64(c.now().Sub(start).Milliseconds())
	status := 0
	success := false
	if err == nil {
		defer resp.Body.Close()
		status = resp.StatusCode
		success = resp.StatusCode >= 200 && resp.StatusCode < 400
	}
	if c.Store != nil {
		if insertErr := c.Store.InsertUptimeCheck(ctx, UptimeCheckRecord{ProjectID: target.ProjectID, ServiceID: target.ServiceID, Timestamp: now, Success: success, LatencyMS: latency, HTTPStatus: status}); insertErr != nil {
			return nil, insertErr
		}
	}
	if success || !target.InternalReady {
		return nil, err
	}
	incident := IncidentRecord{
		ID:               "inc_external_" + target.ServiceID + "_" + fmt.Sprint(now.Unix()),
		ProjectID:        target.ProjectID,
		NodeID:           target.NodeID,
		ServiceID:        target.ServiceID,
		AffectedServices: target.ServiceID,
		AnomalyType:      "external_health_check_failed",
		Severity:         "P1",
		Status:           "detecting",
		ContextJSON:      fmt.Sprintf(`{"external_success":false,"internal_ready":true,"http_status":%d,"latency_ms":%d}`, status, latency),
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if c.Store != nil {
		if insertErr := c.Store.InsertIncident(ctx, incident); insertErr != nil {
			return nil, insertErr
		}
	}
	return &incident, err
}

func (c SyntheticChecker) RunEvery(ctx context.Context, interval time.Duration, target SyntheticTarget) error {
	if interval <= 0 {
		interval = time.Minute
	}
	if _, err := c.Check(ctx, target); err != nil {
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := c.Check(ctx, target); err != nil {
				return err
			}
		}
	}
}

func (c SyntheticChecker) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}
