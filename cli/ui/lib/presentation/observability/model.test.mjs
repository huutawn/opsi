import assert from "node:assert/strict";
import test from "node:test";

import {
  deriveApplicationEvents,
  deriveApplicationRuntimeSummaries,
  deriveDeploymentOutcome,
  deriveExposureRuntimeStatus,
  deriveResourceRuntimeSummaries,
  deriveRuntimeOverview,
  deriveServerPlacement,
  deriveServerRuntimeSummaries,
  deriveWorkloadRuntimeStatus,
  formatFreshness,
  formatReplicas,
  formatResourceTypeLabel,
  formatShortDigest,
} from "./model.ts";

test("formatFreshness and formatting helpers handle factual timestamps safely", () => {
  const now = 1700000000000;
  assert.equal(formatFreshness(null), "Not reported");
  assert.equal(formatFreshness(undefined), "Not reported");
  assert.equal(formatFreshness((now - 10000) / 1000, now), "Observed 10s ago");
  assert.equal(formatFreshness((now - 120000) / 1000, now), "Observed 2m ago");
  assert.equal(formatFreshness((now - 7200000) / 1000, now), "Observed 2h ago");
  assert.equal(formatFreshness((now - 172800000) / 1000, now), "Observed 2d ago");

  assert.equal(formatShortDigest("sha256:abcdef0123456789abcdef0123456789"), "abcdef012345…");
  assert.equal(formatShortDigest("registry.example.com/app@sha256:11223344556677889900"), "112233445566…");
  assert.equal(formatShortDigest(undefined), "Not reported");

  assert.equal(formatReplicas(1, 1), "1/1");
  assert.equal(formatReplicas(0, 2), "0/2");
  assert.equal(formatReplicas(undefined, 1), "?/1");
  assert.equal(formatReplicas(undefined, undefined), "Not reported");

  assert.equal(formatResourceTypeLabel("postgres"), "PostgreSQL");
  assert.equal(formatResourceTypeLabel("valkey"), "Valkey");
  assert.equal(formatResourceTypeLabel("nats"), "NATS");
});

test("deriveWorkloadRuntimeStatus maps factual health truthfully without synthetic metrics", () => {
  // Direct helper tests
  assert.equal(deriveDeploymentOutcome({ id: "d-1", status: "succeeded" }), "succeeded");
  assert.equal(deriveDeploymentOutcome({ id: "d-2", status: "failed" }), "failed");
  assert.equal(deriveDeploymentOutcome(undefined), "unknown");

  assert.equal(deriveExposureRuntimeStatus("svc", "id-1", []).status, "not_configured");
  assert.equal(deriveServerPlacement("id-1", "key-1", null, null, []).nodeName, "Unplaced");

  // Agent unavailable -> unknown
  assert.equal(
    deriveWorkloadRuntimeStatus({ service_id: "api", health: "healthy", pod_count: 1, ready_pods: 1 }, true, false),
    "unknown",
  );

  // Missing telemetry with deployment -> unknown
  assert.equal(deriveWorkloadRuntimeStatus(undefined, true, true), "unknown");

  // Missing telemetry without deployment -> not_deployed
  assert.equal(deriveWorkloadRuntimeStatus(undefined, false, true), "not_deployed");

  // Telemetry healthy and all replicas ready -> ready
  assert.equal(
    deriveWorkloadRuntimeStatus({ service_id: "api", health: "healthy", pod_count: 2, ready_pods: 2 }, true, true),
    "ready",
  );

  // Telemetry ready_pods < pod_count -> degraded
  assert.equal(
    deriveWorkloadRuntimeStatus({ service_id: "api", health: "healthy", pod_count: 2, ready_pods: 1 }, true, true),
    "degraded",
  );

  // Telemetry health degraded -> degraded
  assert.equal(
    deriveWorkloadRuntimeStatus({ service_id: "api", health: "degraded", pod_count: 1, ready_pods: 1 }, true, true),
    "degraded",
  );

  // Telemetry health failed -> failed
  assert.equal(
    deriveWorkloadRuntimeStatus({ service_id: "api", health: "failed", pod_count: 1, ready_pods: 0 }, true, true),
    "failed",
  );
});

test("deployment success and degraded runtime status coexist factually without conflation", () => {
  const services = [
    { id: "svc-web", key: "web", name: "Web App", type: "application", replicas: 2, current_configuration_revision: 3 },
  ];
  const deployments = [
    {
      id: "dep-1",
      service_id: "svc-web",
      status: "succeeded",
      rollout_state: "succeeded",
      desired_digest: "sha256:aaaa0000aaaa0000aaaa0000aaaa0000aaaa0000aaaa0000aaaa0000aaaa0000",
      created_at: "2026-08-17T01:00:00Z",
      updated_at: "2026-08-17T01:05:00Z",
    },
  ];
  // Telemetry observed later: 1/2 replicas ready
  const telemetry = [
    { service_id: "svc-web", health: "degraded", pod_count: 2, ready_pods: 1, restart_count: 3, last_seen_unix: 1723850000 },
  ];

  const summaries = deriveApplicationRuntimeSummaries({
    services,
    deployments,
    telemetry,
    agentAvailable: true,
  });

  assert.equal(summaries.length, 1);
  const app = summaries[0];

  // Deployment fact: succeeded
  assert.equal(app.lastDeploymentOutcome, "succeeded");
  assert.equal(app.lastDeploymentLabel, "Succeeded");

  // Runtime fact: degraded
  assert.equal(app.workloadStatus, "degraded");
  assert.equal(app.workloadLabel, "Degraded");
  assert.equal(app.readyReplicas, 1);
  assert.equal(app.desiredReplicas, 2);
  assert.equal(app.replicasLabel, "1/2");
  assert.equal(app.restartCount, 3);
  assert.equal(app.configurationRevision, 3);
});

test("exposure failure does not mark ready workload as failed", () => {
  const services = [
    { id: "svc-api", key: "api", name: "API Service", type: "application", replicas: 1 },
  ];
  const telemetry = [
    { service_id: "svc-api", health: "healthy", pod_count: 1, ready_pods: 1, last_seen_unix: 1723850000 },
  ];
  const placement = {
    project_id: "proj-1",
    environments: [],
    runtimes: [],
    nodes: [],
    agents: [],
    services: [],
    exposures: [
      { service_key: "api", hostname: "api.example.com", path: "/v1", status: "failed" },
    ],
  };

  const summaries = deriveApplicationRuntimeSummaries({
    services,
    telemetry,
    placement,
    agentAvailable: true,
  });

  assert.equal(summaries.length, 1);
  const app = summaries[0];

  // Workload is Ready
  assert.equal(app.workloadStatus, "ready");
  assert.equal(app.workloadLabel, "Ready");

  // Exposure is Failed
  assert.equal(app.exposureStatus, "failed");
  assert.equal(app.exposureLabel, "Failed");
  assert.equal(app.exposureHostname, "api.example.com");
  assert.equal(app.exposurePath, "/v1");
});

test("server offline state marks server as Offline without fabricating healthy state", () => {
  const nodes = [
    { id: "node-1", name: "server-primary", role: "worker", status: "offline", public_host: "192.0.2.10", cpu_cores: 8, memory_mb: 16384 },
    { id: "node-2", name: "server-secondary", role: "worker", status: "ready", public_host: "192.0.2.11", cpu_cores: 4, memory_mb: 8192 },
  ];
  const topology = {
    schema_version: "opsi.topology_plan/v1",
    id: "top-1",
    project_id: "proj-1",
    revision: 1,
    state_hash: "hash-1",
    plan_hash: "plan-1",
    assignments: [
      { service_id: "svc-1", node_id: "node-1" },
      { service_id: "svc-2", node_id: "node-2" },
    ],
    created_by: "user-1",
    applied_by: "user-1",
    created_at: "2026-08-17T01:00:00Z",
    applied_at: "2026-08-17T01:00:00Z",
  };
  const services = [
    { id: "svc-1", key: "web", name: "Web" },
    { id: "svc-2", key: "api", name: "API" },
  ];

  const summaries = deriveServerRuntimeSummaries({
    nodes,
    topology,
    services,
    agentAvailable: true,
  });

  assert.equal(summaries.length, 2);
  const server1 = summaries.find((s) => s.id === "node-1");
  const server2 = summaries.find((s) => s.id === "node-2");

  assert.equal(server1.status, "offline");
  assert.equal(server1.statusLabel, "Offline");
  assert.equal(server1.agentConnected, false);
  assert.equal(server1.placedWorkloadCount, 1);
  assert.deepEqual(server1.placedServices, ["Web"]);

  assert.equal(server2.status, "ready");
  assert.equal(server2.statusLabel, "Ready");
  assert.equal(server2.placedWorkloadCount, 1);
});

test("managed resource observability extracts PostgreSQL, Valkey, NATS facts without credentials", () => {
  const resources = [
    {
      id: "res-pg",
      name: "main-db",
      type: "postgres",
      version: "16.2",
      status: "ready",
      cpu_cores: 2,
      memory_bytes: 4294967296,
      storage_bytes: 53687091200,
      created_at: "2026-08-17T01:00:00Z",
    },
    {
      id: "res-valkey",
      name: "cache",
      type: "valkey",
      version: "7.2",
      status: "ready",
      created_at: "2026-08-17T01:00:00Z",
    },
    {
      id: "res-nats",
      name: "events",
      type: "nats",
      version: "2.10",
      status: "degraded",
      created_at: "2026-08-17T01:00:00Z",
    },
  ];

  const bindings = [
    { id: "bind-1", service_id: "svc-api", service_key: "api", resource_id: "res-pg" },
    { id: "bind-2", service_id: "svc-web", service_key: "web", resource_id: "res-pg" },
    { id: "bind-3", service_id: "svc-api", service_key: "api", resource_id: "res-valkey" },
  ];

  const summaries = deriveResourceRuntimeSummaries({
    resources,
    bindings,
  });

  assert.equal(summaries.length, 3);

  const pg = summaries.find((r) => r.id === "res-pg");
  assert.equal(pg.typeLabel, "PostgreSQL");
  assert.equal(pg.status, "ready");
  assert.equal(pg.applicationBindingCount, 2);
  assert.deepEqual(pg.boundServiceKeys, ["api", "web"]);
  assert.equal(pg.version, "16.2");
  assert.equal(pg.storageBytes, 53687091200);

  // Ensure no password or URL credentials exist in DTO
  const pgJSON = JSON.stringify(pg);
  assert.doesNotMatch(pgJSON, /password|secret|credentials|postgres:\/\/.*:.*@/i);

  const valkey = summaries.find((r) => r.id === "res-valkey");
  assert.equal(valkey.typeLabel, "Valkey");
  assert.equal(valkey.applicationBindingCount, 1);

  const nats = summaries.find((r) => r.id === "res-nats");
  assert.equal(nats.typeLabel, "NATS");
  assert.equal(nats.status, "degraded");
});

test("deriveRuntimeOverview aggregates operational state and generates actionable failures", () => {
  const applications = [
    {
      id: "app-1",
      name: "Web",
      workloadStatus: "ready",
      workloadLabel: "Ready",
      exposureStatus: "ready",
      lastDeploymentOutcome: "succeeded",
      readyReplicas: 1,
      desiredReplicas: 1,
      restartCount: 0,
      boundResourceCount: 1,
    },
    {
      id: "app-2",
      name: "Worker",
      workloadStatus: "degraded",
      workloadLabel: "Degraded",
      exposureStatus: "not_configured",
      lastDeploymentOutcome: "succeeded",
      readyReplicas: 0,
      desiredReplicas: 2,
      restartCount: 5,
      serverPlacement: "Server A",
      lastSeenUnix: 1723850000,
      lastSeenFreshness: "Observed 10s ago",
      boundResourceCount: 0,
    },
  ];

  const servers = [
    { id: "srv-1", name: "Server A", status: "ready", statusLabel: "Ready", placedWorkloadCount: 2, degradedWorkloadCount: 1 },
    { id: "srv-2", name: "Server B", status: "offline", statusLabel: "Offline", publicHost: "192.0.2.20", placedWorkloadCount: 1, degradedWorkloadCount: 0, lastSeenFreshness: "Observed 5m ago" },
  ];

  const resources = [
    { id: "res-1", name: "Postgres", type: "postgres", typeLabel: "PostgreSQL", status: "ready", statusLabel: "Ready", applicationBindingCount: 1 },
    { id: "res-2", name: "Valkey Cache", type: "valkey", typeLabel: "Valkey", status: "failed", statusLabel: "Failed", applicationBindingCount: 2, serverPlacement: "Server A", lastFailure: "OOMKilled" },
  ];

  const overview = deriveRuntimeOverview({
    applications,
    servers,
    resources,
  });

  assert.equal(overview.applications.ready, 1);
  assert.equal(overview.applications.degraded, 1);
  assert.equal(overview.applications.total, 2);

  assert.equal(overview.servers.ready, 1);
  assert.equal(overview.servers.offline, 1);
  assert.equal(overview.servers.total, 2);

  assert.equal(overview.resources.ready, 1);
  assert.equal(overview.resources.failed, 1);
  assert.equal(overview.resources.total, 2);

  // Actionable failures should identify the degraded worker, offline server, and failed resource
  assert.ok(overview.actionableFailures.length >= 3);
  const workerFail = overview.actionableFailures.find((f) => f.category === "workload");
  assert.ok(workerFail);
  assert.match(workerFail.title, /Worker is Degraded/);
  assert.equal(workerFail.target.service, "app-2");

  const serverFail = overview.actionableFailures.find((f) => f.category === "server");
  assert.ok(serverFail);
  assert.match(serverFail.title, /Server B is Offline/);
  assert.equal(serverFail.target.server, "srv-2");

  const resourceFail = overview.actionableFailures.find((f) => f.category === "resource");
  assert.ok(resourceFail);
  assert.match(resourceFail.title, /Valkey.*is Failed/);
  assert.equal(resourceFail.target.resource, "res-2");
});

test("deriveApplicationEvents correlates deployment jobs and audit records deterministically", () => {
  const deployments = [
    {
      id: "dep-1",
      service_id: "svc-web",
      status: "succeeded",
      rollout_state: "succeeded",
      desired_digest: "sha256:1111222233334444555566667777888899990000111122223333444455556666",
      created_at: "2026-08-17T01:00:00Z",
      updated_at: "2026-08-17T01:02:00Z",
    },
  ];
  const audit = [
    {
      id: "aud-1",
      resource_id: "svc-web",
      resource_type: "Service",
      action: "SERVICE_CONFIGURATION_APPLIED",
      result: "success",
      actor_user_id: "user-1",
      created_at: "2026-08-17T01:03:00Z",
    },
  ];

  const events = deriveApplicationEvents("svc-web", "web", deployments, audit);
  assert.equal(events.length, 2);
  // Deterministic chronological ordering (newest first)
  assert.equal(events[0].id, "audit-aud-1");
  assert.equal(events[1].id, "dep-dep-1");
  assert.equal(events[1].status, "succeeded");
});
