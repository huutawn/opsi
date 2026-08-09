import type { DeploymentJob } from "@/lib/contracts/registry";

export type ReviewSubmitState = "blocked" | "queued" | "succeeded" | "failed";

export function reviewSubmissionKey(reviewID: string, serviceID: string) {
  return `${reviewID}:${serviceID}`;
}

export function retryableReviewStates(state: ReviewSubmitState) {
  return state === "failed";
}

export function deploymentStage(job: Pick<DeploymentJob, "status" | "rollout_state">) {
  switch (job.rollout_state || job.status) {
    case "queued": return "Queued";
    case "leased":
    case "pulling": return "Pulling";
    case "prepared":
    case "applying": return "Applying";
    case "waiting":
    case "waiting_ready": return "Waiting ready";
    case "succeeded": return "Running";
    case "failed":
    case "dead_letter": return "Failed";
    case "rolled_back": return "Rolled back";
    case "rollback_failed": return "Rollback failed";
    default: return job.rollout_state || job.status || "Queued";
  }
}

export function deploymentPhase(job: Pick<DeploymentJob, "action" | "base_deployment_id">) {
  if (job.action === "rollback") return "Rollback";
  return job.base_deployment_id ? "Publish route" : "Deploy workload";
}

export function liveDeploymentHealth(job: Pick<DeploymentJob, "action" | "base_deployment_id" | "status" | "rollout_state">) {
  const state = job.rollout_state || job.status;
  if (["succeeded", "rolled_back"].includes(state)) return "Running";
  if (["failed", "dead_letter", "rollback_failed"].includes(state)) return job.base_deployment_id && job.action !== "rollback" ? "Degraded" : "Failed";
  return deploymentStage(job);
}

export function reviewFingerprint(parts: Array<string | number | undefined>) {
  return parts.map((part) => String(part ?? "")).join("|");
}
