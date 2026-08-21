package restore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	backupagent "github.com/opsi-dev/opsi/agent/internal/backup"
	backupv1 "github.com/opsi-dev/opsi/contracts/go/backupv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
	restorev1 "github.com/opsi-dev/opsi/contracts/go/restorev1"
)

type fakeStore struct {
	data []byte
	info backupagent.ObjectInfo
}

func (s fakeStore) Put(context.Context, string, io.ReadSeeker, int64, string, string) (backupagent.ObjectInfo, error) {
	return s.info, nil
}
func (s fakeStore) Stat(context.Context, string) (backupagent.ObjectInfo, error) { return s.info, nil }
func (s fakeStore) Get(context.Context, string) (io.ReadCloser, backupagent.ObjectInfo, error) {
	return io.NopCloser(bytes.NewReader(s.data)), s.info, nil
}
func (s fakeStore) Delete(context.Context, string) error { return nil }

type fakeRunner struct {
	calls       [][]string
	failRestore bool
	emptyAfter  bool
	toolVersion string
}

func (r *fakeRunner) Run(_ context.Context, _ io.Reader, output io.Writer, name string, args ...string) error {
	r.calls = append(r.calls, append([]string{name}, args...))
	switch len(r.calls) {
	case 1:
		v := r.toolVersion
		if v == "" {
			v = "pg_restore (PostgreSQL) 18.6\n"
		} else if v == "empty" {
			v = ""
		}
		_, _ = io.WriteString(output, v)
	case 2:
		_, _ = io.WriteString(output, "; Archive created\n1259; TABLE public restored\n")
	case 3:
		_, _ = io.WriteString(output, "42|1|0|0|0|0\n")
	case 4:
		if r.failRestore {
			return errors.New("controlled pg_restore SQL error")
		}
	case 5:
		if r.failRestore {
			_, _ = io.WriteString(output, "42|1|0|0|0|0\n")
		} else if r.emptyAfter {
			_, _ = io.WriteString(output, "42|1|0|0|0|0\n")
		} else {
			_, _ = io.WriteString(output, "42|1|1|1|1|0\n")
		}
	}
	return nil
}

func TestExecutorRejectsCorruptArtifactBeforeRestore(t *testing.T) {
	archive := []byte("PGDMP canonical")
	sum := sha256.Sum256([]byte("different"))
	runner := &fakeRunner{}
	e := Executor{Runner: runner, NewStore: func(backupv1.StoreSpec, backupv1.StoreCredential) (backupagent.ObjectStore, error) {
		return fakeStore{data: archive, info: backupagent.ObjectInfo{Size: int64(len(archive)), SHA256: hex.EncodeToString(sum[:]), BackupID: "b"}}, nil
	}}
	result := e.Execute(context.Background(), testLease("b", archive, strings.Repeat("a", 64)))
	if result.FailureCode != restorev1.FailureBackupIntegrity || len(runner.calls) != 0 {
		t.Fatalf("result=%+v calls=%v", result, runner.calls)
	}
}

func TestExecutorRejectsMalformedReviewAuthority(t *testing.T) {
	lease := testLease("b", []byte("PGDMP canonical"), strings.Repeat("a", 64))
	result := (Executor{}).Review(context.Background(), restorev1.ReviewLease{
		LeaseToken: "lease",
		Review:     restorev1.Review{TargetResourceID: lease.TargetSpec.ResourceID, TargetNodeID: lease.TargetSpec.Assignment.NodeID, TargetSpecHash: lease.TargetSpec.SpecHash},
		TargetSpec: lease.TargetSpec,
	})
	if result.Status != restorev1.ReviewFailed || result.FailureCode != restorev1.FailureTargetInvalid {
		t.Fatalf("malformed review result=%+v", result)
	}
}

func TestValidateLeaseNormalizesFactualPostgresVersion(t *testing.T) {
	lease := testLease("b", []byte("PGDMP canonical"), strings.Repeat("a", 64))
	lease.Backup.SourcePostgresVersion = "18.6 (Debian 18.6-1.pgdg12+2)"
	lease.Restore.SourcePostgresVersion = lease.Backup.SourcePostgresVersion
	if err := validateLease(lease); err != nil {
		t.Fatal(err)
	}
	lease.Backup.SourcePostgresVersion = "17.6"
	if err := validateLease(lease); err == nil {
		t.Fatal("unsupported backup source version was accepted")
	}
}

func TestExecutorUsesAtomicRestoreOptionsAndNoPasswordArguments(t *testing.T) {
	archive := []byte("PGDMP canonical")
	sum := sha256.Sum256(archive)
	runner := &fakeRunner{}
	e := Executor{Runner: runner, NewStore: func(backupv1.StoreSpec, backupv1.StoreCredential) (backupagent.ObjectStore, error) {
		return fakeStore{data: archive, info: backupagent.ObjectInfo{Size: int64(len(archive)), SHA256: hex.EncodeToString(sum[:]), BackupID: "b"}}, nil
	}}
	result := e.Execute(context.Background(), testLease("b", archive, hex.EncodeToString(sum[:])))
	if result.Status != restorev1.LifecycleSucceeded {
		t.Fatalf("result=%+v", result)
	}
	joined := strings.Join(runner.calls[3], " ")
	if !strings.Contains(joined, "--single-transaction") || strings.Contains(joined, "--clean") || strings.Contains(joined, "--create") || strings.Contains(joined, "secret") || strings.Contains(joined, "--password") {
		t.Fatalf("restore command=%s", joined)
	}
}

func TestExecutorReportsRollbackWhenRestoreTransactionFails(t *testing.T) {
	archive := []byte("PGDMP canonical")
	sum := sha256.Sum256(archive)
	runner := &fakeRunner{failRestore: true}
	e := Executor{Runner: runner, NewStore: func(backupv1.StoreSpec, backupv1.StoreCredential) (backupagent.ObjectStore, error) {
		return fakeStore{data: archive, info: backupagent.ObjectInfo{Size: int64(len(archive)), SHA256: hex.EncodeToString(sum[:]), BackupID: "b"}}, nil
	}}
	result := e.Execute(context.Background(), testLease("b", archive, hex.EncodeToString(sum[:])))
	if result.FailureCode != restorev1.FailureExecution || !result.RollbackConfirmed || !result.TargetPristineAfterFailure {
		t.Fatalf("result=%+v", result)
	}
}

func TestExecutorRejectsRestoreWithoutUserObjects(t *testing.T) {
	archive := []byte("PGDMP canonical")
	sum := sha256.Sum256(archive)
	runner := &fakeRunner{emptyAfter: true}
	e := Executor{Runner: runner, NewStore: func(backupv1.StoreSpec, backupv1.StoreCredential) (backupagent.ObjectStore, error) {
		return fakeStore{data: archive, info: backupagent.ObjectInfo{Size: int64(len(archive)), SHA256: hex.EncodeToString(sum[:]), BackupID: "b"}}, nil
	}}
	result := e.Execute(context.Background(), testLease("b", archive, hex.EncodeToString(sum[:])))
	if result.FailureCode != restorev1.FailureVerification {
		t.Fatalf("result=%+v", result)
	}
}

func TestExecutorRejectsUnsupportedToolVersion(t *testing.T) {
	archive := []byte("PGDMP canonical")
	sum := sha256.Sum256(archive)

	for _, invalidOutput := range []string{
		"pg_restore (PostgreSQL) 18.5\n",
		"pg_restore (PostgreSQL) 17.6\n",
		"pg_restore (PostgreSQL) 19.0\n",
		"empty",
		"pg_restore 18.6\n",
		"arbitrary string containing 18.6\n",
		"pg_restore (PostgreSQL) 118.6\n",
		"pg_restore wrapper 18.6 fake\n",
		"pg_restore (PostgreSQL) 18.6 trailing\n",
	} {
		runner := &fakeRunner{toolVersion: invalidOutput}
		e := Executor{Runner: runner, NewStore: func(backupv1.StoreSpec, backupv1.StoreCredential) (backupagent.ObjectStore, error) {
			return fakeStore{data: archive, info: backupagent.ObjectInfo{Size: int64(len(archive)), SHA256: hex.EncodeToString(sum[:]), BackupID: "b"}}, nil
		}}
		result := e.Execute(context.Background(), testLease("b", archive, hex.EncodeToString(sum[:])))
		if result.FailureCode != restorev1.FailureVersionUnsupported {
			t.Fatalf("output=%q result=%+v", invalidOutput, result)
		}
	}
}

func testLease(id string, archive []byte, sha string) restorev1.Lease {
	now := time.Now().UTC()
	spec := resourcev1.ManagedResourceSpec{SchemaVersion: resourcev1.ManagedResourceSpecSchemaVersion, ResourceID: "target", ProjectID: "p", EnvironmentID: "e", ResourceType: resourcev1.TypePostgres, Profile: "single-node-experimental", Version: resourcev1.PostgresVersion, Image: resourcev1.PostgresImage, Assignment: resourcev1.ManagedResourceAssignment{RuntimeID: "rt", NodeID: "node", AgentID: "agent"}, Replicas: 1, CPUMillicores: 1, MemoryBytes: 1, Ports: []resourcev1.ManagedResourcePort{{Name: "postgres", Port: 5432, Protocol: resourcev1.ProtocolPostgres}}, Storage: resourcev1.StorageRequest{Persistent: true, SizeBytes: 1, PolicyRef: resourcev1.StoragePolicyDefault}, Connection: resourcev1.ManagedResourceConnection{ServiceName: "target", Host: "target", Port: 5432, Protocol: resourcev1.ProtocolPostgres, Database: "opsi"}, CredentialID: "cred", ConfigurationHash: strings.Repeat("1", 64), TopologyRevision: 1, TopologyHash: strings.Repeat("2", 64)}
	spec.SpecHash, _ = spec.Hash()
	review := restorev1.Restore{SchemaVersion: restorev1.SchemaVersion, ID: "rst", ProjectID: "p", EnvironmentID: "e", BackupID: id, SourceResourceID: "source", TargetResourceID: "target", TargetNodeID: "node", ArtifactSHA256: sha, ArtifactSize: int64(len(archive)), SourcePostgresVersion: resourcev1.PostgresVersion, TargetPostgresVersion: resourcev1.PostgresVersion, SourceProfile: spec.Profile, SourceImage: spec.Image, TargetProfile: spec.Profile, TargetImage: spec.Image, TargetSpecHash: spec.SpecHash, TargetDatabase: "opsi", TargetDatabaseOID: "42", TargetPVCUID: "pvc", PristineEvidenceHash: restorev1.PristineEvidenceHash(restorev1.Review{TargetResourceID: "target", TargetNodeID: "node", TargetSpecHash: spec.SpecHash, TargetPVCUID: "pvc", TargetDatabase: "opsi", TargetDatabaseOID: "42", Objects: restorev1.ObjectSummary{Schemas: 1}}), Lifecycle: restorev1.LifecycleLeased}
	return restorev1.Lease{LeaseToken: "lease", Restore: review, Backup: backupv1.Backup{SchemaVersion: backupv1.SchemaVersion, ID: id, ProjectID: "p", EnvironmentID: "e", SourceResourceID: "source", ResourceType: resourcev1.TypePostgres, BackupType: backupv1.BackupTypePostgresLogical, SourceDatabase: "opsi", SourcePostgresVersion: resourcev1.PostgresVersion, SourceProfile: spec.Profile, SourceImage: spec.Image, Format: backupv1.FormatCustom, Lifecycle: backupv1.LifecycleSucceeded, ArtifactSize: int64(len(archive)), SHA256: sha, PGDumpVersion: "pg_dump (PostgreSQL) 18.6", ArchiveVerified: true, CompletedAt: &now}, TargetSpec: spec, Store: backupv1.StoreSpec{ID: "s", Provider: backupv1.StoreProviderS3, Bucket: "b", Region: "r"}, Credential: backupv1.StoreCredential{AccessKey: "access", SecretKey: "secret"}}
}
