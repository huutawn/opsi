import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import { LocalClient } from "../../lib/api/local-client.ts";

const view = new URL("./deploy-view.tsx", import.meta.url);
const review = new URL("./plan-review.tsx", import.meta.url);
const sourceStep = new URL("./source-step.tsx", import.meta.url);
const client = new URL("../../lib/api/local-client.ts", import.meta.url);

test("Deploy exposes one role-gated action for each actionable run state", async () => {
  const source = await readFile(view, "utf8");
  for (const value of ["Approve & Deploy", "Acknowledge & Continue", "Retry failed step", "Cancel run", "read-only access"]) assert.match(source, new RegExp(value));
  assert.match(source, /plan_hash:\s*run\.plan\.hash/);
  assert.match(source, /preflight_hash:\s*run\.preflight_hash/);
  assert.match(source, /\["owner", "admin", "developer"\]/);
});

test("Deploy result and technical details use factual authority records", async () => {
  const source = await readFile(view, "utf8");
  for (const value of ["Repository is running", "digest_matches_image_id", "Raw build log", "Image digests", "Build records", "Requested", "Assigned", "Reserved", "Available"]) assert.match(source, new RegExp(value));
  assert.doesNotMatch(source, /localStorage|dangerouslySetInnerHTML/);
  assert.doesNotMatch(source, /sessionStorage/);
  assert.match(source, /source_project/);
  assert.match(source, /clearSourceDraft\(projectID\)/);
});

test("Target reuses canonical bootstrap and resumes analysis when a runtime becomes Ready", async () => {
  const source = await readFile(view, "utf8");
  assert.match(source, /BootstrapDialog/);
	assert.match(source, /BootstrapCommand/);
	assert.match(source, /Connect server/);
	assert.match(source, /Boolean\(action\)\s*&&\s*busy\s*===\s*action/);
  assert.match(source, /client\.placementFacts/);
  assert.match(source, /runtime\.status === "ready"/);
  assert.match(source, /deploymentRunAction\(projectID, run\.id, "analyze"/);
  assert.match(source, /bootstrapActive\s*=\s*Boolean\(needsServer\s*&&\s*bootstrapSession/);
});

test("generated secrets remain redacted and workflow calls only Local API", async () => {
  const [plan, api] = await Promise.all([readFile(review, "utf8"), readFile(client, "utf8")]);
  assert.match(plan, /Generated and securely stored/);
  assert.match(plan, /setValue\(""\)/);
  assert.doesNotMatch(plan, /localStorage|sessionStorage|dangerouslySetInnerHTML/);
  for (const action of ["analyze", "approve", "acknowledge", "retry", "cancel"]) assert.match(api, new RegExp(action));
  assert.match(api, /\/api\/local\/projects\/\$\{projectID\}\/deployment-runs/);
  assert.doesNotMatch(api, /\/api\/projects\//);
	assert.match(api, /normalizeDeploymentRun/);
	for (const collection of ["applications", "resources", "dependencies", "bindings", "secrets", "issues"]) {
		assert.match(api, new RegExp(`run\\.plan\\.${collection} \\?\\?= \\[\\]`));
	}
});

test("Source owns GitHub discovery, installation connection, and repository claim", async () => {
  const [viewSource, source, api] = await Promise.all([readFile(view, "utf8"), readFile(sourceStep, "utf8"), readFile(client, "utf8")]);
  for (const label of ["Continue with GitHub", "Connect installation", "Claim & analyze repository", "Analyze repository", "claimed by another project"]) assert.match(source, new RegExp(label.replace("&", "\\&")));
  for (const method of ["startGitHubInstallationDiscovery", "startGitHubInstallationClaim", "claimGitHubRepository"]) assert.match(viewSource, new RegExp(method));
  assert.match(viewSource, /await client\.claimGitHubRepository[\s\S]+await client\.githubRepositories[\s\S]+await start\(\)/);
  assert.match(viewSource, /Sign in again/);
  assert.match(api, /\/api\/local\/projects\/\$\{projectID\}\/github\/installations\/discover/);
});

test("deployment run normalization fills empty analysis scope arrays", async () => {
  const client = new LocalClient();
  client.call = async () => ({
    deployment_runs: [{
      id: "run-1",
      plan: {
        applications: [],
        resources: [],
        dependencies: [],
        bindings: [],
        secrets: [],
        issues: [{ code: "ANALYSIS_TRUNCATED", blocking: true }],
        analysis_scope: {},
      },
      analysis: {},
    }],
  });

  const response = await client.deploymentRuns("proj-1");
  assert.deepEqual(response.deployment_runs[0].plan.analysis_scope, {
    application_roots: [],
    exclude_paths: [],
  });
});
