package buildexecutor

import (
	"context"
	"io"
	"time"
)

func Execute(ctx context.Context, request Request, log io.Writer) (result Result, err error) {
	result = Result{
		BuildJobID: request.Spec.BuildJobID, AttemptID: request.AttemptID, ResolvedCommitSHA: request.Spec.ResolvedCommitSHA,
		Strategy: request.Spec.ResolvedBuildStrategy, DockerfilePath: request.Spec.DockerfilePath, BuildContext: request.Spec.BuildContext,
		Platform: Platform, BuildKitVersion: BuildKitVersion, BuildxVersion: BuildxVersion, StartedAt: time.Now().UTC(), Status: "failed",
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
	output, err := Build(ctx, request.Spec, sourceDir, request.Workspace, request.OutputDir, log)
	if err != nil {
		return result, err
	}
	result.ImageDigest = output.ImageDigest
	result.OCIArtifactPath = output.OCIArtifactPath
	result.OCIArtifactSHA256 = output.OCIArtifactSHA256
	result.BuildMetadataPath = output.MetadataPath
	result.Status = "succeeded"
	return result, nil
}
