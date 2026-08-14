package webhookrelay

import (
	"bytes"
	"context"
	"database/sql"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/opsi-dev/opsi/cloud/internal/postgres"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
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
	store := vault.(postgresManagedResourceCredentialVault)
	legacy := resourcev1.ManagedResourceCredential{CredentialID: id, Username: "opsi", Password: "legacy-management-password", Database: "opsi"}
	ciphertext, nonce, err := store.seal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `DELETE FROM managed_resource_credentials WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO managed_resource_credentials(id,ciphertext,nonce,updated_at) VALUES($1,$2,$3,now())`, id, ciphertext, nonce); err != nil {
		t.Fatal(err)
	}
	first, err := vault.Ensure(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if first.Password != legacy.Password {
		t.Fatal("legacy management credential password was rotated during metadata backfill")
	}
	if err := db.QueryRowContext(context.Background(), `SELECT ciphertext FROM managed_resource_credentials WHERE id = $1`, id).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if first.Database == "" || first.Purpose != resourcev1.CredentialPurposeResourceManagement || first.OwnerID != "vault-test" || first.ResourceID != "vault-test" || bytes.Contains(ciphertext, []byte(first.Username)) || bytes.Contains(ciphertext, []byte(first.Password)) || bytes.Contains(ciphertext, []byte(first.Database)) {
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

func TestPostgresBindingCredentialVaultEncryptsMetadataAndKeepsBindingsIsolated(t *testing.T) {
	db, err := sql.Open("pgx", requirePostgresTestDSN(t, "binding credential vault"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := postgres.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	vault, err := NewPostgresManagedResourceCredentialVault(db, "test-binding-encryption-key")
	if err != nil {
		t.Fatal(err)
	}
	specs := []resourcev1.BindingCredentialSpec{
		{CredentialID: "rbcred-vault-a", BindingID: "rbind-vault-a", ResourceID: "res-vault", Username: "opsi_b_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Database: "opsi"},
		{CredentialID: "rbcred-vault-b", BindingID: "rbind-vault-b", ResourceID: "res-vault", Username: "opsi_b_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Database: "opsi"},
	}
	defer db.ExecContext(context.Background(), `DELETE FROM managed_resource_credentials WHERE id IN ($1,$2)`, specs[0].CredentialID, specs[1].CredentialID)
	first, err := vault.EnsureBinding(context.Background(), specs[0])
	if err != nil {
		t.Fatal(err)
	}
	replay, err := vault.EnsureBinding(context.Background(), specs[0])
	if err != nil || replay != first {
		t.Fatalf("replay=%+v first=%+v err=%v", replay, first, err)
	}
	second, err := vault.EnsureBinding(context.Background(), specs[1])
	if err != nil || second.Password == first.Password || second.Username == first.Username {
		t.Fatalf("first=%+v second=%+v err=%v", first, second, err)
	}
	var ciphertext []byte
	if err := db.QueryRowContext(context.Background(), `SELECT ciphertext FROM managed_resource_credentials WHERE id=$1`, first.CredentialID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	for _, plaintext := range []string{first.Password, first.Username, first.OwnerID, first.ResourceID} {
		if bytes.Contains(ciphertext, []byte(plaintext)) {
			t.Fatalf("binding credential plaintext leaked: %s", plaintext)
		}
	}
	if err := vault.Delete(context.Background(), first.CredentialID); err != nil {
		t.Fatal(err)
	}
	if loaded, err := vault.Get(context.Background(), second.CredentialID); err != nil || loaded != second {
		t.Fatalf("second binding changed after deleting first: loaded=%+v err=%v", loaded, err)
	}
}
