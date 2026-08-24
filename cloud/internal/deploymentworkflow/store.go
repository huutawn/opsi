package deploymentworkflow

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

var ErrNotFound = errors.New("deployment run not found")
var ErrConflict = errors.New("deployment run revision conflict")

type Store interface {
	Create(context.Context, Run, Event, string) (Run, bool, error)
	Get(context.Context, string, string) (Run, error)
	List(context.Context, string, int) ([]Run, error)
	Save(context.Context, Run, uint64, Event) (Run, error)
	Events(context.Context, string, string) ([]Event, error)
	AcquireLease(context.Context, string, string, string, time.Time, time.Duration) (Run, bool, error)
	RenewLease(context.Context, string, string, string, time.Time, time.Duration) (bool, error)
	ReleaseLease(context.Context, string, string, string) error
	Runnable(context.Context, int) ([]Run, error)
}

type memoryRecord struct {
	run            Run
	key            string
	leaseOwner     string
	leaseExpiresAt time.Time
}
type MemoryStore struct {
	mu      sync.Mutex
	records map[string]memoryRecord
	events  map[string][]Event
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{records: map[string]memoryRecord{}, events: map[string][]Event{}}
}
func runKey(projectID, runID string) string { return projectID + "\x00" + runID }

func (s *MemoryStore) Create(_ context.Context, run Run, event Event, key string) (Run, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range s.records {
		if record.run.ProjectID == run.ProjectID && record.key == key {
			return record.run, true, nil
		}
	}
	k := runKey(run.ProjectID, run.ID)
	if _, ok := s.records[k]; ok {
		return Run{}, false, ErrConflict
	}
	s.records[k] = memoryRecord{run: run, key: key}
	s.events[k] = append(s.events[k], event)
	return run, false, nil
}
func (s *MemoryStore) Get(_ context.Context, projectID, runID string) (Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[runKey(projectID, runID)]
	if !ok {
		return Run{}, ErrNotFound
	}
	return normalizeStoredRun(record.run), nil
}
func (s *MemoryStore) List(_ context.Context, projectID string, limit int) ([]Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Run{}
	for _, record := range s.records {
		if record.run.ProjectID == projectID {
			out = append(out, normalizeStoredRun(record.run))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (s *MemoryStore) Save(_ context.Context, run Run, expected uint64, event Event) (Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := runKey(run.ProjectID, run.ID)
	record, ok := s.records[k]
	if !ok {
		return Run{}, ErrNotFound
	}
	if record.run.Revision != expected {
		return Run{}, ErrConflict
	}
	run.Revision = expected + 1
	record.run = run
	s.records[k] = record
	if event.ID != "" {
		s.events[k] = append(s.events[k], event)
	}
	return run, nil
}
func (s *MemoryStore) Events(_ context.Context, projectID, runID string) ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := runKey(projectID, runID)
	if _, ok := s.records[k]; !ok {
		return nil, ErrNotFound
	}
	return append([]Event(nil), s.events[k]...), nil
}
func (s *MemoryStore) AcquireLease(_ context.Context, projectID, runID, owner string, now time.Time, ttl time.Duration) (Run, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := runKey(projectID, runID)
	record, ok := s.records[k]
	if !ok {
		return Run{}, false, ErrNotFound
	}
	if record.leaseOwner != "" && record.leaseOwner != owner && record.leaseExpiresAt.After(now) {
		return record.run, false, nil
	}
	record.leaseOwner = owner
	record.leaseExpiresAt = now.Add(ttl)
	s.records[k] = record
	return normalizeStoredRun(record.run), true, nil
}
func (s *MemoryStore) ReleaseLease(_ context.Context, projectID, runID, owner string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := runKey(projectID, runID)
	record, ok := s.records[k]
	if !ok {
		return ErrNotFound
	}
	if record.leaseOwner == owner {
		record.leaseOwner = ""
		record.leaseExpiresAt = time.Time{}
		s.records[k] = record
	}
	return nil
}
func (s *MemoryStore) RenewLease(_ context.Context, projectID, runID, owner string, now time.Time, ttl time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := runKey(projectID, runID)
	record, ok := s.records[k]
	if !ok {
		return false, ErrNotFound
	}
	if record.leaseOwner != owner || !record.leaseExpiresAt.After(now) {
		return false, nil
	}
	record.leaseExpiresAt = now.Add(ttl)
	s.records[k] = record
	return true, nil
}
func (s *MemoryStore) Runnable(_ context.Context, limit int) ([]Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Run{}
	for _, record := range s.records {
		if normalized := normalizeStoredRun(record.run); Runnable(normalized.State) {
			out = append(out, normalized)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.Before(out[j].UpdatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
