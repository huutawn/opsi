import type { PlacementFacts, TopologyAssignment, TopologyPlan } from "../../contracts/registry.ts";

export type TopologyKind = "environment" | "runtime" | "node" | "agent" | "service";
export type TopologyNode = { id: string; kind: TopologyKind; label: string; status: string; detail: string };
export type TopologyEdge = { from: string; to: string; relation: string };
export type TopologyGraph = { nodes: TopologyNode[]; edges: TopologyEdge[]; unresolved: Array<{ id: string; label: string; reason: string }> };
export type PositionedNode = TopologyNode & { x: number; y: number };

export function buildTopologyGraph(facts: PlacementFacts, plan: TopologyPlan | null): TopologyGraph {
  const nodes: TopologyNode[] = [];
  const edges: TopologyEdge[] = [];
  const unresolved: TopologyGraph["unresolved"] = [];
  const environmentIDs = new Set(facts.environments.map((item) => item.id));
  const runtimeIDs = new Set(facts.runtimes.map((item) => item.id));
  const nodeIDs = new Set(facts.nodes.map((item) => item.id));
  const serviceKeys = new Set(facts.services.map((item) => item.key));

  for (const environment of facts.environments) {
    nodes.push({ id: graphID("environment", environment.id), kind: "environment", label: environment.name, status: environment.status, detail: environment.type });
  }
  for (const runtime of facts.runtimes) {
    nodes.push({ id: graphID("runtime", runtime.id), kind: "runtime", label: runtime.name, status: runtime.status, detail: runtime.type });
    if (environmentIDs.has(runtime.environment_id)) edges.push({ from: graphID("environment", runtime.environment_id), to: graphID("runtime", runtime.id), relation: "runtime.environment_id" });
    else unresolved.push({ id: runtime.id, label: runtime.name, reason: `Environment ${runtime.environment_id || "identity"} is missing.` });
  }
  for (const node of facts.nodes) {
    nodes.push({ id: graphID("node", node.id), kind: "node", label: node.id, status: node.status, detail: capacityLabel(node.cpu_cores, node.memory_mb) });
    if (runtimeIDs.has(node.runtime_id)) edges.push({ from: graphID("runtime", node.runtime_id), to: graphID("node", node.id), relation: "node.runtime_id" });
    else unresolved.push({ id: node.id, label: node.id, reason: `Runtime ${node.runtime_id || "identity"} is missing.` });
  }
  for (const agent of facts.agents) {
    nodes.push({ id: graphID("agent", agent.id), kind: "agent", label: agent.id, status: agent.status, detail: agent.last_seen_at || "Heartbeat not reported" });
    if (!runtimeIDs.has(agent.runtime_id)) unresolved.push({ id: agent.id, label: agent.id, reason: `Runtime ${agent.runtime_id || "identity"} is missing.` });
    else edges.push({ from: graphID("runtime", agent.runtime_id), to: graphID("agent", agent.id), relation: "agent.runtime_id" });
    if (nodeIDs.has(agent.node_id)) edges.push({ from: graphID("node", agent.node_id), to: graphID("agent", agent.id), relation: "agent.node_id" });
    else unresolved.push({ id: agent.id, label: agent.id, reason: `Node ${agent.node_id || "identity"} is missing.` });
  }
  for (const service of facts.services) {
    nodes.push({ id: graphID("service", service.key), kind: "service", label: service.key, status: assignmentFor(plan, service.key) ? "assigned" : "unassigned", detail: service.id });
  }
  for (const assignment of plan?.assignments ?? []) {
    if (!serviceKeys.has(assignment.service_key)) {
      unresolved.push({ id: assignment.service_key, label: assignment.service_key, reason: "Assignment service key does not exactly match service inventory." });
      continue;
    }
    if (!runtimeIDs.has(assignment.runtime_id)) {
      unresolved.push({ id: assignment.service_key, label: assignment.service_key, reason: `Assigned runtime ${assignment.runtime_id} is missing.` });
      continue;
    }
    edges.push({ from: graphID("runtime", assignment.runtime_id), to: graphID("service", assignment.service_key), relation: "TopologyPlan.assignments" });
  }
  return { nodes, edges, unresolved };
}

export function layoutTopology(nodes: TopologyNode[]): PositionedNode[] {
  const columns: TopologyKind[][] = [["environment"], ["runtime"], ["node"], ["agent"], ["service"]];
  const xByKind = new Map<TopologyKind, number>();
  columns.forEach((kinds, index) => kinds.forEach((kind) => xByKind.set(kind, 24 + index * 210)));
  const counters = new Map<number, number>();
  return [...nodes].sort((a, b) => (xByKind.get(a.kind) ?? 0) - (xByKind.get(b.kind) ?? 0) || a.label.localeCompare(b.label)).map((node) => {
    const x = xByKind.get(node.kind) ?? 32;
    const row = counters.get(x) ?? 0;
    counters.set(x, row + 1);
    return { ...node, x, y: 28 + row * 108 };
  });
}

export function assignmentFor(plan: TopologyPlan | null, serviceKey: string): TopologyAssignment | undefined {
  return plan?.assignments.find((item) => item.service_key === serviceKey);
}

export function capacityLabel(cpu?: number, memoryMiB?: number) {
  if (cpu === undefined || memoryMiB === undefined) return "Unknown capacity";
  return `${cpu} CPU · ${memoryMiB} MiB`;
}

export function bootstrapProgress(checkpoint?: { next_step_index: number; last_completed_step?: string }, events = 0) {
  if (!checkpoint && events === 0) return { label: "Not reported", percent: null };
  if (!checkpoint) return { label: `${events} factual events`, percent: null };
  const bounded = Math.max(0, Math.min(4, checkpoint.next_step_index));
  return { label: checkpoint.last_completed_step || `Checkpoint ${bounded}`, percent: bounded * 25 };
}

export function graphID(kind: TopologyKind, id: string) {
  return `${kind}:${id}`;
}
