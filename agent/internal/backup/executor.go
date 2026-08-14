package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/opsi-dev/opsi/agent/internal/deploy"
	backupv1 "github.com/opsi-dev/opsi/contracts/go/backupv1"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

// ponytail: bounded local staging avoids a streaming SigV4 implementation; raise only when measured backups need it.
const maxArtifactBytes int64 = 64 << 30

const postgresToolInfoScript = `set -eu
db=$1
test "$(cat /run/opsi-postgres/database)" = "$db"
u=$(cat /run/opsi-postgres/username)
export PGPASSWORD=$(cat /run/opsi-postgres/password)
pg_dump --version
psql -v ON_ERROR_STOP=1 -qAt -h 127.0.0.1 -U "$u" -d "$db" -c 'SHOW server_version'`

const postgresDumpScript = `set -eu
db=$1
test "$(cat /run/opsi-postgres/database)" = "$db"
u=$(cat /run/opsi-postgres/username)
export PGPASSWORD=$(cat /run/opsi-postgres/password)
exec pg_dump -h 127.0.0.1 -U "$u" -d "$db" -Fc --no-owner --no-privileges`

type CommandRunner interface {
	Run(context.Context, io.Reader, io.Writer, string, ...string) error
}

type ExecCommandRunner struct{}

func (ExecCommandRunner) Run(ctx context.Context, input io.Reader, output io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin, cmd.Stdout = input, output
	stderr := &boundedBuffer{limit: 8192}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		message := deploy.RedactSensitive(strings.TrimSpace(stderr.String()))
		if message == "" {
			message = "command failed"
		}
		return fmt.Errorf("%s: %s", name, message)
	}
	if stderr.overflow {
		return errors.New("command diagnostics exceeded the allowed bound")
	}
	return nil
}

type Executor struct {
	KubectlPath string
	Runner      CommandRunner
	NewStore    func(backupv1.StoreSpec, backupv1.StoreCredential) (ObjectStore, error)
}

func (e Executor) Execute(ctx context.Context, lease backupv1.Lease) backupv1.Result {
	result := backupv1.Result{Status: backupv1.LifecycleFailed, LeaseToken: lease.LeaseToken}
	if err := validateLease(lease); err != nil {
		return fail(result, backupv1.FailureResourceNotReady, err, lease)
	}
	store, err := e.objectStore(lease.Store, lease.Credential)
	if err != nil {
		return fail(result, backupv1.FailureStoreUnavailable, err, lease)
	}
	pgDumpVersion, serverVersion, err := e.postgresVersions(ctx, lease.SourceSpec)
	if err != nil {
		return fail(result, backupv1.FailureDatabaseUnavailable, err, lease)
	}
	file, err := os.CreateTemp("", "opsi-postgres-backup-*.dump")
	if err != nil {
		return fail(result, backupv1.FailureDumpFailed, err, lease)
	}
	path := file.Name()
	defer os.Remove(path)
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return fail(result, backupv1.FailureDumpFailed, err, lease)
	}
	hasher := sha256.New()
	writer := &artifactWriter{file: file, hash: hasher, limit: maxArtifactBytes}
	if err := e.runKubectl(ctx, nil, writer, lease.SourceSpec, postgresDumpScript, lease.Backup.SourceDatabase); err != nil {
		return fail(result, backupv1.FailureDumpFailed, err, lease)
	}
	if err := file.Sync(); err != nil || writer.size == 0 {
		if err == nil {
			err = errors.New("pg_dump produced an empty artifact")
		}
		return fail(result, backupv1.FailureDumpFailed, err, lease)
	}
	sha := hex.EncodeToString(hasher.Sum(nil))
	info, err := store.Put(ctx, lease.Backup.ObjectKey, file, writer.size, sha, lease.Backup.ID)
	if err != nil {
		_ = store.Delete(ctx, lease.Backup.ObjectKey)
		return fail(result, backupv1.FailureUploadFailed, err, lease)
	}
	verified, err := store.Stat(ctx, lease.Backup.ObjectKey)
	if err != nil || verified.Size != writer.size || verified.SHA256 != sha || verified.BackupID != lease.Backup.ID {
		_ = store.Delete(ctx, lease.Backup.ObjectKey)
		if err == nil {
			err = errors.New("uploaded backup object metadata does not match")
		}
		return fail(result, backupv1.FailureIntegrityFailed, err, lease)
	}
	if err := verifyRemoteChecksum(ctx, store, lease.Backup.ObjectKey, writer.size, sha); err != nil {
		_ = store.Delete(ctx, lease.Backup.ObjectKey)
		return fail(result, backupv1.FailureIntegrityFailed, err, lease)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = store.Delete(ctx, lease.Backup.ObjectKey)
		return fail(result, backupv1.FailureArtifactInvalid, err, lease)
	}
	listing := &boundedBuffer{limit: 4 << 20}
	if err := e.runKubectlArgs(ctx, file, listing, lease.SourceSpec, "pg_restore", "--list"); err != nil || listing.Len() == 0 || listing.overflow {
		_ = store.Delete(ctx, lease.Backup.ObjectKey)
		if err == nil {
			err = errors.New("pg_restore archive listing is empty or too large")
		}
		return fail(result, backupv1.FailureArtifactInvalid, err, lease)
	}
	result.Status, result.SourcePostgresVersion, result.PGDumpVersion = backupv1.LifecycleSucceeded, serverVersion, pgDumpVersion
	result.ArtifactSize, result.SHA256, result.ArchiveVerified = writer.size, sha, true
	result.ObjectETag, result.ObjectVersionID = first(info.ETag, verified.ETag), first(info.VersionID, verified.VersionID)
	return result
}

func validateLease(lease backupv1.Lease) error {
	if lease.LeaseToken == "" || lease.Backup.ID == "" || lease.Backup.BackupType != backupv1.BackupTypePostgresLogical || lease.Backup.Format != backupv1.FormatCustom || lease.Backup.SourceDatabase != backupv1.CanonicalDatabase || lease.SourceSpec.ResourceType != resourcev1.TypePostgres || lease.SourceSpec.ResourceID != lease.Backup.SourceResourceID || lease.SourceSpec.SpecHash != lease.Backup.SourceSpecHash || lease.SourceSpec.Connection.Database != lease.Backup.SourceDatabase {
		return errors.New("backup lease authority is invalid")
	}
	if err := lease.SourceSpec.Validate(); err != nil {
		return err
	}
	return nil
}

func (e Executor) postgresVersions(ctx context.Context, spec resourcev1.ManagedResourceSpec) (string, string, error) {
	out := &boundedBuffer{limit: 4096}
	if err := e.runKubectl(ctx, nil, out, spec, postgresToolInfoScript, spec.Connection.Database); err != nil {
		return "", "", err
	}
	lines := strings.FieldsFunc(strings.TrimSpace(out.String()), func(r rune) bool { return r == '\r' || r == '\n' })
	if len(lines) != 2 {
		return "", "", errors.New("PostgreSQL tool version evidence is invalid")
	}
	pgDump, server := strings.TrimSpace(lines[0]), strings.TrimSpace(lines[1])
	if !strings.Contains(pgDump, "(PostgreSQL) "+spec.Version) || !strings.HasPrefix(server, spec.Version) {
		return "", "", errors.New("PostgreSQL backup tooling does not match the source runtime")
	}
	return pgDump, server, nil
}

func (e Executor) runKubectl(ctx context.Context, input io.Reader, output io.Writer, spec resourcev1.ManagedResourceSpec, script string, values ...string) error {
	args := []string{"sh", "-ec", script, "opsi-backup"}
	args = append(args, values...)
	return e.runKubectlArgs(ctx, input, output, spec, args...)
}

func (e Executor) runKubectlArgs(ctx context.Context, input io.Reader, output io.Writer, spec resourcev1.ManagedResourceSpec, command ...string) error {
	args := []string{"exec"}
	if input != nil {
		args = append(args, "-i")
	}
	args = append(args, "pod/"+spec.Connection.ServiceName+"-0", "-n", deploymentv1.StableDNSName("opsi", spec.ProjectID, spec.EnvironmentID), "-c", "postgres", "--")
	args = append(args, command...)
	runner := e.Runner
	if runner == nil {
		runner = ExecCommandRunner{}
	}
	return runner.Run(ctx, input, output, first(e.KubectlPath, "kubectl"), args...)
}

func (e Executor) objectStore(spec backupv1.StoreSpec, credential backupv1.StoreCredential) (ObjectStore, error) {
	if e.NewStore != nil {
		return e.NewStore(spec, credential)
	}
	return NewS3Store(spec, credential)
}

func verifyRemoteChecksum(ctx context.Context, store ObjectStore, key string, size int64, expected string) error {
	body, _, err := store.Get(ctx, key)
	if err != nil {
		return err
	}
	defer body.Close()
	h := sha256.New()
	written, err := io.Copy(h, body)
	if err != nil {
		return err
	}
	if written != size || hex.EncodeToString(h.Sum(nil)) != expected {
		return errors.New("remote backup checksum does not match")
	}
	return nil
}

func fail(result backupv1.Result, code string, err error, lease backupv1.Lease) backupv1.Result {
	result.FailureCode = code
	message := "backup failed"
	if err != nil {
		message = err.Error()
	}
	for _, secret := range []string{lease.Credential.AccessKey, lease.Credential.SecretKey, lease.Credential.SessionToken} {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[REDACTED]")
		}
	}
	result.FailureMessageRedacted = deploy.RedactSensitive(message)
	if len(result.FailureMessageRedacted) > 512 {
		result.FailureMessageRedacted = result.FailureMessageRedacted[:512]
	}
	return result
}

type artifactWriter struct {
	file  io.Writer
	hash  hash.Hash
	limit int64
	size  int64
}

func (w *artifactWriter) Write(value []byte) (int, error) {
	if w.size+int64(len(value)) > w.limit {
		return 0, errors.New("backup artifact exceeded the allowed bound")
	}
	n, err := w.file.Write(value)
	if n > 0 {
		_, _ = w.hash.Write(value[:n])
		w.size += int64(n)
	}
	return n, err
}

type boundedBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
	written := len(value)
	remaining := b.limit - b.Len()
	if remaining <= 0 {
		b.overflow = true
		return written, nil
	}
	if len(value) > remaining {
		b.overflow = true
		value = value[:remaining]
	}
	_, _ = b.Buffer.Write(value)
	return written, nil
}

func first(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
