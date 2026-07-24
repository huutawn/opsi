package deploy

import (
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	store, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "agent.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
