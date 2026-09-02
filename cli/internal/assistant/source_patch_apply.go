package assistant

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/opsi-dev/opsi/cli/internal/mcp"
)

const (
	ErrSourcePatchNotAttested  = "SOURCE_PATCH_NOT_ATTESTED"
	ErrSourcePatchHeadMismatch = "SOURCE_PATCH_HEAD_MISMATCH"
	ErrSourcePatchTargetDirty  = "SOURCE_PATCH_TARGET_DIRTY"
	ErrSourcePatchPreimage     = "SOURCE_PATCH_PREIMAGE_MISMATCH"
	ErrSourcePatchPathUnsafe   = "SOURCE_PATCH_PATH_UNSAFE"
	ErrSourcePatchApply        = "SOURCE_PATCH_APPLY_FAILED"
)

type SourcePatchApplyReceipt struct {
	Status       string    `json:"status"`
	Reused       bool      `json:"reused,omitempty"`
	ProposalHash string    `json:"proposal_hash"`
	SourceCommit string    `json:"source_commit"`
	ChangedFiles []string  `json:"changed_files"`
	JournalID    string    `json:"journal_id,omitempty"`
	AppliedAt    time.Time `json:"applied_at"`
}

type stagedPatchFile struct {
	relative       string
	target         string
	before         []byte
	after          []byte
	mode           os.FileMode
	alreadyApplied bool
}

func (m *Manager) ApplySourcePatch(ctx context.Context, projectID, turnID, proposalHash, expectedCommit string) (SourcePatchApplyReceipt, error) {
	m.patchMu.Lock()
	defer m.patchMu.Unlock()
	m.mu.RLock()
	turn, ok := m.turns[turnID]
	repoRoot := m.repoRoot
	m.mu.RUnlock()
	if !ok || turn.ProjectID != projectID || turn.State != "succeeded" {
		return SourcePatchApplyReceipt{}, patchApplyError(ErrSourcePatchNotAttested, "assistant turn is unavailable")
	}
	var candidate *SourcePatchProposal
	for index := range turn.SourcePatchProposals {
		value := &turn.SourcePatchProposals[index]
		if value.ProposalHash == proposalHash {
			candidate = value
			break
		}
	}
	if candidate == nil || candidate.ValidationStatus != "VALID" && candidate.ValidationStatus != "VALID_WITH_WARNINGS" {
		return SourcePatchApplyReceipt{}, patchApplyError(ErrSourcePatchNotAttested, "source patch was not attested")
	}
	if strings.TrimSpace(expectedCommit) == "" || expectedCommit != candidate.SourceCommit {
		return SourcePatchApplyReceipt{}, patchApplyError(ErrSourcePatchHeadMismatch, "expected source commit does not match the attested patch")
	}
	root, err := safeRepositoryRoot(repoRoot)
	if err != nil {
		return SourcePatchApplyReceipt{}, patchApplyError(ErrSourcePatchPathUnsafe, err.Error())
	}
	head, err := gitOutput(ctx, root, "rev-parse", "HEAD")
	if err != nil || head != expectedCommit {
		return SourcePatchApplyReceipt{}, patchApplyError(ErrSourcePatchHeadMismatch, "local HEAD no longer matches the reviewed source commit")
	}
	proposal, err := mcp.ParseSourcePatchProposalJSON(candidate.Proposal)
	if err != nil {
		return SourcePatchApplyReceipt{}, patchApplyError(ErrSourcePatchNotAttested, "stored source patch is invalid")
	}
	if proposal.ProjectID != projectID || proposal.EnvironmentID != candidate.EnvironmentID || proposal.ApplicationID != candidate.ApplicationID || proposal.Provenance.SourceCommit != candidate.SourceCommit || proposal.Provenance.ApplicationRoot != candidate.ApplicationRoot {
		return SourcePatchApplyReceipt{}, patchApplyError(ErrSourcePatchNotAttested, "stored source patch identity does not match its attestation")
	}
	files, err := stageSourcePatch(ctx, root, proposal)
	if err != nil {
		return SourcePatchApplyReceipt{}, err
	}
	if allPatchesApplied(files) {
		return SourcePatchApplyReceipt{Status: "applied", Reused: true, ProposalHash: proposalHash, SourceCommit: expectedCommit, ChangedFiles: patchPaths(files), AppliedAt: time.Now().UTC()}, nil
	}
	if anyPatchApplied(files) {
		return SourcePatchApplyReceipt{}, patchApplyError(ErrSourcePatchPreimage, "source patch is only partially present in the local worktree")
	}
	journalID := fmt.Sprintf("%d-%s", time.Now().UTC().UnixNano(), shortHash(proposalHash))
	journal, err := writePatchJournal(root, journalID, files)
	if err != nil {
		return SourcePatchApplyReceipt{}, patchApplyError(ErrSourcePatchApply, "create local recovery journal: "+err.Error())
	}
	if err := writeStagedFiles(files); err != nil {
		rollbackErr := restoreStagedFiles(files)
		if rollbackErr != nil {
			return SourcePatchApplyReceipt{}, patchApplyError(ErrSourcePatchApply, "patch failed and local rollback requires manual recovery from "+journal)
		}
		return SourcePatchApplyReceipt{}, patchApplyError(ErrSourcePatchApply, "patch write failed and was rolled back")
	}
	return SourcePatchApplyReceipt{Status: "applied", ProposalHash: proposalHash, SourceCommit: expectedCommit, ChangedFiles: patchPaths(files), JournalID: journalID, AppliedAt: time.Now().UTC()}, nil
}

func stageSourcePatch(ctx context.Context, root string, proposal mcp.SourcePatchProposal) ([]stagedPatchFile, error) {
	files := make([]stagedPatchFile, 0, len(proposal.Files))
	for _, file := range proposal.Files {
		relative := mcp.JoinApplicationPath(proposal.Provenance.ApplicationRoot, file.Path)
		target, err := safeTargetPath(root, relative)
		if err != nil {
			return nil, patchApplyError(ErrSourcePatchPathUnsafe, err.Error())
		}
		if err := gitQuiet(ctx, root, "diff", "--cached", "--quiet", "--", relative); err != nil {
			return nil, patchApplyError(ErrSourcePatchTargetDirty, "target has staged changes: "+relative)
		}
		blobID, err := gitOutput(ctx, root, "rev-parse", "HEAD:"+relative)
		if err != nil || !strings.EqualFold(blobID, file.BaseBlobSHA) {
			return nil, patchApplyError(ErrSourcePatchPreimage, "Git blob no longer matches: "+relative)
		}
		before, err := os.ReadFile(target)
		if err != nil {
			return nil, patchApplyError(ErrSourcePatchPathUnsafe, "read target: "+relative)
		}
		headBytes, err := gitBytes(ctx, root, "show", "HEAD:"+relative)
		after, err := mcp.ApplySourcePatchFile(file.Path, headBytes, file.UnifiedDiff)
		if err != nil {
			return nil, patchApplyError(ErrSourcePatchPreimage, "patch no longer applies: "+relative)
		}
		alreadyApplied := bytes.Equal(before, after)
		if !bytes.Equal(before, headBytes) && !alreadyApplied {
			return nil, patchApplyError(ErrSourcePatchPreimage, "worktree content no longer matches HEAD: "+relative)
		}
		info, err := os.Stat(target)
		if err != nil || !info.Mode().IsRegular() {
			return nil, patchApplyError(ErrSourcePatchPathUnsafe, "target is not a regular file: "+relative)
		}
		files = append(files, stagedPatchFile{relative: relative, target: target, before: before, after: after, mode: info.Mode().Perm(), alreadyApplied: alreadyApplied})
	}
	return files, nil
}

func safeRepositoryRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errors.New("local repository is unavailable")
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || !filepath.IsAbs(resolved) {
		return "", errors.New("local repository root is unsafe")
	}
	if info, err := os.Stat(filepath.Join(resolved, ".git")); err != nil || info == nil {
		return "", errors.New("local repository is not a Git worktree")
	}
	return resolved, nil
}

func safeTargetPath(root, relative string) (string, error) {
	if strings.HasPrefix(relative, ".git/") || relative == ".git" {
		return "", errors.New("patch cannot target .git")
	}
	target := filepath.Join(root, filepath.FromSlash(relative))
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil || !strings.HasPrefix(resolved, root+string(os.PathSeparator)) {
		return "", errors.New("patch target is outside the local repository")
	}
	info, err := os.Lstat(target)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", errors.New("patch target must be a regular non-symlink file")
	}
	return target, nil
}

func writePatchJournal(root, id string, files []stagedPatchFile) (string, error) {
	dir := filepath.Join(root, ".git", "opsi", "source-patch", id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	for index, file := range files {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%03d.before", index)), file.before, 0600); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func writeStagedFiles(files []stagedPatchFile) error {
	for _, file := range files {
		current, err := os.ReadFile(file.target)
		if err != nil || !bytes.Equal(current, file.before) {
			return errors.New("target changed during patch application")
		}
		temp, err := os.CreateTemp(filepath.Dir(file.target), ".opsi-patch-*")
		if err != nil {
			return err
		}
		name := temp.Name()
		if _, err = temp.Write(file.after); err == nil {
			err = temp.Chmod(file.mode)
		}
		if err == nil {
			err = temp.Sync()
		}
		if closeErr := temp.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(name)
			return err
		}
		if err = os.Rename(name, file.target); err != nil {
			_ = os.Remove(name)
			return err
		}
	}
	return nil
}

func restoreStagedFiles(files []stagedPatchFile) error {
	for _, file := range files {
		if err := os.WriteFile(file.target, file.before, file.mode); err != nil {
			return err
		}
	}
	return nil
}

func patchPaths(files []stagedPatchFile) []string {
	out := make([]string, 0, len(files))
	for _, file := range files {
		out = append(out, file.relative)
	}
	return out
}
func allPatchesApplied(files []stagedPatchFile) bool {
	return len(files) > 0 && !anyUnappliedPatch(files)
}
func anyPatchApplied(files []stagedPatchFile) bool {
	for _, file := range files {
		if file.alreadyApplied {
			return true
		}
	}
	return false
}
func anyUnappliedPatch(files []stagedPatchFile) bool {
	for _, file := range files {
		if !file.alreadyApplied {
			return true
		}
	}
	return false
}
func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:12]
}
func patchApplyError(code, message string) *AssistantError {
	return &AssistantError{Code: code, Message: message}
}
func gitOutput(ctx context.Context, root string, args ...string) (string, error) {
	out, err := gitBytes(ctx, root, args...)
	return strings.TrimSpace(string(out)), err
}
func gitBytes(ctx context.Context, root string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	return command.Output()
}
func gitQuiet(ctx context.Context, root string, args ...string) error {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	if err := command.Run(); err != nil {
		return err
	}
	return nil
}
