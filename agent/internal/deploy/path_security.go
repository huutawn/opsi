package deploy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var windowsAbsPath = regexp.MustCompile(`^[A-Za-z]:[\\/]`)

func safeRelPath(base, input string) (string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", errors.New("deployment path is required")
	}
	if strings.Contains(input, "\\") {
		return "", errors.New("deployment path must use forward slash relative paths")
	}
	if filepath.IsAbs(input) || windowsAbsPath.MatchString(input) {
		return "", errors.New("deployment path must be relative")
	}

	cleaned := filepath.Clean(input)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) {
		return "", errors.New("deployment path escapes checkout root")
	}

	baseAbs, err := filepath.Abs(base)
	if err != nil {
		return "", fmt.Errorf("resolve checkout root: %w", err)
	}
	targetAbs := filepath.Join(baseAbs, cleaned)
	if err := ensureInside(baseAbs, targetAbs); err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(targetAbs); err == nil {
		if err := ensureInside(baseAbs, resolved); err != nil {
			return "", errors.New("deployment path symlink escapes checkout root")
		}
	}
	return targetAbs, nil
}

func validateSafeRelPath(field, input string) error {
	if _, err := safeRelPath(".", input); err != nil {
		return fmt.Errorf("%s is invalid: %w", field, err)
	}
	return nil
}

func ensureInside(baseAbs, targetAbs string) error {
	rel, err := filepath.Rel(baseAbs, targetAbs)
	if err != nil {
		return fmt.Errorf("resolve deployment path: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return errors.New("deployment path escapes checkout root")
	}
	return nil
}
