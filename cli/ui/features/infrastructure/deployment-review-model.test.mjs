import assert from "node:assert/strict";
import test from "node:test";

import { deploymentPhase, deploymentStage, liveDeploymentHealth, retryableReviewStates, reviewSubmissionKey } from "./deployment-review-model.ts";

test("multi-service submission keys are independent and partial retry skips existing jobs", () => {
  assert.notEqual(reviewSubmissionKey("review-1", "api"), reviewSubmissionKey("review-1", "worker"));
  assert.equal(retryableReviewStates("failed"), true);
  for (const state of ["blocked", "queued", "succeeded"]) assert.equal(retryableReviewStates(state), false);
});

test("Cloud rollout states map to the required live topology stages", () => {
  assert.equal(deploymentStage({ status: "queued" }), "Queued");
  assert.equal(deploymentStage({ status: "leased" }), "Pulling");
  assert.equal(deploymentStage({ status: "applying" }), "Applying");
  assert.equal(deploymentStage({ status: "waiting_ready" }), "Waiting ready");
  assert.equal(deploymentStage({ status: "succeeded" }), "Running");
  assert.equal(deploymentStage({ status: "failed" }), "Failed");
  assert.equal(deploymentPhase({}), "Deploy workload");
  assert.equal(deploymentPhase({ base_deployment_id: "dep-1", action: "apply" }), "Publish route");
  assert.equal(deploymentPhase({ base_deployment_id: "dep-2", action: "rollback" }), "Rollback");
  assert.equal(liveDeploymentHealth({ status: "failed", base_deployment_id: "dep-1", action: "apply" }), "Degraded");
  assert.equal(liveDeploymentHealth({ status: "rolled_back", base_deployment_id: "dep-2", action: "rollback" }), "Running");
});
