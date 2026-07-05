package secret

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type recordingSecretRunner struct {
	input []byte
	name  string
	args  []string
	out   []byte
	err   error
	calls int
}

func (r *recordingSecretRunner) Run(_ context.Context, input []byte, name string, args ...string) ([]byte, error) {
	r.calls++
	r.input = append([]byte(nil), input...)
	r.name = name
	r.args = append([]string(nil), args...)
	return r.out, r.err
}

type secretManifestPayload struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Type       string            `json:"type"`
	Metadata   map[string]string `json:"metadata"`
	Data       map[string]string `json:"data"`
}

func decodeManifestValue(t *testing.T, manifest []byte, key string) string {
	t.Helper()
	var payload secretManifestPayload
	if err := json.Unmarshal(manifest, &payload); err != nil {
		t.Fatalf("manifest is not valid json: %v\n%s", err, manifest)
	}
	if payload.APIVersion != "v1" || payload.Kind != "Secret" || payload.Type != "Opaque" {
		t.Fatalf("not a kubernetes secret manifest: %#v", payload)
	}
	decoded, err := base64.StdEncoding.DecodeString(payload.Data[key])
	if err != nil {
		t.Fatalf("secret field %q is not base64 data: %v", key, err)
	}
	return string(decoded)
}

func TestKubernetesSecretStorePutUsesStdinManifestNoArgSecrets(t *testing.T) {
	runner := &recordingSecretRunner{}
	store := KubernetesSecretStore{KubectlPath: "kubectl-test", Runner: runner}
	ref := SecretRef{ProjectID: "proj", ServiceID: "api", Namespace: "prod", Name: "db"}
	value := SecretValue{Username: "app-user", Password: "p@ss super secret"}

	if err := store.Put(context.Background(), ref, value); err != nil {
		t.Fatal(err)
	}
	if runner.name != "kubectl-test" || strings.Join(runner.args, " ") != "apply -f -" {
		t.Fatalf("bad kubectl call: name=%q args=%v", runner.name, runner.args)
	}
	args := strings.Join(runner.args, " ")
	for _, forbidden := range []string{"from-literal", value.Username, value.Password} {
		if strings.Contains(args, forbidden) {
			t.Fatalf("secret argv leak %q in args %q", forbidden, args)
		}
	}
	if got := decodeManifestValue(t, runner.input, "username"); got != value.Username {
		t.Fatalf("username not in stdin manifest: %q", got)
	}
	if got := decodeManifestValue(t, runner.input, "password"); got != value.Password {
		t.Fatalf("password not in stdin manifest: %q", got)
	}
}

func TestKubernetesSecretStorePutTOTPUsesStdinManifestNoArgSecrets(t *testing.T) {
	runner := &recordingSecretRunner{}
	store := KubernetesSecretStore{KubectlPath: "kubectl-test", TOTPNamespace: "opsi-system", Runner: runner}
	auth := AuthContext{ProjectID: "proj", UserID: "owner@example.test"}
	secretValue := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"

	if err := store.PutTOTP(context.Background(), auth, secretValue); err != nil {
		t.Fatal(err)
	}
	args := strings.Join(runner.args, " ")
	for _, forbidden := range []string{"from-literal", secretValue} {
		if strings.Contains(args, forbidden) {
			t.Fatalf("totp argv leak %q in args %q", forbidden, args)
		}
	}
	if got := decodeManifestValue(t, runner.input, "totp_secret"); got != secretValue {
		t.Fatalf("totp secret not in stdin manifest: %q", got)
	}
}

func TestKubernetesSecretStoreApplyErrorRedactsSecretValues(t *testing.T) {
	password := "p@ss super secret"
	encoded := base64.StdEncoding.EncodeToString([]byte(password))
	runner := &recordingSecretRunner{
		out: []byte("apply failed for password=" + password + " data=" + encoded),
		err: errors.New("kubectl rejected " + password),
	}
	store := KubernetesSecretStore{Runner: runner}
	err := store.Put(context.Background(), SecretRef{ProjectID: "proj", ServiceID: "api", Namespace: "prod", Name: "db"}, SecretValue{Username: "app", Password: password})
	if err == nil {
		t.Fatal("expected apply failure")
	}
	msg := err.Error()
	if strings.Contains(msg, password) || strings.Contains(msg, encoded) {
		t.Fatalf("error leaked secret: %s", msg)
	}
	if !strings.Contains(msg, "[REDACTED]") {
		t.Fatalf("error was not redacted: %s", msg)
	}
}

func TestKubernetesSecretStoreInvalidInputFailsBeforeKubectl(t *testing.T) {
	runner := &recordingSecretRunner{}
	store := KubernetesSecretStore{Runner: runner}
	err := store.Put(context.Background(), SecretRef{ProjectID: "proj", ServiceID: "api", Namespace: "prod"}, SecretValue{Username: "u", Password: "p"})
	if err == nil {
		t.Fatal("expected invalid ref error")
	}
	if runner.calls != 0 {
		t.Fatalf("kubectl called for invalid input: %d", runner.calls)
	}
}
