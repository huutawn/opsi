package adminbootstrap

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/opsi-dev/opsi/cloud/internal/auth"
	cloudpostgres "github.com/opsi-dev/opsi/cloud/internal/postgres"
)

func TestPostgresBootstrapOwnerIsIdempotentAcrossRestart(t *testing.T) {
	dsn := requirePostgresTestDSN(t)
	db := openMigratedPostgres(t, dsn)
	resetBootstrapMarker(t, db)
	suffix := slugify(newID("test"))
	rawPAT, patHash, _, err := auth.NewPAT(time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	req, err := NormalizeAndValidate(Request{
		Email: "Owner-" + suffix + "@Example.test", OrgName: "Bootstrap " + suffix, OrgSlug: "org-" + suffix,
		ProjectName: "Project " + suffix, ProjectSlug: "project-" + suffix, OAuthProvider: "github",
		OAuthSubject: "subject-" + suffix, IssuePAT: true, PATTokenHash: patHash,
	}, "github")
	if err != nil {
		t.Fatal(err)
	}
	first, err := (Service{DB: db}).ProvisionBootstrapOwner(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !first.UserCreated || !first.OrgCreated || !first.ProjectCreated || !first.MembershipCreated || !first.OAuthLinked || !first.PATCreated || first.Reused {
		t.Fatalf("unexpected first result: %+v", first)
	}
	assertBootstrapCounts(t, db, first, 1)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db = openMigratedPostgres(t, dsn)
	defer db.Close()
	second, err := (Service{DB: db}).ProvisionBootstrapOwner(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if first.UserID != second.UserID || first.OrganizationID != second.OrganizationID || first.ProjectID != second.ProjectID || !second.Reused || second.PATCreated || !second.InitialPATUnavailable || !second.OAuthLinked {
		t.Fatalf("unexpected restart result: first=%+v second=%+v", first, second)
	}
	assertBootstrapCounts(t, db, first, 1)
	verified, err := (auth.Service{Store: auth.PostgresStore{DB: db}}).VerifyPAT(t.Context(), auth.VerifyRequest{Token: rawPAT, ProjectID: first.ProjectID})
	if err != nil || verified.UserID != first.UserID || verified.Role != "owner" {
		t.Fatalf("initial PAT did not survive restart: result=%+v err=%v", verified, err)
	}
	var metadata string
	if err := db.QueryRow(`SELECT metadata_redacted::text FROM cloud_audit_events WHERE action='ADMIN_BOOTSTRAP_OWNER_COMPLETED' AND project_id=$1 ORDER BY created_at DESC LIMIT 1`, first.ProjectID).Scan(&metadata); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(metadata, rawPAT) || strings.Contains(metadata, req.PATTokenHash) || strings.Contains(metadata, req.OAuthSubject) || strings.Contains(metadata, req.Email) {
		t.Fatalf("audit contains sensitive bootstrap material: %s", metadata)
	}
}

func TestPostgresBootstrapOwnerLinksExistingMarkerOAuthIdentity(t *testing.T) {
	dsn := requirePostgresTestDSN(t)
	db := openMigratedPostgres(t, dsn)
	defer db.Close()
	resetBootstrapMarker(t, db)
	suffix := slugify(newID("link"))
	link, err := NormalizeAndValidate(Request{LinkExistingOwner: true, OAuthProvider: "github", OAuthSubject: "143307746"}, "github")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (Service{DB: db}).ProvisionBootstrapOwner(t.Context(), link); ErrorCode(err) != CodeNotInitialized {
		t.Fatalf("link without bootstrap marker err=%v", err)
	}
	_, patHash, _, err := auth.NewPAT(time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	initial, err := NormalizeAndValidate(Request{
		Email: suffix + "@example.test", OrgName: "Link " + suffix, OrgSlug: "org-" + suffix,
		ProjectName: "Link " + suffix, ProjectSlug: "project-" + suffix, IssuePAT: true, PATTokenHash: patHash,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := (Service{DB: db}).ProvisionBootstrapOwner(t.Context(), initial)
	if err != nil {
		t.Fatal(err)
	}
	linked, err := (Service{DB: db}).ProvisionBootstrapOwner(t.Context(), link)
	if err != nil {
		t.Fatal(err)
	}
	if linked.UserID != owner.UserID || linked.OrganizationID != owner.OrganizationID || linked.ProjectID != owner.ProjectID || !linked.OAuthLinked || linked.Reused {
		t.Fatalf("unexpected linked result: owner=%+v linked=%+v", owner, linked)
	}
	userID, err := (auth.PostgresStore{DB: db}).OAuthUser(t.Context(), "github", "143307746")
	if err != nil || userID != owner.UserID {
		t.Fatalf("linked OAuth user=%q err=%v", userID, err)
	}
	reused, err := (Service{DB: db}).ProvisionBootstrapOwner(t.Context(), link)
	if err != nil || !reused.Reused {
		t.Fatalf("idempotent link result=%+v err=%v", reused, err)
	}
	var metadata string
	if err := db.QueryRow(`SELECT metadata_redacted::text FROM cloud_audit_events WHERE action='ADMIN_BOOTSTRAP_OWNER_OAUTH_LINKED' AND project_id=$1 ORDER BY created_at DESC LIMIT 1`, owner.ProjectID).Scan(&metadata); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(metadata, link.OAuthSubject) {
		t.Fatalf("OAuth subject leaked into audit: %s", metadata)
	}
}

func TestPostgresBootstrapOwnerConcurrentSameAndConflictingInput(t *testing.T) {
	dsn := requirePostgresTestDSN(t)
	db := openMigratedPostgres(t, dsn)
	defer db.Close()
	resetBootstrapMarker(t, db)
	suffix := slugify(newID("race"))
	req, err := NormalizeAndValidate(Request{Email: suffix + "@example.test", OrgName: "Org", OrgSlug: "org-" + suffix, ProjectName: "Project", ProjectSlug: "project-" + suffix, OAuthProvider: "github", OAuthSubject: suffix}, "github")
	if err != nil {
		t.Fatal(err)
	}
	results := make([]Result, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = (Service{DB: db}).ProvisionBootstrapOwner(context.Background(), req)
		}(i)
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || results[0].UserID != results[1].UserID || results[0].ProjectID != results[1].ProjectID || results[0].Reused == results[1].Reused {
		t.Fatalf("same-input race failed: results=%+v errors=%v", results, errs)
	}
	assertBootstrapCounts(t, db, results[0], 0)

	resetBootstrapMarker(t, db)
	conflicts := []Request{req, req}
	for i := range conflicts {
		conflicts[i].Email = string(rune('a'+i)) + "-different-" + req.Email
		conflicts[i].OrgSlug = string(rune('a'+i)) + "-different-" + req.OrgSlug
		conflicts[i].ProjectSlug = string(rune('a'+i)) + "-different-" + req.ProjectSlug
		conflicts[i].OAuthSubject = string(rune('a'+i)) + "-different-" + req.OAuthSubject
	}
	results = make([]Result, 2)
	errs = make([]error, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = (Service{DB: db}).ProvisionBootstrapOwner(context.Background(), conflicts[i])
		}(i)
	}
	wg.Wait()
	winner, loser := -1, -1
	for i, err := range errs {
		switch ErrorCode(err) {
		case "":
			if err == nil {
				winner = i
			}
		case CodeAlreadyInitialized:
			loser = i
		}
	}
	if winner < 0 || loser < 0 {
		t.Fatalf("different-input race did not produce one winner and one conflict: results=%+v errors=%v", results, errs)
	}
	var partial int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE lower(email)=lower($1)`, conflicts[loser].Email).Scan(&partial); err != nil || partial != 0 {
		t.Fatalf("conflicting invocation left partial user: count=%d err=%v", partial, err)
	}
}

func TestPostgresBootstrapOwnerReusesPartialStateAndRejectsOAuthConflict(t *testing.T) {
	dsn := requirePostgresTestDSN(t)
	db := openMigratedPostgres(t, dsn)
	defer db.Close()
	resetBootstrapMarker(t, db)
	suffix := slugify(newID("partial"))
	userID, orgID, projectID := newID("user"), newID("org"), newID("proj")
	if _, err := db.Exec(`INSERT INTO users(id,email) VALUES($1,$2)`, userID, suffix+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO organizations(id,name,slug) VALUES($1,'Partial',$2)`, orgID, "org-"+suffix); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := db.Exec(`INSERT INTO projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES($1,$2,'Partial',$3,'no_nodes',$4,$5,$5)`, projectID, orgID, "project-"+suffix, userID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO environments(id,org_id,project_id,name,type,status) VALUES($1,$2,$3,'default','dev','active')`, newID("env"), orgID, projectID); err != nil {
		t.Fatal(err)
	}
	var envID string
	if err := db.QueryRow(`SELECT id FROM environments WHERE project_id=$1`, projectID).Scan(&envID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO runtimes(id,org_id,project_id,environment_id,name,type,status) VALUES($1,$2,$3,$4,'default','k3s','no_nodes')`, newID("rt"), orgID, projectID, envID); err != nil {
		t.Fatal(err)
	}
	req, err := NormalizeAndValidate(Request{Email: suffix + "@example.test", OrgName: "Partial", OrgSlug: "org-" + suffix, ProjectName: "Partial", ProjectSlug: "project-" + suffix, OAuthProvider: "github", OAuthSubject: suffix}, "github")
	if err != nil {
		t.Fatal(err)
	}
	result, err := (Service{DB: db}).ProvisionBootstrapOwner(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.UserID != userID || result.OrganizationID != orgID || result.ProjectID != projectID || !result.MembershipCreated || result.UserCreated || result.OrgCreated || result.ProjectCreated {
		t.Fatalf("partial reuse failed: %+v", result)
	}

	resetBootstrapMarker(t, db)
	if _, err := db.Exec(`UPDATE user_memberships SET role='developer' WHERE org_id=$1 AND user_id=$2`, orgID, userID); err != nil {
		t.Fatal(err)
	}
	conflictingOwner := req
	conflictingOwner.Email = "different-" + req.Email
	conflictingOwner.OAuthSubject = "different-" + req.OAuthSubject
	if _, err := (Service{DB: db}).ProvisionBootstrapOwner(t.Context(), conflictingOwner); ErrorCode(err) != CodeProjectOwnerConflict {
		t.Fatalf("expected project owner conflict, got %v", err)
	}
	var partialOwnerUser int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE lower(email)=lower($1)`, conflictingOwner.Email).Scan(&partialOwnerUser); err != nil || partialOwnerUser != 0 {
		t.Fatalf("project owner conflict left partial user: count=%d err=%v", partialOwnerUser, err)
	}

	resetBootstrapMarker(t, db)
	if _, err := db.Exec(`UPDATE user_memberships SET role='owner' WHERE org_id=$1 AND user_id=$2`, orgID, userID); err != nil {
		t.Fatal(err)
	}
	otherUser := newID("user")
	if _, err := db.Exec(`INSERT INTO users(id,email) VALUES($1,$2)`, otherUser, "other-"+suffix+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE oauth_identities SET user_id=$1 WHERE provider=$2 AND subject=$3`, otherUser, req.OAuthProvider, req.OAuthSubject); err != nil {
		t.Fatal(err)
	}
	if _, err := (Service{DB: db}).ProvisionBootstrapOwner(t.Context(), req); ErrorCode(err) != CodeOAuthIdentityConflict {
		t.Fatalf("expected OAuth conflict, got %v", err)
	}
}

func TestPostgresBootstrapOwnerPATOutputFailureRollsBack(t *testing.T) {
	dsn := requirePostgresTestDSN(t)
	db := openMigratedPostgres(t, dsn)
	defer db.Close()
	resetBootstrapMarker(t, db)
	suffix := slugify(newID("patfail"))
	req, err := NormalizeAndValidate(Request{
		Email: suffix + "@example.test", OrgName: "PAT Failure", OrgSlug: "org-" + suffix,
		ProjectName: "PAT Failure", ProjectSlug: "project-" + suffix, IssuePAT: true,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (Service{DB: db}).ProvisionBootstrapOwner(t.Context(), req); ErrorCode(err) != CodePATOutputUnavailable {
		t.Fatalf("expected PAT output failure, got %v", err)
	}
	for _, check := range []struct {
		query string
		arg   string
	}{
		{`SELECT COUNT(*) FROM users WHERE lower(email)=lower($1)`, req.Email},
		{`SELECT COUNT(*) FROM organizations WHERE lower(slug)=lower($1)`, req.OrgSlug},
		{`SELECT COUNT(*) FROM projects WHERE lower(slug)=lower($1)`, req.ProjectSlug},
	} {
		var count int
		if err := db.QueryRow(check.query, check.arg).Scan(&count); err != nil || count != 0 {
			t.Fatalf("PAT output failure left partial state for %q: count=%d err=%v", check.query, count, err)
		}
	}
}

func requirePostgresTestDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("OPSI_TEST_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("OPSI_REQUIRE_POSTGRES_TESTS") == "1" {
			t.Fatal("set OPSI_TEST_DATABASE_URL to run Postgres admin bootstrap tests")
		}
		t.Skip("set OPSI_TEST_DATABASE_URL to run Postgres admin bootstrap tests")
	}
	return dsn
}

func openMigratedPostgres(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := cloudpostgres.Migrate(t.Context(), db); err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db
}

func resetBootstrapMarker(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`DELETE FROM cloud_admin_bootstrap_state WHERE key=$1`, bootstrapStateKey); err != nil {
		t.Fatal(err)
	}
}

func assertBootstrapCounts(t *testing.T, db *sql.DB, result Result, wantPAT int) {
	t.Helper()
	queries := []struct {
		query string
		args  []any
		want  int
	}{
		{`SELECT COUNT(*) FROM users WHERE id=$1`, []any{result.UserID}, 1},
		{`SELECT COUNT(*) FROM organizations WHERE id=$1`, []any{result.OrganizationID}, 1},
		{`SELECT COUNT(*) FROM projects WHERE id=$1`, []any{result.ProjectID}, 1},
		{`SELECT COUNT(*) FROM user_memberships WHERE org_id=$1 AND user_id=$2 AND role='owner'`, []any{result.OrganizationID, result.UserID}, 1},
		{`SELECT COUNT(*) FROM project_memberships WHERE project_id=$1 AND user_id=$2 AND lower(role)='owner'`, []any{result.ProjectID, result.UserID}, 1},
		{`SELECT COUNT(*) FROM environments WHERE project_id=$1`, []any{result.ProjectID}, 1},
		{`SELECT COUNT(*) FROM runtimes WHERE project_id=$1`, []any{result.ProjectID}, 1},
		{`SELECT COUNT(*) FROM oauth_identities WHERE user_id=$1`, []any{result.UserID}, 1},
		{`SELECT COUNT(*) FROM personal_access_tokens WHERE user_id=$1 AND purpose=$2`, []any{result.UserID, bootstrapPATPurpose}, wantPAT},
		{`SELECT COUNT(*) FROM cloud_admin_bootstrap_state WHERE key=$1`, []any{bootstrapStateKey}, 1},
	}
	for _, item := range queries {
		var got int
		if err := db.QueryRow(item.query, item.args...).Scan(&got); err != nil || got != item.want {
			t.Fatalf("count query %q = %d, want %d, err=%v", item.query, got, item.want, err)
		}
	}
}

func TestBootstrapResultJSONContainsNoSecretFields(t *testing.T) {
	data, err := json.Marshal(Result{UserID: "user", OrganizationID: "org", ProjectID: "project", PATCreated: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{`"pat"`, `"token"`, `"secret"`, `"password"`} {
		if strings.Contains(strings.ToLower(string(data)), forbidden) {
			t.Fatalf("result JSON contains forbidden field: %s", data)
		}
	}
}
