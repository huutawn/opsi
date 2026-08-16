package cutover

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	backupagent "github.com/opsi-dev/opsi/agent/internal/backup"
	"github.com/opsi-dev/opsi/agent/internal/deploy"
	cutoverv1 "github.com/opsi-dev/opsi/contracts/go/cutoverv1"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

const pingScript = `set -eu
role=$1; db=$2
IFS= read -r password
export PGPASSWORD=$password
psql -v ON_ERROR_STOP=1 -qAt -h 127.0.0.1 -U "$role" -d "$db" -c 'SELECT 1'`

const privilegeScript = `set -eu
role=$1; db=$2
manager=$(cat /run/opsi-postgres/username)
export PGPASSWORD=$(cat /run/opsi-postgres/password)
psql -v ON_ERROR_STOP=1 -qAt -h 127.0.0.1 -U "$manager" -d "$db" -c "SELECT rolcanlogin::int||':'||rolsuper::int||':'||rolcreatedb::int||':'||rolcreaterole::int||':'||rolreplication::int||':'||rolbypassrls::int FROM pg_roles WHERE rolname='$role'; SELECT has_database_privilege('$role', '$db', 'CONNECT')::int||':'||has_schema_privilege('$role', 'public', 'USAGE')::int||':'||has_schema_privilege('$role', 'public', 'CREATE')::int"`

type Executor struct {
	KubectlPath string
	Runner      backupagent.CommandRunner
}

func (e Executor) Review(ctx context.Context, lease cutoverv1.ReviewLease) cutoverv1.ReviewResult {
	result := cutoverv1.ReviewResult{
		Status:     cutoverv1.ReviewFailed,
		LeaseToken: lease.LeaseToken,
	}
	if lease.LeaseToken == "" || lease.Review.ID == "" ||
		lease.SourceSpec.ResourceID != lease.Review.SourceResourceID ||
		lease.TargetSpec.ResourceID != lease.Review.TargetResourceID ||
		lease.SourceCredential == nil || lease.TargetCredential == nil {
		return failReview(result, cutoverv1.FailureTargetInvalid, errors.New("cutover review lease authority is invalid"))
	}

	// 1. Source DB connectivity preflight (SELECT 1)
	if err := e.ping(ctx, lease.SourceSpec, *lease.SourceCredential); err != nil {
		return failReview(result, cutoverv1.FailureDatabaseUnavailable, fmt.Errorf("source database connectivity preflight failed: %w", err))
	}

	// 2. Target DB connectivity preflight (SELECT 1)
	if err := e.ping(ctx, lease.TargetSpec, *lease.TargetCredential); err != nil {
		return failReview(result, cutoverv1.FailureDatabaseUnavailable, fmt.Errorf("target database connectivity preflight failed: %w", err))
	}

	// 3. Target role privilege preflight
	roleAttrs, err := e.checkPrivileges(ctx, lease.TargetSpec, *lease.TargetCredential)
	if err != nil {
		return failReview(result, cutoverv1.FailurePrivilegeInvalid, fmt.Errorf("target role privilege preflight failed: %w", err))
	}

	summary := lease.Review.ValidationSummary
	summary.SourceSQLPreflight = "PASS"
	summary.TargetSQLPreflight = "PASS"
	summary.TargetRoleAttributes = roleAttrs
	summary.SourceBindingReady = true
	summary.TargetBindingReady = true
	summary.TargetRestoreReady = true

	review := lease.Review
	review.ValidationSummary = summary
	evidenceHash := cutoverv1.EvidenceHash(review)

	result.Status = cutoverv1.ReviewSucceeded
	result.SourceSQLPreflight = "PASS"
	result.TargetSQLPreflight = "PASS"
	result.TargetRoleAttributes = roleAttrs
	result.ValidationSummary = summary
	result.EvidenceHash = evidenceHash
	return result
}

func (e Executor) ping(ctx context.Context, spec resourcev1.ManagedResourceSpec, cred resourcev1.ManagedResourceCredential) error {
	var out bytes.Buffer
	input := bytes.NewReader([]byte(cred.Password + "\n"))
	err := e.run(ctx, input, &out, spec, pingScript, cred.Username, cred.Database)
	if err != nil {
		return err
	}
	if strings.TrimSpace(out.String()) != "1" {
		return fmt.Errorf("ping returned unexpected output: %q", out.String())
	}
	return nil
}

func (e Executor) checkPrivileges(ctx context.Context, spec resourcev1.ManagedResourceSpec, cred resourcev1.ManagedResourceCredential) (string, error) {
	var out bytes.Buffer
	err := e.run(ctx, nil, &out, spec, privilegeScript, cred.Username, cred.Database)
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) < 2 {
		return "", fmt.Errorf("privilege check output incomplete: %q", out.String())
	}
	roleLine := strings.TrimSpace(lines[0])
	privLine := strings.TrimSpace(lines[1])

	// roleLine should be 1:0:0:0:0:0 (canlogin=1, super=0, createdb=0, createrole=0, replication=0, bypassrls=0)
	if roleLine != "1:0:0:0:0:0" {
		return "", fmt.Errorf("role has invalid attributes: %s", roleLine)
	}
	// privLine should be 1:1:1 (connect=1, usage=1, create=1)
	if privLine != "1:1:1" {
		return "", fmt.Errorf("role has invalid privileges: %s", privLine)
	}
	return "LOGIN,NOSUPERUSER,NOCREATEDB,NOCREATEROLE,NOREPLICATION,NOBYPASSRLS", nil
}

func (e Executor) run(ctx context.Context, input io.Reader, output io.Writer, spec resourcev1.ManagedResourceSpec, script string, values ...string) error {
	args := []string{"sh", "-ec", script, "opsi-cutover"}
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

func failReview(r cutoverv1.ReviewResult, code string, err error) cutoverv1.ReviewResult {
	r.Status = cutoverv1.ReviewFailed
	r.FailureCode = code
	message := "cutover review preflight failed"
	if err != nil {
		message = err.Error()
	}
	message = deploy.RedactSensitive(message)
	if len(message) > 512 {
		message = message[:512]
	}
	r.FailureMessageRedacted = message
	return r
}
