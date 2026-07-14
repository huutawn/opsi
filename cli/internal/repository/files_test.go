package repository

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFilePlanSafeOverwriteAndNoOp(t *testing.T) {
	root := t.TempDir()
	specs := []FileSpec{{Path: ".opsi/opsi-cd.yaml", Content: []byte("config\n")}, {Path: ".github/workflows/opsi-cd.yaml", Content: []byte("workflow\n")}}
	plan, err := PrepareFiles(root, specs, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteFiles(plan, WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, spec := range specs {
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(spec.Path)))
		if err != nil || info.Mode().Perm() != 0o644 {
			t.Fatalf("file %s info=%v err=%v", spec.Path, info, err)
		}
	}
	plan, err = PrepareFiles(root, specs, false, false)
	if err != nil || plan.Changes[0].Action != "unchanged" || plan.Changes[1].Action != "unchanged" {
		t.Fatalf("no-op plan=%+v err=%v", plan, err)
	}
	changed := []FileSpec{{Path: specs[0].Path, Content: []byte("new\n")}, specs[1]}
	if _, err := PrepareFiles(root, changed, false, false); err == nil {
		t.Fatal("default overwrite accepted")
	}
	if _, err := PrepareFiles(root, changed, true, false); err == nil {
		t.Fatal("--force without --yes accepted")
	}
	plan, err = PrepareFiles(root, changed, true, true)
	if err != nil || plan.Changes[0].Action != "updated" || plan.Changes[0].OldSHA256 == plan.Changes[0].NewSHA256 {
		t.Fatalf("overwrite plan=%+v err=%v", plan, err)
	}
	if err := WriteFiles(plan, WriteOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestSecondFileFailureRollsBackFirst(t *testing.T) {
	root := t.TempDir()
	firstPath := filepath.Join(root, ".opsi", "opsi-cd.yaml")
	if err := os.MkdirAll(filepath.Dir(firstPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(firstPath, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	specs := []FileSpec{{Path: ".opsi/opsi-cd.yaml", Content: []byte("updated\n")}, {Path: ".github/workflows/opsi-cd.yaml", Content: []byte("workflow\n")}}
	plan, err := PrepareFiles(root, specs, true, true)
	if err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("second file failed")
	err = WriteFiles(plan, WriteOptions{BeforeWrite: func(index int, _ FileChange) error {
		if index == 1 {
			return sentinel
		}
		return nil
	}})
	if !errors.Is(err, sentinel) {
		t.Fatalf("error=%v", err)
	}
	content, err := os.ReadFile(firstPath)
	if err != nil || string(content) != "original\n" {
		t.Fatalf("first file not rolled back: %q err=%v", content, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".github", "workflows", "opsi-cd.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("second file unexpectedly exists: %v", err)
	}
}

func TestFileTargetsRejectSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".opsi"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, ".opsi", "opsi-cd.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareFiles(root, []FileSpec{{Path: ".opsi/opsi-cd.yaml", Content: []byte("new")}}, true, true); err == nil {
		t.Fatal("symlink target accepted")
	}
}
