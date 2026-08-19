package buildexecutor

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/buildjob"
	"github.com/opsi-dev/opsi/cloud/internal/sourcescanner"
)

func Execute(ctx context.Context, request Request, log io.Writer) (result Result, err error) {
	result = Result{
		BuildJobID: request.Spec.BuildJobID, AttemptID: request.AttemptID, ResolvedCommitSHA: request.Spec.ResolvedCommitSHA,
		Strategy: request.Spec.ResolvedBuildStrategy, DockerfilePath: request.Spec.DockerfilePath, BuildContext: request.Spec.BuildContext,
		Platform: Platform, StartedAt: time.Now().UTC(), Status: "failed",
	}
	if request.Spec.ResolvedBuildStrategy == buildjob.StrategyDockerfile {
		result.BuildKitVersion, result.BuildxVersion, result.BuilderIdentity = BuildKitVersion, BuildxVersion, BuildKitImage
	}
	defer func() {
		result.CompletedAt = time.Now().UTC()
		if typed, ok := err.(Error); ok {
			result.FailureCode = typed.Code
		}
	}()
	sourceDir, err := Materialize(ctx, request, log)
	if err != nil {
		return result, err
	}

	// ADC-05: Deterministic source risk scan.
	// Bounded, advisory, non-blocking. Scanner failure never fails the build.
	var deps []sourcescanner.Dependency
	if request.Spec.ScanContext != nil && len(request.Spec.ScanContext.ScanDependenciesJSON) > 0 {
		var rawDeps []struct {
			LogicalName       string `json:"logical_name"`
			Protocol          string `json:"protocol"`
			Strategy          string `json:"strategy"`
			AccessContext     string `json:"access_context"`
			Path              string `json:"path"`
			InjectionMappings []struct {
				EnvName string `json:"env_name"`
			} `json:"injection_mappings"`
		}
		if json.Unmarshal(request.Spec.ScanContext.ScanDependenciesJSON, &rawDeps) == nil {
			for _, d := range rawDeps {
				var envKeys []string
				for _, m := range d.InjectionMappings {
					if m.EnvName != "" {
						envKeys = append(envKeys, m.EnvName)
					}
				}
				deps = append(deps, sourcescanner.Dependency{
					LogicalName:     d.LogicalName,
					Protocol:        d.Protocol,
					Strategy:        d.Strategy,
					AccessContext:   d.AccessContext,
					Path:            d.Path,
					DeclaredEnvKeys: envKeys,
				})
			}
		}
	}
	scanOpts := sourcescanner.ScanOptions{
		RepositoryID: request.Spec.RepositoryID,
		CommitSHA:    request.Spec.ResolvedCommitSHA,
	}
	if request.Spec.ScanContext != nil {
		scanOpts.ApplicationID = request.Spec.ScanContext.ApplicationID
		scanOpts.ProjectID = request.Spec.ScanContext.ProjectID
	}
	scanReport := sourcescanner.ScanWithOptions(ctx, sourceDir, request.Spec.ApplicationRoot, deps, sourcescanner.DefaultLimits(), scanOpts)
	scanReport.BuildJobID = request.Spec.BuildJobID
	result.SourceRiskReport = &scanReport

	if request.OutputDir != "" {
		if reportData, rerr := json.MarshalIndent(scanReport, "", "  "); rerr == nil {
			_ = os.WriteFile(filepath.Join(request.OutputDir, "source-risk-report.json"), append(reportData, '\n'), 0600)
		}
	}
	var output BuildOutput
	switch request.Spec.ResolvedBuildStrategy {
	case buildjob.StrategyDockerfile:
		if request.Publisher != nil {
			output, err = Publish(ctx, request.Spec, sourceDir, request.Workspace, request.OutputDir, request.Publisher, log)
		} else {
			output, err = Build(ctx, request.Spec, sourceDir, request.Workspace, request.OutputDir, log)
		}
	case buildjob.StrategyBuildpack:
		output, err = Buildpack(ctx, request.Spec, sourceDir, request.Workspace, request.OutputDir, request.Publisher, log)
	default:
		err = Error{Code: "BUILD_SPEC_INVALID", Phase: "contract", Message: "canonical Build Spec strategy is invalid"}
	}
	if err != nil {
		return result, err
	}
	result.ImageDigest = output.ImageDigest
	result.BuildDescriptor = output.Descriptor
	result.Remote = output.Remote
	if output.Remote.Descriptor.Digest != "" {
		result.RegistryReference = request.Spec.Publication.DigestReference(output.ImageDigest)
	}
	result.OCIArtifactPath = output.OCIArtifactPath
	result.OCIArtifactSHA256 = output.OCIArtifactSHA256
	result.BuildMetadataPath = output.MetadataPath
	result.Builder = output.Builder
	if output.Builder.BuilderImage != "" {
		result.BuilderIdentity = output.Builder.BuilderImage
	}
	result.Status = "succeeded"
	return result, nil
}
