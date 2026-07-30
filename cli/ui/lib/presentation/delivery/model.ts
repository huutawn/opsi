import type { BuildRecord, DeploymentJob, GitHubBinding, GitHubRepository, ServiceRecord } from "../../contracts/registry.ts";

export type PipelineStatus = "not_configured" | "not_reported" | "waiting" | "in_progress" | "succeeded" | "failed" | "rolled_back" | "unavailable";
export type DeliveryAvailability = "ready" | "unavailable";

export type PipelineStage = {
  status: PipelineStatus;
  label: string;
  factualSource: string;
  primaryIdentity?: string;
  timestamp?: string;
  nextAction: string;
  targetTab: "source" | "builds" | "deployments" | "exposure";
  explanation: string;
};

export type PipelineResult = {
  stages: { source: PipelineStage; build: PipelineStage; artifact: PipelineStage; deploy: PipelineStage; verify: PipelineStage };
  activeBinding?: GitHubBinding;
  latestBuild?: BuildRecord;
  linkedDeployment?: DeploymentJob;
  unlinkedDeployments: DeploymentJob[];
  currentRelease?: DeploymentJob;
};

export function derivePipeline(input: {
  projectID: string;
  service: ServiceRecord;
  bindings: GitHubBinding[];
  repositories: GitHubRepository[];
  builds: BuildRecord[];
  deployments: DeploymentJob[];
  availability: { source: DeliveryAvailability; builds: DeliveryAvailability; deployments: DeliveryAvailability };
}): PipelineResult {
  const activeBinding = input.bindings.find((item) => item.project_id === input.projectID && item.service_id === input.service.id && item.status === "active" && input.repositories.some((repository) => repository.repository_id === item.repository_id && repository.status === "active" && repository.claim_status === "active"));
  const builds = newestFirst(input.builds.filter((item) => item.project_id === input.projectID && item.service_id === input.service.id));
  const latestBuild = builds[0];
  const serviceDeployments = input.deployments.filter((item) => item.service_id === input.service.id);
  const linkedDeployment = latestBuild ? newestFirst(serviceDeployments.filter((item) => item.snapshot?.authority.build_record.id === latestBuild.id))[0] : undefined;
  const unlinkedDeployments = serviceDeployments.filter((item) => !item.snapshot?.authority.build_record.id);
  const currentRelease = newestFirst(serviceDeployments.filter(isFactualCurrentRelease))[0];

  return {
    activeBinding,
    latestBuild,
    linkedDeployment,
    unlinkedDeployments,
    currentRelease,
    stages: {
      source: sourceStage(input.availability.source, activeBinding),
      build: buildStage(input.availability.builds, latestBuild),
      artifact: artifactStage(input.availability.builds, latestBuild),
      deploy: deployStage(input.availability.deployments, latestBuild, linkedDeployment),
      verify: verifyStage(input.availability.deployments, linkedDeployment),
    },
  };
}

function sourceStage(availability: DeliveryAvailability, binding?: GitHubBinding): PipelineStage {
  if (availability === "unavailable") return stage("unavailable", "Source unavailable", "GitHub repository inventory", "Retry source inventory", "source", "The Local API could not report repository binding state.");
  if (!binding) return stage("not_configured", "Source not configured", "GitHub service binding", "Configure source", "source", "No active canonical repository-to-service binding exists.");
  return stage("succeeded", "Source configured", "Active GitHub service binding", "Inspect source binding", "source", "Project, service, repository, service key, and active binding identity match.", binding.id);
}

function buildStage(availability: DeliveryAvailability, build?: BuildRecord): PipelineStage {
  if (availability === "unavailable") return stage("unavailable", "Build records unavailable", "Cloud BuildRecord API", "Retry build records", "builds", "Previously loaded build facts remain factual, but the source is currently unavailable.");
  if (!build) return stage("not_reported", "No trusted BuildRecord received", "Cloud BuildRecord API", "Inspect workflow delivery", "builds", "Cloud has not received an accepted BuildRecord; this is not proof that GitHub Actions failed.");
  const status = build.build.status === "succeeded" ? "succeeded" : build.build.status === "failed" ? "failed" : "in_progress";
  return stage(status, status === "succeeded" ? "Trusted build accepted" : status === "failed" ? "BuildRecord reports failure" : "BuildRecord in progress", "BuildRecord.build.status", status === "succeeded" ? "Inspect immutable artifact" : "Inspect build evidence", "builds", "The state comes directly from the latest BuildRecord for the exact service ID.", build.id, build.created_at);
}

function artifactStage(availability: DeliveryAvailability, build?: BuildRecord): PipelineStage {
  if (availability === "unavailable") return stage("unavailable", "Artifact unavailable", "Cloud BuildRecord API", "Retry build records", "builds", "Artifact identity cannot be refreshed while BuildRecord data is unavailable.");
  if (!build || build.build.status !== "succeeded") return stage("not_reported", "Immutable artifact not reported", "Accepted BuildRecord", "Wait for a successful BuildRecord", "builds", "An artifact is trusted only after a successful BuildRecord reports a repository and full digest.");
  if (!build.build.oci_repository || !fullDigest(build.build.oci_digest)) return stage("not_reported", "Immutable artifact incomplete", "BuildRecord OCI fields", "Inspect BuildRecord", "builds", "The accepted record does not contain a complete immutable OCI identity.");
  return stage("succeeded", "Immutable artifact ready", "BuildRecord OCI repository and digest", "Prepare deployment", "deployments", "The runtime artifact is the full repository@sha256 digest, never a tag or source SHA.", build.build.oci_digest, build.created_at);
}

function deployStage(availability: DeliveryAvailability, build?: BuildRecord, job?: DeploymentJob): PipelineStage {
  if (availability === "unavailable") return stage("unavailable", "Deployment state unavailable", "Cloud DeploymentJob API", "Retry deployments", "deployments", "Previously loaded rollout facts remain visible while Cloud is unavailable.");
  if (build?.build.status === "succeeded" && !job) return stage("not_reported", "Artifact ready — no deployment observed", "Exact BuildRecord-to-DeploymentJob correlation", "Prepare deployment", "deployments", "No DeploymentJob snapshot references this exact BuildRecord ID.");
  if (!job) return stage("not_reported", "No deployment observed", "Cloud DeploymentJob API", "Inspect deployments", "deployments", "No factual deployment is linked to the selected build.");
  const stateValue = job.rollout_state || job.status;
  const status = stateValue === "rolled_back" ? "rolled_back" : ["failed", "rollback_failed", "cancelled"].includes(stateValue) ? "failed" : ["succeeded"].includes(stateValue) ? "succeeded" : ["prepared", "queued", "leased", "waiting"].includes(stateValue) ? "waiting" : "in_progress";
  const identity = status === "rolled_back" ? job.current_digest || job.terminal_result?.current_digest || job.known_good_id : job.id;
  const label = status === "rolled_back" ? "Rolled back to known-good" : status === "failed" && job.failure_code === "NO_KNOWN_GOOD" ? "Failed — no known-good snapshot" : `Deployment ${humanState(stateValue)}`;
  return stage(status, label, "DeploymentJob rollout state", status === "failed" ? "Inspect failure evidence" : "Inspect rollout", "deployments", status === "rolled_back" ? "The restored digest and known-good identity come from the factual rollback result." : "The stage reflects the exact linked DeploymentJob, not routing eligibility or a nearby timestamp.", identity, job.finished_at || job.updated_at || job.created_at);
}

function verifyStage(availability: DeliveryAvailability, job?: DeploymentJob): PipelineStage {
  if (availability === "unavailable") return stage("unavailable", "Verification unavailable", "DeploymentJob terminal evidence", "Retry deployment detail", "deployments", "Runtime verification cannot be refreshed while the deployment source is unavailable.");
  if (!job) return stage("not_reported", "Verification not reported", "DeploymentJob terminal evidence", "Observe a deployment", "deployments", "Runtime verification requires a linked DeploymentJob.");
  const stateValue = job.rollout_state || job.status;
  if (!["succeeded", "rolled_back", "failed", "rollback_failed", "cancelled"].includes(stateValue)) return stage(stateValue === "waiting" || stateValue === "prepared" || stateValue === "queued" || stateValue === "leased" ? "waiting" : "in_progress", "Runtime verification pending", "DeploymentJob rollout state", "Watch rollout", "deployments", "A non-terminal rollout is never presented as verified.", job.id, job.updated_at || job.created_at);
  if (stateValue === "failed" || stateValue === "rollback_failed" || stateValue === "cancelled") return stage("failed", "Runtime verification failed", "DeploymentJob terminal result", "Inspect failure or rollback", "deployments", job.failure_code === "NO_KNOWN_GOOD" ? "The rollout failed and no factual known-good snapshot was available." : "The terminal rollout did not verify the desired runtime state.", job.failure_code || job.id, job.finished_at || job.updated_at);
  const terminal = job.terminal_result;
  const currentDigest = terminal?.current_digest || job.current_digest;
  const evidence = terminal?.readiness_evidence_hash || job.readiness_evidence_hash;
  if (!terminal || !job.finished_at || !terminal.application_image_id || typeof terminal.available_replicas !== "number" || !evidence || !fullDigest(currentDigest || "")) return stage("not_reported", "Verification not reported", "DeploymentJob terminal evidence", "Inspect technical evidence", "deployments", "The job is terminal but application image ID, replicas, evidence hash, terminal time, or current digest is missing.", job.id, job.finished_at || job.updated_at);
  return stage(stateValue === "rolled_back" ? "rolled_back" : "succeeded", stateValue === "rolled_back" ? "Known-good runtime verified" : "Runtime verified", "DeploymentJob terminal result and readiness evidence", "Inspect runtime evidence", "deployments", "Application image ID, available replicas, readiness evidence, terminal time, and current digest are factual.", currentDigest, job.finished_at);
}

function stage(status: PipelineStatus, label: string, factualSource: string, nextAction: string, targetTab: PipelineStage["targetTab"], explanation: string, primaryIdentity?: string, timestamp?: string): PipelineStage {
  return { status, label, factualSource, primaryIdentity, timestamp, nextAction, targetTab, explanation };
}

function newestFirst<T extends { created_at: string; updated_at?: string; finished_at?: string }>(items: T[]) {
  return [...items].sort((left, right) => timeOf(right) - timeOf(left));
}

function timeOf(item: { created_at: string; updated_at?: string; finished_at?: string }) {
  return Date.parse(item.finished_at || item.updated_at || item.created_at) || 0;
}

function isFactualCurrentRelease(job: DeploymentJob) {
  const stateValue = job.rollout_state || job.status;
  return ["succeeded", "rolled_back"].includes(stateValue) && fullDigest(job.current_digest || job.terminal_result?.current_digest || "") && Boolean(job.finished_at || job.terminal_result);
}

function fullDigest(value: string) {
  return /^sha256:[a-f0-9]{64}$/.test(value);
}

function humanState(value: string) {
  return value.replaceAll("_", " ");
}
