import assert from "node:assert/strict";
import test from "node:test";

import { deliveryActivity, deriveProjectSummary, emptyFoundation, normalizeStatus, readinessAggregate, serviceRows } from "./project.ts";

const project = { id: "proj-1", org_id: "org-1", name: "Checkout", slug: "checkout", status: "ready" };
const service = { id: "svc-api", name: "api", type: "application", status: "ready", source_type: "image", replicas: 2 };

function summary(overrides = {}) {
  return deriveProjectSummary({
    project,
    readiness: { project_id: project.id, status: "ready", can_deploy: true },
    services: [service],
    deployments: [{ id: "dep-1", service_id: service.id, status: "succeeded", created_at: "2026-07-29T01:00:00Z" }],
    foundation: {
      ...emptyFoundation,
      placement: { project_id: project.id, environments: [], runtimes: [], nodes: [{ id: "node-1", project_id: project.id, runtime_id: "rt-1", status: "healthy" }], agents: [], services: [] },
      telemetry: [{ service_id: service.id, health: "healthy", pod_count: 2, ready_pods: 2 }],
      incidents: [],
      builds: [],
      sources: { runtime: "available", builds: "available", telemetry: "available", incidents: "available" },
      ...overrides,
    },
  });
}

test("status normalization is truthful and bounded", () => {
  assert.equal(normalizeStatus("ready"), "healthy");
  assert.equal(normalizeStatus("rolling_back"), "in_progress");
  assert.equal(normalizeStatus("offline"), "unavailable");
  assert.equal(normalizeStatus("invented"), "unknown");
});

test("readiness arithmetic never turns 1/2 ready into healthy", () => {
  assert.deepEqual(readinessAggregate([{ service_id: "worker", health: "healthy", pod_count: 2, ready_pods: 1 }]), { ready: 1, desired: 2 });
  const result = summary({ telemetry: [{ service_id: service.id, health: "healthy", pod_count: 2, ready_pods: 1 }] });
  assert.equal(result.overall, "degraded");
  assert.equal(result.attention[0].detail, "1/2 ready");
});

test("open incident and unavailable sources cannot aggregate to healthy", () => {
  assert.equal(summary({ incidents: [{ incident_id: "inc-1", project_id: project.id, service_id: service.id, status: "open", severity: "warning" }] }).overall, "degraded");
  assert.equal(summary({ telemetry: [], sources: { runtime: "available", builds: "available", telemetry: "unavailable", incidents: "unavailable" } }).overall, "unavailable");
  assert.equal(deriveProjectSummary({ project, readiness: { project_id: project.id, status: "ready", can_deploy: false }, services: [], deployments: [], foundation: { ...emptyFoundation, sources: { runtime: "available", builds: "available", telemetry: "available", incidents: "available" } } }).overall, "unknown");
  assert.equal(serviceRows({ services: [service], telemetry: [], telemetrySource: "unavailable", deployments: [], placement: null, topology: null })[0].health, "unavailable");
});

test("service rows do not attach telemetry by display name", () => {
  const row = serviceRows({ services: [service], telemetry: [{ service_id: service.name, health: "healthy", pod_count: 2, ready_pods: 2 }], telemetrySource: "available", deployments: [], placement: null, topology: null })[0];
  assert.equal(row.telemetry, undefined);
  assert.equal(row.health, "unknown");
});

test("failed latest build outranks an older successful deployment", () => {
  const result = summary({ builds: [{ id: "build-2", project_id: project.id, service_id: service.id, service_key: "api", repository_id: 1, repository_owner_id: 1, active_binding_id: "binding-1", created_at: "2026-07-29T02:00:00Z", schema_version: "opsi.build_record/v1", workload: { issuer: "github", subject: "repo", repository_id: 1, repository_owner_id: 1, ref: "refs/heads/main", sha: "abcdef", event_name: "push", workflow: "build", workflow_ref: "build.yml", run_id: 1, run_attempt: 1 }, build: { config_hash: "config", platform: "linux/amd64", oci_repository: "example/api", oci_digest: "sha256:abc", status: "failed" } }] });
  assert.equal(result.overall, "failed");
  assert.match(result.attention[0].title, /build failed/i);
});

test("sparse delivery data stays a factual timeline", () => {
  const sparse = deliveryActivity([{ id: "dep-1", service_id: "api", status: "succeeded", created_at: "2026-07-29T01:00:00Z" }]);
  assert.equal(sparse.kind, "timeline");
  const chart = deliveryActivity([
    { id: "dep-1", service_id: "api", status: "succeeded", created_at: "2026-07-27T01:00:00Z" },
    { id: "dep-2", service_id: "api", status: "failed", created_at: "2026-07-28T01:00:00Z" },
    { id: "dep-3", service_id: "api", status: "rolled_back", created_at: "2026-07-29T01:00:00Z" },
  ]);
  assert.equal(chart.kind, "chart");
  assert.equal(chart.buckets.length, 3);
});
