import assert from "node:assert/strict";
import test from "node:test";
import { bootstrapPollInterval, bootstrapProgress, canvasDraftIssues, canvasDraftStatus, canvasPlacement, capacityLabel, latestActiveBootstrap, moveCanvasPlacement, serverLifecycle, topologyOnboarding } from "./model.ts";

const facts = {
  project_id: "p1",
  environments: [{ id: "env-1", project_id: "p1", name: "Production", type: "prod", status: "active" }],
  runtimes: [{ id: "rt-1", project_id: "p1", environment_id: "env-1", name: "Primary", type: "k3s", status: "ready" }],
  nodes: [{ id: "node-1", project_id: "p1", runtime_id: "rt-1", status: "healthy", cpu_cores: 4, memory_mb: 8192 }],
  agents: [{ id: "agent-1", project_id: "p1", runtime_id: "rt-1", node_id: "node-1", status: "active", capabilities: {} }],
  services: [{ id: "svc-1", project_id: "p1", key: "api" }],
};
const plan = { schema_version: "opsi.topology_plan/v1", id: "topo-1", project_id: "p1", revision: 2, state_hash: "state", plan_hash: "plan", created_by: "u", applied_by: "u", created_at: "now", applied_at: "now", assignments: [{ service_key: "api", environment_id: "env-1", runtime_id: "rt-1", replicas: 2, cpu_request_millicores: 100, memory_request_bytes: 1024, exposure: { mode: "none" } }] };

test("canvas draft keeps applied fields, permits incomplete placement, and resets by deletion", () => {
  const moved = moveCanvasPlacement(plan, {}, "api", { ...facts.runtimes[0], id: "rt-2" });
  assert.equal(canvasDraftStatus(plan, moved, "api"), "moved");
  assert.equal(canvasPlacement(plan, moved, "api").replicas, 2);
  const removed = moveCanvasPlacement(plan, moved, "api");
  assert.equal(canvasDraftStatus(plan, removed, "api"), "pending removal");
  assert.equal(canvasDraftIssues(canvasPlacement(plan, removed, "api")).length, 0);
  assert.deepEqual(moveCanvasPlacement(plan, removed, "api", facts.runtimes[0]), {});

  const placed = moveCanvasPlacement(null, {}, "api", facts.runtimes[0]);
  assert.equal(canvasDraftStatus(null, placed, "api"), "new placement");
  assert.deepEqual(canvasDraftIssues(canvasPlacement(null, placed, "api")), ["Replicas are missing.", "CPU request is missing.", "Memory request is missing.", "Exposure is missing."]);
});

test("capacity and progress stay truthful", () => {
  assert.equal(capacityLabel(undefined, 512), "Unknown capacity");
  assert.deepEqual(bootstrapProgress(undefined), { label: "Not reported", percent: null });
  assert.deepEqual(bootstrapProgress({ next_step_index: 2, last_completed_step: "Installing" }), { label: "Installing", percent: 50 });
});

test("topology onboarding follows factual project state", () => {
  const withoutServer = { ...facts, runtimes: [], nodes: [], agents: [], services: [] };
  assert.equal(topologyOnboarding(withoutServer, null, []).action, "Connect server");
  assert.deepEqual(topologyOnboarding(withoutServer, null, [{ id: "boot-1", status: "installing", role: "first_server", checkpoint: { plan_version: "v1", next_step_index: 2, last_completed_step: "preflight" }, created_at: "now" }]), {
    kind: "bootstrap", title: "Server connection in progress", description: "Bootstrap boot-1 is installing.", action: "Inspect progress", progress: { label: "preflight", percent: 50 }, sessionID: "boot-1",
  });
  assert.equal(topologyOnboarding({ ...facts, services: [] }, null, []).action, "Add application");
  assert.equal(topologyOnboarding(facts, null, []).action, "Plan placement");
  assert.equal(topologyOnboarding(facts, plan, []).action, "Inspect topology");
});

test("server lifecycle requires a usable node and active Agent", () => {
  assert.equal(serverLifecycle(facts, []).status, "Ready");
  assert.equal(serverLifecycle({ ...facts, agents: [{ ...facts.agents[0], status: "offline" }] }, []).status, "Offline");
  assert.equal(topologyOnboarding({ ...facts, agents: [{ ...facts.agents[0], status: "offline" }] }, null, []).action, "Inspect topology");
});

test("ready facts win over stale bootstrap and failed sessions retry", () => {
  const stale = { id: "boot-old", status: "installing", role: "first_server", created_at: "2026-07-01T00:00:00Z" };
  assert.equal(serverLifecycle(facts, [stale]).status, "Ready");
  assert.equal(serverLifecycle(facts, [stale]).session, undefined);
  assert.equal(topologyOnboarding({ ...facts, services: [] }, null, [stale]).action, "Add application");
  const failed = { id: "boot-failed", status: "dead_letter", role: "first_server", last_failure_code: "SSH_FAILED", last_failure_message_redacted: "Connection refused", created_at: "2026-07-02T00:00:00Z" };
  assert.deepEqual(topologyOnboarding({ ...facts, runtimes: [], nodes: [], agents: [], services: [] }, null, [failed]), { kind: "retry", title: "Server bootstrap failed", description: "Connection refused", action: "Retry bootstrap", sessionID: "boot-failed" });
});

test("newest active bootstrap is selected and polling stays within the requested interval", () => {
  const sessions = [
    { id: "older", status: "installing", role: "worker", created_at: "2026-07-01T00:00:00Z" },
    { id: "failed-newer", status: "failed", role: "worker", created_at: "2026-07-03T00:00:00Z" },
    { id: "newer-active", status: "waiting_agent", role: "worker", created_at: "2026-07-02T00:00:00Z" },
  ];
  assert.equal(latestActiveBootstrap(sessions)?.id, "newer-active");
  assert.equal(bootstrapPollInterval, 4_000);
});
