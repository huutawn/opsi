import assert from "node:assert/strict";
import test from "node:test";
import { bootstrapProgress, buildTopologyGraph, capacityLabel, layoutTopology } from "./model.ts";

const facts = {
  project_id: "p1",
  environments: [{ id: "env-1", project_id: "p1", name: "Production", type: "prod", status: "active" }],
  runtimes: [{ id: "rt-1", project_id: "p1", environment_id: "env-1", name: "Primary", type: "k3s", status: "ready" }],
  nodes: [{ id: "node-1", project_id: "p1", runtime_id: "rt-1", status: "healthy", cpu_cores: 4, memory_mb: 8192 }],
  agents: [{ id: "agent-1", project_id: "p1", runtime_id: "rt-1", node_id: "node-1", status: "active", capabilities: {} }],
  services: [{ id: "svc-1", project_id: "p1", key: "api" }],
};
const plan = { schema_version: "opsi.topology_plan/v1", id: "topo-1", project_id: "p1", revision: 2, state_hash: "state", plan_hash: "plan", created_by: "u", applied_by: "u", created_at: "now", applied_at: "now", assignments: [{ service_key: "api", environment_id: "env-1", runtime_id: "rt-1", replicas: 2, cpu_request_millicores: 100, memory_request_bytes: 1024, exposure: { mode: "none" } }] };

test("topology edges use exact IDs and keep unresolved identity separate", () => {
  const graph = buildTopologyGraph(facts, plan);
  assert.equal(graph.edges.filter((edge) => edge.relation === "TopologyPlan.assignments").length, 1);
  assert.equal(graph.unresolved.length, 0);
  const missing = buildTopologyGraph({ ...facts, nodes: [{ ...facts.nodes[0], runtime_id: "missing" }] }, plan);
  assert.equal(missing.edges.some((edge) => edge.from === "runtime:missing"), false);
  assert.match(missing.unresolved[0].reason, /Runtime missing/);
});

test("layout is deterministic and capacity/progress stay truthful", () => {
  const graph = buildTopologyGraph(facts, plan);
  assert.deepEqual(layoutTopology(graph.nodes), layoutTopology(graph.nodes));
  assert.equal(capacityLabel(undefined, 512), "Unknown capacity");
  assert.deepEqual(bootstrapProgress(undefined), { label: "Not reported", percent: null });
  assert.deepEqual(bootstrapProgress({ next_step_index: 2, last_completed_step: "Installing" }), { label: "Installing", percent: 50 });
});
