package postgres

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

type proposalReviewMigrationRecorder struct{ statements []string }

func (r *proposalReviewMigrationRecorder) ExecContext(_ context.Context, statement string, _ ...any) (sql.Result, error) {
	r.statements = append(r.statements, statement)
	return nil, nil
}

func TestMigrateMCP04ProposalReviewExpandsConfigurationKind(t *testing.T) {
	recorder := &proposalReviewMigrationRecorder{}
	if err := MigrateMCP04ProposalReview(context.Background(), recorder); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(recorder.statements, "\n")
	if !strings.Contains(joined, "'dependency','service_configuration','source_patch'") {
		t.Fatal("proposal review kind constraint does not admit service_configuration during the compatibility window")
	}
	if !strings.Contains(joined, "pg_get_constraintdef") || !strings.Contains(joined, "DROP CONSTRAINT IF EXISTS proposal_reviews_kind_check") {
		t.Fatal("existing proposal review constraint is not migrated idempotently")
	}
}
