import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const files = {
  pipeline: new URL("./pipeline-view.tsx", import.meta.url),
  builds: new URL("./builds-view.tsx", import.meta.url),
  deployments: new URL("./deployments-view.tsx", import.meta.url),
  detail: new URL("./deployment-detail.tsx", import.meta.url),
  review: new URL("../infrastructure/deployment-review.tsx", import.meta.url),
  exposure: new URL("./exposure-view.tsx", import.meta.url),
  source: new URL("./source-view.tsx", import.meta.url),
  client: new URL("../../lib/api/local-client.ts", import.meta.url),
  router: new URL("../console/router-map.tsx", import.meta.url),
};

test("Delivery has one canonical view and no deployment/exposure page alias", async () => {
  const source = await Promise.all(Object.values(files).map((file) => readFile(file, "utf8"))).then((parts) => parts.join("\n"));
  assert.match(source, /DeliveryView/);
  assert.match(source, /pipeline: DeliveryView/);
  assert.doesNotMatch(source, /mode === ["']immutable_image["']/);
  assert.doesNotMatch(source, /DeploymentsView.*ExposureView|ExposureView.*DeploymentsView/);
  assert.doesNotMatch(source, /exposures:\s*unknown\[\]/);
  assert.doesNotMatch(source, /fake|optimistic/i);
});

test("Build filters use existing Local API query fields and actions only navigate", async () => {
  const [builds, client] = await Promise.all([readFile(files.builds, "utf8"), readFile(files.client, "utf8")]);
  for (const field of ["service_key", "repository_id", "sha", "status", "cursor"]) assert.match(client, new RegExp(field));
  assert.match(builds, /Prepare Deployment/);
  assert.match(builds, /tab: "deployments"/);
  assert.doesNotMatch(builds, /deploymentApply|deploymentPreview/);
});

test("Topology review sends authority expectations and publishes Exposure only after workload success", async () => {
  const source = await readFile(files.review, "utf8");
  for (const field of ["expected_topology_revision", "expected_topology_hash", "expected_configuration_revision", "expected_configuration_state_hash", "expected_deployment_policy_revision", "expected_deployment_policy_hash"]) assert.match(source, new RegExp(field));
  assert.match(source, /Cloud compiles immutable WorkloadSpec/);
  assert.match(source, /deploymentPreview/);
  assert.match(source, /deploymentApply/);
  assert.match(source, /waitForTerminalDeployment/);
  assert.match(source, /exposurePreview/);
  assert.match(source, /exposureApply/);
  assert.ok(source.indexOf("waitForTerminalDeployment") < source.lastIndexOf("exposureApply"));
  assert.match(source, /changes\[0\] === "unchanged"/);
  assert.doesNotMatch(source, /workload\s*:/);
});

test("deployment actions and exposure semantics are factual and Local-only", async () => {
  const [detail, exposure, source, client] = await Promise.all([readFile(files.detail, "utf8"), readFile(files.exposure, "utf8"), readFile(files.source, "utf8"), readFile(files.client, "utf8")]);
  for (const required of ["rollback_eligible", "DEPLOYMENT_LEASE_ATTEMPTS_EXHAUSTED", "confirmation: exact ? job.id", "Clean Up Preview", "readiness_evidence_hash"]) assert.match(detail, new RegExp(required.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
  assert.match(exposure, /Configured ≠ Publicly Verified/);
  assert.match(exposure, /external DNS\/TLS verification is unavailable/);
  assert.match(source, /service_key/);
  assert.match(source, /Configure Policy/);
  assert.match(client, /\{ exposures: DeploymentJob\[\] \}/);
  assert.doesNotMatch(client, /\/api\/projects\//);
  assert.doesNotMatch(source + detail + exposure, /\b(?:localStorage|sessionStorage)\s*\.|["']Authorization["']\s*:/);
});

test("router keeps Delivery URL drill-down state when changing tabs", async () => {
  const [router, tabs] = await Promise.all([readFile(files.router, "utf8"), readFile(new URL("../../components/navigation/tabs.tsx", import.meta.url), "utf8")]);
  assert.match(router, /routeHref\(\{ \.\.\.route, tab: tab\.id \}\)/);
  assert.match(tabs, /role="tablist"/);
  assert.match(tabs, /aria-selected=/);
  for (const key of ["ArrowRight", "ArrowLeft", "Home", "End"]) assert.match(tabs, new RegExp(key));
});

test("pipeline loading remains distinct from empty or ready conclusions", async () => {
  const pipeline = await readFile(files.pipeline, "utf8");
  assert.match(pipeline, /sourceState === "loading"/);
  assert.match(pipeline, /Loading delivery pipeline/);
});
