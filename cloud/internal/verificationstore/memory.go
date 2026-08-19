package verificationstore

import (
	"context"
	"crypto/rand"
	"sync"
	"time"

	verificationv1 "github.com/opsi-dev/opsi/contracts/go/verificationv1"
)

type MemoryStore struct {
	mu   sync.RWMutex
	runs map[string]verificationv1.VerificationRun // key: id
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		runs: make(map[string]verificationv1.VerificationRun),
	}
}

func (m *MemoryStore) Create(_ context.Context, r verificationv1.VerificationRun) (verificationv1.VerificationRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r.ID == "" {
		id, _ := newOpaqueID(rand.Reader, "dvr-")
		r.ID = id
	}
	r.SchemaVersion = verificationv1.SchemaVersion
	if r.StartedAt.IsZero() {
		r.StartedAt = time.Now().UTC()
	}
	m.runs[r.ID] = r
	return r, nil
}

func (m *MemoryStore) Update(_ context.Context, r verificationv1.VerificationRun) (verificationv1.VerificationRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.runs[r.ID]; !ok {
		return r, ErrNotFound
	}
	m.runs[r.ID] = r
	return r, nil
}

func (m *MemoryStore) Get(_ context.Context, projectID, runID string) (verificationv1.VerificationRun, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.runs[runID]
	if !ok || r.ProjectID != projectID {
		return verificationv1.VerificationRun{}, ErrNotFound
	}
	return r, nil
}

func (m *MemoryStore) GetLatest(_ context.Context, projectID, environmentID, consumerApplicationID, dependencyLogicalName string) (verificationv1.VerificationRun, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var latest verificationv1.VerificationRun
	found := false
	for _, r := range m.runs {
		if r.ProjectID == projectID && r.EnvironmentID == environmentID && r.ConsumerApplicationID == consumerApplicationID && r.DependencyLogicalName == dependencyLogicalName {
			if !found || r.StartedAt.After(latest.StartedAt) {
				latest = r
				found = true
			}
		}
	}
	if !found {
		return verificationv1.VerificationRun{}, ErrNotFound
	}
	return latest, nil
}

func (m *MemoryStore) ListForDeployment(_ context.Context, projectID, deploymentJobID string) ([]verificationv1.VerificationRun, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var runs []verificationv1.VerificationRun
	for _, r := range m.runs {
		if r.ProjectID == projectID && r.DeploymentJobID == deploymentJobID {
			runs = append(runs, r)
		}
	}
	return runs, nil
}
