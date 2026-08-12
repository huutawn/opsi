import assert from "node:assert/strict";
import test from "node:test";

import { acceptedDigest, applicationFacts, buildState, deploymentState, exactSourceSHA, placementLabel } from "./model.ts";

const service = { id: "svc-api", name: "api", type: "application", status: "draft", source_type: "git" };
const assignment = { service_key: "api", environment_id: "env-1", runtime_id: "runtime-1", replicas: 1, cpu_request_millicores: 250, memory_request_bytes: 268435456, exposure: { mode: "none" } };

test("application presentation keeps build, placement, and deployment facts independent", () => {
  const [facts] = applicationFacts({
    services: [service], bindings: [], installations: [], repositories: [], environmentID: "env-1",
    topology: { assignments: [] }, placement: null, deployments: [], exposures: [],
    buildJobs: { "svc-api": [{ id: "job-1", application_id: "svc-api", status: "succeeded", created_at: "2026-08-12T01:00:00Z", source: { resolved_commit_sha: "job-sha" } }] },
    buildRecords: [{ id: "record-1", service_id: "svc-api", service_key: "api", created_at: "2026-08-12T00:00:00Z", workload: { sha: "record-sha" }, build: { status: "succeeded", oci_digest: "sha256:accepted" } }],
  });
  assert.equal(buildState(facts), "succeeded");
  assert.equal(placementLabel(facts), "Unplaced");
  assert.equal(deploymentState(facts), "not_deployed");
  assert.equal(exactSourceSHA(facts), "job-sha");
  assert.equal(acceptedDigest(facts), "sha256:accepted");
});

test("placement uses applied TopologyPlan runtime facts, never ServiceRecord fields", () => {
  const [facts] = applicationFacts({
    services: [{ ...service, namespace: "legacy", replicas: 99 }], bindings: [], installations: [], repositories: [], environmentID: "env-1",
    topology: { assignments: [assignment] }, placement: { runtimes: [{ id: "runtime-1", name: "Primary" }], nodes: [{ id: "node-1", runtime_id: "runtime-1" }] }, deployments: [], exposures: [], buildJobs: {}, buildRecords: [],
  });
  assert.equal(placementLabel(facts), "Primary · node-1");
  assert.equal(facts.assignment, assignment);
});
