package buildexecutor

import (
	"context"
	"io"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/buildjob"
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
