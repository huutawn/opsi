import type {
  BuildJob,
  BuildRecord,
  DeploymentJob,
  DeploymentPreview,
  GitHubBinding,
  GitHubRepository,
  PlacementFacts,
  ServiceRecord,
} from "../../contracts/registry.ts";

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

export type RollbackCandidate = {
  deploymentID: string;
  knownGoodID: string;
  targetDigest: string;
  sourceSHA?: string;
  configurationRevision?: number;
  completedAt?: string;
  isEligible: boolean;
  blockedReason?: string;
};

export type CurrentDeliveryState = {
  serviceID: string;
  serviceName: string;
  currentDeployment?: DeploymentJob;
  currentBuildRecord?: BuildRecord;
  latestBuildJob?: BuildJob;
  latestBuildRecord?: BuildRecord;
  configurationRevision?: number;
  deployedDigest?: string;
  sourceSHA?: string;
  sourceRef?: string;
  rolloutState: string;
  runtimeStatus: "healthy" | "degraded" | "in_progress" | "failed" | "not_deployed" | "unknown";
  runtimeLabel: string;
  lastSuccessfulAt?: string;
  serverPlacement?: string;
  exposureURL?: string;
  activeOperation?: {
    type: "build" | "deployment" | "rollback";
    id: string;
    status: string;
    startedAt?: string;
  };
  canDeployNewerBuild: boolean;
  newerAcceptedBuild?: BuildRecord;
  rollbackCandidate?: RollbackCandidate;
};

export type BuildRow = {
  id: string;
  type: "job" | "record";
  serviceID: string;
  serviceKey: string;
  serviceName: string;
  ref: string;
  sha: string;
  strategy: string;
  status: "pending" | "ready" | "running" | "succeeded" | "failed" | "cancelled" | "accepted";
  digest?: string;
  createdAt: string;
  completedAt?: string;
  failureCode?: string;
  failureMessage?: string;
  buildRecordID?: string;
  buildJobID?: string;
  provenance?: {
    workflow?: string;
    runId?: number;
    attempt?: number;
    platform?: string;
  };
  rawJob?: BuildJob;
  rawRecord?: BuildRecord;
};

export type DeploymentRow = {
  id: string;
  serviceID: string;
  serviceName: string;
  environmentID: string;
  environmentName?: string;
  status: string;
  rolloutState: string;
  preview: boolean;
  desiredDigest: string;
  currentDigest?: string;
  configurationRevision?: number;
  buildRecordID?: string;
  createdAt: string;
  finishedAt?: string;
  rollbackEligible: boolean;
  knownGoodID?: string;
  failureCode?: string;
  failureMessage?: string;
  runtimeID?: string;
  nodeID?: string;
  rawJob: DeploymentJob;
};

export type DeploymentDiffCategory = "Artifact" | "Configuration" | "Resources" | "Bindings" | "Placement" | "Exposure";

export type CategorizedDiffItem = {
  category: DeploymentDiffCategory;
  field: string;
  before?: string;
  after?: string;
  description: string;
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

export function deriveCurrentDeliveryState(input: {
  service: ServiceRecord;
  deployments: DeploymentJob[];
  builds: BuildRecord[];
  buildJobs?: BuildJob[];
  placement?: PlacementFacts | null;
}): CurrentDeliveryState {
  const { service, deployments, builds, buildJobs = [], placement } = input;
  const serviceDeployments = newestFirst(deployments.filter((job) => job.service_id === service.id));
  const serviceBuilds = newestFirst(builds.filter((record) => record.service_id === service.id && record.build.status === "succeeded"));
  const serviceBuildJobs = newestFirst(buildJobs.filter((job) => job.application_id === service.id));

  // Current deployed release is the newest succeeded or rolled_back deployment
  const currentDeployment = serviceDeployments.find(isFactualCurrentRelease);
  const latestDeployment = serviceDeployments[0];
  const latestBuildJob = serviceBuildJobs[0];
  const latestBuildRecord = serviceBuilds[0];

  // Active operation: build or deployment
  let activeOperation: CurrentDeliveryState["activeOperation"] = undefined;
  if (latestDeployment && !isTerminal(latestDeployment.rollout_state || latestDeployment.status)) {
    const isRollback = latestDeployment.action === "rollback" || latestDeployment.rollout_state === "rolling_back";
    activeOperation = {
      type: isRollback ? "rollback" : "deployment",
      id: latestDeployment.id,
      status: latestDeployment.rollout_state || latestDeployment.status,
      startedAt: latestDeployment.started_at || latestDeployment.created_at,
    };
  } else if (latestBuildJob && !["succeeded", "failed", "cancelled"].includes(latestBuildJob.status)) {
    activeOperation = {
      type: "build",
      id: latestBuildJob.id,
      status: latestBuildJob.status,
      startedAt: latestBuildJob.created_at,
    };
  }

  // Deployed digest and source facts come from current deployment authority
  const deployedDigest = currentDeployment?.current_digest || currentDeployment?.terminal_result?.current_digest;
  const currentBuildRecord = currentDeployment?.snapshot?.authority.build_record;
  const sourceSHA = currentBuildRecord?.workload.sha;
  const sourceRef = currentBuildRecord?.workload.ref;
  const configurationRevision = currentDeployment?.snapshot?.authority.service_configuration_revision ?? service.configuration?.revision;

  // Runtime status & label (distinguishing Deployment outcome from Live runtime state)
  let runtimeStatus: CurrentDeliveryState["runtimeStatus"] = "not_deployed";
  let runtimeLabel = "Not deployed";

  if (currentDeployment) {
    const term = currentDeployment.terminal_result;
    const hasReplicas = typeof term?.available_replicas === "number" && term.available_replicas > 0;
    const hasEvidence = Boolean(term?.readiness_evidence_hash || currentDeployment.readiness_evidence_hash);
    const hasImageId = Boolean(term?.application_image_id);

    if (currentDeployment.rollout_state === "rolled_back") {
      runtimeStatus = "degraded";
      runtimeLabel = "Rolled back to known-good";
    } else if (hasReplicas && hasEvidence && hasImageId) {
      runtimeStatus = "healthy";
      runtimeLabel = `Verified (${term?.available_replicas} replica${term?.available_replicas === 1 ? "" : "s"} ready)`;
    } else {
      runtimeStatus = "degraded";
      runtimeLabel = "Runtime verification incomplete";
    }
  } else if (latestDeployment && (latestDeployment.rollout_state === "failed" || latestDeployment.status === "failed")) {
    runtimeStatus = "failed";
    runtimeLabel = `Deployment failed (${latestDeployment.failure_code || "unspecified"})`;
  } else if (latestDeployment && !isTerminal(latestDeployment.rollout_state || latestDeployment.status)) {
    runtimeStatus = "in_progress";
    runtimeLabel = `Rollout in progress (${humanState(latestDeployment.rollout_state || latestDeployment.status)})`;
  }

  // Can deploy newer build?
  const canDeployNewerBuild = Boolean(
    latestBuildRecord &&
    latestBuildRecord.build.status === "succeeded" &&
    (!currentDeployment || currentDeployment.snapshot?.authority.build_record.id !== latestBuildRecord.id)
  );

  // Rollback candidate
  const rollbackCandidate = currentDeployment ? findRollbackCandidate(serviceDeployments, currentDeployment.id) : undefined;

  // Server placement
  let serverPlacement: string | undefined = undefined;
  if (currentDeployment?.runtime_id) {
    const runtime = placement?.runtimes.find((r) => r.id === currentDeployment.runtime_id);
    const node = placement?.nodes.find((n) => n.id === currentDeployment.node_id);
    serverPlacement = [runtime?.name || currentDeployment.runtime_id, node?.id].filter(Boolean).join(" / ");
  }

  // Exposure URL
  const exposureSpec = currentDeployment?.exposure_spec;
  const exposureURL = exposureSpec?.hostname ? `http://${exposureSpec.hostname}${exposureSpec.path || "/"}` : undefined;

  return {
    serviceID: service.id,
    serviceName: service.name,
    currentDeployment,
    currentBuildRecord,
    latestBuildJob,
    latestBuildRecord,
    configurationRevision,
    deployedDigest,
    sourceSHA,
    sourceRef,
    rolloutState: currentDeployment ? (currentDeployment.rollout_state || currentDeployment.status) : "not_deployed",
    runtimeStatus,
    runtimeLabel,
    lastSuccessfulAt: currentDeployment?.finished_at || currentDeployment?.updated_at,
    serverPlacement,
    exposureURL,
    activeOperation,
    canDeployNewerBuild,
    newerAcceptedBuild: canDeployNewerBuild ? latestBuildRecord : undefined,
    rollbackCandidate,
  };
}

export function findRollbackCandidate(deployments: DeploymentJob[], currentJobID?: string): RollbackCandidate | undefined {
  const current = deployments.find((job) => job.id === currentJobID);
  if (!current) return undefined;

  if (current.rollback_eligible && (current.known_good_id || current.terminal_result?.known_good_id)) {
    const targetID = current.known_good_id || current.terminal_result?.known_good_id || "";
    const targetJob = deployments.find((job) => job.id === targetID || job.snapshot?.authority.topology_plan_id === targetID);
    return {
      deploymentID: current.id,
      knownGoodID: targetID,
      targetDigest: current.previous_digest || current.terminal_result?.previous_digest || targetJob?.current_digest || "Exact known-good authority",
      sourceSHA: targetJob?.snapshot?.authority.build_record.workload.sha,
      configurationRevision: targetJob?.snapshot?.authority.service_configuration_revision,
      completedAt: targetJob?.finished_at,
      isEligible: true,
    };
  }

  if (current.rollback_blocked_reason) {
    return {
      deploymentID: current.id,
      knownGoodID: current.known_good_id || "",
      targetDigest: current.previous_digest || "Unavailable",
      isEligible: false,
      blockedReason: current.rollback_blocked_reason,
    };
  }

  return undefined;
}

export function deriveBuildRows(input: {
  buildJobs: BuildJob[];
  buildRecords: BuildRecord[];
  services: ServiceRecord[];
}): BuildRow[] {
  const { buildJobs, buildRecords, services } = input;
  const serviceMap = new Map(services.map((s) => [s.id, s]));
  const matchedRecordIDs = new Set<string>();
  const rows: BuildRow[] = [];

  for (const job of buildJobs) {
    const service = serviceMap.get(job.application_id);
    const serviceName = service?.name || job.application_id;
    const linkedRecord = buildRecords.find((r) => r.id === job.build_record_id || r.build.build_job_id === job.id);
    if (linkedRecord) matchedRecordIDs.add(linkedRecord.id);

    rows.push({
      id: job.id,
      type: "job",
      serviceID: job.application_id,
      serviceKey: serviceName,
      serviceName,
      ref: job.source?.selected_ref || "",
      sha: job.source?.resolved_commit_sha || "",
      strategy: job.resolved_build_strategy || job.requested_build_strategy || "auto",
      status: job.status,
      digest: linkedRecord?.build.oci_digest,
      createdAt: job.created_at,
      completedAt: job.completed_at,
      failureCode: job.failure_code,
      failureMessage: job.failure_message_redacted,
      buildRecordID: job.build_record_id || linkedRecord?.id,
      buildJobID: job.id,
      provenance: {
        platform: linkedRecord?.build.platform,
      },
      rawJob: job,
      rawRecord: linkedRecord,
    });
  }

  for (const record of buildRecords) {
    if (matchedRecordIDs.has(record.id)) continue;
    const service = serviceMap.get(record.service_id);
    const serviceName = service?.name || record.service_key;

    rows.push({
      id: record.id,
      type: "record",
      serviceID: record.service_id,
      serviceKey: record.service_key,
      serviceName,
      ref: record.workload?.ref || "",
      sha: record.workload?.sha || "",
      strategy: record.build.build_strategy || "dockerfile",
      status: record.build.status === "succeeded" ? "accepted" : "failed",
      digest: record.build.oci_digest,
      createdAt: record.created_at,
      completedAt: record.created_at,
      buildRecordID: record.id,
      buildJobID: record.build.build_job_id,
      provenance: {
        workflow: record.workload?.workflow_ref,
        runId: record.workload?.run_id,
        attempt: record.workload?.run_attempt,
        platform: record.build.platform,
      },
      rawRecord: record,
    });
  }

  return rows.sort((a, b) => (Date.parse(b.createdAt) || 0) - (Date.parse(a.createdAt) || 0));
}

export function deriveDeploymentRows(input: {
  deployments: DeploymentJob[];
  services: ServiceRecord[];
}): DeploymentRow[] {
  const { deployments, services } = input;
  const serviceMap = new Map(services.map((s) => [s.id, s]));

  return newestFirst(deployments).map((job) => {
    const service = serviceMap.get(job.service_id);
    const desiredDigest = job.desired_digest || job.snapshot?.image.digest || "";
    const currentDigest = job.current_digest || job.terminal_result?.current_digest;

    return {
      id: job.id,
      serviceID: job.service_id,
      serviceName: service?.name || job.service_id,
      environmentID: job.environment_id || "",
      status: job.status,
      rolloutState: job.rollout_state || job.status,
      preview: Boolean(job.snapshot?.preview),
      desiredDigest,
      currentDigest,
      configurationRevision: job.snapshot?.authority.service_configuration_revision,
      buildRecordID: job.snapshot?.authority.build_record.id,
      createdAt: job.created_at,
      finishedAt: job.finished_at,
      rollbackEligible: Boolean(job.rollback_eligible),
      knownGoodID: job.known_good_id || job.terminal_result?.known_good_id,
      failureCode: job.failure_code || job.terminal_result?.failure_code,
      failureMessage: job.failure_message_redacted || job.terminal_result?.failure_message_redacted,
      runtimeID: job.runtime_id,
      nodeID: job.node_id,
      rawJob: job,
    };
  });
}

export function categorizeDeploymentDiff(preview?: DeploymentPreview | null): CategorizedDiffItem[] {
  if (!preview) return [];
  const items: CategorizedDiffItem[] = [];
  const current = preview.current;
  const proposed = preview.snapshot;

  // Artifact diff
  if (!current || current.image.digest !== proposed.image.digest) {
    items.push({
      category: "Artifact",
      field: "Image Digest",
      before: current?.image.digest ? formatDigest(current.image.digest, 16) : "None",
      after: formatDigest(proposed.image.digest, 16),
      description: `Immutable image updated to ${formatDigest(proposed.image.digest, 16)}`,
    });
  }

  // Configuration diff
  const currentRev = current?.authority.service_configuration_revision;
  const proposedRev = proposed.authority.service_configuration_revision;
  if (currentRev !== proposedRev) {
    items.push({
      category: "Configuration",
      field: "Configuration Revision",
      before: currentRev !== undefined ? `Revision ${currentRev}` : "Initial",
      after: proposedRev !== undefined ? `Revision ${proposedRev}` : "Latest",
      description: "Service runtime configuration revision changed",
    });
  }

  // Resources diff
  const curWorkload = current?.workload;
  const propWorkload = proposed.workload;
  if (!curWorkload || curWorkload.replicas !== propWorkload.replicas) {
    items.push({
      category: "Resources",
      field: "Replicas",
      before: curWorkload ? String(curWorkload.replicas) : "None",
      after: String(propWorkload.replicas),
      description: `Replicas set to ${propWorkload.replicas}`,
    });
  }
  if (!curWorkload || curWorkload.resources.requests.cpu !== propWorkload.resources.requests.cpu || curWorkload.resources.requests.memory !== propWorkload.resources.requests.memory) {
    items.push({
      category: "Resources",
      field: "CPU / Memory Requests",
      before: curWorkload ? `${curWorkload.resources.requests.cpu} / ${curWorkload.resources.requests.memory}` : "None",
      after: `${propWorkload.resources.requests.cpu} / ${propWorkload.resources.requests.memory}`,
      description: `Compute requests: ${propWorkload.resources.requests.cpu} CPU, ${propWorkload.resources.requests.memory} memory`,
    });
  }

  // Placement diff
  if (!current || current.authority.runtime_id !== proposed.authority.runtime_id || current.authority.node_id !== proposed.authority.node_id) {
    items.push({
      category: "Placement",
      field: "Target Server / Node",
      before: current ? `${current.authority.runtime_id} / ${current.authority.node_id}` : "Unplaced",
      after: `${proposed.authority.runtime_id} / ${proposed.authority.node_id}`,
      description: `Placed on runtime ${proposed.authority.runtime_id} / node ${proposed.authority.node_id}`,
    });
  }

  // Exposure diff
  if (propWorkload.exposure.mode === "internal") {
    items.push({
      category: "Exposure",
      field: "Exposure Mode",
      before: curWorkload?.exposure.mode,
      after: "internal",
      description: "Internal cluster communication only",
    });
  }

  if (items.length === 0 && preview.changes.length > 0) {
    for (const change of preview.changes) {
      items.push({
        category: "Configuration",
        field: change,
        description: `Change detected: ${humanState(change)}`,
      });
    }
  }

  return items;
}

export function hasActiveDeployment(serviceID: string, deployments: DeploymentJob[]) {
  return deployments.some((job) => job.service_id === serviceID && !isTerminal(job.rollout_state || job.status));
}

export function activeDeploymentFor(serviceID: string, deployments: DeploymentJob[]) {
  return deployments.find((job) => job.service_id === serviceID && !isTerminal(job.rollout_state || job.status));
}

export function isDeploymentStaleError(code = "") {
  return [
    "TOPOLOGY_REVIEW_STALE",
    "CONFIGURATION_REVIEW_STALE",
    "POLICY_REVIEW_STALE",
    "ROUTING_TOPOLOGY_CHANGED",
    "ROUTING_POLICY_CHANGED",
    "EXPOSURE_STATE_CONFLICT",
    "DEPLOYMENT_AUTHORITY_REVOKED",
    "DEPLOYMENT_BUILD_AUTHORITY_REVOKED",
  ].includes(code);
}

export function mapDeploymentError(code = "", message = "Deployment operation failed.") {
  const guidance: Record<string, { title: string; action: string }> = {
    TOPOLOGY_REVIEW_STALE: {
      title: "Topology changed since review",
      action: "Review deployment again with the updated TopologyPlan.",
    },
    CONFIGURATION_REVIEW_STALE: {
      title: "Configuration changed since review",
      action: "Review deployment again to adopt the latest ServiceConfiguration revision.",
    },
    POLICY_REVIEW_STALE: {
      title: "Deployment policy changed since review",
      action: "Review deployment again with the updated policy expectations.",
    },
    DEPLOYMENT_LEASE_ACTIVE: {
      title: "Another deployment is actively in progress",
      action: "Wait for the current rollout to reach a terminal state before starting another deployment.",
    },
    DEPLOYMENT_LOCKED: {
      title: "Application deployment lock is held",
      action: "Wait for the active lock holder to release the deployment lease.",
    },
    NO_KNOWN_GOOD: {
      title: "No factual known-good deployment exists",
      action: "Rollback is available only after at least one factual deployment has succeeded.",
    },
    BUILD_RECORD_NOT_ACCEPTED: {
      title: "BuildRecord is not accepted",
      action: "Deploy only an accepted BuildRecord with status succeeded.",
    },
    BUILD_RECORD_NOT_FOUND: {
      title: "BuildRecord not found",
      action: "Select a valid BuildRecord from the build inventory.",
    },
    WORKLOAD_CANONICAL_MISMATCH: {
      title: "Workload spec mismatch",
      action: "Refresh the preview to synchronize with the Cloud compiler.",
    },
    DEPLOYMENT_LEASE_ATTEMPTS_EXHAUSTED: {
      title: "Deployment lease attempts exhausted",
      action: "Check Agent connectivity on the target server, then retry the same job.",
    },
  };

  return {
    title: guidance[code]?.title || humanState(code || "DEPLOYMENT_FAILED"),
    action: guidance[code]?.action || message || "Retry after reviewing the factual error evidence.",
  };
}

export function formatDigest(digest = "", length = 16) {
  if (!digest || !digest.startsWith("sha256:")) return digest || "Not reported";
  const hex = digest.slice(7);
  if (hex.length <= length) return digest;
  return `sha256:${hex.slice(0, Math.max(6, length - 4))}…${hex.slice(-4)}`;
}

export function isFactualCurrentRelease(job: DeploymentJob) {
  const stateValue = job.rollout_state || job.status;
  return ["succeeded", "rolled_back"].includes(stateValue) && fullDigest(job.current_digest || job.terminal_result?.current_digest || "") && Boolean(job.finished_at || job.terminal_result);
}

export function fullDigest(value: string) {
  return /^sha256:[a-f0-9]{64}$/.test(value);
}

export function humanState(value: string) {
  return value.replaceAll("_", " ");
}

export function isTerminal(status?: string) {
  return ["succeeded", "failed", "rolled_back", "rollback_failed", "cancelled", "cleaned"].includes(status ?? "");
}

function newestFirst<T extends { created_at: string; updated_at?: string; finished_at?: string }>(items: T[]) {
  return [...items].sort((left, right) => timeOf(right) - timeOf(left));
}

function timeOf(item: { created_at: string; updated_at?: string; finished_at?: string }) {
  return Date.parse(item.finished_at || item.updated_at || item.created_at) || 0;
}
