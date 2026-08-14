package webhookrelay

import (
	"bytes"
	"context"
	"database/sql"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/opsi-dev/opsi/cloud/internal/postgres"
)

func TestPostgresManagedResourceCredentialVaultEncryptsReusesAndDeletes(t *testing.T) {
	db, err := sql.Open("pgx", requirePostgresTestDSN(t, "managed resource credential vault"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := postgres.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	const id = "mrcred-vault-test"
	defer db.ExecContext(context.Background(), `DELETE FROM managed_resource_credentials WHERE id = $1`, id)
	vault, err := NewPostgresManagedResourceCredentialVault(db, "test-managed-resource-encryption-key")
	if err != nil {
		t.Fatal(err)
	}
	first, err := vault.Ensure(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var ciphertext []byte
	if err := db.QueryRowContext(context.Background(), `SELECT ciphertext FROM managed_resource_credentials WHERE id = $1`, id).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if first.Database == "" || bytes.Contains(ciphertext, []byte(first.Username)) || bytes.Contains(ciphertext, []byte(first.Password)) || bytes.Contains(ciphertext, []byte(first.Database)) {
		t.Fatal("managed resource credential was stored as plaintext")
	}
	second, err := vault.Ensure(context.Background(), id)
	if err != nil || second != first {
		t.Fatalf("credential was regenerated: first=%+v second=%+v err=%v", first, second, err)
	}
	loaded, err := vault.Get(context.Background(), id)
	if err != nil || loaded != first {
		t.Fatalf("loaded=%+v first=%+v err=%v", loaded, first, err)
	}
	if err := vault.Delete(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := vault.Get(context.Background(), id); err == nil {
		t.Fatal("deleted managed resource credential remained available")
	}
}
