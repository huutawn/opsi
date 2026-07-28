package actionapproval

import (
	"errors"

	"github.com/opsi-dev/opsi/cli/internal/keychain"
)

var ErrSecureStoreUnavailable = errors.New("OS secure store does not support ActionPlane keys")

type Store struct{ Backend keychain.Store }

func (s Store) actionStore() (keychain.ActionStore, error) {
	store, ok := s.Backend.(keychain.ActionStore)
	if !ok || store == nil {
		return nil, ErrSecureStoreUnavailable
	}
	return store, nil
}
func (s Store) SavePrivateKey(id string, value []byte) error {
	store, err := s.actionStore()
	if err != nil {
		return err
	}
	return store.SetActionPrivateKey(id, value)
}
func (s Store) PrivateKey(id string) ([]byte, error) {
	store, err := s.actionStore()
	if err != nil {
		return nil, err
	}
	return store.GetActionPrivateKey(id)
}
func (s Store) DeletePrivateKey(id string) error {
	store, err := s.actionStore()
	if err != nil {
		return err
	}
	return store.DeleteActionPrivateKey(id)
}
func (s Store) SavePendingGrant(id string, value []byte) error {
	store, err := s.actionStore()
	if err != nil {
		return err
	}
	return store.SetPendingApproval(id, value)
}
func (s Store) PendingGrant(id string) ([]byte, error) {
	store, err := s.actionStore()
	if err != nil {
		return nil, err
	}
	return store.GetPendingApproval(id)
}
func (s Store) DeletePendingGrant(id string) error {
	store, err := s.actionStore()
	if err != nil {
		return err
	}
	return store.DeletePendingApproval(id)
}
