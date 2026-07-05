package secret

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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

type TOTPStore interface {
	PutTOTP(ctx context.Context, auth AuthContext, secret string) error
	GetTOTP(ctx context.Context, auth AuthContext) (string, error)
}

type RolloutRestarter interface {
	Restart(ctx context.Context, ref SecretRef) error
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
	KubectlPath   string
	TOTPNamespace string
	Runner        SecretApplyRunner
}

type KubernetesRolloutRestarter struct {
	KubectlPath string
	Timeout     string
}

type SecretApplyRunner interface {
	Run(ctx context.Context, input []byte, name string, args ...string) ([]byte, error)
}

type ExecSecretApplyRunner struct{}

func (ExecSecretApplyRunner) Run(ctx context.Context, input []byte, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = bytes.NewReader(input)
	return cmd.CombinedOutput()
}

func (s KubernetesSecretStore) Put(ctx context.Context, ref SecretRef, value SecretValue) error {
	if err := validateRef(ref); err != nil {
		return err
	}
	manifest, err := secretManifest(ref.Namespace, ref.Name, map[string]string{
		"username": value.Username,
		"password": value.Password,
	})
	if err != nil {
		return fmt.Errorf("build kubernetes secret manifest: %w", err)
	}
	return s.applySecretManifest(ctx, manifest, value.Username, value.Password)
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

func (s KubernetesSecretStore) PutTOTP(ctx context.Context, auth AuthContext, secretValue string) error {
	if auth.ProjectID == "" || auth.UserID == "" {
		return errors.New("project_id and user_id are required")
	}
	name := TOTPSecretName(auth.ProjectID, auth.UserID)
	namespace := firstNonEmpty(s.TOTPNamespace, "default")
	manifest, err := secretManifest(namespace, name, map[string]string{"totp_secret": secretValue})
	if err != nil {
		return fmt.Errorf("build kubernetes totp secret manifest: %w", err)
	}
	return s.applySecretManifest(ctx, manifest, secretValue)
}

func (s KubernetesSecretStore) GetTOTP(ctx context.Context, auth AuthContext) (string, error) {
	if auth.ProjectID == "" || auth.UserID == "" {
		return "", errors.New("project_id and user_id are required")
	}
	kubectl := firstNonEmpty(s.KubectlPath, "kubectl")
	name := TOTPSecretName(auth.ProjectID, auth.UserID)
	namespace := firstNonEmpty(s.TOTPNamespace, "default")
	out, err := exec.CommandContext(ctx, kubectl, "-n", namespace, "get", "secret", name, "-o", "json").Output()
	if err != nil {
		return "", fmt.Errorf("kubectl get totp secret: %w", err)
	}
	var payload struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return "", fmt.Errorf("parse kubernetes totp secret: %w", err)
	}
	secretValue, err := decodeSecretField(payload.Data["totp_secret"])
	if err != nil {
		return "", fmt.Errorf("decode totp_secret: %w", err)
	}
	return secretValue, nil
}

func (s KubernetesRolloutRestarter) Restart(ctx context.Context, ref SecretRef) error {
	if err := validateRef(ref); err != nil {
		return err
	}
	kubectl := firstNonEmpty(s.KubectlPath, "kubectl")
	if err := exec.CommandContext(ctx, kubectl, "-n", ref.Namespace, "rollout", "restart", "deployment/"+ref.ServiceID).Run(); err != nil {
		return fmt.Errorf("kubectl rollout restart: %w", err)
	}
	timeout := firstNonEmpty(s.Timeout, "10m")
	if err := exec.CommandContext(ctx, kubectl, "-n", ref.Namespace, "rollout", "status", "deployment/"+ref.ServiceID, "--timeout", timeout).Run(); err != nil {
		return fmt.Errorf("kubectl rollout status after restart: %w", err)
	}
	return nil
}

func validateRef(ref SecretRef) error {
	if ref.ProjectID == "" || ref.ServiceID == "" || ref.Name == "" || ref.Namespace == "" {
		return errors.New("project_id, service_id, namespace and secret name are required")
	}
	return nil
}

func (s KubernetesSecretStore) applySecretManifest(ctx context.Context, manifest []byte, sensitive ...string) error {
	runner := s.Runner
	if runner == nil {
		runner = ExecSecretApplyRunner{}
	}
	out, err := runner.Run(ctx, manifest, firstNonEmpty(s.KubectlPath, "kubectl"), "apply", "-f", "-")
	if err != nil {
		return fmt.Errorf("kubectl apply secret manifest failed: %s: %s", redactValues(err.Error(), sensitive...), redactValues(strings.TrimSpace(string(out)), sensitive...))
	}
	return nil
}

func secretManifest(namespace, name string, values map[string]string) ([]byte, error) {
	data := make(map[string]string, len(values))
	for key, value := range values {
		data[key] = base64.StdEncoding.EncodeToString([]byte(value))
	}
	return json.Marshal(struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Type       string `json:"type"`
		Metadata   struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
		Data map[string]string `json:"data"`
	}{
		APIVersion: "v1",
		Kind:       "Secret",
		Type:       "Opaque",
		Metadata: struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		}{Name: name, Namespace: namespace},
		Data: data,
	})
}

func redactValues(message string, values ...string) string {
	for _, value := range values {
		if value == "" {
			continue
		}
		message = strings.ReplaceAll(message, value, "[REDACTED]")
		message = strings.ReplaceAll(message, base64.StdEncoding.EncodeToString([]byte(value)), "[REDACTED]")
	}
	return message
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

func TOTPSecretName(projectID, userID string) string {
	sum := sha256.Sum256([]byte(projectID + ":" + userID))
	return "opsi-totp-" + hex.EncodeToString(sum[:])[:24]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
