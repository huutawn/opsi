import type { DeploymentJob } from "../../lib/contracts/registry.ts";

export const deploymentPollInterval = 5_000;

export function terminalDeployment(job?: DeploymentJob | null) {
  return ["succeeded", "failed", "rolled_back", "rollback_failed", "cancelled", "cleaned"].includes(job?.rollout_state || job?.status || "");
}

export function shouldPoll(projectID: string, deploymentID: string, job: DeploymentJob | null, hidden: boolean) {
  return Boolean(projectID && deploymentID && !hidden && !terminalDeployment(job));
}
