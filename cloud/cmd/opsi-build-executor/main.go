package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"

	"github.com/opsi-dev/opsi/cloud/internal/buildexecutor"
	"github.com/opsi-dev/opsi/cloud/internal/buildjob"
)

func main() {
	var cloudURL, jobID, attemptID, outputDir, workspace string
	var runID uint64
	var runAttempt uint64
	flag.StringVar(&cloudURL, "cloud-url", "", "HTTPS Opsi Cloud origin")
	flag.StringVar(&jobID, "build-job-id", "", "canonical BuildJob ID")
	flag.StringVar(&attemptID, "attempt-id", "", "runner attempt ID")
	flag.Uint64Var(&runID, "github-run-id", 0, "trusted GitHub Actions run ID")
	flag.Uint64Var(&runAttempt, "github-run-attempt", 0, "trusted GitHub Actions run attempt")
	flag.StringVar(&outputDir, "output-dir", "", "local structured metadata output directory")
	flag.StringVar(&workspace, "workspace", "", "temporary executor workspace")
	flag.Parse()
	if err := run(context.Background(), cloudURL, jobID, attemptID, runID, runAttempt, outputDir, workspace); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cloudURL, jobID, attemptID string, runID, runAttempt uint64, outputDir, workspace string) error {
	parsed, err := url.Parse(cloudURL)
	lease := []byte(os.Getenv("OPSI_RUNNER_LEASE"))
	registryCredential := []byte(os.Getenv("GITHUB_TOKEN"))
	registryUsername := os.Getenv("GITHUB_ACTOR")
	_ = os.Unsetenv("OPSI_RUNNER_LEASE")
	_ = os.Unsetenv("GITHUB_TOKEN")
	_ = os.Unsetenv("GITHUB_ACTOR")
	defer destroy(lease)
	defer destroy(registryCredential)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" || parsed.Path != "" && parsed.Path != "/" || jobID == "" || attemptID == "" || runID == 0 || runAttempt == 0 || runAttempt > math.MaxUint32 || outputDir == "" || len(lease) == 0 || registryUsername == "" || len(registryCredential) == 0 {
		return errors.New("executor input is invalid")
	}
	client := buildexecutor.Client{BaseURL: cloudURL, Lease: lease}
	spec, err := client.BuildSpec(ctx, jobID)
	if err != nil {
		return err
	}
	access, err := client.SourceAccess(ctx, jobID, attemptID, runID, uint32(runAttempt))
	if err != nil {
		if reportErr := client.Fail(ctx, jobID, attemptID, runnerFailureCode(err)); reportErr != nil {
			return errors.New("source access failed and Cloud rejected the failure result")
		}
		return err
	}
	defer access.Destroy()
	if access.Repository != spec.Repository || access.RepositoryID != spec.RepositoryID || access.GitHubInstallationID != spec.GitHubInstallationID || access.ResolvedCommitSHA != spec.ResolvedCommitSHA {
		return errors.New("source access does not match canonical Build Spec")
	}
	removeWorkspace := false
	if workspace == "" {
		workspace, err = os.MkdirTemp("", "opsi-build-executor-*")
		if err != nil {
			return errors.New("executor workspace is unavailable")
		}
		removeWorkspace = true
	}
	if removeWorkspace {
		defer os.RemoveAll(workspace)
	}
	publisher := buildexecutor.GHCRRegistryPublisher{Username: registryUsername, Credential: registryCredential}
	result, executeErr := buildexecutor.Execute(ctx, buildexecutor.Request{Spec: spec, AttemptID: attemptID, RemoteURL: buildexecutor.GitHubRemoteURL(spec.Repository), Credential: access.Credential(), Workspace: workspace, OutputDir: outputDir, Publisher: publisher}, os.Stdout)
	resultPath := filepath.Join(outputDir, "executor-result.json")
	data, marshalErr := json.MarshalIndent(result, "", "  ")
	if marshalErr != nil || os.MkdirAll(outputDir, 0o700) != nil || os.WriteFile(resultPath, append(data, '\n'), 0o600) != nil {
		return errors.New("executor result metadata cannot be written")
	}
	if executeErr != nil {
		code := result.FailureCode
		if code == "" {
			code = "EXECUTOR_INFRASTRUCTURE_FAILED"
		}
		if reportErr := client.Fail(ctx, jobID, attemptID, code); reportErr != nil {
			return errors.New("executor failed and Cloud rejected the failure result")
		}
		return executeErr
	}
	completion, err := client.Complete(ctx, result)
	if err != nil || completion.Digest != result.ImageDigest || completion.BuildJobState != buildjob.StatusSucceeded || completion.BuildRecordID == "" {
		return errors.New("Cloud rejected the executor result")
	}
	return nil
}

func destroy(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func runnerFailureCode(err error) string {
	var executorErr buildexecutor.Error
	if errors.As(err, &executorErr) {
		switch executorErr.Code {
		case "SOURCE_ACCESS_DENIED", "SOURCE_TOKEN_UNAVAILABLE":
			return executorErr.Code
		}
	}
	return "EXECUTOR_INFRASTRUCTURE_FAILED"
}
