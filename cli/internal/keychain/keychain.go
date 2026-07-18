package keychain

import (
	"errors"
	"sync"
)

const patKey = "default-pat"

var (
	ErrPATNotFound         = errors.New("PAT is not stored in the OS keychain")
	ErrKeychainTimeout     = errors.New("OS keychain did not respond before the deadline; unlock Secret Service and try again")
	ErrKeychainUnavailable = errors.New("OS keychain is unavailable or locked; unlock Secret Service and try again")
)

type Store interface {
	SetPAT(token string) error
	GetPAT() (string, error)
	DeletePAT() error
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
