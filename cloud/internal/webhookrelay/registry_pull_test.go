package webhookrelay

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/opsi-dev/opsi/cloud/internal/buildjob"
	"github.com/opsi-dev/opsi/cloud/internal/postgres"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
)

type memoryRegistryPullVault struct {
	credential deploymentv1.RegistryPullCredential
	puts       int
}

func (v *memoryRegistryPullVault) Put(_ context.Context, _ string, credential deploymentv1.RegistryPullCredential) error {
	v.credential = credential
	v.puts++
	return nil
}

func (v *memoryRegistryPullVault) Get(_ context.Context, _ string) (deploymentv1.RegistryPullCredential, bool, error) {
	return v.credential, v.credential.Password != "", nil
}

func TestGHCRRegistryPullProviderScopesCanonicalPrivateImagesAndRotatesLazily(t *testing.T) {
	dir := t.TempDir()
	usernameFile := filepath.Join(dir, "username")
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(usernameFile, []byte("opsi-pull\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenFile, []byte("token-one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	vault := &memoryRegistryPullVault{}
	provider := NewGHCRRegistryPullCredentialProvider(
		buildjob.RegistryConfig{Host: "ghcr.io", Namespace: "opsi", RepositoryPrefix: "builds", Visibility: "private"},
		vault,
		RegistryPullConfig{UsernameFile: usernameFile, TokenFile: tokenFile},
	)
	privateImage, _ := deploymentv1.NewImmutableImage("ghcr.io/opsi/builds/app-a", "sha256:"+strings.Repeat("a", 64))
	publicImage, _ := deploymentv1.NewImmutableImage("docker.io/library/nginx", "sha256:"+strings.Repeat("b", 64))
	ref, ok := provider.Reference(privateImage)
	if !ok || ref.Provider != "ghcr" || ref.CredentialID != "hosted-opsi" || ref.Registry != "ghcr.io" {
		t.Fatalf("private ref=%+v ok=%v", ref, ok)
	}
	if _, ok := provider.Reference(publicImage); ok {
		t.Fatal("public image received a private registry credential reference")
	}
	credential, err := provider.Resolve(context.Background(), *ref)
	if err != nil || credential.Password != "token-one" || vault.puts != 1 {
		t.Fatalf("first resolve credential=%+v puts=%d err=%v", credential.Reference, vault.puts, err)
	}
	if err := os.WriteFile(tokenFile, []byte("token-two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	credential, err = provider.Resolve(context.Background(), *ref)
	if err != nil || credential.Password != "token-two" || vault.puts != 2 {
		t.Fatalf("rotated resolve puts=%d err=%v", vault.puts, err)
	}
}

func TestGHCRRegistryPullProviderFailsClosedWithoutHostedCredential(t *testing.T) {
	provider := NewGHCRRegistryPullCredentialProvider(buildjob.RegistryConfig{Host: "ghcr.io", Namespace: "opsi", RepositoryPrefix: "builds", Visibility: "private"}, nil, RegistryPullConfig{})
	image, _ := deploymentv1.NewImmutableImage("ghcr.io/opsi/builds/app-a", "sha256:"+strings.Repeat("a", 64))
	ref, _ := provider.Reference(image)
	_, err := provider.Resolve(context.Background(), *ref)
	if !errors.Is(err, ErrRegistryPullCredentialUnavailable) {
		t.Fatalf("err=%v", err)
	}
}

func TestRegistryPullCredentialJSONIsOnlyPresentOnTrustedAgentCommand(t *testing.T) {
	ref := deploymentv1.RegistryPullCredentialReference{Provider: "ghcr", CredentialID: "hosted-opsi", Registry: "ghcr.io"}
	workload := deploymentv1.WorkloadSpec{RegistryPullCredential: &ref}
	command := deploymentv1.AgentCommand{Workload: workload, RegistryPullCredential: &deploymentv1.RegistryPullCredential{Reference: ref, Username: "opsi-pull", Password: "do-not-leak"}}
	if strings.Contains(mustJSONText(t, workload), "do-not-leak") || !strings.Contains(mustJSONText(t, command), "do-not-leak") {
		t.Fatal("credential crossed the WorkloadSpec/AgentCommand transport boundary")
	}
}

func TestPostgresRegistryPullCredentialVaultEncryptsAtRest(t *testing.T) {
	db, err := sql.Open("pgx", requirePostgresTestDSN(t, "registry pull credential vault"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := postgres.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	const id = "hosted-opsi-test"
	defer db.ExecContext(context.Background(), `DELETE FROM registry_pull_credentials WHERE id = $1`, id)
	vault, err := NewPostgresRegistryPullCredentialVault(db, "test-registry-pull-encryption-key")
	if err != nil {
		t.Fatal(err)
	}
	credential := deploymentv1.RegistryPullCredential{Reference: deploymentv1.RegistryPullCredentialReference{Provider: "ghcr", CredentialID: id, Registry: "ghcr.io"}, Username: "opsi-pull", Password: "plaintext-must-not-appear"}
	if err := vault.Put(context.Background(), id, credential); err != nil {
		t.Fatal(err)
	}
	var ciphertext []byte
	if err := db.QueryRowContext(context.Background(), `SELECT ciphertext FROM registry_pull_credentials WHERE id = $1`, id).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte(credential.Username)) || bytes.Contains(ciphertext, []byte(credential.Password)) {
		t.Fatal("registry pull credential was stored as plaintext")
	}
	loaded, ok, err := vault.Get(context.Background(), id)
	if err != nil || !ok || loaded != credential {
		t.Fatalf("loaded=%+v ok=%v err=%v", loaded.Reference, ok, err)
	}
}

func mustJSONText(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
