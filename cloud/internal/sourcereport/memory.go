package sourcereport

import (
	"context"
	"crypto/rand"
	"sync"
	"time"
)

type MemoryStore struct {
	mu      sync.RWMutex
	reports map[string]Report // key: project:app:repo:commit:root:version
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		reports: make(map[string]Report),
	}
}

func reportKey(projectID, applicationID string, repoID int64, commitSHA, root, version string) string {
	return projectID + ":" + applicationID + ":" + commitSHA + ":" + root + ":" + version
}

func (m *MemoryStore) Upsert(_ context.Context, r Report) (Report, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r.ID == "" {
		id, _ := newOpaqueID(rand.Reader, "srr-")
		r.ID = id
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	k := reportKey(r.ProjectID, r.ApplicationID, r.RepositoryID, r.CommitSHA, r.ApplicationRoot, r.ScannerVersion)
	_, exists := m.reports[k]
	m.reports[k] = r
	return r, !exists, nil
}

func (m *MemoryStore) GetLatest(_ context.Context, projectID, applicationID string, repositoryID int64, commitSHA, applicationRoot, scannerVersion string) (Report, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	k := reportKey(projectID, applicationID, repositoryID, commitSHA, applicationRoot, scannerVersion)
	r, ok := m.reports[k]
	if !ok {
		return Report{}, ErrNotFound
	}
	return r, nil
}

func (m *MemoryStore) GetForBuildJob(_ context.Context, buildJobID string) (Report, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, r := range m.reports {
		if r.BuildJobID == buildJobID {
			return r, nil
		}
	}
	return Report{}, ErrNotFound
}

func (m *MemoryStore) GetForCommit(_ context.Context, projectID, applicationID, commitSHA string) (Report, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var latest Report
	found := false
	for _, r := range m.reports {
		if r.ProjectID == projectID && r.ApplicationID == applicationID && r.CommitSHA == commitSHA {
			if !found || r.CreatedAt.After(latest.CreatedAt) {
				latest = r
				found = true
			}
		}
	}
	if !found {
		return Report{}, ErrNotFound
	}
	return latest, nil
}
