package webhookrelay

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type AlertManager struct {
	cfg    AlertConfig
	client *http.Client
	mu     sync.Mutex
	sent   map[string]time.Time
}

type AlertNotification struct {
	ID         string    `json:"id"`
	ProjectID  string    `json:"project_id"`
	Severity   string    `json:"severity"`
	Status     string    `json:"status"`
	Title      string    `json:"title"`
	ResourceID string    `json:"resource_id,omitempty"`
	RunbookID  string    `json:"runbook_id"`
	SentAt     time.Time `json:"sent_at"`
}

type alertmanagerWebhook struct {
	Alerts []struct {
		Status      string            `json:"status"`
		Labels      map[string]string `json:"labels"`
		Annotations map[string]string `json:"annotations"`
	} `json:"alerts"`
}

func NewAlertManager(cfg AlertConfig) *AlertManager {
	if cfg.MinSeverity == "" {
		cfg.MinSeverity = "medium"
	}
	return &AlertManager{cfg: cfg, client: &http.Client{Timeout: 5 * time.Second}, sent: map[string]time.Time{}}
}

func (m *AlertManager) Notify(projectID string, alerts []SupportAlert) {
	if m == nil {
		return
	}
	for _, alert := range alerts {
		if !m.shouldSend(projectID, alert) {
			continue
		}
		notification := AlertNotification{
			ID:         alert.ID,
			ProjectID:  projectID,
			Severity:   alert.Severity,
			Status:     alert.Status,
			Title:      alert.Title,
			ResourceID: alert.ResourceID,
			RunbookID:  alert.RunbookID,
			SentAt:     time.Now().UTC(),
		}
		if err := m.deliver(notification); err != nil {
			_ = m.writeOutbox(notification)
		}
	}
}

func (s *Server) handleInternalAlerts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if token := s.Config.Alerts.InternalToken; token != "" && bearerToken(r) != token {
		writeError(w, http.StatusUnauthorized, "alert token is required")
		return
	}
	var req alertmanagerWebhook
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid alertmanager payload")
		return
	}
	sent := 0
	for _, alert := range req.Alerts {
		notification, ok := alertmanagerNotification(alert.Status, alert.Labels, alert.Annotations)
		if !ok {
			continue
		}
		if err := s.alerts.deliver(notification); err != nil {
			_ = s.alerts.writeOutbox(notification)
		}
		sent++
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": sent})
}

func alertmanagerNotification(status string, labels, annotations map[string]string) (AlertNotification, bool) {
	name := safeAlertText(labels["alertname"])
	severity := safeSeverity(labels["severity"])
	if name == "" || severity == "" || !severityAtLeast(severity, "medium") {
		return AlertNotification{}, false
	}
	return AlertNotification{
		ID:         strings.ToLower(name),
		ProjectID:  firstNonEmpty(safeAlertText(labels["project_id"]), "control-plane"),
		Severity:   severity,
		Status:     safeAlertText(firstNonEmpty(status, "firing")),
		Title:      firstNonEmpty(safeAlertText(annotations["summary"]), name),
		ResourceID: safeAlertText(labels["resource_id"]),
		RunbookID:  safeAlertText(annotations["runbook"]),
		SentAt:     time.Now().UTC(),
	}, true
}

func (m *AlertManager) shouldSend(projectID string, alert SupportAlert) bool {
	if alert.Status != "firing" || !severityAtLeast(alert.Severity, m.cfg.MinSeverity) {
		return false
	}
	key := projectID + ":" + alert.ID + ":" + alert.Status
	m.mu.Lock()
	defer m.mu.Unlock()
	if last, ok := m.sent[key]; ok && time.Since(last) < 15*time.Minute {
		return false
	}
	m.sent[key] = time.Now()
	return true
}

func (m *AlertManager) deliver(notification AlertNotification) error {
	if m.cfg.WebhookURL == "" {
		return m.writeOutbox(notification)
	}
	body, err := json.Marshal(notification)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, m.cfg.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/json")
	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return os.ErrInvalid
	}
	return nil
}

func (m *AlertManager) writeOutbox(notification AlertNotification) error {
	if m.cfg.OutboxPath == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(m.cfg.OutboxPath), 0700); err != nil {
		return err
	}
	body, err := json.Marshal(notification)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(m.cfg.OutboxPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(body, '\n'))
	return err
}

func severityAtLeast(got, min string) bool {
	return severityRank(got) >= severityRank(min)
}

func severityRank(value string) int {
	switch value {
	case "critical", "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}

func safeSeverity(value string) string {
	switch value {
	case "critical", "high":
		return "high"
	case "medium":
		return "medium"
	case "low":
		return "low"
	default:
		return ""
	}
}

func safeAlertText(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 120 {
		value = value[:120]
	}
	value = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, value)
	return value
}
