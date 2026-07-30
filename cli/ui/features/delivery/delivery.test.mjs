import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const files = {
  pipeline: new URL("./pipeline-view.tsx", import.meta.url),
  builds: new URL("./builds-view.tsx", import.meta.url),
  deployments: new URL("./deployments-view.tsx", import.meta.url),
  detail: new URL("./deployment-detail.tsx", import.meta.url),
  create: new URL("./deployment-create.tsx", import.meta.url),
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

test("manual deployment refuses hidden defaults and preview must be eligible before review", async () => {
  const source = await readFile(files.create, "utf8");
  for (const forbidden of ["8080", "replicas: 1", "100m", "128Mi"]) assert.doesNotMatch(source, new RegExp(forbidden.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
  assert.match(source, /No port, replica, resource, termination, or readiness default is assumed/);
  for (const label of ["Readiness Initial Delay Seconds", "Readiness Period Seconds", "Readiness Timeout Seconds", "Readiness Failure Threshold"]) assert.match(source, new RegExp(label));
  assert.match(source, /!preview\.eligible/);
  assert.match(source, /deploymentDiff/);
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
  const router = await readFile(files.router, "utf8");
  assert.match(router, /routeHref\(\{ \.\.\.route, tab: tab\.id \}\)/);
  assert.match(router, /aria-current=/);
});
