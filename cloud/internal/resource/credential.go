package resource

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
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
	if credential, ok := a.credentials[id]; ok {
		return credential, nil
	}
	password := make([]byte, 32)
	if _, err := rand.Read(password); err != nil {
		return resourcev1.ManagedResourceCredential{}, err
	}
	credential := resourcev1.ManagedResourceCredential{CredentialID: id, Username: "opsi", Password: base64.RawURLEncoding.EncodeToString(password), Database: "opsi"}
	a.credentials[id] = credential
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
