package buildjob

import (
	"bytes"
	"context"
	"sort"
	"sync"
	"time"
)

type MemoryStore struct {
	mu            sync.Mutex
	byID          map[string]Job
	byIdempotency map[string]string
	attempts      map[string]DispatchAttempt
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{byID: map[string]Job{}, byIdempotency: map[string]string{}, attempts: map[string]DispatchAttempt{}}
}

func (s *MemoryStore) ReserveDispatch(_ context.Context, projectID, applicationID string, attempt DispatchAttempt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.byID[attempt.BuildJobID]
	if !ok || job.ProjectID != projectID || job.ApplicationID != applicationID {
		return Error{Code: "BUILD_JOB_NOT_FOUND", Status: 404, Message: "BuildJob was not found.", Cause: "build_job"}
	}
	if err := validateDispatchableJob(job); err != nil {
		return err
	}
	for _, current := range s.attempts {
		if current.BuildJobID == job.ID && (current.LastState == DispatchStateDispatching || current.LastState == DispatchStateDispatched || current.LastState == DispatchStateClaimed) {
			return Error{Code: "DUPLICATE_ACTIVE_DISPATCH", Status: 409, Message: "BuildJob already has an active dispatch attempt.", Cause: "executor_dispatch"}
		}
	}
	s.attempts[attempt.AttemptID] = attempt
	return nil
}

func (s *MemoryStore) CompleteDispatch(_ context.Context, attemptID string, facts DispatchFacts, now time.Time) (DispatchAttempt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	attempt, ok := s.attempts[attemptID]
	if !ok || attempt.LastState != DispatchStateDispatching {
		return DispatchAttempt{}, unavailable()
	}
	attempt.LastState = DispatchStateDispatched
	attempt.DispatchedAt = now
	attempt.RunID = facts.RunID
	attempt.RunAttempt = facts.RunAttempt
	attempt.RunURL = facts.RunURL
	s.attempts[attemptID] = attempt
	return attempt, nil
}

func (s *MemoryStore) RejectDispatch(_ context.Context, attemptID, code string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	attempt, ok := s.attempts[attemptID]
	if !ok {
		return unavailable()
	}
	attempt.LastState = DispatchStateRejected
	attempt.FailureCode = code
	attempt.CompletedAt = &now
	s.attempts[attemptID] = attempt
	return nil
}

func (s *MemoryStore) ClaimDispatch(_ context.Context, jobID, attemptID string, identity RunnerIdentity, leaseHash []byte, expiresAt, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	attempt, ok := s.attempts[attemptID]
	if !ok || attempt.BuildJobID != jobID {
		return Error{Code: "EXECUTOR_RUN_MISMATCH", Status: 409, Message: "Executor run does not match the dispatch attempt.", Cause: "executor_run"}
	}
	if attempt.ClaimedAt != nil || attempt.LastState == DispatchStateClaimed {
		return Error{Code: "RUNNER_CLAIM_CONSUMED", Status: 409, Message: "Runner claim was already consumed.", Cause: "runner_claim"}
	}
	if attempt.LastState != DispatchStateDispatched || attempt.RunID != 0 && attempt.RunID != identity.RunID || attempt.RunAttempt != 0 && attempt.RunAttempt != identity.RunAttempt {
		return Error{Code: "EXECUTOR_RUN_MISMATCH", Status: 409, Message: "Executor run does not match the dispatch attempt.", Cause: "executor_run"}
	}
	job, ok := s.byID[jobID]
	if !ok || job.Status != StatusReady {
		return Error{Code: "BUILD_JOB_NOT_READY", Status: 409, Message: "BuildJob is not ready for runner claim.", Cause: "build_job_status"}
	}
	job.Status = StatusRunning
	job.UpdatedAt = now
	s.byID[jobID] = job
	attempt.LastState = DispatchStateClaimed
	attempt.RunID = identity.RunID
	attempt.RunAttempt = identity.RunAttempt
	attempt.RunURL = "https://github.com/" + identity.Repository + "/actions/runs/" + uintString(identity.RunID)
	attempt.ClaimedAt = &now
	attempt.LeaseHash = append([]byte(nil), leaseHash...)
	attempt.LeaseExpiresAt = expiresAt
	s.attempts[attemptID] = attempt
	return nil
}

func (s *MemoryStore) GetBuildSpec(_ context.Context, jobID string, leaseHash []byte, now time.Time) (BuildSpec, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, attempt := range s.attempts {
		if !bytes.Equal(attempt.LeaseHash, leaseHash) {
			continue
		}
		if attempt.BuildJobID != jobID {
			return BuildSpec{}, Error{Code: "RUNNER_LEASE_SCOPE_MISMATCH", Status: 403, Message: "Runner lease cannot access this BuildJob.", Cause: "runner_lease_scope"}
		}
		if attempt.LeaseExpiresAt.IsZero() || !now.Before(attempt.LeaseExpiresAt) {
			return BuildSpec{}, Error{Code: "RUNNER_LEASE_EXPIRED", Status: 401, Message: "Runner lease has expired.", Cause: "runner_lease"}
		}
		if attempt.LastState != DispatchStateClaimed {
			return BuildSpec{}, Error{Code: "RUNNER_LEASE_REVOKED", Status: 409, Message: "Runner lease is no longer valid for this dispatch attempt.", Cause: "runner_lease"}
		}
		job, ok := s.byID[jobID]
		if !ok || job.Status != StatusRunning {
			return BuildSpec{}, Error{Code: "RUNNER_LEASE_REVOKED", Status: 409, Message: "Runner lease is no longer valid for this BuildJob.", Cause: "build_job_status"}
		}
		return buildSpec(job), nil
	}
	return BuildSpec{}, Error{Code: "RUNNER_LEASE_INVALID", Status: 401, Message: "Runner lease is invalid.", Cause: "runner_lease"}
}

func (s *MemoryStore) Create(_ context.Context, job Job) (Job, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := idempotencyScope(job.ProjectID, job.ApplicationID, job.IdempotencyKey)
	if id, ok := s.byIdempotency[key]; ok {
		return s.byID[id], true, nil
	}
	s.byID[job.ID] = job
	s.byIdempotency[key] = job.ID
	return job, false, nil
}

func (s *MemoryStore) Get(_ context.Context, projectID, applicationID, jobID string) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.byID[jobID]
	if !ok || job.ProjectID != projectID || job.ApplicationID != applicationID {
		return Job{}, Error{Code: "BUILD_JOB_NOT_FOUND", Status: 404, Message: "BuildJob was not found.", Cause: "build_job"}
	}
	return job, nil
}

func (s *MemoryStore) GetByIdempotency(_ context.Context, projectID, applicationID, key string) (Job, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.byIdempotency[idempotencyScope(projectID, applicationID, key)]
	if !ok {
		return Job{}, false, nil
	}
	return s.byID[id], true, nil
}

func (s *MemoryStore) List(_ context.Context, projectID, applicationID, status string, limit int) ([]Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs := make([]Job, 0)
	for _, job := range s.byID {
		if job.ProjectID == projectID && job.ApplicationID == applicationID && (status == "" || job.Status == status) {
			jobs = append(jobs, job)
		}
	}
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].CreatedAt.Equal(jobs[j].CreatedAt) {
			return jobs[i].ID > jobs[j].ID
		}
		return jobs[i].CreatedAt.After(jobs[j].CreatedAt)
	})
	if len(jobs) > limit {
		jobs = jobs[:limit]
	}
	return jobs, nil
}
