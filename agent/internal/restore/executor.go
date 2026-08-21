package restore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	backupagent "github.com/opsi-dev/opsi/agent/internal/backup"
	"github.com/opsi-dev/opsi/agent/internal/deploy"
	backupv1 "github.com/opsi-dev/opsi/contracts/go/backupv1"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
	restorev1 "github.com/opsi-dev/opsi/contracts/go/restorev1"
)

const maxArtifactBytes int64 = 64 << 30

const inspectScript = `set -eu
db=$1
test "$(cat /run/opsi-postgres/database)" = "$db"
u=$(cat /run/opsi-postgres/username)
export PGPASSWORD=$(cat /run/opsi-postgres/password)
psql -v ON_ERROR_STOP=1 -qAt -F '|' -h 127.0.0.1 -U "$u" -d "$db" <<'SQL'
SELECT (SELECT oid::text FROM pg_database WHERE datname=current_database()),
 count(DISTINCT n.oid) FILTER (WHERE n.nspname !~ '^pg_' AND n.nspname <> 'information_schema'),
 count(DISTINCT c.oid) FILTER (WHERE c.relkind IN ('r','p')),
 count(DISTINCT c.oid) FILTER (WHERE c.relkind='S'),
 count(DISTINCT c.oid) FILTER (WHERE c.relkind IN ('i','I')),
 count(DISTINCT p.oid)
FROM pg_namespace n
LEFT JOIN pg_class c ON c.relnamespace=n.oid
LEFT JOIN pg_proc p ON p.pronamespace=n.oid
WHERE n.nspname !~ '^pg_' AND n.nspname <> 'information_schema';
SQL`

const versionScript = `set -eu
pg_restore --version`

const restoreScript = `set -eu
db=$1
test "$(cat /run/opsi-postgres/database)" = "$db"
u=$(cat /run/opsi-postgres/username)
export PGPASSWORD=$(cat /run/opsi-postgres/password)
exec pg_restore -h 127.0.0.1 -U "$u" --dbname="$db" --single-transaction --no-owner --no-privileges`

type Executor struct {
	KubectlPath string
	Runner      backupagent.CommandRunner
	NewStore    func(backupv1.StoreSpec, backupv1.StoreCredential) (backupagent.ObjectStore, error)
}

func (e Executor) Review(ctx context.Context, lease restorev1.ReviewLease) restorev1.ReviewResult {
	result := restorev1.ReviewResult{Status: restorev1.ReviewFailed, LeaseToken: lease.LeaseToken}
	if lease.LeaseToken == "" || lease.Review.ID == "" || lease.TargetSpec.ResourceID != lease.Review.TargetResourceID || lease.TargetSpec.Assignment.NodeID != lease.Review.TargetNodeID || lease.TargetSpec.SpecHash != lease.Review.TargetSpecHash {
		return reviewFail(result, restorev1.FailureTargetInvalid, errors.New("restore review lease authority is invalid"))
	}
	oid, objects, err := e.inspect(ctx, lease.TargetSpec)
	if err != nil {
		return reviewFail(result, restorev1.FailureDatabaseUnavailable, err)
	}
	if !objects.Pristine() {
		return reviewFail(result, restorev1.FailureTargetNotEmpty, errors.New("target database contains user objects"))
	}
	review := lease.Review
	review.TargetDatabaseOID, review.Objects = oid, objects
	result.Status, result.TargetDatabaseOID, result.Objects = restorev1.ReviewSucceeded, oid, objects
	result.PristineEvidenceHash = restorev1.PristineEvidenceHash(review)
	return result
}

func (e Executor) Execute(ctx context.Context, lease restorev1.Lease) restorev1.Result {
	result := restorev1.Result{Status: restorev1.LifecycleFailed, LeaseToken: lease.LeaseToken}
	if err := validateLease(lease); err != nil {
		return fail(result, restorev1.FailureTargetInvalid, err, lease)
	}
	store, err := e.objectStore(lease.Store, lease.Credential)
	if err != nil {
		return fail(result, restorev1.FailureDownload, err, lease)
	}
	info, err := store.Stat(ctx, lease.Backup.ObjectKey)
	if err != nil || info.Size != lease.Restore.ArtifactSize || info.SHA256 != lease.Restore.ArtifactSHA256 || info.BackupID != lease.Backup.ID {
		if err == nil {
			err = errors.New("backup object metadata does not match immutable authority")
		}
		return fail(result, restorev1.FailureBackupIntegrity, err, lease)
	}
	body, getInfo, err := store.Get(ctx, lease.Backup.ObjectKey)
	if err != nil {
		return fail(result, restorev1.FailureDownload, err, lease)
	}
	defer body.Close()
	file, err := os.CreateTemp("", "opsi-postgres-restore-*.dump")
	if err != nil {
		return fail(result, restorev1.FailureDownload, err, lease)
	}
	path := file.Name()
	defer os.Remove(path)
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return fail(result, restorev1.FailureDownload, err, lease)
	}
	h := sha256.New()
	written, err := io.Copy(io.MultiWriter(file, h), io.LimitReader(body, maxArtifactBytes+1))
	if err != nil || written != lease.Restore.ArtifactSize || getInfo.Size != lease.Restore.ArtifactSize || hex.EncodeToString(h.Sum(nil)) != lease.Restore.ArtifactSHA256 {
		if err == nil {
			err = errors.New("downloaded backup checksum or size does not match")
		}
		return fail(result, restorev1.FailureBackupIntegrity, err, lease)
	}
	versionOut := &boundedBuffer{limit: 4096}
	if err := e.run(ctx, nil, versionOut, lease.TargetSpec, versionScript); err != nil {
		return fail(result, restorev1.FailureDatabaseUnavailable, err, lease)
	}
	version := strings.TrimSpace(versionOut.String())
	toolRelease, ok := resourcev1.ParsePostgresToolRelease(version)
	supportedRelease, supportedOK := resourcev1.ParsePostgresVersion(resourcev1.PostgresVersion)
	if !ok || !supportedOK || toolRelease != supportedRelease {
		return fail(result, restorev1.FailureVersionUnsupported, errors.New("pg_restore tooling version is unsupported"), lease)
	}
	result.PGRestoreVersion = version
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fail(result, restorev1.FailureBackupIntegrity, err, lease)
	}
	listing := &boundedBuffer{limit: 4 << 20}
	if err := e.runArgs(ctx, file, listing, lease.TargetSpec, "pg_restore", "--list"); err != nil || listing.Len() == 0 || listing.overflow {
		if err == nil {
			err = errors.New("custom archive listing is empty or too large")
		}
		return fail(result, restorev1.FailureBackupIntegrity, err, lease)
	}
	result.ArchiveVerified = true
	oid, before, err := e.inspect(ctx, lease.TargetSpec)
	if err != nil {
		return fail(result, restorev1.FailureDatabaseUnavailable, err, lease)
	}
	review := restorev1.Review{TargetResourceID: lease.Restore.TargetResourceID, TargetNodeID: lease.Restore.TargetNodeID, TargetSpecHash: lease.Restore.TargetSpecHash, TargetPVCUID: lease.Restore.TargetPVCUID, TargetDatabase: lease.Restore.TargetDatabase, TargetDatabaseOID: oid, Objects: before}
	if oid != lease.Restore.TargetDatabaseOID || !before.Pristine() || restorev1.PristineEvidenceHash(review) != lease.Restore.PristineEvidenceHash {
		return fail(result, restorev1.FailureStaleReview, errors.New("target pristine identity changed after review"), lease)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fail(result, restorev1.FailureBackupIntegrity, err, lease)
	}
	if err := e.run(ctx, file, io.Discard, lease.TargetSpec, restoreScript, lease.Restore.TargetDatabase); err != nil {
		_, after, inspectErr := e.inspect(ctx, lease.TargetSpec)
		if inspectErr == nil && after.Pristine() {
			result.RollbackConfirmed, result.TargetPristineAfterFailure = true, true
			return fail(result, restorev1.FailureExecution, err, lease)
		}
		return fail(result, restorev1.FailureTargetStateUnknown, errors.New("restore failed and target pristine state could not be proven"), lease)
	}
	postOID, objects, err := e.inspect(ctx, lease.TargetSpec)
	if err != nil || postOID != oid || objects.Pristine() {
		if err == nil {
			err = errors.New("post-restore object verification failed")
		}
		return fail(result, restorev1.FailureVerification, err, lease)
	}
	result.Status, result.RestoredObjects = restorev1.LifecycleSucceeded, objects
	result.VerificationMetadata = map[string]string{"database": lease.Restore.TargetDatabase, "database_oid": postOID, "connectivity": "authenticated", "transaction": "committed"}
	return result
}

func validateLease(l restorev1.Lease) error {
	if l.LeaseToken == "" || l.Restore.ID == "" || l.Backup.ID != l.Restore.BackupID || l.Backup.SourceResourceID == l.Restore.TargetResourceID || l.Backup.SHA256 != l.Restore.ArtifactSHA256 || l.Backup.ArtifactSize != l.Restore.ArtifactSize || l.Backup.Lifecycle != backupv1.LifecycleSucceeded || l.TargetSpec.ResourceID != l.Restore.TargetResourceID || l.TargetSpec.Assignment.NodeID != l.Restore.TargetNodeID || l.TargetSpec.SpecHash != l.Restore.TargetSpecHash || !resourcev1.CompatiblePostgresVersions(l.Backup.SourcePostgresVersion, l.Restore.SourcePostgresVersion) || !resourcev1.CompatiblePostgresVersions(l.Restore.SourcePostgresVersion, l.Restore.TargetPostgresVersion) || !resourcev1.CompatiblePostgresVersions(l.Restore.SourcePostgresVersion, l.TargetSpec.Version) || l.TargetSpec.Profile != l.Restore.TargetProfile || l.TargetSpec.Image != resourcev1.PostgresImage {
		return errors.New("restore lease authority is invalid")
	}
	return l.TargetSpec.Validate()
}
func (e Executor) inspect(ctx context.Context, spec resourcev1.ManagedResourceSpec) (string, restorev1.ObjectSummary, error) {
	out := &boundedBuffer{limit: 4096}
	if err := e.run(ctx, nil, out, spec, inspectScript, spec.Connection.Database); err != nil {
		return "", restorev1.ObjectSummary{}, err
	}
	parts := strings.Split(strings.TrimSpace(out.String()), "|")
	if len(parts) != 6 {
		return "", restorev1.ObjectSummary{}, errors.New("PostgreSQL object evidence is invalid")
	}
	values := make([]int64, 5)
	for i := range values {
		n, err := strconv.ParseInt(parts[i+1], 10, 64)
		if err != nil {
			return "", restorev1.ObjectSummary{}, errors.New("PostgreSQL object counts are invalid")
		}
		values[i] = n
	}
	return parts[0], restorev1.ObjectSummary{Schemas: values[0], Tables: values[1], Sequences: values[2], Indexes: values[3], Functions: values[4]}, nil
}
func (e Executor) run(ctx context.Context, input io.Reader, output io.Writer, spec resourcev1.ManagedResourceSpec, script string, values ...string) error {
	args := []string{"sh", "-ec", script, "opsi-restore"}
	args = append(args, values...)
	return e.runArgs(ctx, input, output, spec, args...)
}
func (e Executor) runArgs(ctx context.Context, input io.Reader, output io.Writer, spec resourcev1.ManagedResourceSpec, command ...string) error {
	args := []string{"exec"}
	if input != nil {
		args = append(args, "-i")
	}
	args = append(args, "pod/"+spec.Connection.ServiceName+"-0", "-n", deploymentv1.StableDNSName("opsi", spec.ProjectID, spec.EnvironmentID), "-c", "postgres", "--")
	args = append(args, command...)
	runner := e.Runner
	if runner == nil {
		runner = backupagent.ExecCommandRunner{}
	}
	path := e.KubectlPath
	if path == "" {
		path = "kubectl"
	}
	return runner.Run(ctx, input, output, path, args...)
}
func (e Executor) objectStore(spec backupv1.StoreSpec, credential backupv1.StoreCredential) (backupagent.ObjectStore, error) {
	if e.NewStore != nil {
		return e.NewStore(spec, credential)
	}
	return backupagent.NewS3Store(spec, credential)
}
func reviewFail(r restorev1.ReviewResult, code string, err error) restorev1.ReviewResult {
	r.FailureCode, r.FailureMessageRedacted = code, sanitize(err)
	return r
}
func fail(r restorev1.Result, code string, err error, l restorev1.Lease) restorev1.Result {
	r.FailureCode, r.FailureMessageRedacted = code, sanitize(err)
	for _, secret := range []string{l.Credential.AccessKey, l.Credential.SecretKey, l.Credential.SessionToken} {
		if secret != "" {
			r.FailureMessageRedacted = strings.ReplaceAll(r.FailureMessageRedacted, secret, "[REDACTED]")
		}
	}
	return r
}
func sanitize(err error) string {
	message := "restore failed"
	if err != nil {
		message = err.Error()
	}
	message = deploy.RedactSensitive(message)
	if len(message) > 512 {
		message = message[:512]
	}
	return message
}

type boundedBuffer struct {
	value    strings.Builder
	limit    int
	overflow bool
}

func (b *boundedBuffer) Write(v []byte) (int, error) {
	if b.value.Len()+len(v) > b.limit {
		b.overflow = true
		return 0, fmt.Errorf("command output exceeded allowed bound")
	}
	return b.value.Write(v)
}
func (b *boundedBuffer) String() string { return b.value.String() }
func (b *boundedBuffer) Len() int       { return b.value.Len() }
