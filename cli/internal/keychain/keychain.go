package keychain

import (
	"errors"
	"sync"
)

const patKey = "default-pat"

var (
	ErrPATNotFound           = errors.New("PAT is not stored in the OS keychain")
	ErrKeychainTimeout       = errors.New("OS keychain did not respond before the deadline; unlock Secret Service and try again")
	ErrKeychainUnavailable   = errors.New("OS keychain is unavailable or locked; unlock Secret Service and try again")
	ErrActionSecretNotFound  = errors.New("ActionPlane secure item is not stored")
	ErrActionStoreUnverified = errors.New("ActionPlane secure storage backend is unsupported or unverified on this platform")
)

type Store interface {
	SetPAT(token string) error
	GetPAT() (string, error)
	DeletePAT() error
}

type ActionStore interface {
	SetActionPrivateKey(string, []byte) error
	GetActionPrivateKey(string) ([]byte, error)
	DeleteActionPrivateKey(string) error
	SetPendingApproval(string, []byte) error
	GetPendingApproval(string) ([]byte, error)
	DeletePendingApproval(string) error
}

type FakeStore struct {
	mu          sync.Mutex
	token       string
	privateKeys map[string][]byte
	pending     map[string][]byte
}

func NewFakeStore() *FakeStore {
	return &FakeStore{privateKeys: map[string][]byte{}, pending: map[string][]byte{}}
}

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
		return "", ErrPATNotFound
	}
	return s.token, nil
}

func (s *FakeStore) DeletePAT() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = ""
	return nil
}

func (s *FakeStore) SetActionPrivateKey(id string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.privateKeys[id] = append([]byte(nil), value...)
	return nil
}
func (s *FakeStore) GetActionPrivateKey(id string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.privateKeys[id]
	if !ok {
		return nil, ErrActionSecretNotFound
	}
	return append([]byte(nil), value...), nil
}
func (s *FakeStore) DeleteActionPrivateKey(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.privateKeys, id)
	return nil
}
func (s *FakeStore) SetPendingApproval(id string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending[id] = append([]byte(nil), value...)
	return nil
}
func (s *FakeStore) GetPendingApproval(id string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.pending[id]
	if !ok {
		return nil, ErrActionSecretNotFound
	}
	return append([]byte(nil), value...), nil
}
func (s *FakeStore) DeletePendingApproval(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pending, id)
	return nil
}

var _ ActionStore = (*FakeStore)(nil)
