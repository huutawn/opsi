package backup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	backupv1 "github.com/opsi-dev/opsi/contracts/go/backupv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

type fakeCommandRunner struct {
	calls   [][]string
	failAt  int
	listing string
}

func (r *fakeCommandRunner) Run(_ context.Context, _ io.Reader, output io.Writer, name string, args ...string) error {
	r.calls = append(r.calls, append([]string{name}, args...))
	if r.failAt == len(r.calls) {
		return errors.New("controlled command failure")
	}
	switch len(r.calls) {
	case 1:
		_, _ = io.WriteString(output, "pg_dump (PostgreSQL) 18.6 (Debian 18.6-1.pgdg13+1)\n18.6 (Debian 18.6-1.pgdg13+1)\n")
	case 2:
		_, _ = output.Write([]byte("PGDMP\x01logical-custom-archive"))
	case 3:
		listing := r.listing
		if listing == "" {
			listing = "; Archive created\n1259; 1259 1 TABLE public backup_acceptance opsi_b_role\n"
		}
		_, _ = io.WriteString(output, listing)
	}
	return nil
}

type fakeStore struct {
	data         []byte
	info         ObjectInfo
	putErr       error
	statMismatch bool
	corrupt      bool
	deleted      bool
}

func (s *fakeStore) Put(_ context.Context, _ string, body io.ReadSeeker, size int64, sha, backupID string) (ObjectInfo, error) {
	if s.putErr != nil {
		return ObjectInfo{}, s.putErr
	}
	_, _ = body.Seek(0, io.SeekStart)
	s.data, _ = io.ReadAll(body)
	s.info = ObjectInfo{Size: size, SHA256: sha, BackupID: backupID, ETag: "etag-1", VersionID: "version-1"}
	return s.info, nil
}

func (s *fakeStore) Stat(context.Context, string) (ObjectInfo, error) {
	info := s.info
	if s.statMismatch {
		info.Size++
	}
	return info, nil
}

func (s *fakeStore) Get(context.Context, string) (io.ReadCloser, ObjectInfo, error) {
	data := append([]byte(nil), s.data...)
	if s.corrupt && len(data) > 0 {
		data[len(data)-1] ^= 0xff
	}
	return io.NopCloser(bytes.NewReader(data)), s.info, nil
}

func (s *fakeStore) Delete(context.Context, string) error { s.deleted = true; return nil }

func TestExecutorUsesPinnedPostgresToolsAndVerifiesRemoteArtifact(t *testing.T) {
	runner, store, lease := &fakeCommandRunner{}, &fakeStore{}, backupLease(t)
	executor := Executor{Runner: runner, NewStore: func(backupv1.StoreSpec, backupv1.StoreCredential) (ObjectStore, error) { return store, nil }}
	result := executor.Execute(context.Background(), lease)
	if result.Status != backupv1.LifecycleSucceeded || !result.ArchiveVerified || result.ArtifactSize != int64(len(store.data)) || len(result.SHA256) != 64 || result.PGDumpVersion == "" || result.SourcePostgresVersion == "" {
		t.Fatalf("result=%+v", result)
	}
	joined := commandText(runner.calls)
	for _, required := range []string{"pod/postgres-backup-0", "pg_dump -h 127.0.0.1", "-Fc --no-owner --no-privileges", "pg_restore --list"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("missing command %q in %s", required, joined)
		}
	}
	for _, secret := range []string{lease.Credential.AccessKey, lease.Credential.SecretKey} {
		if strings.Contains(joined, secret) {
			t.Fatalf("credential leaked into command arguments: %q", secret)
		}
	}
}

func TestExecutorTypedFailuresRemoveNonAuthoritativeObject(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*fakeCommandRunner, *fakeStore)
		code    string
		deleted bool
	}{
		{name: "database unavailable", setup: func(r *fakeCommandRunner, _ *fakeStore) { r.failAt = 1 }, code: backupv1.FailureDatabaseUnavailable},
		{name: "dump failed", setup: func(r *fakeCommandRunner, _ *fakeStore) { r.failAt = 2 }, code: backupv1.FailureDumpFailed},
		{name: "upload failed", setup: func(_ *fakeCommandRunner, s *fakeStore) { s.putErr = errors.New("upload unavailable") }, code: backupv1.FailureUploadFailed, deleted: true},
		{name: "head mismatch", setup: func(_ *fakeCommandRunner, s *fakeStore) { s.statMismatch = true }, code: backupv1.FailureIntegrityFailed, deleted: true},
		{name: "checksum mismatch", setup: func(_ *fakeCommandRunner, s *fakeStore) { s.corrupt = true }, code: backupv1.FailureIntegrityFailed, deleted: true},
		{name: "invalid archive", setup: func(r *fakeCommandRunner, _ *fakeStore) { r.failAt = 3 }, code: backupv1.FailureArtifactInvalid, deleted: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runner, store, lease := &fakeCommandRunner{}, &fakeStore{}, backupLease(t)
			tc.setup(runner, store)
			result := (Executor{Runner: runner, NewStore: func(backupv1.StoreSpec, backupv1.StoreCredential) (ObjectStore, error) { return store, nil }}).Execute(context.Background(), lease)
			if result.Status != backupv1.LifecycleFailed || result.FailureCode != tc.code || store.deleted != tc.deleted {
				t.Fatalf("result=%+v deleted=%t", result, store.deleted)
			}
			if strings.Contains(result.FailureMessageRedacted, lease.Credential.SecretKey) {
				t.Fatal("S3 secret leaked into failure")
			}
		})
	}
}

func backupLease(t *testing.T) backupv1.Lease {
	t.Helper()
	spec := resourcev1.ManagedResourceSpec{
		SchemaVersion: resourcev1.ManagedResourceSpecSchemaVersion, ResourceID: "res-1", ProjectID: "project-1", EnvironmentID: "env-1", ResourceType: resourcev1.TypePostgres,
		Profile: "single-node-experimental", Version: resourcev1.PostgresVersion, Image: resourcev1.PostgresImage, Assignment: resourcev1.ManagedResourceAssignment{RuntimeID: "runtime-1", NodeID: "node-1", AgentID: "agent-1"},
		Replicas: 1, CPUMillicores: 250, MemoryBytes: 256 << 20, Ports: []resourcev1.ManagedResourcePort{{Name: "postgres", Port: 5432, Protocol: resourcev1.ProtocolPostgres}},
		Storage: resourcev1.StorageRequest{Persistent: true, SizeBytes: 1 << 30, PolicyRef: resourcev1.StoragePolicyDefault}, Connection: resourcev1.ManagedResourceConnection{ServiceName: "postgres-backup", Host: "postgres-backup.svc", Port: 5432, Protocol: resourcev1.ProtocolPostgres, Database: backupv1.CanonicalDatabase},
		CredentialID: "management-1", ConfigurationHash: strings.Repeat("a", 64), TopologyRevision: 1, TopologyHash: strings.Repeat("b", 64),
	}
	spec.SpecHash, _ = spec.Hash()
	return backupv1.Lease{
		LeaseToken: "lease-1", SourceSpec: spec,
		Backup: backupv1.Backup{SchemaVersion: backupv1.SchemaVersion, ID: "bkp_1", ProjectID: spec.ProjectID, EnvironmentID: spec.EnvironmentID, SourceResourceID: spec.ResourceID, SourceNodeID: spec.Assignment.NodeID, ResourceType: resourcev1.TypePostgres, BackupType: backupv1.BackupTypePostgresLogical, SourceDatabase: backupv1.CanonicalDatabase, SourceSpecHash: spec.SpecHash, Format: backupv1.FormatCustom, ObjectKey: "projects/project-1/backups/bkp_1.dump"},
		Store:  backupv1.StoreSpec{ID: "store-1", Provider: backupv1.StoreProviderS3, Endpoint: "https://s3.example.test", Bucket: "backups", Region: "test-1"}, Credential: backupv1.StoreCredential{AccessKey: "access-canary", SecretKey: "secret-canary"},
	}
}

func commandText(calls [][]string) string {
	parts := make([]string, 0, len(calls))
	for _, call := range calls {
		parts = append(parts, strings.Join(call, " "))
	}
	return strings.Join(parts, "\n")
}
