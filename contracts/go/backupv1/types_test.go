package backupv1

import (
	"strings"
	"testing"
	"time"

	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

func TestSucceededBackupRequiresLogicalArchiveEvidence(t *testing.T) {
	now := time.Now().UTC()
	value := Backup{
		SchemaVersion: SchemaVersion, ID: "bkp_1", ProjectID: "project-1", EnvironmentID: "env-1", SourceResourceID: "res-1", SourceNodeID: "node-1",
		ResourceType: resourcev1.TypePostgres, BackupType: BackupTypePostgresLogical, SourceDatabase: CanonicalDatabase, Format: FormatCustom,
		Lifecycle: LifecycleSucceeded, ArtifactSize: 42, SHA256: strings.Repeat("a", 64), PGDumpVersion: "pg_dump (PostgreSQL) 18.6", SourcePostgresVersion: "18.6", ArchiveVerified: true, CompletedAt: &now,
	}
	if err := value.ValidateSucceeded(); err != nil {
		t.Fatal(err)
	}
	value.ArchiveVerified = false
	if err := value.ValidateSucceeded(); err == nil {
		t.Fatal("unverified archive accepted")
	}
}

func TestSucceededBackupRejectsNonCanonicalChecksum(t *testing.T) {
	now := time.Now()
	value := Backup{SchemaVersion: SchemaVersion, ID: "bkp-1", ProjectID: "project-1", EnvironmentID: "env-1", SourceResourceID: "res-1", ResourceType: resourcev1.TypePostgres, BackupType: BackupTypePostgresLogical, SourceDatabase: CanonicalDatabase, Format: FormatCustom, Lifecycle: LifecycleSucceeded, ArtifactSize: 1, SHA256: strings.Repeat("G", 64), PGDumpVersion: "pg_dump (PostgreSQL) 18.6", SourcePostgresVersion: "18.6", ArchiveVerified: true, CompletedAt: &now}
	if value.ValidateSucceeded() == nil {
		t.Fatal("non-hex checksum was accepted")
	}
	value.SHA256 = strings.Repeat("A", 64)
	if value.ValidateSucceeded() == nil {
		t.Fatal("uppercase checksum was accepted")
	}
}

func TestStoreCredentialValidationRejectsControlCharacters(t *testing.T) {
	if err := (StoreCredential{AccessKey: "access", SecretKey: "secret\nleak"}).Validate(); err == nil {
		t.Fatal("credential with newline accepted")
	}
}
