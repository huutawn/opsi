package actiondevice

import (
	"errors"

	actionv1 "github.com/opsi-dev/opsi/contracts/go/actionv1"
)

type Device = actionv1.ActionDevice
type DeviceStatus = actionv1.DeviceStatus

const (
	DeviceActive  = actionv1.DeviceActive
	DeviceRevoked = actionv1.DeviceRevoked
)

var (
	ErrPrincipalRequired  = errors.New("authenticated principal is required")
	ErrPermissionDenied   = errors.New("action device permission denied")
	ErrInvalidDevice      = errors.New("invalid action device")
	ErrDeviceNotFound     = errors.New("action device not found")
	ErrDeviceRevoked      = errors.New("action device is revoked")
	ErrReplayConflict     = errors.New("action device idempotency conflict")
	ErrStorageUnavailable = errors.New("action device storage unavailable")
)

type Principal struct {
	ProjectID string
	UserID    string
	Role      string
}

type RegisterRequest struct {
	DisplayName    string `json:"display_name"`
	PublicKey      []byte `json:"public_key"`
	IdempotencyKey string `json:"idempotency_key"`
}

type AuditEvent struct {
	ID        string
	ProjectID string
	Actor     string
	Action    string
	DeviceID  string
	CreatedAt int64
}
