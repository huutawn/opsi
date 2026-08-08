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
    default: return job.rollout_state || job.status || "Queued";
  }
}

export function reviewFingerprint(parts: Array<string | number | undefined>) {
  return parts.map((part) => String(part ?? "")).join("|");
}
