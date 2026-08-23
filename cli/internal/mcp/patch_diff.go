package mcp

import (
	"bytes"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

var hunkHeaderPattern = regexp.MustCompile(`^@@ -([1-9][0-9]*)(?:,([0-9]+))? \+([1-9][0-9]*)(?:,([0-9]+))? @@(?: .*)?$`)

type patchHunk struct {
	oldStart int
	oldCount int
	lines    []string
}

type parsedPatch struct {
	normalized   string
	hunks        []patchHunk
	changedLines int
}

func parseUnifiedDiff(filePath, diff string) (parsedPatch, error) {
	if strings.TrimSpace(diff) == "" || !utf8.ValidString(diff) {
		return parsedPatch{}, &DomainError{Code: ErrCodePatchMalformed, Message: "unified diff must be non-empty UTF-8 text"}
	}
	diff = normalizeDiff(diff)
	if strings.Contains(diff, "\x00") || strings.Contains(diff, "GIT binary patch") || strings.Contains(diff, "Binary files ") {
		return parsedPatch{}, &DomainError{Code: ErrCodePatchMalformed, Message: "binary patch content is not supported"}
	}
	lines := strings.Split(strings.TrimSuffix(diff, "\n"), "\n")
	if len(lines) < 3 || lines[0] != "--- a/"+filePath || lines[1] != "+++ b/"+filePath {
		return parsedPatch{}, &DomainError{Code: ErrCodePatchMalformed, Message: "diff headers must exactly match the proposed relative path"}
	}
	parsed := parsedPatch{normalized: diff}
	previousOldEnd := 0
	for index := 2; index < len(lines); {
		match := hunkHeaderPattern.FindStringSubmatch(lines[index])
		if match == nil {
			return parsedPatch{}, &DomainError{Code: ErrCodePatchMalformed, Message: "diff contains an invalid hunk header or unsupported metadata"}
		}
		hunk := patchHunk{oldStart: parsePatchInt(match[1]), oldCount: patchCount(match[2])}
		if hunk.oldStart < previousOldEnd {
			return parsedPatch{}, &DomainError{Code: ErrCodePatchMalformed, Message: "diff hunks overlap or are out of order"}
		}
		index++
		oldSeen, newSeen := 0, 0
		for index < len(lines) && !strings.HasPrefix(lines[index], "@@ -") {
			line := lines[index]
			if len(line) == 0 || (line[0] != ' ' && line[0] != '+' && line[0] != '-') || strings.HasPrefix(line, "\\ No newline") {
				return parsedPatch{}, &DomainError{Code: ErrCodePatchMalformed, Message: "diff hunk contains unsupported content"}
			}
			if line[0] != '+' {
				oldSeen++
			}
			if line[0] != '-' {
				newSeen++
			}
			if line[0] == '+' || line[0] == '-' {
				parsed.changedLines++
			}
			hunk.lines = append(hunk.lines, line)
			index++
		}
		if oldSeen != hunk.oldCount || newSeen != patchCount(match[4]) || len(hunk.lines) == 0 {
			return parsedPatch{}, &DomainError{Code: ErrCodePatchMalformed, Message: "diff hunk line counts are inconsistent"}
		}
		parsed.hunks = append(parsed.hunks, hunk)
		previousOldEnd = hunk.oldStart + hunk.oldCount
	}
	return parsed, nil
}

func virtualApply(content []byte, patch parsedPatch) ([]byte, error) {
	if !utf8.Valid(content) {
		return nil, &DomainError{Code: ErrCodeSourceBinaryUnsupported, Message: "patch targets non-text source"}
	}
	hasTrailingNewline := bytes.HasSuffix(content, []byte("\n"))
	source := strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
	if len(source) == 1 && source[0] == "" && len(content) == 0 {
		source = nil
	}
	result, cursor := make([]string, 0, len(source)), 0
	for _, hunk := range patch.hunks {
		if hunk.oldStart-1 < cursor || hunk.oldStart-1 > len(source) {
			return nil, &DomainError{Code: ErrCodePatchPreimageMismatch, Message: "hunk does not match the declared source position"}
		}
		result, cursor = append(result, source[cursor:hunk.oldStart-1]...), hunk.oldStart-1
		for _, line := range hunk.lines {
			text := line[1:]
			switch line[0] {
			case ' ':
				if cursor >= len(source) || source[cursor] != text {
					return nil, &DomainError{Code: ErrCodePatchPreimageMismatch, Message: "hunk context does not exactly match immutable source"}
				}
				result, cursor = append(result, text), cursor+1
			case '-':
				if cursor >= len(source) || source[cursor] != text {
					return nil, &DomainError{Code: ErrCodePatchPreimageMismatch, Message: "hunk removal does not exactly match immutable source"}
				}
				cursor++
			case '+':
				result = append(result, text)
			}
		}
	}
	output := strings.Join(append(result, source[cursor:]...), "\n")
	if hasTrailingNewline {
		output += "\n"
	}
	return []byte(output), nil
}

func isForbiddenPatchPath(value string) bool {
	if value == "" || value == ".git" || strings.HasPrefix(value, ".git/") {
		return true
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "vendor" || segment == "node_modules" || segment == "dist" || segment == "build" || segment == "coverage" || segment == "generated" || strings.HasSuffix(segment, ".generated") {
			return true
		}
	}
	return strings.HasSuffix(value, ".gen.go") || strings.Contains(path.Base(value), ".generated.")
}

func validGitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F')) {
			return false
		}
	}
	return true
}

func normalizeDiff(value string) string { return strings.ReplaceAll(value, "\r\n", "\n") }

func parsePatchInt(value string) int {
	result := 0
	for _, character := range value {
		result = result*10 + int(character-'0')
	}
	return result
}

func patchCount(value string) int {
	if value == "" {
		return 1
	}
	return parsePatchInt(value)
}

func patchAddsSecret(patch parsedPatch) bool {
	for _, hunk := range patch.hunks {
		for _, line := range hunk.lines {
			if line[0] == '+' && hasSecretLiteral(line[1:]) {
				return true
			}
		}
	}
	return false
}

func hasNearbyEvidence(evidence []SourcePatchEvidence, file string, hunks []patchHunk) bool {
	for _, hunk := range hunks {
		matched := false
		for _, item := range evidence {
			if item.File == file && item.Line >= hunk.oldStart-150 && item.Line <= hunk.oldStart+hunk.oldCount+150 {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func safePatchPreview(value string) string {
	redacted, _ := RedactSourceSecrets(value)
	return secretLiteralPattern.ReplaceAllString(redacted, "[REDACTED]")
}
