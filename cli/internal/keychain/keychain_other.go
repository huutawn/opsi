//go:build !linux

package keychain

import (
	"fmt"

	keyring "github.com/99designs/keyring"
)

// OSStore retains the native macOS and Windows keychain integrations.
type OSStore struct {
	ring keyring.Keyring
}

func NewOSStore() (*OSStore, error) {
	ring, err := keyring.Open(keyring.Config{ServiceName: "opsi"})
	if err != nil {
		return nil, fmt.Errorf("open OS keychain: %w", err)
	}
	return &OSStore{ring: ring}, nil
}

func (s *OSStore) SetPAT(token string) error {
	return s.ring.Set(keyring.Item{Key: patKey, Data: []byte(token)})
}

func (s *OSStore) GetPAT() (string, error) {
	item, err := s.ring.Get(patKey)
	if err != nil {
		return "", err
	}
	return string(item.Data), nil
}

func (s *OSStore) DeletePAT() error {
	return s.ring.Remove(patKey)
}

var _ Store = (*OSStore)(nil)
