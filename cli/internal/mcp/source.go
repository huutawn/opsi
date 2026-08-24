package mcp

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/opsi-dev/opsi/cli/internal/repository"
)

const (
	DefaultMaxFileBytes   = 64 * 1024  // 64 KiB
	AbsoluteMaxFileBytes  = 256 * 1024 // 256 KiB
	DefaultFileListLimit  = 50
	AbsoluteFileListLimit = 200
	DefaultSearchLimit    = 20
	AbsoluteSearchLimit   = 50
	MaxSearchFiles        = 500
	MaxSearchBytes        = 4 * 1024 * 1024 // 4 MiB
)

var (
	// ADC-05 embedded credential patterns
	credURIPattern     = regexp.MustCompile(`([a-zA-Z0-9+.-]+://)([^/\s:@]*):([^/\s:@]+)@([^\s"'\` + "`" + `]+)`)
	credEnvPattern     = regexp.MustCompile(`(?i)(password|secret|token|api_key|pat)\s*[:=]\s*["']([^"']+)["']`)
	privateKeyPattern  = regexp.MustCompile(`(?s)-----BEGIN (?:[A-Z ]+ )?PRIVATE KEY-----.*?-----END (?:[A-Z ]+ )?PRIVATE KEY-----`)
	knownTokenPattern  = regexp.MustCompile(`(?i)\b(?:opsi_(?:pat|agent_token)|registry_auth_basic|source_embedded_pass)[a-z0-9_-]*`)
	commonBinaryExtMap = map[string]bool{
		".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".ico": true,
		".webp": true, ".svgz": true, ".pdf": true, ".zip": true, ".tar": true,
		".gz": true, ".tgz": true, ".bz2": true, ".xz": true, ".7z": true,
		".exe": true, ".dll": true, ".so": true, ".dylib": true, ".bin": true,
		".wasm": true, ".class": true, ".jar": true, ".pyc": true, ".o": true,
		".a": true, ".woff": true, ".woff2": true, ".ttf": true, ".eot": true,
	}
)

type SourceService struct {
	Runner repository.CommandRunner
}

// SourceBlob is an immutable Git blob resolved through an exact commit.
// Its content is intentionally never read from the working tree.
type SourceBlob struct {
	ObjectID string
	Content  []byte
	IsBinary bool
	Mode     string
}

func NewSourceService(runner repository.CommandRunner) *SourceService {
	if runner == nil {
		runner = repository.ExecRunner{}
	}
	return &SourceService{Runner: runner}
}

// CleanRelativePath strictly validates that relativePath is clean, relative,
// does not contain "..", does not start with "/", and contains no null bytes.
func CleanRelativePath(rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "", errors.New("path cannot be empty")
	}
	if strings.ContainsRune(rel, 0) {
		return "", errors.New("path contains null bytes")
	}
	if strings.Contains(rel, "\\") {
		return "", errors.New("backslash paths are not supported")
	}
	if strings.Contains(rel, "..") {
		return "", errors.New("path traversal with '..' is not allowed")
	}
	// Normalize forward slashes
	rel = filepath.ToSlash(rel)
	if strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, "\\") {
		return "", errors.New("path must be relative, not absolute")
	}
	cleaned := path.Clean(rel)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.Contains(cleaned, "/../") {
		return "", errors.New("path traversal with '..' is not allowed")
	}
	return cleaned, nil
}

// CleanApplicationRoot cleans the application root path.
func CleanApplicationRoot(appRoot string) string {
	appRoot = strings.TrimSpace(appRoot)
	appRoot = filepath.ToSlash(appRoot)
	appRoot = strings.TrimPrefix(appRoot, "./")
	appRoot = strings.Trim(appRoot, "/")
	if appRoot == "." {
		return ""
	}
	return path.Clean(appRoot)
}

// JoinApplicationPath joins cleaned applicationRoot and cleaned relativePath.
func JoinApplicationPath(appRoot, relPath string) string {
	appRoot = CleanApplicationRoot(appRoot)
	if appRoot == "" {
		return relPath
	}
	if relPath == "" {
		return appRoot
	}
	return appRoot + "/" + relPath
}

// VerifyCommitExists checks that the exact commit SHA is valid and present in git.
func (s *SourceService) VerifyCommitExists(ctx context.Context, repoRoot, commitSHA string) error {
	commitSHA = strings.TrimSpace(commitSHA)
	if len(commitSHA) < 7 || len(commitSHA) > 64 {
		return errors.New("invalid commit SHA length")
	}
	for _, r := range commitSHA {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return errors.New("invalid characters in commit SHA")
		}
	}
	_, err := s.Runner.Run(ctx, "git", "-C", repoRoot, "rev-parse", "--verify", commitSHA+"^{commit}")
	if err != nil {
		return fmt.Errorf("exact commit %s unavailable: %w", commitSHA, err)
	}
	return nil
}

// ListFiles lists files in ApplicationRoot for a specific commit.
func (s *SourceService) ListFiles(ctx context.Context, repoRoot, commitSHA, applicationRoot, pathPrefix string, limit int, cursor string) (SourceFilesResult, error) {
	if err := s.VerifyCommitExists(ctx, repoRoot, commitSHA); err != nil {
		return SourceFilesResult{}, fmt.Errorf("%s: %w", ErrCodeSourceSnapshotUnavailable, err)
	}
	if limit <= 0 {
		limit = DefaultFileListLimit
	}
	if limit > AbsoluteFileListLimit {
		limit = AbsoluteFileListLimit
	}

	appRoot := CleanApplicationRoot(applicationRoot)
	cleanedPrefix := ""
	if pathPrefix != "" {
		var err error
		cleanedPrefix, err = CleanRelativePath(pathPrefix)
		if err != nil {
			return SourceFilesResult{}, fmt.Errorf("%s: %w", ErrCodeSourcePathInvalid, err)
		}
	}

	// Use git ls-tree to list all files at that commit
	// Format: <mode> <type> <object> <size> \t <path>
	var output []byte
	var err error
	if appRoot == "" {
		output, err = s.Runner.Run(ctx, "git", "-C", repoRoot, "ls-tree", "-r", "-l", commitSHA)
	} else {
		output, err = s.Runner.Run(ctx, "git", "-C", repoRoot, "ls-tree", "-r", "-l", commitSHA, "--", appRoot)
	}
	if err != nil {
		return SourceFilesResult{}, fmt.Errorf("%s: git ls-tree failed: %w", ErrCodeSourceSnapshotUnavailable, err)
	}

	allFiles := make([]SourceFileItem, 0)
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		filePath := parts[1]
		metaParts := strings.Fields(parts[0])
		if len(metaParts) < 4 {
			continue
		}
		itemType := metaParts[1]
		sizeStr := metaParts[3]
		sizeBytes, _ := strconv.ParseInt(sizeStr, 10, 64)

		// Compute relative path to applicationRoot
		var relPath string
		if appRoot == "" {
			relPath = filePath
		} else {
			if filePath == appRoot {
				continue
			}
			if !strings.HasPrefix(filePath, appRoot+"/") {
				continue
			}
			relPath = strings.TrimPrefix(filePath, appRoot+"/")
		}

		if cleanedPrefix != "" && !strings.HasPrefix(relPath, cleanedPrefix) {
			continue
		}

		// Skip sensitive git/environment files
		baseName := path.Base(relPath)
		if baseName == ".git" || strings.HasPrefix(baseName, ".env") || baseName == "id_rsa" || baseName == "id_ed25519" {
			continue
		}

		isBin := isBinaryExtension(relPath)
		allFiles = append(allFiles, SourceFileItem{
			Path:        relPath,
			SizeBytes:   sizeBytes,
			IsBinary:    isBin,
			IsDirectory: itemType == "tree",
		})
	}

	totalFiles := len(allFiles)
	offset := 0
	if cursor != "" {
		if parsed, err := strconv.Atoi(cursor); err == nil && parsed >= 0 && parsed < totalFiles {
			offset = parsed
		}
	}

	end := offset + limit
	if end > totalFiles {
		end = totalFiles
	}

	page := allFiles[offset:end]
	nextCursor := ""
	if end < totalFiles {
		nextCursor = strconv.Itoa(end)
	}

	return SourceFilesResult{
		CommitSHA:       commitSHA,
		ApplicationRoot: appRoot,
		TotalFiles:      totalFiles,
		Files:           page,
		NextCursor:      nextCursor,
	}, nil
}

// ReadFile reads the exact file content from git object store at the commit.
func (s *SourceService) ReadFile(ctx context.Context, repoRoot, commitSHA, applicationRoot, relativePath string, maxBytes int) (SourceFileResult, error) {
	if err := s.VerifyCommitExists(ctx, repoRoot, commitSHA); err != nil {
		return SourceFileResult{}, fmt.Errorf("%s: %w", ErrCodeSourceSnapshotUnavailable, err)
	}
	cleanedRel, err := CleanRelativePath(relativePath)
	if err != nil {
		return SourceFileResult{}, fmt.Errorf("%s: %w", ErrCodeSourcePathInvalid, err)
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxFileBytes
	}
	if maxBytes > AbsoluteMaxFileBytes {
		maxBytes = AbsoluteMaxFileBytes
	}

	fullPath := JoinApplicationPath(applicationRoot, cleanedRel)

	// Read git object content using git cat-file -p <commitSHA>:<fullPath>
	output, err := s.Runner.Run(ctx, "git", "-C", repoRoot, "cat-file", "-p", commitSHA+":"+fullPath)
	if err != nil {
		return SourceFileResult{}, fmt.Errorf("%s: file %q not found in commit %s: %w", ErrCodeNotFound, cleanedRel, commitSHA, err)
	}

	sizeBytes := int64(len(output))
	isBin := isBinaryData(output, cleanedRel)
	if isBin {
		return SourceFileResult{
			CommitSHA:       commitSHA,
			ApplicationRoot: CleanApplicationRoot(applicationRoot),
			RelativePath:    cleanedRel,
			SizeBytes:       sizeBytes,
			IsBinary:        true,
		}, nil
	}

	truncated := false
	var contentBytes []byte
	if len(output) > maxBytes {
		contentBytes = output[:maxBytes]
		truncated = true
	} else {
		contentBytes = output
	}

	rawText := string(contentBytes)
	redactedText, wasRedacted := RedactSourceSecrets(rawText)

	return SourceFileResult{
		CommitSHA:       commitSHA,
		ApplicationRoot: CleanApplicationRoot(applicationRoot),
		RelativePath:    cleanedRel,
		SizeBytes:       sizeBytes,
		Content:         redactedText,
		Truncated:       truncated,
		IsBinary:        false,
		Redacted:        wasRedacted,
	}, nil
}

// ReadBlob resolves a text blob and its canonical Git object ID at an exact
// commit. It is the source authority used by virtual patch validation.
func (s *SourceService) ReadBlob(ctx context.Context, repoRoot, commitSHA, applicationRoot, relativePath string) (SourceBlob, error) {
	if err := s.VerifyCommitExists(ctx, repoRoot, commitSHA); err != nil {
		return SourceBlob{}, fmt.Errorf("%s: %w", ErrCodeSourceSnapshotUnavailable, err)
	}
	cleanedRel, err := CleanRelativePath(relativePath)
	if err != nil {
		return SourceBlob{}, fmt.Errorf("%s: %w", ErrCodeSourcePathInvalid, err)
	}
	fullPath := JoinApplicationPath(applicationRoot, cleanedRel)
	entry, err := s.Runner.Run(ctx, "git", "-C", repoRoot, "ls-tree", commitSHA, "--", fullPath)
	if err != nil {
		return SourceBlob{}, fmt.Errorf("%s: source tree entry is unavailable", ErrCodeSourceSnapshotUnavailable)
	}
	entryFields := strings.Fields(string(entry))
	if len(entryFields) < 3 || entryFields[1] != "blob" || (entryFields[0] != "100644" && entryFields[0] != "100755") {
		return SourceBlob{}, fmt.Errorf("%s: source path is not a regular text file", ErrCodeSourcePathInvalid)
	}
	objectID, err := s.Runner.Run(ctx, "git", "-C", repoRoot, "rev-parse", "--verify", commitSHA+":"+fullPath)
	if err != nil {
		return SourceBlob{}, fmt.Errorf("%s: file %q not found in exact source", ErrCodeNotFound, cleanedRel)
	}
	objectID = bytes.TrimSpace(objectID)
	if len(objectID) != 40 && len(objectID) != 64 {
		return SourceBlob{}, fmt.Errorf("%s: source blob identity is invalid", ErrCodeSourceSnapshotUnavailable)
	}
	for _, value := range objectID {
		if !((value >= '0' && value <= '9') || (value >= 'a' && value <= 'f') || (value >= 'A' && value <= 'F')) {
			return SourceBlob{}, fmt.Errorf("%s: source blob identity is invalid", ErrCodeSourceSnapshotUnavailable)
		}
	}
	content, err := s.Runner.Run(ctx, "git", "-C", repoRoot, "cat-file", "-p", string(objectID))
	if err != nil {
		return SourceBlob{}, fmt.Errorf("%s: exact source blob is unavailable", ErrCodeSourceSnapshotUnavailable)
	}
	return SourceBlob{ObjectID: string(objectID), Content: content, IsBinary: isBinaryData(content, cleanedRel), Mode: entryFields[0]}, nil
}

// Search performs literal text search across files in the commit.
func (s *SourceService) Search(ctx context.Context, repoRoot, commitSHA, applicationRoot, query, pathPrefix string, limit int) (SourceSearchResult, error) {
	if err := s.VerifyCommitExists(ctx, repoRoot, commitSHA); err != nil {
		return SourceSearchResult{}, fmt.Errorf("%s: %w", ErrCodeSourceSnapshotUnavailable, err)
	}
	query = strings.TrimSpace(query)
	if len(query) < 2 {
		return SourceSearchResult{}, fmt.Errorf("%s: search query must be at least 2 characters", ErrCodeInvalidArgument)
	}
	if len(query) > 128 {
		return SourceSearchResult{}, fmt.Errorf("%s: search query cannot exceed 128 characters", ErrCodeInvalidArgument)
	}
	if limit <= 0 {
		limit = DefaultSearchLimit
	}
	if limit > AbsoluteSearchLimit {
		limit = AbsoluteSearchLimit
	}

	appRoot := CleanApplicationRoot(applicationRoot)
	searchPath := appRoot
	if pathPrefix != "" {
		cleanedPrefix, err := CleanRelativePath(pathPrefix)
		if err != nil {
			return SourceSearchResult{}, fmt.Errorf("%s: %w", ErrCodeSourcePathInvalid, err)
		}
		searchPath = JoinApplicationPath(appRoot, cleanedPrefix)
	}

	args := []string{"-C", repoRoot, "grep", "-n", "-I", "-F", query, commitSHA}
	if searchPath != "" {
		args = append(args, "--", searchPath)
	}

	output, err := s.Runner.Run(ctx, "git", args...)
	if err != nil {
		// Exit code 1 from git grep means 0 matches found (not a fatal error)
		return SourceSearchResult{
			CommitSHA:       commitSHA,
			ApplicationRoot: appRoot,
			Query:           query,
			Matches:         []SourceSearchMatch{},
			MatchesCount:    0,
		}, nil
	}

	matches := make([]SourceSearchMatch, 0)
	scanner := bufio.NewScanner(bytes.NewReader(output))
	truncated := false

	for scanner.Scan() {
		line := scanner.Text()
		// git grep output format: <commitSHA>:<path>:<lineNum>:<content>
		parts := strings.SplitN(line, ":", 4)
		if len(parts) < 4 {
			continue
		}
		filePath := parts[1]
		lineNum, _ := strconv.Atoi(parts[2])
		snippet := parts[3]

		// Relativize path
		var relPath string
		if appRoot == "" {
			relPath = filePath
		} else {
			if !strings.HasPrefix(filePath, appRoot+"/") && filePath != appRoot {
				continue
			}
			relPath = strings.TrimPrefix(filePath, appRoot+"/")
		}

		redactedSnippet, _ := RedactSourceSecrets(snippet)
		matches = append(matches, SourceSearchMatch{
			File:         relPath,
			LineNumber:   lineNum,
			MatchSnippet: redactedSnippet,
		})

		if len(matches) >= limit {
			truncated = true
			break
		}
	}

	return SourceSearchResult{
		CommitSHA:       commitSHA,
		ApplicationRoot: appRoot,
		Query:           query,
		Matches:         matches,
		MatchesCount:    len(matches),
		Truncated:       truncated,
	}, nil
}

// RedactSourceSecrets redacts suspected embedded credentials from source text.
func RedactSourceSecrets(input string) (string, bool) {
	redacted := false
	output := credURIPattern.ReplaceAllStringFunc(input, func(m string) string {
		redacted = true
		return credURIPattern.ReplaceAllString(m, "$1$2:[REDACTED]@$4")
	})
	output = credEnvPattern.ReplaceAllStringFunc(output, func(m string) string {
		redacted = true
		return credEnvPattern.ReplaceAllString(m, `$1="[REDACTED]"`)
	})
	output = privateKeyPattern.ReplaceAllStringFunc(output, func(string) string {
		redacted = true
		return "[REDACTED_PRIVATE_KEY]"
	})
	output = knownTokenPattern.ReplaceAllStringFunc(output, func(string) string {
		redacted = true
		return "[REDACTED]"
	})
	return output, redacted
}

func isBinaryExtension(p string) bool {
	ext := strings.ToLower(path.Ext(p))
	return commonBinaryExtMap[ext]
}

func isBinaryData(data []byte, p string) bool {
	if isBinaryExtension(p) {
		return true
	}
	checkLen := len(data)
	if checkLen > 1024 {
		checkLen = 1024
	}
	for i := 0; i < checkLen; i++ {
		if data[i] == 0 {
			return true
		}
	}
	if !utf8.Valid(data[:checkLen]) {
		return true
	}
	return false
}
