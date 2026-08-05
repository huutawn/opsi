import type { BootstrapSession, PlacementFacts, TopologyAssignment, TopologyPlan } from "../../contracts/registry.ts";

export type CanvasPlacement = {
  runtime_id: string | null;
  environment_id?: string;
  replicas?: number;
  cpu_request_millicores?: number;
  memory_request_bytes?: number;
  exposure?: TopologyAssignment["exposure"];
};
export type CanvasDraft = Record<string, CanvasPlacement>;
export type CanvasDraftStatus = "unchanged" | "moved" | "new placement" | "pending removal";
export type TopologyOnboardingState = {
  kind: "connect" | "bootstrap" | "retry" | "application" | "placement" | "inspect";
  title: string;
  description: string;
  action: "Connect server" | "Inspect progress" | "Retry bootstrap" | "Add application" | "Plan placement" | "Inspect topology";
  progress?: ReturnType<typeof bootstrapProgress>;
  sessionID?: string;
};
export type ServerLifecycle = {
  status: "Connecting" | "Bootstrapping" | "Ready" | "Offline" | "Failed" | "Unknown";
  runtime?: PlacementFacts["runtimes"][number];
  node?: PlacementFacts["nodes"][number];
  agent?: PlacementFacts["agents"][number];
  session?: BootstrapSession;
};

const activeBootstrapStatuses = new Set(["created", "pending", "retry_wait", "preflight", "validating", "connecting", "installing", "installing_k3s", "installing_agent", "registering_agent", "waiting_agent", "verifying_agent", "verifying"]);
const connectingBootstrapStatuses = new Set(["created", "pending", "retry_wait", "connecting"]);
const usableNodeStatuses = new Set(["healthy", "ready", "active"]);
export const bootstrapPollInterval = 4_000;

export function assignmentFor(plan: TopologyPlan | null, serviceKey: string): TopologyAssignment | undefined {
  return plan?.assignments.find((item) => item.service_key === serviceKey);
}

export function canvasPlacement(plan: TopologyPlan | null, draft: CanvasDraft, serviceKey: string): CanvasPlacement {
  if (Object.hasOwn(draft, serviceKey)) return draft[serviceKey];
  const assignment = assignmentFor(plan, serviceKey);
  return assignment ? { ...assignment } : { runtime_id: null };
}

export function moveCanvasPlacement(plan: TopologyPlan | null, draft: CanvasDraft, serviceKey: string, runtime?: PlacementFacts["runtimes"][number]): CanvasDraft {
  const current = canvasPlacement(plan, draft, serviceKey);
  const next = runtime ? { ...current, runtime_id: runtime.id, environment_id: runtime.environment_id } : { ...current, runtime_id: null };
  const appliedRuntime = assignmentFor(plan, serviceKey)?.runtime_id ?? null;
  const updated = { ...draft };
  if (next.runtime_id === appliedRuntime) delete updated[serviceKey];
  else updated[serviceKey] = next;
  return updated;
}

export function canvasDraftStatus(plan: TopologyPlan | null, draft: CanvasDraft, serviceKey: string): CanvasDraftStatus {
  const applied = assignmentFor(plan, serviceKey)?.runtime_id ?? null;
  const target = canvasPlacement(plan, draft, serviceKey).runtime_id;
  if (applied === target) return "unchanged";
  if (!applied) return "new placement";
  if (!target) return "pending removal";
  return "moved";
}

export function canvasDraftIssues(placement: CanvasPlacement): string[] {
  if (!placement.runtime_id) return [];
  const issues: string[] = [];
  if (!placement.replicas) issues.push("Replicas are missing.");
  if (!placement.cpu_request_millicores) issues.push("CPU request is missing.");
  if (!placement.memory_request_bytes) issues.push("Memory request is missing.");
  if (!placement.exposure) issues.push("Exposure is missing.");
  return issues;
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

export function latestActiveBootstrap(sessions: BootstrapSession[]) {
  return latestBootstrap(sessions.filter((session) => activeBootstrapStatuses.has(session.status)));
}

export function terminalBootstrap(session?: BootstrapSession) {
  return !session || !activeBootstrapStatuses.has(session.status);
}

export function serverLifecycle(facts: PlacementFacts, sessions: BootstrapSession[]): ServerLifecycle {
  const active = latestActiveBootstrap(sessions);
  const latest = latestBootstrap(sessions);
  for (const runtime of facts.runtimes) {
    const nodes = facts.nodes.filter((node) => node.runtime_id === runtime.id && usableNodeStatuses.has(node.status));
    const agent = facts.agents.find((item) => item.runtime_id === runtime.id && item.status === "active" && nodes.some((node) => node.id === item.node_id));
    if (agent) return { status: "Ready", runtime, node: nodes.find((node) => node.id === agent.node_id), agent, session: active ? undefined : latest };
  }
  if (active) return { status: connectingBootstrapStatuses.has(active.status) ? "Connecting" : "Bootstrapping", session: active };
  const runtime = facts.runtimes[0];
  const node = facts.nodes.find((item) => item.runtime_id === runtime?.id) ?? facts.nodes[0];
  const agent = facts.agents.find((item) => item.runtime_id === runtime?.id && (!node || item.node_id === node.id)) ?? facts.agents[0];
  if (latest && ["failed", "dead_letter"].includes(latest.status)) return { status: "Failed", runtime, node, agent, session: latest };
  if (runtime || node || agent) return { status: "Offline", runtime, node, agent, session: latest };
  return { status: "Unknown", session: latest };
}

export function topologyOnboarding(facts: PlacementFacts, plan: TopologyPlan | null, sessions: BootstrapSession[]): TopologyOnboardingState {
  const lifecycle = serverLifecycle(facts, sessions);
  if (lifecycle.status === "Connecting" || lifecycle.status === "Bootstrapping") return { kind: "bootstrap", title: "Server connection in progress", description: `Bootstrap ${lifecycle.session?.id} is ${lifecycle.session?.status}.`, action: "Inspect progress", progress: bootstrapProgress(lifecycle.session?.checkpoint), sessionID: lifecycle.session?.id };
  if (lifecycle.status === "Failed") return { kind: "retry", title: "Server bootstrap failed", description: lifecycle.session?.last_failure_message_redacted || lifecycle.session?.last_failure_code || "The latest bootstrap session failed.", action: "Retry bootstrap", sessionID: lifecycle.session?.id };
  if (lifecycle.status === "Unknown" && !lifecycle.session) return { kind: "connect", title: "Connect the first server", description: "No server facts are reported for this project.", action: "Connect server" };
  if (lifecycle.status !== "Ready") return { kind: "inspect", title: `Server status is ${lifecycle.status.toLowerCase()}`, description: "Runtime, node, and active Agent facts do not currently establish a ready server.", action: "Inspect topology" };
  if (facts.services.length === 0) return { kind: "application", title: "Add the first application", description: "A server is ready, but the service catalog is empty.", action: "Add application" };
  const unassigned = facts.services.filter((service) => !plan?.assignments.some((assignment) => assignment.service_key === service.key));
  if (unassigned.length) return { kind: "placement", title: "Place unassigned services", description: `${unassigned.map((service) => service.key).join(", ")} ${unassigned.length === 1 ? "needs" : "need"} a runtime assignment.`, action: "Plan placement" };
  return { kind: "inspect", title: "Topology is ready to inspect", description: `TopologyPlan r${plan?.revision ?? 0} assigns every reported service.`, action: "Inspect topology" };
}

function latestBootstrap(sessions: BootstrapSession[]) {
  return [...sessions].sort((a, b) => b.created_at.localeCompare(a.created_at))[0];
}
