package postgres

import (
	"context"
	"database/sql"
)

func MigrateP05AApplicationSource(ctx context.Context, db *sql.DB) error {
	// Keep legacy service columns intact; registry owns their removal after canonical binding reads are proven in production.
	statements := []string{
		`ALTER TABLE github_service_bindings ADD COLUMN IF NOT EXISTS selected_ref TEXT`,
		`ALTER TABLE github_service_bindings ADD COLUMN IF NOT EXISTS application_root TEXT`,
		`ALTER TABLE github_service_bindings ADD COLUMN IF NOT EXISTS build_context TEXT`,
		`ALTER TABLE github_service_bindings ADD COLUMN IF NOT EXISTS build_strategy TEXT`,
		`ALTER TABLE github_service_bindings ADD COLUMN IF NOT EXISTS dockerfile_path TEXT`,
		`UPDATE github_service_bindings b
		 SET selected_ref=COALESCE(NULLIF(s.branch,''),NULLIF(r.default_branch,''),'main'),
		     application_root=CASE WHEN COALESCE(NULLIF(s.build_context,''),'.')='.' OR
		       (COALESCE(NULLIF(s.build_context,''),'.') !~ '^/' AND position(E'\\' in COALESCE(NULLIF(s.build_context,''),'.'))=0 AND
		        COALESCE(NULLIF(s.build_context,''),'.') !~ '(^|/)\.\.?(/|$)' AND COALESCE(NULLIF(s.build_context,''),'.') !~ '//|/$' AND
		        COALESCE(NULLIF(s.build_context,''),'.') !~ '[[:cntrl:]]')
		       THEN COALESCE(NULLIF(s.build_context,''),'.') ELSE '.' END,
		     build_context=CASE WHEN COALESCE(NULLIF(s.build_context,''),'.')='.' OR
		       (COALESCE(NULLIF(s.build_context,''),'.') !~ '^/' AND position(E'\\' in COALESCE(NULLIF(s.build_context,''),'.'))=0 AND
		        COALESCE(NULLIF(s.build_context,''),'.') !~ '(^|/)\.\.?(/|$)' AND COALESCE(NULLIF(s.build_context,''),'.') !~ '//|/$' AND
		        COALESCE(NULLIF(s.build_context,''),'.') !~ '[[:cntrl:]]')
		       THEN COALESCE(NULLIF(s.build_context,''),'.') ELSE '.' END,
		     build_strategy=CASE
		       WHEN s.build_method='buildpack' THEN 'buildpack'
		       WHEN s.build_method='dockerfile' AND s.dockerfile IS NOT NULL AND s.dockerfile<>'' AND s.dockerfile<>'.' AND
		         s.dockerfile !~ '^/' AND position(E'\\' in s.dockerfile)=0 AND s.dockerfile !~ '(^|/)\.\.?(/|$)' AND
		         s.dockerfile !~ '//|/$' AND s.dockerfile !~ '[[:cntrl:]]' THEN 'dockerfile'
		       ELSE 'auto' END,
		     dockerfile_path=CASE WHEN s.build_method='dockerfile' AND s.dockerfile IS NOT NULL AND s.dockerfile<>'' AND s.dockerfile<>'.' AND
		       s.dockerfile !~ '^/' AND position(E'\\' in s.dockerfile)=0 AND s.dockerfile !~ '(^|/)\.\.?(/|$)' AND
		       s.dockerfile !~ '//|/$' AND s.dockerfile !~ '[[:cntrl:]]' THEN s.dockerfile ELSE NULL END
		 FROM control_services s, github_repositories r
		 WHERE b.service_id=s.id AND b.repository_id=r.repository_id AND b.installation_id=r.installation_id AND b.application_root IS NULL`,
		`ALTER TABLE github_service_bindings ALTER COLUMN selected_ref SET DEFAULT 'main'`,
		`ALTER TABLE github_service_bindings ALTER COLUMN selected_ref SET NOT NULL`,
		`ALTER TABLE github_service_bindings ALTER COLUMN application_root SET DEFAULT '.'`,
		`ALTER TABLE github_service_bindings ALTER COLUMN application_root SET NOT NULL`,
		`ALTER TABLE github_service_bindings ALTER COLUMN build_context SET DEFAULT '.'`,
		`ALTER TABLE github_service_bindings ALTER COLUMN build_context SET NOT NULL`,
		`ALTER TABLE github_service_bindings ALTER COLUMN build_strategy SET DEFAULT 'auto'`,
		`ALTER TABLE github_service_bindings ALTER COLUMN build_strategy SET NOT NULL`,
		`DO $$ BEGIN
		 IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='github_service_bindings_source_check' AND conrelid='github_service_bindings'::regclass) THEN
		  ALTER TABLE github_service_bindings ADD CONSTRAINT github_service_bindings_source_check CHECK (
		   selected_ref<>'' AND selected_ref=btrim(selected_ref) AND selected_ref !~ '[[:cntrl:]]' AND
		   build_strategy IN ('auto','dockerfile','buildpack') AND
		   (application_root='.' OR (application_root<>'' AND application_root !~ '^/' AND position(E'\\' in application_root)=0 AND application_root !~ '(^|/)\.\.?(/|$)' AND application_root !~ '//|/$' AND application_root !~ '[[:cntrl:]]')) AND
		   (build_context='.' OR (build_context<>'' AND build_context !~ '^/' AND position(E'\\' in build_context)=0 AND build_context !~ '(^|/)\.\.?(/|$)' AND build_context !~ '//|/$' AND build_context !~ '[[:cntrl:]]')) AND
		   (build_context='.' OR application_root=build_context OR left(application_root,length(build_context)+1)=build_context||'/') AND
		   (dockerfile_path IS NULL OR (dockerfile_path<>'' AND dockerfile_path<>'.' AND dockerfile_path !~ '^/' AND position(E'\\' in dockerfile_path)=0 AND dockerfile_path !~ '(^|/)\.\.?(/|$)' AND dockerfile_path !~ '//|/$' AND dockerfile_path !~ '[[:cntrl:]]')) AND
		   (build_strategy<>'dockerfile' OR dockerfile_path IS NOT NULL)
		  );
		 END IF;
		END $$`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
