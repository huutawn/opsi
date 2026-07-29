package repository

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type FileSpec struct {
	Path    string
	Content []byte
}

type FileChange struct {
	Path      string `json:"path"`
	Action    string `json:"action"`
	OldSHA256 string `json:"old_sha256,omitempty"`
	NewSHA256 string `json:"new_sha256"`
	spec      FileSpec
	original  []byte
	mode      os.FileMode
}

type FilePlan struct {
	Root    string       `json:"-"`
	Changes []FileChange `json:"files"`
}

type WriteOptions struct {
	BeforeWrite func(index int, change FileChange) error
}

func PrepareFiles(root string, specs []FileSpec, force, yes bool) (FilePlan, error) {
	if force && !yes {
		return FilePlan{}, errors.New("--force requires --yes")
	}
	plan := FilePlan{Root: root}
	for _, spec := range specs {
		if err := inspectParents(root, spec.Path); err != nil {
			return FilePlan{}, err
		}
		target := filepath.Join(root, filepath.FromSlash(spec.Path))
		change := FileChange{Path: spec.Path, Action: "created", NewSHA256: digest(spec.Content), spec: spec, mode: 0o644}
		info, err := os.Lstat(target)
		switch {
		case errors.Is(err, os.ErrNotExist):
		case err != nil:
			return FilePlan{}, fmt.Errorf("inspect %s: %w", spec.Path, err)
		case info.Mode()&os.ModeSymlink != 0:
			return FilePlan{}, fmt.Errorf("refusing symlink target %s", spec.Path)
		case !info.Mode().IsRegular():
			return FilePlan{}, fmt.Errorf("target %s is not a regular file", spec.Path)
		default:
			original, err := os.ReadFile(target)
			if err != nil {
				return FilePlan{}, fmt.Errorf("read %s: %w", spec.Path, err)
			}
			change.original = original
			change.mode = info.Mode().Perm()
			change.OldSHA256 = digest(original)
			if string(original) == string(spec.Content) {
				change.Action = "unchanged"
			} else if force && yes {
				change.Action = "updated"
			} else {
				return FilePlan{}, fmt.Errorf("%s already exists with different content; use --force --yes to overwrite", spec.Path)
			}
		}
		plan.Changes = append(plan.Changes, change)
	}
	return plan, nil
}

func WriteFiles(plan FilePlan, options WriteOptions) error {
	written := make([]FileChange, 0, len(plan.Changes))
	for index, change := range plan.Changes {
		if change.Action == "unchanged" {
			continue
		}
		if options.BeforeWrite != nil {
			if err := options.BeforeWrite(index, change); err != nil {
				return rollback(plan.Root, written, err)
			}
		}
		if err := atomicWrite(plan.Root, change.Path, change.spec.Content, 0o644); err != nil {
			return rollback(plan.Root, written, err)
		}
		written = append(written, change)
	}
	return nil
}

func rollback(root string, written []FileChange, cause error) error {
	var rollbackErr error
	for index := len(written) - 1; index >= 0; index-- {
		change := written[index]
		if change.Action == "created" {
			if err := os.Remove(filepath.Join(root, filepath.FromSlash(change.Path))); err != nil && !errors.Is(err, os.ErrNotExist) {
				rollbackErr = errors.Join(rollbackErr, err)
			}
			continue
		}
		if err := atomicWrite(root, change.Path, change.original, change.mode); err != nil {
			rollbackErr = errors.Join(rollbackErr, err)
		}
	}
	if rollbackErr != nil {
		return errors.Join(cause, fmt.Errorf("rollback local bootstrap: %w", rollbackErr))
	}
	return cause
}

func atomicWrite(root, relative string, content []byte, mode os.FileMode) error {
	directory := filepath.Dir(filepath.Join(root, filepath.FromSlash(relative)))
	if err := makeDirectories(root, directory); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".opsi-init-*")
	if err != nil {
		return fmt.Errorf("create temporary file for %s: %w", relative, err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, filepath.Join(root, filepath.FromSlash(relative))); err != nil {
		return fmt.Errorf("replace %s: %w", relative, err)
	}
	return nil
}

func inspectParents(root, relative string) error {
	target := filepath.Join(root, filepath.FromSlash(relative))
	if err := ensureInside(root, target); err != nil {
		return err
	}
	current := filepath.Dir(target)
	for current != root {
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			current = filepath.Dir(current)
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink parent for %s", relative)
		}
		if !info.IsDir() {
			return fmt.Errorf("parent for %s is not a directory", relative)
		}
		current = filepath.Dir(current)
	}
	return nil
}

func makeDirectories(root, directory string) error {
	relative, err := filepath.Rel(root, directory)
	if err != nil {
		return err
	}
	if relative == "." {
		return nil
	}
	current := root
	for _, segment := range splitPath(relative) {
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("unsafe parent directory %s", current)
		}
	}
	return nil
}

func splitPath(value string) []string {
	var parts []string
	for value != "." && value != string(filepath.Separator) && value != "" {
		directory, file := filepath.Split(value)
		parts = append([]string{file}, parts...)
		value = filepath.Clean(directory)
	}
	return parts
}

func digest(content []byte) string {
	value := sha256.Sum256(content)
	return fmt.Sprintf("%x", value[:])
}
