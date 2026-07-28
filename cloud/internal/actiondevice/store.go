package actiondevice

import (
	"context"
	"sync"
)

type Store interface {
	Register(context.Context, Device) (Device, bool, error)
	List(context.Context, string) ([]Device, error)
	Get(context.Context, string, string) (Device, error)
	Revoke(context.Context, string, string, string, int64) (Device, bool, error)
}

type MemoryStore struct {
	mu       sync.Mutex
	devices  map[string]Device
	byReplay map[string]string
	audits   []AuditEvent
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{devices: map[string]Device{}, byReplay: map[string]string{}}
}

func (s *MemoryStore) Register(_ context.Context, device Device) (Device, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	replayKey := device.ProjectID + "\x00" + device.OwnerPrincipal + "\x00" + device.IdempotencyKey
	if id := s.byReplay[replayKey]; id != "" {
		current := s.devices[deviceKey(device.ProjectID, id)]
		if current.DisplayName != device.DisplayName || current.FingerprintSHA256 != device.FingerprintSHA256 || string(current.PublicKey) != string(device.PublicKey) {
			return Device{}, false, ErrReplayConflict
		}
		return cloneDevice(current), true, nil
	}
	auditID, err := newID("audit")
	if err != nil {
		return Device{}, false, ErrStorageUnavailable
	}
	s.devices[deviceKey(device.ProjectID, device.ID)] = cloneDevice(device)
	s.byReplay[replayKey] = device.ID
	s.audits = append(s.audits, AuditEvent{ID: auditID, ProjectID: device.ProjectID, Actor: device.TrustedActor, Action: "action_device.register", DeviceID: device.ID, CreatedAt: device.CreatedAt.UnixNano()})
	return cloneDevice(device), false, nil
}

func (s *MemoryStore) List(_ context.Context, projectID string) ([]Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := []Device{}
	for _, device := range s.devices {
		if device.ProjectID == projectID {
			result = append(result, cloneDevice(device))
		}
	}
	return result, nil
}

func (s *MemoryStore) Get(_ context.Context, projectID, deviceID string) (Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	device, ok := s.devices[deviceKey(projectID, deviceID)]
	if !ok {
		return Device{}, ErrDeviceNotFound
	}
	return cloneDevice(device), nil
}

func (s *MemoryStore) Revoke(_ context.Context, projectID, deviceID, actor string, revokedAt int64) (Device, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := deviceKey(projectID, deviceID)
	device, ok := s.devices[key]
	if !ok {
		return Device{}, false, ErrDeviceNotFound
	}
	if device.Status == DeviceRevoked {
		return cloneDevice(device), false, nil
	}
	auditID, err := newID("audit")
	if err != nil {
		return Device{}, false, ErrStorageUnavailable
	}
	at := unixNano(revokedAt)
	device.Status = DeviceRevoked
	device.RevokedAt = &at
	s.devices[key] = cloneDevice(device)
	s.audits = append(s.audits, AuditEvent{ID: auditID, ProjectID: projectID, Actor: actor, Action: "action_device.revoke", DeviceID: deviceID, CreatedAt: revokedAt})
	return cloneDevice(device), true, nil
}

func (s *MemoryStore) AuditCount() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.audits) }

func deviceKey(projectID, deviceID string) string { return projectID + "\x00" + deviceID }
func cloneDevice(device Device) Device {
	device.PublicKey = append([]byte(nil), device.PublicKey...)
	return device
}
