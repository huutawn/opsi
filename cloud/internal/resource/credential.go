package resource

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

type MemoryCredentialAuthority struct {
	mu              sync.Mutex
	credentials     map[string]resourcev1.ManagedResourceCredential
	workloadSecrets map[string]resourcev1.WorkloadSecretMetadata
	replays         map[string]string
}

func NewMemoryCredentialAuthority() *MemoryCredentialAuthority {
	return &MemoryCredentialAuthority{credentials: map[string]resourcev1.ManagedResourceCredential{}, workloadSecrets: map[string]resourcev1.WorkloadSecretMetadata{}, replays: map[string]string{}}
}

func (a *MemoryCredentialAuthority) Ensure(_ context.Context, id string) (resourcev1.ManagedResourceCredential, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	resourceID := strings.TrimPrefix(id, "mrcred-")
	if credential, ok := a.credentials[id]; ok {
		if (credential.Purpose != "" && credential.Purpose != resourcev1.CredentialPurposeResourceManagement) || (credential.OwnerID != "" && credential.OwnerID != resourceID) || (credential.ResourceID != "" && credential.ResourceID != resourceID) {
			return resourcev1.ManagedResourceCredential{}, errors.New("management credential identity conflict")
		}
		credential.Purpose, credential.OwnerID, credential.ResourceID = resourcev1.CredentialPurposeResourceManagement, resourceID, resourceID
		a.credentials[id] = credential
		return credential, nil
	}
	password := make([]byte, 32)
	if _, err := rand.Read(password); err != nil {
		return resourcev1.ManagedResourceCredential{}, err
	}
	credential := resourcev1.ManagedResourceCredential{CredentialID: id, Purpose: resourcev1.CredentialPurposeResourceManagement, OwnerID: resourceID, ResourceID: resourceID, Username: "opsi", Password: base64.RawURLEncoding.EncodeToString(password), Database: "opsi"}
	a.credentials[id] = credential
	return credential, nil
}

func (a *MemoryCredentialAuthority) EnsureBinding(_ context.Context, spec resourcev1.BindingCredentialSpec) (resourcev1.ManagedResourceCredential, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if credential, ok := a.credentials[spec.CredentialID]; ok {
		if credential.ValidateBinding(spec.BindingID, spec.ResourceID) != nil || credential.Username != spec.Username || credential.Database != spec.Database {
			return resourcev1.ManagedResourceCredential{}, errors.New("binding credential identity conflict")
		}
		return credential, nil
	}
	password := make([]byte, 32)
	if _, err := rand.Read(password); err != nil {
		return resourcev1.ManagedResourceCredential{}, err
	}
	credential := resourcev1.ManagedResourceCredential{CredentialID: spec.CredentialID, Purpose: resourcev1.CredentialPurposeResourceBinding, OwnerID: spec.BindingID, ResourceID: spec.ResourceID, Username: spec.Username, Password: base64.RawURLEncoding.EncodeToString(password), Database: spec.Database}
	a.credentials[spec.CredentialID] = credential
	return credential, nil
}

func (a *MemoryCredentialAuthority) ListWorkloadSecrets(_ context.Context, projectID, serviceID string) ([]resourcev1.WorkloadSecretMetadata, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := []resourcev1.WorkloadSecretMetadata{}
	for _, metadata := range a.workloadSecrets {
		if metadata.ProjectID == projectID && metadata.ServiceID == serviceID {
			out = append(out, metadata)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LogicalName < out[j].LogicalName })
	return out, nil
}

func (a *MemoryCredentialAuthority) GetWorkloadSecret(_ context.Context, projectID, serviceID, logicalName string) (resourcev1.WorkloadSecretMetadata, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	metadata, ok := a.workloadSecrets[workloadSecretScope(projectID, serviceID, logicalName)]
	if !ok {
		return resourcev1.WorkloadSecretMetadata{}, errors.New("workload secret not found")
	}
	return metadata, nil
}

func (a *MemoryCredentialAuthority) UpsertWorkloadSecret(_ context.Context, spec resourcev1.WorkloadSecretUpsert) (resourcev1.WorkloadSecretMetadata, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if spec.CredentialID == "" || spec.ProjectID == "" || spec.ServiceID == "" || resourcev1.ValidateWorkloadSecretLogicalName(spec.LogicalName) != nil || spec.IdempotencyKey == "" || spec.Value == "" || len(spec.Value) > 8192 || strings.ContainsAny(spec.Value, "\x00\r\n") {
		return resourcev1.WorkloadSecretMetadata{}, false, errors.New("workload secret upsert is invalid")
	}
	payload := sha256.Sum256([]byte(spec.ProjectID + "\x00" + spec.ServiceID + "\x00" + spec.LogicalName + "\x00" + spec.Value))
	replayKey := spec.ProjectID + "\x00" + spec.IdempotencyKey
	if previous, ok := a.replays[replayKey]; ok {
		if previous != hex.EncodeToString(payload[:]) {
			return resourcev1.WorkloadSecretMetadata{}, false, errors.New("workload secret idempotency conflict")
		}
		metadata, found := a.workloadSecrets[workloadSecretScope(spec.ProjectID, spec.ServiceID, spec.LogicalName)]
		if !found {
			for _, candidate := range a.workloadSecrets {
				if candidate.ID == spec.CredentialID && candidate.ProjectID == spec.ProjectID && candidate.LogicalName == spec.LogicalName {
					metadata, found = candidate, true
					break
				}
			}
		}
		return metadata, found, nil
	}
	key := workloadSecretScope(spec.ProjectID, spec.ServiceID, spec.LogicalName)
	current := a.workloadSecrets[key]
	revision := current.Revision + 1
	if revision == 0 {
		revision = 1
	}
	credential := resourcev1.ManagedResourceCredential{CredentialID: spec.CredentialID, Purpose: resourcev1.CredentialPurposeWorkloadSecret, OwnerID: spec.ServiceID, ResourceID: spec.ProjectID, Username: "value", Password: spec.Value}
	if err := credential.ValidateWorkloadSecret(spec.ProjectID, spec.ServiceID); err != nil {
		return resourcev1.WorkloadSecretMetadata{}, false, err
	}
	a.credentials[spec.CredentialID] = credential
	metadata := workloadSecretMetadata(spec.CredentialID, spec.ProjectID, spec.ServiceID, spec.LogicalName, revision, time.Now().UTC())
	a.workloadSecrets[key] = metadata
	a.replays[replayKey] = hex.EncodeToString(payload[:])
	return metadata, false, nil
}

func (a *MemoryCredentialAuthority) BindWorkloadSecret(_ context.Context, projectID, currentScope, serviceID, logicalName string) (resourcev1.WorkloadSecretMetadata, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	currentKey := workloadSecretScope(projectID, currentScope, logicalName)
	metadata, ok := a.workloadSecrets[currentKey]
	if !ok {
		return resourcev1.WorkloadSecretMetadata{}, errors.New("workload secret not found")
	}
	targetKey := workloadSecretScope(projectID, serviceID, logicalName)
	if target, exists := a.workloadSecrets[targetKey]; exists && target.ID != metadata.ID {
		return resourcev1.WorkloadSecretMetadata{}, errors.New("workload secret binding conflict")
	}
	credential, ok := a.credentials[metadata.ID]
	if !ok || credential.ValidateWorkloadSecret(projectID, currentScope) != nil {
		return resourcev1.WorkloadSecretMetadata{}, errors.New("workload secret credential is invalid")
	}
	credential.OwnerID = serviceID
	a.credentials[metadata.ID] = credential
	delete(a.workloadSecrets, currentKey)
	metadata.ServiceID = serviceID
	metadata.UpdatedAt = time.Now().UTC()
	a.workloadSecrets[targetKey] = metadata
	return metadata, nil
}

func workloadSecretScope(projectID, serviceID, logicalName string) string {
	return projectID + "\x00" + serviceID + "\x00" + logicalName
}
func workloadSecretMetadata(id, projectID, serviceID, logicalName string, revision uint64, now time.Time) resourcev1.WorkloadSecretMetadata {
	return resourcev1.WorkloadSecretMetadata{ID: id, Reference: "workload-secret://" + id, ProjectID: projectID, ServiceID: serviceID, LogicalName: logicalName, Revision: revision, Status: "ready", UpdatedAt: now}
}

func (a *MemoryCredentialAuthority) EnsureWorkloadSecret(_ context.Context, spec resourcev1.WorkloadSecretSpec) (resourcev1.ManagedResourceCredential, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if credential, ok := a.credentials[spec.CredentialID]; ok {
		if credential.ValidateWorkloadSecret(spec.ProjectID, spec.ServiceID) != nil {
			return resourcev1.ManagedResourceCredential{}, errors.New("workload secret identity conflict")
		}
		if spec.LogicalName != "" {
			key := workloadSecretScope(spec.ProjectID, spec.ServiceID, spec.LogicalName)
			if _, exists := a.workloadSecrets[key]; !exists {
				a.workloadSecrets[key] = workloadSecretMetadata(spec.CredentialID, spec.ProjectID, spec.ServiceID, spec.LogicalName, 1, time.Now().UTC())
			}
		}
		return credential, nil
	}
	value := make([]byte, 48)
	if _, err := rand.Read(value); err != nil {
		return resourcev1.ManagedResourceCredential{}, err
	}
	credential := resourcev1.ManagedResourceCredential{CredentialID: spec.CredentialID, Purpose: resourcev1.CredentialPurposeWorkloadSecret, OwnerID: spec.ServiceID, ResourceID: spec.ProjectID, Username: "value", Password: base64.RawURLEncoding.EncodeToString(value)}
	a.credentials[spec.CredentialID] = credential
	if spec.LogicalName != "" {
		key := workloadSecretScope(spec.ProjectID, spec.ServiceID, spec.LogicalName)
		if _, exists := a.workloadSecrets[key]; !exists {
			a.workloadSecrets[key] = workloadSecretMetadata(spec.CredentialID, spec.ProjectID, spec.ServiceID, spec.LogicalName, 1, time.Now().UTC())
		}
	}
	return credential, nil
}

func (a *MemoryCredentialAuthority) Get(_ context.Context, id string) (resourcev1.ManagedResourceCredential, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	credential, ok := a.credentials[id]
	if !ok {
		return resourcev1.ManagedResourceCredential{}, errors.New("credential not found")
	}
	return credential, nil
}

func (a *MemoryCredentialAuthority) Delete(_ context.Context, id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.credentials, id)
	return nil
}
