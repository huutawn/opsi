//go:build darwin

package keychain

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type OSStore struct{ timeout time.Duration }

func NewOSStore() (*OSStore, error)          { return &OSStore{timeout: 5 * time.Second}, nil }
func (s *OSStore) SetPAT(value string) error { return s.set(patKey, []byte(value)) }
func (s *OSStore) GetPAT() (string, error)   { value, err := s.get(patKey); return string(value), err }
func (s *OSStore) DeletePAT() error          { return s.delete(patKey) }
func (s *OSStore) SetActionPrivateKey(id string, value []byte) error {
	return ErrActionStoreUnverified
}
func (s *OSStore) GetActionPrivateKey(id string) ([]byte, error) {
	return nil, ErrActionStoreUnverified
}
func (s *OSStore) DeleteActionPrivateKey(id string) error { return ErrActionStoreUnverified }
func (s *OSStore) SetPendingApproval(id string, value []byte) error {
	return ErrActionStoreUnverified
}
func (s *OSStore) GetPendingApproval(id string) ([]byte, error) { return nil, ErrActionStoreUnverified }
func (s *OSStore) DeletePendingApproval(id string) error        { return ErrActionStoreUnverified }

func (s *OSStore) set(key string, value []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	encoded := base64.StdEncoding.EncodeToString(value)
	cmd := exec.CommandContext(ctx, "/usr/bin/security", "add-generic-password", "-a", key, "-s", "opsi", "-U")
	cmd.Stdin = bytes.NewReader([]byte(encoded))
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("store OS keychain item: %w", err)
	}
	return nil
}
func (s *OSStore) get(key string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, "/usr/bin/security", "find-generic-password", "-a", key, "-s", "opsi", "-w").Output()
	if err != nil {
		return nil, ErrActionSecretNotFound
	}
	value, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(output)))
	if err != nil {
		return nil, errors.New("OS keychain item is invalid")
	}
	return value, nil
}
func (s *OSStore) delete(key string) error {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	if err := exec.CommandContext(ctx, "/usr/bin/security", "delete-generic-password", "-a", key, "-s", "opsi").Run(); err != nil {
		return ErrActionSecretNotFound
	}
	return nil
}

var _ ActionStore = (*OSStore)(nil)
