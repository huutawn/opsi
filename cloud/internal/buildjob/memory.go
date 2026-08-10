package buildjob

import (
	"context"
	"sort"
	"sync"
)

type MemoryStore struct {
	mu            sync.Mutex
	byID          map[string]Job
	byIdempotency map[string]string
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{byID: map[string]Job{}, byIdempotency: map[string]string{}}
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
