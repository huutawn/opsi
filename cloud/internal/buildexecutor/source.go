package buildexecutor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func GitHubRemoteURL(repository string) string {
	return "https://github.com/" + repository + ".git"
}

func Materialize(ctx context.Context, request Request, log io.Writer) (string, error) {
	if err := request.Spec.Validate(); err != nil || request.RemoteURL == "" || request.Workspace == "" || len(request.Credential) == 0 {
		destroy(request.Credential)
		return "", Error{Code: "SOURCE_MATERIALIZATION_FAILED", Phase: "source", Message: "source materialization input is invalid"}
	}
	defer destroy(request.Credential)
	sourceDir := filepath.Join(request.Workspace, "source")
	if err := os.Mkdir(sourceDir, 0o700); err != nil {
		return "", Error{Code: "SOURCE_MATERIALIZATION_FAILED", Phase: "source", Message: "source directory cannot be created"}
	}
	gitHome := filepath.Join(request.Workspace, "git-home")
	if err := os.Mkdir(gitHome, 0o700); err != nil {
		return "", Error{Code: "SOURCE_MATERIALIZATION_FAILED", Phase: "source", Message: "Git credential home cannot be created"}
	}
	defer os.RemoveAll(gitHome)
	baseEnv := commandEnv(map[string]string{"HOME": gitHome, "XDG_CONFIG_HOME": gitHome, "GIT_CONFIG_NOSYSTEM": "1", "GIT_TERMINAL_PROMPT": "0"})
	if output, err := run(ctx, sourceDir, baseEnv, "git", "init", "--quiet"); err != nil {
		writeSanitized(log, output, request.Credential)
		return "", Error{Code: "SOURCE_MATERIALIZATION_FAILED", Phase: "source", Message: "Git repository initialization failed"}
	}
	if output, err := run(ctx, sourceDir, baseEnv, "git", "remote", "add", "origin", request.RemoteURL); err != nil {
		writeSanitized(log, output, request.Credential)
		return "", Error{Code: "SOURCE_MATERIALIZATION_FAILED", Phase: "source", Message: "Git remote configuration failed"}
	}
	askpass, err := writeAskPass(request.Workspace)
	if err != nil {
		return "", Error{Code: "SOURCE_TOKEN_UNAVAILABLE", Phase: "source", Message: "temporary source credential helper is unavailable"}
	}
	fetchEnv := commandEnv(map[string]string{
		"HOME": gitHome, "XDG_CONFIG_HOME": gitHome, "GIT_CONFIG_NOSYSTEM": "1", "GIT_TERMINAL_PROMPT": "0",
		"GIT_ASKPASS": askpass, "OPSI_GIT_USERNAME": "x-access-token", "OPSI_GIT_TOKEN": string(request.Credential),
	})
	output, fetchErr := run(ctx, sourceDir, fetchEnv, "git", "fetch", "--no-tags", "--depth=1", "origin", request.Spec.ResolvedCommitSHA)
	if removeErr := os.Remove(askpass); removeErr != nil {
		return "", Error{Code: "SOURCE_MATERIALIZATION_FAILED", Phase: "source", Message: "temporary source credential helper cleanup failed"}
	}
	if fetchErr != nil {
		writeSanitized(log, output, request.Credential)
		return "", Error{Code: "EXACT_COMMIT_UNAVAILABLE", Phase: "source", Message: "the immutable commit could not be fetched"}
	}
	if output, err := run(ctx, sourceDir, baseEnv, "git", "checkout", "--detach", "--quiet", "FETCH_HEAD"); err != nil {
		writeSanitized(log, output, request.Credential)
		return "", Error{Code: "SOURCE_MATERIALIZATION_FAILED", Phase: "source", Message: "detached checkout failed"}
	}
	head, err := run(ctx, sourceDir, baseEnv, "git", "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(string(head)) != request.Spec.ResolvedCommitSHA {
		return "", Error{Code: "CHECKOUT_SHA_MISMATCH", Phase: "source", Message: "materialized HEAD does not match the immutable commit"}
	}
	if _, err := os.Stat(filepath.Join(sourceDir, ".gitmodules")); err == nil {
		return "", Error{Code: "SOURCE_SUBMODULES_UNSUPPORTED", Phase: "source", Message: "Git submodules are not supported"}
	} else if !os.IsNotExist(err) {
		return "", Error{Code: "SOURCE_MATERIALIZATION_FAILED", Phase: "source", Message: "submodule metadata cannot be inspected"}
	}
	if hasLFSAttributes(sourceDir) {
		return "", Error{Code: "SOURCE_GIT_LFS_UNSUPPORTED", Phase: "source", Message: "Git LFS is not supported"}
	}
	if err := os.RemoveAll(filepath.Join(sourceDir, ".git")); err != nil {
		return "", Error{Code: "SOURCE_MATERIALIZATION_FAILED", Phase: "source", Message: "Git metadata cleanup failed"}
	}
	if err := os.RemoveAll(gitHome); err != nil {
		return "", Error{Code: "SOURCE_MATERIALIZATION_FAILED", Phase: "source", Message: "temporary Git configuration cleanup failed"}
	}
	if found, err := treeContains(sourceDir, request.Credential); err != nil || found {
		return "", Error{Code: "SOURCE_CREDENTIAL_LEAK", Phase: "source", Message: "source credential cleanup verification failed"}
	}
	return sourceDir, nil
}

func writeAskPass(directory string) (string, error) {
	file, err := os.CreateTemp(directory, "git-askpass-*")
	if err != nil {
		return "", err
	}
	name := file.Name()
	if _, err := io.WriteString(file, "#!/bin/sh\ncase \"$1\" in *Username*) printf '%s\\n' \"$OPSI_GIT_USERNAME\";; *Password*) printf '%s\\n' \"$OPSI_GIT_TOKEN\";; *) exit 1;; esac\n"); err != nil {
		file.Close()
		os.Remove(name)
		return "", err
	}
	if err := file.Close(); err != nil {
		os.Remove(name)
		return "", err
	}
	if err := os.Chmod(name, 0o700); err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}

func hasLFSAttributes(root string) bool {
	found := false
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || found || entry.IsDir() || entry.Name() != ".gitattributes" {
			return nil
		}
		file, readErr := os.Open(path)
		if readErr != nil {
			found = true
			return nil
		}
		data, readErr := io.ReadAll(io.LimitReader(file, 1<<20))
		file.Close()
		if readErr != nil {
			found = true
			return nil
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") && strings.Contains(strings.ReplaceAll(line, " ", ""), "filter=lfs") {
				found = true
				break
			}
		}
		return nil
	})
	return found
}

func treeContains(root string, secret []byte) (bool, error) {
	found := false
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if found || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		found, err = fileContains(path, secret)
		return err
	})
	return found, err
}

func fileContains(path string, value []byte) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	const chunkSize = 64 << 10
	buffer := make([]byte, chunkSize+len(value))
	carry := 0
	for {
		read, readErr := file.Read(buffer[carry : carry+chunkSize])
		total := carry + read
		if bytes.Contains(buffer[:total], value) {
			return true, nil
		}
		if readErr == io.EOF {
			return false, nil
		}
		if readErr != nil {
			return false, readErr
		}
		carry = min(len(value)-1, total)
		copy(buffer[:carry], buffer[total-carry:total])
	}
}

func commandEnv(extra map[string]string) []string {
	env := []string{"PATH=" + os.Getenv("PATH")}
	for key, value := range extra {
		env = append(env, key+"="+value)
	}
	return env
}

func run(ctx context.Context, directory string, env []string, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = directory
	command.Env = env
	return command.CombinedOutput()
}

func writeSanitized(target io.Writer, output, credential []byte) {
	if target == nil || len(output) == 0 {
		return
	}
	_, _ = fmt.Fprint(target, string(bytes.ReplaceAll(output, credential, []byte("[REDACTED]"))))
}

func destroy(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
