package resource

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"sync"

	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

type MemoryCredentialAuthority struct {
	mu          sync.Mutex
	credentials map[string]resourcev1.ManagedResourceCredential
}

func NewMemoryCredentialAuthority() *MemoryCredentialAuthority {
	return &MemoryCredentialAuthority{credentials: map[string]resourcev1.ManagedResourceCredential{}}
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
