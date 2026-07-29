package actiondevice

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	actionv1 "github.com/opsi-dev/opsi/contracts/go/actionv1"
)

type Service struct {
	Store Store
	Now   func() time.Time
}

func (s Service) Register(ctx context.Context, principal Principal, request RegisterRequest) (Device, bool, error) {
	if err := authorize(principal, false); err != nil {
		return Device{}, false, err
	}
	display := strings.TrimSpace(request.DisplayName)
	idempotency := strings.TrimSpace(request.IdempotencyKey)
	if display == "" || len(display) > 80 || idempotency == "" || len(idempotency) > 128 || len(request.PublicKey) != ed25519.PublicKeySize || s.Store == nil {
		return Device{}, false, ErrInvalidDevice
	}
	key := append([]byte(nil), request.PublicKey...)
	sum := sha256.Sum256(key)
	now := s.clock()
	deviceID, err := newID("device")
	if err != nil {
		return Device{}, false, ErrStorageUnavailable
	}
	device := Device{SchemaVersion: actionv1.SchemaVersion, ID: deviceID, ProjectID: principal.ProjectID, OwnerPrincipal: principal.UserID, DisplayName: display, PublicKey: key, FingerprintSHA256: hex.EncodeToString(sum[:]), Status: DeviceActive, TrustedActor: principal.UserID, IdempotencyKey: idempotency, CreatedAt: now}
	return s.Store.Register(ctx, device)
}

func (s Service) List(ctx context.Context, principal Principal) ([]Device, error) {
	if err := authorize(principal, true); err != nil {
		return nil, err
	}
	if s.Store == nil {
		return nil, ErrStorageUnavailable
	}
	devices, err := s.Store.List(ctx, principal.ProjectID)
	if err != nil {
		return nil, err
	}
	for i := range devices {
		devices[i].PublicKey = nil
		devices[i].IdempotencyKey = ""
	}
	return devices, nil
}

func (s Service) Get(ctx context.Context, principal Principal, deviceID string) (Device, error) {
	if err := authorize(principal, true); err != nil {
		return Device{}, err
	}
	if s.Store == nil {
		return Device{}, ErrStorageUnavailable
	}
	return s.Store.Get(ctx, principal.ProjectID, strings.TrimSpace(deviceID))
}

func (s Service) Revoke(ctx context.Context, principal Principal, deviceID string) (Device, bool, error) {
	if err := authorize(principal, false); err != nil {
		return Device{}, false, err
	}
	if s.Store == nil {
		return Device{}, false, ErrStorageUnavailable
	}
	return s.Store.Revoke(ctx, principal.ProjectID, strings.TrimSpace(deviceID), principal.UserID, s.clock().UnixNano())
}

func (s Service) ResolveActive(ctx context.Context, projectID, deviceID, owner string) (Device, error) {
	if s.Store == nil {
		return Device{}, ErrStorageUnavailable
	}
	device, err := s.Store.Get(ctx, strings.TrimSpace(projectID), strings.TrimSpace(deviceID))
	if err != nil {
		return Device{}, err
	}
	if device.ProjectID != projectID {
		return Device{}, ErrDeviceNotFound
	}
	if device.OwnerPrincipal != owner {
		return Device{}, ErrPermissionDenied
	}
	if device.Status != DeviceActive {
		return Device{}, ErrDeviceRevoked
	}
	if len(device.PublicKey) != ed25519.PublicKeySize {
		return Device{}, ErrInvalidDevice
	}
	return device, nil
}

func authorize(principal Principal, read bool) error {
	if strings.TrimSpace(principal.ProjectID) == "" || strings.TrimSpace(principal.UserID) == "" {
		return ErrPrincipalRequired
	}
	role := strings.ToLower(strings.TrimSpace(principal.Role))
	if role == "owner" || role == "developer" || (read && role == "viewer") {
		return nil
	}
	return ErrPermissionDenied
}

func (s Service) clock() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func newID(prefix string) (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate action device identity: %w", err)
	}
	return prefix + "-" + hex.EncodeToString(value[:]), nil
}

func unixNano(value int64) time.Time { return time.Unix(0, value).UTC() }
