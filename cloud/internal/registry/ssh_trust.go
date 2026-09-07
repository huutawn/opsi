package registry

import (
	"crypto/subtle"
	"encoding/base64"
	"strings"
	"time"
)

const (
	TrustStatusActive     = "active"
	TrustStatusSuperseded = "superseded"

	TrustStateFirstSeen = "first_seen"
	TrustStateMatched   = "matched"
	TrustStateChanged   = "changed"

	ObservationStatusPending   = "pending"
	ObservationStatusConfirmed = "confirmed"
	ObservationStatusConsumed  = "consumed"
	ObservationStatusExpired   = "expired"

	BootstrapWaitingHostKeyConfirmation = "waiting_host_key_confirmation"
)

type SSHHostKeyTrust struct {
	ID           string     `json:"id"`
	ProjectID    string     `json:"project_id"`
	Host         string     `json:"host"`
	Port         int        `json:"port"`
	Algorithm    string     `json:"algorithm"`
	PublicKey    string     `json:"public_key"`
	Fingerprint  string     `json:"fingerprint"`
	Status       string     `json:"status"` // "active", "superseded"
	CreatedBy    string     `json:"created_by,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	SupersededAt *time.Time `json:"superseded_at,omitempty"`
	SupersededBy string     `json:"superseded_by,omitempty"`
}

type SSHHostKeyObservation struct {
	ID                  string    `json:"id"`
	ProjectID           string    `json:"project_id"`
	Host                string    `json:"host"`
	Port                int       `json:"port"`
	ResolvedIP          string    `json:"resolved_ip"`
	Algorithm           string    `json:"algorithm"`
	PublicKey           string    `json:"public_key"`
	Fingerprint         string    `json:"fingerprint"`
	TrustState          string    `json:"trust_state"` // "first_seen", "matched", "changed"
	PreviousFingerprint string    `json:"previous_fingerprint,omitempty"`
	Status              string    `json:"status"` // "pending", "confirmed", "consumed", "expired"
	CreatedBy           string    `json:"created_by,omitempty"`
	ExpiresAt           time.Time `json:"expires_at"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

func normalizeHost(host string) string {
	return strings.ToLower(strings.TrimSpace(host))
}

func comparePublicKeys(a, b string) bool {
	rawA, errA := base64.StdEncoding.DecodeString(strings.TrimSpace(a))
	rawB, errB := base64.StdEncoding.DecodeString(strings.TrimSpace(b))
	if errA != nil || errB != nil {
		return false
	}
	return subtle.ConstantTimeCompare(rawA, rawB) == 1
}

// CreateSSHHostKeyObservation records a new probe result for a project endpoint.
// It determines trust_state by checking the current active trust for (projectID, host, port).
func (s *Service) CreateSSHHostKeyObservation(projectID, host string, port int, resolvedIP, algorithm, publicKey, fingerprint, createdBy string, now time.Time) (SSHHostKeyObservation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	project, ok := s.projects[projectID]
	if !ok {
		return SSHHostKeyObservation{}, ErrNotFound
	}

	normHost := normalizeHost(host)
	now = now.UTC()
	expiresAt := now.Add(10 * time.Minute)

	// Check current active trust
	trustState := TrustStateFirstSeen
	var previousFingerprint string

	activeTrust, hasActive := s.getActiveTrustLocked(projectID, normHost, port)
	if hasActive {
		if comparePublicKeys(activeTrust.PublicKey, publicKey) {
			trustState = TrustStateMatched
		} else {
			trustState = TrustStateChanged
			previousFingerprint = activeTrust.Fingerprint
		}
	}

	obs := SSHHostKeyObservation{
		ID:                  newID("probe"),
		ProjectID:           projectID,
		Host:                normHost,
		Port:                port,
		ResolvedIP:          resolvedIP,
		Algorithm:           algorithm,
		PublicKey:           publicKey,
		Fingerprint:         fingerprint,
		TrustState:          trustState,
		PreviousFingerprint: previousFingerprint,
		Status:              ObservationStatusPending,
		CreatedBy:           createdBy,
		ExpiresAt:           expiresAt,
		CreatedAt:           now,
		UpdatedAt:           now,
	}

	if s.sshObservations == nil {
		s.sshObservations = map[string]SSHHostKeyObservation{}
	}
	s.sshObservations[obs.ID] = obs

	s.audit = append(s.audit, AuditEvent{
		ID:           newID("aud"),
		OrgID:        project.OrgID,
		ProjectID:    projectID,
		ActorType:    "user",
		ActorUserID:  createdBy,
		Action:       "SSH_HOST_KEY_OBSERVED",
		ResourceType: "ssh_host_key_observation",
		ResourceID:   obs.ID,
		Result:       "success",
		MetadataRedacted: map[string]any{
			"host":        normHost,
			"port":        port,
			"trust_state": trustState,
			"fingerprint": fingerprint,
		},
		CreatedAt: now,
	})

	return obs, nil
}

func (s *Service) getActiveTrustLocked(projectID, host string, port int) (SSHHostKeyTrust, bool) {
	for _, trust := range s.sshTrusts {
		if trust.ProjectID == projectID && trust.Host == host && trust.Port == port && trust.Status == TrustStatusActive {
			return trust, true
		}
	}
	return SSHHostKeyTrust{}, false
}

// GetSSHHostKeyObservation retrieves an observation by ID within a project.
func (s *Service) GetSSHHostKeyObservation(projectID, observationID string) (SSHHostKeyObservation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	obs, ok := s.sshObservations[observationID]
	if !ok || obs.ProjectID != projectID {
		return SSHHostKeyObservation{}, ErrNotFound
	}
	return obs, nil
}

// GetActiveSSHHostKeyTrust retrieves the currently active trust for (projectID, host, port).
func (s *Service) GetActiveSSHHostKeyTrust(projectID, host string, port int) (SSHHostKeyTrust, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	trust, ok := s.getActiveTrustLocked(projectID, normalizeHost(host), port)
	if !ok {
		return SSHHostKeyTrust{}, ErrNotFound
	}
	return trust, nil
}

// GetSSHHostKeyTrust retrieves a specific trust record by ID.
func (s *Service) GetSSHHostKeyTrust(projectID, trustID string) (SSHHostKeyTrust, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	trust, ok := s.sshTrusts[trustID]
	if !ok || trust.ProjectID != projectID {
		return SSHHostKeyTrust{}, ErrNotFound
	}
	return trust, nil
}

// ListSSHHostKeyTrusts returns all host key trusts for a project.
func (s *Service) ListSSHHostKeyTrusts(projectID string) ([]SSHHostKeyTrust, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var result []SSHHostKeyTrust
	for _, trust := range s.sshTrusts {
		if trust.ProjectID == projectID {
			result = append(result, trust)
		}
	}
	return result, nil
}

// ConfirmSSHHostKeyRotation confirms a rotation observation, superseding the old trust
// and establishing a new active trust.
func (s *Service) ConfirmSSHHostKeyRotation(projectID, observationID, actorID, expectedFingerprint, idempotencyKey string, now time.Time) (SSHHostKeyTrust, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if idempotencyKey != "" {
		scope := "confirm-rotation:" + projectID + ":" + observationID + ":" + idempotencyKey
		if prior, ok := s.idempotency[scope].(SSHHostKeyTrust); ok {
			return prior, nil
		}
	}

	project, ok := s.projects[projectID]
	if !ok {
		return SSHHostKeyTrust{}, ErrNotFound
	}

	obs, ok := s.sshObservations[observationID]
	if !ok || obs.ProjectID != projectID {
		return SSHHostKeyTrust{}, ErrNotFound
	}

	now = now.UTC()
	if !now.Before(obs.ExpiresAt) {
		return SSHHostKeyTrust{}, APIError{Status: 410, Code: "SSH_HOST_KEY_PROBE_EXPIRED", Message: "SSH host-key probe has expired"}
	}
	if obs.Status != ObservationStatusPending && obs.Status != ObservationStatusConfirmed {
		return SSHHostKeyTrust{}, APIError{Status: 409, Code: "SSH_HOST_KEY_PROBE_INVALID_STATUS", Message: "SSH host-key probe is not in a confirmable status"}
	}

	if expectedFingerprint != "" && expectedFingerprint != obs.Fingerprint {
		return SSHHostKeyTrust{}, APIError{Status: 400, Code: "FINGERPRINT_MISMATCH", Message: "provided fingerprint does not match the observed host key"}
	}

	// If already confirmed under same observation, return existing active trust
	if obs.Status == ObservationStatusConfirmed {
		if active, ok := s.getActiveTrustLocked(projectID, obs.Host, obs.Port); ok && active.Fingerprint == obs.Fingerprint {
			return active, nil
		}
	}

	// Supersede existing active trust for this endpoint
	if active, ok := s.getActiveTrustLocked(projectID, obs.Host, obs.Port); ok {
		active.Status = TrustStatusSuperseded
		active.SupersededAt = &now
		active.SupersededBy = actorID
		s.sshTrusts[active.ID] = active
	}

	// Create new active trust
	newTrust := SSHHostKeyTrust{
		ID:          newID("trust"),
		ProjectID:   projectID,
		Host:        obs.Host,
		Port:        obs.Port,
		Algorithm:   obs.Algorithm,
		PublicKey:   obs.PublicKey,
		Fingerprint: obs.Fingerprint,
		Status:      TrustStatusActive,
		CreatedBy:   actorID,
		CreatedAt:   now,
	}
	if s.sshTrusts == nil {
		s.sshTrusts = map[string]SSHHostKeyTrust{}
	}
	s.sshTrusts[newTrust.ID] = newTrust

	// Mark observation confirmed
	obs.Status = ObservationStatusConfirmed
	obs.UpdatedAt = now
	s.sshObservations[observationID] = obs

	if idempotencyKey != "" {
		scope := "confirm-rotation:" + projectID + ":" + observationID + ":" + idempotencyKey
		s.idempotency[scope] = newTrust
	}

	s.audit = append(s.audit, AuditEvent{
		ID:           newID("aud"),
		OrgID:        project.OrgID,
		ProjectID:    projectID,
		ActorType:    "user",
		ActorUserID:  actorID,
		Action:       "SSH_HOST_KEY_ROTATION_CONFIRMED",
		ResourceType: "ssh_host_key_trust",
		ResourceID:   newTrust.ID,
		Result:       "success",
		MetadataRedacted: map[string]any{
			"host":        newTrust.Host,
			"port":        newTrust.Port,
			"fingerprint": newTrust.Fingerprint,
			"probe_id":    observationID,
		},
		CreatedAt: now,
	})

	return newTrust, nil
}

// ResumeBootstrapSession resumes a session in waiting_host_key_confirmation with a confirmed probe.
func (s *Service) ResumeBootstrapSession(projectID, sessionID, observationID, actorID, idempotencyKey string, now time.Time) (BootstrapSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if idempotencyKey != "" {
		scope := "resume-bootstrap:" + projectID + ":" + sessionID + ":" + idempotencyKey
		if prior, ok := s.idempotency[scope].(BootstrapSession); ok {
			return prior, nil
		}
	}

	project, ok := s.projects[projectID]
	if !ok {
		return BootstrapSession{}, ErrNotFound
	}

	session, ok := s.bootstraps[sessionID]
	if !ok || session.ProjectID != projectID {
		return BootstrapSession{}, ErrNotFound
	}

	if session.Status != BootstrapWaitingHostKeyConfirmation {
		return BootstrapSession{}, APIError{Status: 409, Code: "BOOTSTRAP_NOT_WAITING_HOST_KEY_CONFIRMATION", Message: "bootstrap session is not waiting for host-key confirmation"}
	}

	obs, ok := s.sshObservations[observationID]
	if !ok || obs.ProjectID != projectID {
		return BootstrapSession{}, ErrNotFound
	}

	now = now.UTC()
	if !now.Before(obs.ExpiresAt) {
		return BootstrapSession{}, APIError{Status: 410, Code: "SSH_HOST_KEY_PROBE_EXPIRED", Message: "SSH host-key probe has expired"}
	}
	if obs.Status != ObservationStatusConfirmed {
		return BootstrapSession{}, APIError{Status: 409, Code: "SSH_HOST_KEY_PROBE_NOT_CONFIRMED", Message: "SSH host-key probe must be confirmed before resuming"}
	}
	if normalizeHost(session.PublicHost) != obs.Host || session.SSHPort != obs.Port {
		return BootstrapSession{}, APIError{Status: 400, Code: "SSH_HOST_KEY_ENDPOINT_MISMATCH", Message: "probe endpoint does not match bootstrap session"}
	}

	activeTrust, ok := s.getActiveTrustLocked(projectID, obs.Host, obs.Port)
	if !ok || activeTrust.Fingerprint != obs.Fingerprint {
		return BootstrapSession{}, APIError{Status: 409, Code: "SSH_HOST_KEY_ACTIVE_TRUST_MISMATCH", Message: "active trust does not match confirmed probe"}
	}

	// Resume the session using the same checkpoint
	session.SSHHostKeyTrustID = activeTrust.ID
	session.ResolvedIP = obs.ResolvedIP
	session.Status = BootstrapPending
	session.LastFailureCode = ""
	session.LastFailureRedacted = ""
	session.UpdatedAt = now
	clearBootstrapLease(&session)

	s.bootstraps[sessionID] = session
	s.appendBootstrapDurabilityEventLocked(session, "info", "resumed", "bootstrap session resumed with confirmed SSH host key", now)

	if idempotencyKey != "" {
		scope := "resume-bootstrap:" + projectID + ":" + sessionID + ":" + idempotencyKey
		s.idempotency[scope] = session
	}

	s.audit = append(s.audit, AuditEvent{
		ID:           newID("aud"),
		OrgID:        project.OrgID,
		ProjectID:    projectID,
		ActorType:    "user",
		ActorUserID:  actorID,
		Action:       "BOOTSTRAP_SESSION_RESUMED",
		ResourceType: "bootstrap_session",
		ResourceID:   session.ID,
		Result:       "success",
		MetadataRedacted: map[string]any{
			"host":     session.PublicHost,
			"port":     session.SSHPort,
			"trust_id": activeTrust.ID,
			"probe_id": observationID,
		},
		CreatedAt: now,
	})

	return session, nil
}
