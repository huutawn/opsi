package deploy

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestEngineRejectsMissingImmutableCommand(t *testing.T) {
	engine := NewEngine(openTestStore(t), EngineConfig{})
	if _, err := engine.Deploy(context.Background(), Request{}, nil); !errors.Is(err, ErrLegacyDeploymentRetired) {
		t.Fatalf("error = %v", err)
	}
}

func openTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	store, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "agent.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
