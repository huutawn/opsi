package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

type patOutput struct {
	target    string
	temporary string
	existed   bool
}

func preparePATOutput(target string) (*patOutput, error) {
	if target == "" || !filepath.IsAbs(target) {
		return nil, fmt.Errorf("pat-output-file must be an absolute path")
	}
	target = filepath.Clean(target)
	if insideGitWorktree(filepath.Dir(target)) {
		return nil, fmt.Errorf("pat-output-file must not be inside a Git worktree")
	}
	if _, err := os.Lstat(target); err == nil {
		return &patOutput{target: target, existed: true}, nil
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect PAT output file: %w", err)
	}
	parent := filepath.Dir(target)
	info, err := os.Stat(parent)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("PAT output parent directory must exist")
	}
	var suffix [12]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return nil, fmt.Errorf("generate PAT output filename: %w", err)
	}
	temporary := filepath.Join(parent, "."+filepath.Base(target)+".opsi-"+hex.EncodeToString(suffix[:]))
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create temporary PAT output: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(temporary)
		return nil, fmt.Errorf("close temporary PAT output: %w", err)
	}
	return &patOutput{target: target, temporary: temporary}, nil
}

func (o *patOutput) write(raw string) error {
	if o == nil || o.existed || o.temporary == "" {
		return fmt.Errorf("PAT output is unavailable")
	}
	file, err := os.OpenFile(o.temporary, os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("open temporary PAT output: %w", err)
	}
	if _, err := file.WriteString(raw); err != nil {
		file.Close()
		return fmt.Errorf("write temporary PAT output: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("sync temporary PAT output: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close temporary PAT output: %w", err)
	}
	return nil
}

func (o *patOutput) finalize() error {
	if o == nil || o.existed || o.temporary == "" {
		return fmt.Errorf("PAT output is unavailable")
	}
	if err := os.Link(o.temporary, o.target); err != nil {
		return err
	}
	if err := os.Remove(o.temporary); err != nil {
		return err
	}
	o.temporary = ""
	return nil
}

func (o *patOutput) cleanup() {
	if o == nil || o.temporary == "" {
		return
	}
	_ = os.Remove(o.temporary)
	o.temporary = ""
}

func insideGitWorktree(path string) bool {
	for {
		gitPath := filepath.Join(path, ".git")
		if info, err := os.Stat(gitPath); err == nil && (!info.IsDir() || fileExists(filepath.Join(gitPath, "HEAD"))) {
			return true
		}
		parent := filepath.Dir(path)
		if parent == path {
			return false
		}
		path = parent
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
