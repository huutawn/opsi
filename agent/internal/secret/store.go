package secret

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

type Store interface {
	Put(ctx context.Context, ref SecretRef, value SecretValue) error
	Get(ctx context.Context, ref SecretRef) (SecretValue, error)
}

type MemoryStore struct {
	mu    sync.Mutex
	items map[string]SecretValue
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{items: map[string]SecretValue{}}
}

func (s *MemoryStore) Put(_ context.Context, ref SecretRef, value SecretValue) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[refKey(ref)] = value
	return nil
}

func (s *MemoryStore) Get(_ context.Context, ref SecretRef) (SecretValue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.items[refKey(ref)]
	if !ok {
		return SecretValue{}, errors.New("secret not found")
	}
	return value, nil
}

type KubernetesSecretStore struct {
	KubectlPath string
}

func (s KubernetesSecretStore) Put(ctx context.Context, ref SecretRef, value SecretValue) error {
	if err := validateRef(ref); err != nil {
		return err
	}
	kubectl := firstNonEmpty(s.KubectlPath, "kubectl")
	create := exec.CommandContext(ctx, kubectl, "-n", ref.Namespace, "create", "secret", "generic", ref.Name,
		"--from-literal=username="+value.Username,
		"--from-literal=password="+value.Password,
		"--dry-run=client", "-o", "yaml")
	apply := exec.CommandContext(ctx, kubectl, "apply", "-f", "-")
	pipe, err := create.StdoutPipe()
	if err != nil {
		return err
	}
	apply.Stdin = pipe
	var applyErr strings.Builder
	apply.Stderr = &applyErr
	if err := create.Start(); err != nil {
		return fmt.Errorf("start kubectl create secret: %w", err)
	}
	if err := apply.Start(); err != nil {
		return fmt.Errorf("start kubectl apply secret: %w", err)
	}
	createErr := create.Wait()
	applyWaitErr := apply.Wait()
	if createErr != nil {
		return fmt.Errorf("kubectl create secret: %w", createErr)
	}
	if applyWaitErr != nil {
		return fmt.Errorf("kubectl apply secret: %w: %s", applyWaitErr, applyErr.String())
	}
	return nil
}

func (s KubernetesSecretStore) Get(ctx context.Context, ref SecretRef) (SecretValue, error) {
	if err := validateRef(ref); err != nil {
		return SecretValue{}, err
	}
	kubectl := firstNonEmpty(s.KubectlPath, "kubectl")
	out, err := exec.CommandContext(ctx, kubectl, "-n", ref.Namespace, "get", "secret", ref.Name, "-o", "json").Output()
	if err != nil {
		return SecretValue{}, fmt.Errorf("kubectl get secret: %w", err)
	}
	var payload struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return SecretValue{}, fmt.Errorf("parse kubernetes secret: %w", err)
	}
	username, err := decodeSecretField(payload.Data["username"])
	if err != nil {
		return SecretValue{}, fmt.Errorf("decode username: %w", err)
	}
	password, err := decodeSecretField(payload.Data["password"])
	if err != nil {
		return SecretValue{}, fmt.Errorf("decode password: %w", err)
	}
	return SecretValue{Username: username, Password: password}, nil
}

func validateRef(ref SecretRef) error {
	if ref.ProjectID == "" || ref.ServiceID == "" || ref.Name == "" || ref.Namespace == "" {
		return errors.New("project_id, service_id, namespace and secret name are required")
	}
	return nil
}

func decodeSecretField(value string) (string, error) {
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

func refKey(ref SecretRef) string {
	return ref.ProjectID + "/" + ref.Namespace + "/" + ref.ServiceID + "/" + ref.Name
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
