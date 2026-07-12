package registry

import (
	"errors"
	"time"
)

func bootstrapRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := bootstrapRetryBaseDelay
	for i := 1; i < attempt && delay < bootstrapRetryMaximumDelay; i++ {
		delay *= 2
		if delay > bootstrapRetryMaximumDelay {
			return bootstrapRetryMaximumDelay
		}
	}
	return delay
}

func (s *Service) RenewBootstrapLease(projectID, sessionID, workerID, rawLeaseToken string, now time.Time, leaseDuration time.Duration) (BootstrapSession, error) {
	if leaseDuration <= 0 {
		return BootstrapSession{}, errors.New("bootstrap lease duration must be positive")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.bootstraps[sessionID]
	if !ok || session.ProjectID != projectID {
		return BootstrapSession{}, ErrNotFound
	}
	if err := validateBootstrapLease(session, workerID, rawLeaseToken, now); err != nil {
		return BootstrapSession{}, err
	}
	now = now.UTC()
	expiresAt := now.Add(leaseDuration)
	session.LeaseHeartbeatAt = &now
	session.LeaseExpiresAt = &expiresAt
	session.UpdatedAt = now
	s.bootstraps[sessionID] = session
	return session, nil
}

func (s *Service) RecoverExpiredBootstrapLeases(now time.Time) (BootstrapRecoverySummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recoverExpiredBootstrapLeasesLocked(now.UTC()), nil
}

func (s *Service) recoverExpiredBootstrapLeasesLocked(now time.Time) BootstrapRecoverySummary {
	var summary BootstrapRecoverySummary
	for id, session := range s.bootstraps {
		if !isLeasedBootstrapStatus(session.Status) || session.LeaseExpiresAt == nil || session.LeaseExpiresAt.After(now) {
			continue
		}
		session.LastFailureCode = "BOOTSTRAP_LEASE_EXPIRED"
		session.LastFailureRedacted = "bootstrap worker lease expired"
		clearBootstrapLease(&session)
		session.UpdatedAt = now
		if !now.Before(session.ExpiresAt) {
			session.Status = "expired"
			session.FinishedAt = &now
			summary.Expired = append(summary.Expired, session)
			s.appendBootstrapDurabilityEventLocked(session, "warn", "BOOTSTRAP_LEASE_EXPIRED", "bootstrap session expired after worker lease loss", now)
		} else if session.AttemptCount >= effectiveBootstrapMaxAttempts(session.MaxAttempts) {
			session.Status = BootstrapDeadLetter
			session.DeadLetteredAt = &now
			session.FinishedAt = &now
			summary.DeadLettered = append(summary.DeadLettered, session)
			s.appendBootstrapDurabilityEventLocked(session, "error", "BOOTSTRAP_DEAD_LETTERED", session.LastFailureRedacted, now)
		} else {
			next := now.Add(bootstrapRetryDelay(session.AttemptCount))
			session.Status = BootstrapRetryWait
			session.NextAttemptAt = &next
			session.FinishedAt = nil
			summary.Recovered = append(summary.Recovered, session)
			s.appendBootstrapDurabilityEventLocked(session, "warn", "BOOTSTRAP_RETRY_SCHEDULED", session.LastFailureRedacted, now)
		}
		s.bootstraps[id] = session
		action, result := "BOOTSTRAP_RETRY_SCHEDULED", "success"
		if session.Status == BootstrapDeadLetter {
			action, result = "BOOTSTRAP_DEAD_LETTERED", "failure"
		} else if session.Status == "expired" {
			action, result = "BOOTSTRAP_LEASE_EXPIRED", "failure"
		}
		s.appendBootstrapAuditLocked(session, action, result, map[string]any{"attempt_count": session.AttemptCount, "max_attempts": session.MaxAttempts, "failure_code": session.LastFailureCode, "next_attempt_at": session.NextAttemptAt})
		s.refreshProjectLocked(session.ProjectID)
	}
	for id, session := range s.bootstraps {
		if !isActiveBootstrap(session.Status) || isLeasedBootstrapStatus(session.Status) || now.Before(session.ExpiresAt) {
			continue
		}
		session.Status = "expired"
		session.UpdatedAt = now
		session.FinishedAt = &now
		clearBootstrapLease(&session)
		s.bootstraps[id] = session
		summary.Expired = append(summary.Expired, session)
		s.appendBootstrapDurabilityEventLocked(session, "warn", "expired", "bootstrap session expired", now)
		s.appendBootstrapAuditLocked(session, "BOOTSTRAP_LEASE_EXPIRED", "failure", map[string]any{"failure_code": "BOOTSTRAP_SESSION_EXPIRED"})
		s.refreshProjectLocked(session.ProjectID)
	}
	return summary
}

func (s *Service) FinishBootstrapSessionForLease(projectID, sessionID, workerID, leaseToken string, result BootstrapFinishResult, now time.Time) (BootstrapSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.bootstraps[sessionID]
	if !ok || session.ProjectID != projectID {
		return BootstrapSession{}, ErrNotFound
	}
	if err := validateBootstrapLease(session, workerID, leaseToken, now); err != nil {
		return BootstrapSession{}, err
	}
	now = now.UTC()
	if result.Status == "completed" || result.Status == "succeeded" {
		session.Status = result.Status
		session.FinishedAt = &now
		session.NextAttemptAt = nil
		session.DeadLetteredAt = nil
		session.LastFailureCode = ""
		session.LastFailureRedacted = ""
		clearBootstrapLease(&session)
		session.UpdatedAt = now
		s.bootstraps[sessionID] = session
		s.appendBootstrapDurabilityEventLocked(session, "info", result.Status, "bootstrap completed after verified Agent heartbeat", now)
		s.refreshProjectLocked(projectID)
		return session, nil
	}
	if result.Status == "cancelled" {
		session.Status = "cancelled"
		session.FinishedAt = &now
		clearBootstrapLease(&session)
		session.UpdatedAt = now
		s.bootstraps[sessionID] = session
		s.appendBootstrapDurabilityEventLocked(session, "warn", "cancelled", RedactString(result.MessageRedacted), now)
		s.refreshProjectLocked(projectID)
		return session, nil
	}
	if result.Status != "failed" || result.FailureCode == "" {
		return BootstrapSession{}, APIError{Status: 400, Code: "INVALID_BOOTSTRAP_FINISH", Message: "failed bootstrap finish requires failure_code"}
	}
	session.LastFailureCode = result.FailureCode
	session.LastFailureRedacted = RedactString(result.MessageRedacted)
	if session.LastFailureRedacted == "" {
		session.LastFailureRedacted = "bootstrap attempt failed"
	}
	clearBootstrapLease(&session)
	session.UpdatedAt = now
	if result.Retryable && session.AttemptCount < effectiveBootstrapMaxAttempts(session.MaxAttempts) && now.Before(session.ExpiresAt) {
		next := now.Add(bootstrapRetryDelay(session.AttemptCount))
		session.Status = BootstrapRetryWait
		session.NextAttemptAt = &next
		session.FinishedAt = nil
		s.appendBootstrapDurabilityEventLocked(session, "warn", "BOOTSTRAP_RETRY_SCHEDULED", session.LastFailureRedacted, now)
	} else {
		session.Status = BootstrapDeadLetter
		session.DeadLetteredAt = &now
		session.FinishedAt = &now
		s.appendBootstrapDurabilityEventLocked(session, "error", "BOOTSTRAP_DEAD_LETTERED", session.LastFailureRedacted, now)
	}
	s.bootstraps[sessionID] = session
	s.refreshProjectLocked(projectID)
	return session, nil
}

func (s *Service) ManualRetryBootstrapSession(projectID, sessionID, idempotencyKey string, now time.Time) (BootstrapManualRetryResult, error) {
	if idempotencyKey == "" {
		return BootstrapManualRetryResult{}, APIError{Status: 400, Code: "IDEMPOTENCY_KEY_REQUIRED", Message: "Idempotency-Key is required"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	scope := "bootstrap-retry:" + projectID + ":" + sessionID + ":" + idempotencyKey
	if prior, ok := s.idempotency[scope].(BootstrapSession); ok {
		return BootstrapManualRetryResult{Session: prior}, nil
	}
	session, ok := s.bootstraps[sessionID]
	if !ok || session.ProjectID != projectID {
		return BootstrapManualRetryResult{}, ErrNotFound
	}
	if session.Status != BootstrapDeadLetter {
		return BootstrapManualRetryResult{}, APIError{Status: 409, Code: "BOOTSTRAP_NOT_DEAD_LETTER", Message: "bootstrap session is not dead-lettered"}
	}
	now = now.UTC()
	if !now.Before(session.ExpiresAt) {
		return BootstrapManualRetryResult{}, APIError{Status: 409, Code: "BOOTSTRAP_SESSION_EXPIRED", Message: "expired bootstrap session cannot be retried"}
	}
	session.Status = BootstrapPending
	session.AttemptCount = 0
	session.NextAttemptAt = nil
	session.DeadLetteredAt = nil
	session.FinishedAt = nil
	clearBootstrapLease(&session)
	session.UpdatedAt = now
	s.bootstraps[sessionID] = session
	s.idempotency[scope] = session
	s.appendBootstrapDurabilityEventLocked(session, "info", "BOOTSTRAP_MANUAL_RETRY_REQUESTED", "bootstrap session manually returned to pending", now)
	s.refreshProjectLocked(projectID)
	return BootstrapManualRetryResult{Session: session, Applied: true}, nil
}

func effectiveBootstrapMaxAttempts(value int) int {
	if value <= 0 {
		return defaultBootstrapMaxAttempts
	}
	return value
}

func clearBootstrapLease(session *BootstrapSession) {
	session.LeaseOwner = ""
	session.LeaseTokenHash = ""
	session.LeaseExpiresAt = nil
	session.LeaseHeartbeatAt = nil
	session.LeasedAt = nil
}

func (s *Service) appendBootstrapDurabilityEventLocked(session BootstrapSession, level, step, message string, now time.Time) {
	s.events[session.ID] = append(s.events[session.ID], BootstrapEvent{ID: newID("evt"), OrgID: session.OrgID, ProjectID: session.ProjectID, SessionID: session.ID, NodeID: session.NodeID, Level: level, Step: step, MessageRedacted: RedactString(message), ProgressPercent: bootstrapProgress(session.Status), CreatedAt: now})
}

func (s *Service) appendBootstrapAuditLocked(session BootstrapSession, action, result string, metadata map[string]any) {
	s.audit = append(s.audit, AuditEvent{ID: newID("aud"), OrgID: session.OrgID, ProjectID: session.ProjectID, ActorType: "worker", Action: action, ResourceType: "bootstrap_session", ResourceID: session.ID, Result: result, MetadataRedacted: RedactMap(metadata), CreatedAt: session.UpdatedAt})
}
