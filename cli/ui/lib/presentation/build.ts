import type { BuildJob } from "@/lib/contracts/registry";

export const terminalBuild = (job?: BuildJob | null) => Boolean(job && ["succeeded", "failed", "cancelled"].includes(job.status));

export function buildFailureCategory(code = "") {
  if (code.startsWith("GITHUB_") || code.startsWith("EXACT_COMMIT") || code.startsWith("BUILD_SOURCE")) return "Source";
  if (code.startsWith("DOCKERFILE")) return "Dockerfile";
  if (code.startsWith("BUILDPACK")) return "Buildpacks";
  if (code.startsWith("REGISTRY")) return "Registry";
  return "Executor";
}

export function buildFailure(code = "", fallback = "Build failed.") {
  const guidance: Record<string, { title: string; action: string }> = {
    GITHUB_INSTALLATION_UNAVAILABLE: { title: "GitHub installation unavailable", action: "Reconnect the GitHub installation, then retry the same build." },
    GITHUB_REPOSITORY_UNAVAILABLE: { title: "Repository unavailable", action: "Restore repository access for this project, then retry." },
    GITHUB_REF_NOT_FOUND: { title: "Branch or ref unavailable", action: "Choose an existing branch or ref in Source." },
    GITHUB_COMMIT_UNRESOLVED: { title: "Commit unresolved", action: "Check that the selected ref resolves to a commit, then retry." },
    EXACT_COMMIT_UNAVAILABLE: { title: "Exact commit unavailable", action: "Restore access to the resolved commit, then retry." },
    BUILD_SOURCE_INVALID: { title: "Invalid source path", action: "Check Application root, Build context, and Dockerfile path without changing their intended scope." },
    BUILD_SOURCE_INVALID_SCOPE: { title: "Invalid source binding", action: "Resume source binding for this Application before building." },
    DOCKERFILE_PATH_REQUIRED: { title: "Dockerfile path required", action: "Enter the repository-relative Dockerfile path." },
    DOCKERFILE_NOT_FOUND: { title: "Dockerfile missing", action: "Choose an existing Dockerfile path or switch Build method to Automatic or Buildpacks." },
    DOCKERFILE_AMBIGUOUS: { title: "Multiple Dockerfiles found", action: "Choose Dockerfile and enter the exact path to use." },
    USER_BUILD_FAILED: { title: "Source build failed", action: "Review the redacted build failure, fix the source or Dockerfile, then start a new build." },
    BUILDPACK_DETECTION_FAILED: { title: "Buildpacks detection failed", action: "Use a supported source layout or choose a Dockerfile build." },
    BUILDPACK_BUILD_FAILED: { title: "Buildpacks build failed", action: "Fix the application source reported by the build, then start a new build." },
    BUILDPACK_BUILDER_UNAVAILABLE: { title: "Buildpacks builder unavailable", action: "Retry when the configured builder is available." },
    BUILDPACK_RUN_IMAGE_UNAVAILABLE: { title: "Buildpacks run image unavailable", action: "Retry when the configured run image is available." },
    BUILDPACK_MONOREPO_UNSUPPORTED: { title: "Shared workspace is unsupported by Buildpacks", action: "This application depends on files outside its application root. Use a Dockerfile build or choose a self-contained application directory." },
    EXECUTOR_INFRASTRUCTURE_FAILED: { title: "Build executor unavailable", action: "Retry when the build executor is available." },
    BUILD_JOB_UNAVAILABLE: { title: "Build service unavailable", action: "Retry when the canonical BuildJob service is available." },
    REGISTRY_AUTH_FAILED: { title: "Registry publication unavailable", action: "Retry after Opsi restores registry publication access." },
    REGISTRY_PUSH_FAILED: { title: "Registry publication failed", action: "Retry the build; contact the platform owner if publication remains unavailable." },
    REGISTRY_ARTIFACT_NOT_FOUND: { title: "Published image unavailable", action: "Retry the build so Opsi can verify the immutable registry artifact." },
    REGISTRY_DIGEST_MISMATCH: { title: "Published image verification failed", action: "Start a new build; Opsi will not accept an unverified digest." },
  };
  return { title: guidance[code]?.title ?? fallback, action: guidance[code]?.action ?? "Retry the same reviewed attempt after checking the factual error." };
}
