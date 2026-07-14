package repository

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParseGitHubOrigin(t *testing.T) {
	tests := map[string]string{
		"https://github.com/Owner/Repo.git":   "Owner/Repo",
		"https://github.com/owner/repo":       "owner/repo",
		"git@github.com:owner/repo.git":       "owner/repo",
		"ssh://git@github.com/owner/repo.git": "owner/repo",
	}
	for raw, expected := range tests {
		origin, err := ParseGitHubOrigin(raw)
		if err != nil {
			t.Fatalf("parse %q: %v", raw, err)
		}
		if origin.FullName != expected {
			t.Fatalf("origin=%+v expected=%q", origin, expected)
		}
	}
	for _, raw := range []string{
		"https://gitlab.com/owner/repo.git",
		"https://token@github.com/owner/repo.git",
		"https://user:password@github.com/owner/repo.git",
		"https://github.com:8443/owner/repo.git",
		"https://github.com/owner/repo/extra",
		"git@github.com:owner/repo:extra",
		"git@github.com:owner/repo.git\nmalicious",
		"ssh://root@github.com/owner/repo.git",
	} {
		if _, err := ParseGitHubOrigin(raw); err == nil {
			t.Fatalf("unsafe origin accepted: %q", raw)
		}
	}
}

type fakeRunner struct {
	calls [][]string
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if reflect.DeepEqual(args, []string{"-C", "/work", "rev-parse", "--show-toplevel"}) {
		return []byte("/repo\n"), nil
	}
	if reflect.DeepEqual(args, []string{"-C", "/repo", "remote", "get-url", "origin"}) {
		return []byte("git@github.com:owner/repo.git\n"), nil
	}
	return nil, errors.New("unexpected command")
}

func TestDetectUsesSafeGitCommands(t *testing.T) {
	runner := &fakeRunner{}
	local, err := Detect(context.Background(), runner, "/work")
	if err != nil {
		t.Fatal(err)
	}
	if local.Root != "/repo" || local.Origin.FullName != "owner/repo" || len(runner.calls) != 2 {
		t.Fatalf("local=%+v calls=%v", local, runner.calls)
	}
	for _, call := range runner.calls {
		if strings.Join(call, " ") == "git remote -v" || strings.Contains(strings.Join(call, " "), "remote -v") {
			t.Fatalf("unsafe git command used: %v", call)
		}
	}
}

func TestMatchInventory(t *testing.T) {
	origin := GitHubOrigin{Owner: "Owner", Name: "Repo", FullName: "Owner/Repo"}
	repositories := []InventoryRepository{{RepositoryID: 123, InstallationID: 9, FullName: "owner/repo", DefaultBranch: "main", Status: "active"}}
	matched, err := MatchInventory(repositories, origin, 0)
	if err != nil || matched.RepositoryID != 123 {
		t.Fatalf("matched=%+v err=%v", matched, err)
	}
	matched, err = MatchInventory(repositories, origin, 123)
	if err != nil || matched.RepositoryID != 123 {
		t.Fatalf("explicit matched=%+v err=%v", matched, err)
	}
	if _, err := MatchInventory(repositories, GitHubOrigin{FullName: "other/repo"}, 123); err == nil {
		t.Fatal("explicit ID/full-name mismatch accepted")
	}
	if _, err := MatchInventory(repositories, origin, 999); !errors.Is(err, ErrRepositoryNotFound) {
		t.Fatalf("missing explicit ID error=%v", err)
	}
	for _, mutation := range []func(*InventoryRepository){
		func(value *InventoryRepository) { value.Status = "removed" },
		func(value *InventoryRepository) { value.Archived = true },
		func(value *InventoryRepository) { value.Disabled = true },
	} {
		candidate := repositories[0]
		mutation(&candidate)
		if _, err := MatchInventory([]InventoryRepository{candidate}, origin, 0); err == nil {
			t.Fatalf("unavailable repository accepted: %+v", candidate)
		}
	}
	if _, err := MatchInventory(nil, origin, 0); !errors.Is(err, ErrRepositoryNotFound) {
		t.Fatalf("missing error=%v", err)
	}
	duplicates := append(repositories, repositories[0])
	duplicates[1].RepositoryID = 124
	if _, err := MatchInventory(duplicates, origin, 0); err == nil {
		t.Fatal("multiple metadata matches accepted")
	}
}
