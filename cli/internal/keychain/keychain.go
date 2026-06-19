package keychain

import (
	"fmt"
	"sync"

	keyring "github.com/99designs/keyring"
)

const patKey = "default-pat"

type Store interface {
	SetPAT(token string) error
	GetPAT() (string, error)
}

type OSStore struct {
	ring keyring.Keyring
}

func NewOSStore() (*OSStore, error) {
	ring, err := keyring.Open(keyring.Config{
		ServiceName: "opsi",
		AllowedBackends: []keyring.BackendType{
			keyring.SecretServiceBackend,
			keyring.PassBackend,
			keyring.KWalletBackend,
			keyring.KeyCtlBackend,
			keyring.KeychainBackend,
			keyring.WinCredBackend,
		},
	})
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

type FakeStore struct {
	mu    sync.Mutex
	token string
}

func NewFakeStore() *FakeStore { return &FakeStore{} }

func (s *FakeStore) SetPAT(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = token
	return nil
}

func (s *FakeStore) GetPAT() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token == "" {
		return "", fmt.Errorf("PAT not found")
	}
	return s.token, nil
}
