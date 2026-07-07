package dr

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/opsi-dev/opsi/agent/internal/deploy"
	"github.com/opsi-dev/opsi/agent/internal/svcatalog"
	"github.com/opsi-dev/opsi/agent/internal/telemetry"
	_ "modernc.org/sqlite"
)

type StatePaths struct {
	DeployDB         string
	ServiceCatalogDB string
	TelemetryDB      string
}

func BackupAgentState(ctx context.Context, outDir string, paths StatePaths) error {
	if outDir == "" {
		return errors.New("backup output directory is required")
	}
	if paths.DeployDB == "" && paths.ServiceCatalogDB == "" && paths.TelemetryDB == "" {
		return errors.New("at least one agent state database path is required")
	}
	if err := os.MkdirAll(filepath.Join(outDir, "agent"), 0o700); err != nil {
		return fmt.Errorf("create agent backup dir: %w", err)
	}
	if paths.DeployDB != "" {
		if st, err := deploy.OpenSQLiteStore(paths.DeployDB); err != nil {
			return fmt.Errorf("migrate deploy db before backup: %w", err)
		} else {
			_ = st.Close()
		}
		if err := sqliteVacuumInto(ctx, paths.DeployDB, filepath.Join(outDir, "agent", "deploy.sqlite")); err != nil {
			return err
		}
	}
	if paths.ServiceCatalogDB != "" {
		if st, err := svcatalog.OpenStore(paths.ServiceCatalogDB); err != nil {
			return fmt.Errorf("migrate service catalog db before backup: %w", err)
		} else {
			_ = st.Close()
		}
		if err := sqliteVacuumInto(ctx, paths.ServiceCatalogDB, filepath.Join(outDir, "agent", "service_catalog.sqlite")); err != nil {
			return err
		}
	}
	if paths.TelemetryDB != "" {
		if st, err := telemetry.OpenSQLiteStore(paths.TelemetryDB); err != nil {
			return fmt.Errorf("migrate telemetry db before backup: %w", err)
		} else {
			_ = st.Close()
		}
		if err := backupTelemetryMetadata(ctx, paths.TelemetryDB, filepath.Join(outDir, "agent", "telemetry_metadata.sqlite")); err != nil {
			return err
		}
	}
	return nil
}

func RestoreAgentState(ctx context.Context, inDir string, paths StatePaths) error {
	if inDir == "" {
		return errors.New("backup input directory is required")
	}
	if paths.DeployDB == "" && paths.ServiceCatalogDB == "" && paths.TelemetryDB == "" {
		return errors.New("at least one agent restore database path is required")
	}
	if paths.DeployDB != "" {
		if err := restoreSQLite(filepath.Join(inDir, "agent", "deploy.sqlite"), paths.DeployDB); err != nil {
			return err
		}
		st, err := deploy.OpenSQLiteStore(paths.DeployDB)
		if err != nil {
			return fmt.Errorf("verify restored deploy db: %w", err)
		}
		_ = st.Close()
	}
	if paths.ServiceCatalogDB != "" {
		if err := restoreSQLite(filepath.Join(inDir, "agent", "service_catalog.sqlite"), paths.ServiceCatalogDB); err != nil {
			return err
		}
		st, err := svcatalog.OpenStore(paths.ServiceCatalogDB)
		if err != nil {
			return fmt.Errorf("verify restored service catalog db: %w", err)
		}
		_ = st.Close()
	}
	if paths.TelemetryDB != "" {
		if err := restoreSQLite(filepath.Join(inDir, "agent", "telemetry_metadata.sqlite"), paths.TelemetryDB); err != nil {
			return err
		}
		st, err := telemetry.OpenSQLiteStore(paths.TelemetryDB)
		if err != nil {
			return fmt.Errorf("verify restored telemetry metadata db: %w", err)
		}
		_ = st.Close()
	}
	return nil
}

func sqliteVacuumInto(ctx context.Context, src, dst string) error {
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("agent state db %q is required: %w", src, err)
	}
	if err := os.RemoveAll(dst); err != nil {
		return fmt.Errorf("remove stale sqlite backup: %w", err)
	}
	db, err := sql.Open("sqlite", src)
	if err != nil {
		return fmt.Errorf("open sqlite source: %w", err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return fmt.Errorf("checkpoint sqlite source: %w", err)
	}
	if _, err := db.ExecContext(ctx, "VACUUM INTO "+sqliteQuote(dst)); err != nil {
		return fmt.Errorf("backup sqlite source: %w", err)
	}
	return nil
}

func backupTelemetryMetadata(ctx context.Context, src, dst string) error {
	if err := os.RemoveAll(dst); err != nil {
		return fmt.Errorf("remove stale telemetry backup: %w", err)
	}
	st, err := telemetry.OpenSQLiteStore(dst)
	if err != nil {
		return fmt.Errorf("create sanitized telemetry backup: %w", err)
	}
	_ = st.Close()
	db, err := sql.Open("sqlite", dst)
	if err != nil {
		return fmt.Errorf("open sanitized telemetry backup: %w", err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "ATTACH DATABASE "+sqliteQuote(src)+" AS src"); err != nil {
		return fmt.Errorf("attach telemetry source: %w", err)
	}
	for _, stmt := range []string{
		`INSERT INTO incidents SELECT * FROM src.incidents`,
		`INSERT INTO audit_log SELECT * FROM src.audit_log`,
		`INSERT INTO uptime_checks SELECT * FROM src.uptime_checks`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("copy sanitized telemetry metadata: %w", err)
		}
	}
	return nil
}

func restoreSQLite(src, dst string) error {
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("backup artifact %q is required: %w", src, err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return fmt.Errorf("create restore dir: %w", err)
	}
	tmp := dst + ".restore-tmp"
	if err := copyFile(src, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("install restored sqlite db: %w", err)
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open backup artifact: %w", err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create restore temp file: %w", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("copy backup artifact: %w", err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close restore temp file: %w", err)
	}
	return nil
}

func sqliteQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
