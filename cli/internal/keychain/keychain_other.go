//go:build !linux && !darwin

package keychain

import "errors"

// OSStore retains the native macOS and Windows keychain integrations.
type OSStore struct {
}

func NewOSStore() (*OSStore, error) { return nil, errors.New("OS secure keychain is unsupported") }

func (s *OSStore) SetPAT(token string) error {
	return errors.New("OS secure keychain is unsupported")
}

func (s *OSStore) GetPAT() (string, error) {
	return "", errors.New("OS secure keychain is unsupported")
}

func (s *OSStore) DeletePAT() error {
	return errors.New("OS secure keychain is unsupported")
}

var _ Store = (*OSStore)(nil)
