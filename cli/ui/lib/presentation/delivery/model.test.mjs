import assert from "node:assert/strict";
import test from "node:test";

import { derivePipeline } from "./model.ts";

const digest = `sha256:${"a".repeat(64)}`;
const service = { id: "svc-api", name: "API", type: "application", status: "ready", source_type: "image" };
const repository = { repository_id: 101, installation_id: 42, full_name: "acme/app", status: "active", claim_status: "active" };
const binding = { id: "binding-api", project_id: "proj-1", service_id: service.id, repository_id: 101, installation_id: 42, service_key: "api", config_path: ".opsi/opsi-cd.yaml", status: "active" };

function build(status = "succeeded") {
  return {
    schema_version: "opsi.build_record/v1", id: `build-${status}`, project_id: "proj-1", repository_id: 101, repository_owner_id: 7,
    active_binding_id: binding.id, service_id: service.id, service_key: "api", created_at: "2026-07-30T01:00:00Z",
    workload: { issuer: "github", subject: "repo:acme/app", repository_id: 101, repository_owner_id: 7, ref: "refs/heads/main", sha: "b".repeat(40), event_name: "push", workflow: "build", workflow_ref: "acme/app/.github/workflows/build.yml@refs/heads/main", run_id: 8, run_attempt: 1 },
    build: { config_hash: "config", platform: "linux/amd64", oci_repository: "ghcr.io/acme/api", oci_digest: digest, status },
  };
}

function job(overrides = {}) {
  return {
    id: "dep-1", project_id: "proj-1", service_id: service.id, environment_id: "env-1", runtime_id: "runtime-1", status: "waiting",
    created_at: "2026-07-30T02:00:00Z", updated_at: "2026-07-30T02:01:00Z", desired_digest: digest,
    snapshot: { project_id: "proj-1", image: { repository: "ghcr.io/acme/api", digest, reference: `ghcr.io/acme/api@${digest}` }, authority: { build_record: build(), topology_plan_id: "topo-1", topology_revision: 1, deployment_policy_id: "policy-1", deployment_policy_revision: 1, runtime_id: "runtime-1", node_id: "node-1", agent_id: "agent-1" }, workload: { service_key: "api", container_port: 8080 }, spec_hash: "spec" },
    ...overrides,
  };
}

function pipeline({ builds = [], deployments = [], availability = {} } = {}) {
  return derivePipeline({ projectID: "proj-1", service, bindings: [binding], repositories: [repository], builds, deployments, availability: { source: "ready", builds: "ready", deployments: "ready", ...availability } });
}

test("active source binding without a BuildRecord is truthful", () => {
  const result = pipeline();
  assert.equal(result.stages.source.status, "succeeded");
  assert.equal(result.stages.build.status, "not_reported");
  assert.equal(result.stages.build.label, "No trusted BuildRecord received");
});

test("factual failed and succeeded BuildRecords drive build and artifact stages", () => {
  assert.equal(pipeline({ builds: [build("failed")] }).stages.build.status, "failed");
  const result = pipeline({ builds: [build()] });
  assert.equal(result.stages.build.status, "succeeded");
  assert.equal(result.stages.artifact.status, "succeeded");
  assert.equal(result.stages.artifact.primaryIdentity, digest);
  assert.equal(result.stages.deploy.label, "Artifact ready — no deployment observed");
});

test("deployment correlation uses exact snapshot BuildRecord identity", () => {
  const accepted = build();
  const historical = job({ id: "dep-old", snapshot: undefined, desired_digest: digest, status: "succeeded" });
  const wrong = job({ id: "dep-wrong", snapshot: { ...job().snapshot, authority: { ...job().snapshot.authority, build_record: { ...accepted, id: "other-build" } } } });
  const exact = job({ id: "dep-exact", snapshot: { ...job().snapshot, authority: { ...job().snapshot.authority, build_record: accepted } } });
  const result = pipeline({ builds: [accepted], deployments: [historical, wrong, exact] });
  assert.equal(result.linkedDeployment?.id, "dep-exact");
  assert.equal(result.unlinkedDeployments.length, 1);
});

test("waiting or incomplete terminal rollout never becomes verified", () => {
  assert.equal(pipeline({ builds: [build()], deployments: [job()] }).stages.verify.status, "waiting");
  const incomplete = job({ status: "succeeded", finished_at: "2026-07-30T02:05:00Z", current_digest: digest, terminal_result: { status: "succeeded", application_image: `ghcr.io/acme/api@${digest}`, application_image_id: "", available_replicas: 2 } });
  const result = pipeline({ builds: [build()], deployments: [incomplete] });
  assert.equal(result.stages.verify.status, "not_reported");
  assert.equal(result.stages.verify.label, "Verification not reported");
});

test("complete factual success verifies and factual rollback restores known-good", () => {
  const succeeded = job({ status: "succeeded", finished_at: "2026-07-30T02:05:00Z", current_digest: digest, readiness_evidence_hash: "evidence", terminal_result: { status: "succeeded", application_image: `ghcr.io/acme/api@${digest}`, application_image_id: `containerd://${digest}`, available_replicas: 2, readiness_evidence_hash: "evidence", current_digest: digest } });
  assert.equal(pipeline({ builds: [build()], deployments: [succeeded] }).stages.verify.status, "succeeded");
  const restored = `sha256:${"c".repeat(64)}`;
  const rolledBack = job({ status: "rolled_back", rollout_state: "rolled_back", finished_at: "2026-07-30T02:06:00Z", current_digest: restored, previous_digest: digest, known_good_id: "kg-1", terminal_result: { status: "rolled_back", application_image: `ghcr.io/acme/api@${restored}`, application_image_id: `containerd://${restored}`, available_replicas: 2, readiness_evidence_hash: "rollback-evidence", current_digest: restored, known_good_id: "kg-1" } });
  const result = pipeline({ builds: [build()], deployments: [rolledBack] });
  assert.equal(result.stages.deploy.status, "rolled_back");
  assert.equal(result.stages.deploy.primaryIdentity, restored);
});

test("NO_KNOWN_GOOD remains failed and source API failure is unavailable", () => {
  const failed = job({ status: "failed", failure_code: "NO_KNOWN_GOOD" });
  assert.equal(pipeline({ builds: [build()], deployments: [failed] }).stages.deploy.status, "failed");
  assert.equal(pipeline({ availability: { source: "unavailable" } }).stages.source.status, "unavailable");
});
