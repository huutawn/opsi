import type { BootstrapSession, PlacementFacts, TopologyAssignment, TopologyDraft, TopologyPlan } from "../../contracts/registry.ts";

export type CanvasPlacement = {
  runtime_id: string | null;
  environment_id?: string;
  replicas?: number;
  cpu_request_millicores?: number;
  memory_request_bytes?: number;
  exposure?: TopologyAssignment["exposure"];
  rationale?: TopologyAssignment["rationale"];
};
export type CanvasDraft = Record<string, CanvasPlacement>;
export type CanvasDraftStatus = "unchanged" | "edited" | "moved" | "new placement" | "pending removal";
export type TopologyResourceKind = "server" | "application" | "managed-service" | "external-resource" | "unsupported";
export type TopologyResourcePresentation = {
  kind: TopologyResourceKind;
  sourceKind: string;
  supported: boolean;
  kindLabel: string;
  name: string;
  status: string;
  badge: string;
  tone: "ready" | "warning" | "failed" | "neutral";
  state: "factual" | "draft" | "unsupported";
  context: string;
  ariaLabel: string;
  notice?: string;
  draftState?: CanvasDraftStatus;
  facts: Array<{ label: string; value: string }>;
  capabilities: { acceptsPlacement: boolean; connectable: boolean; movable: boolean };
};
export type TopologyResourcePresentationInput = {
  kind: string;
  name: string;
  status: string;
  context: string;
  ariaDetail?: string;
  notice?: string;
  badge?: string;
  tone?: TopologyResourcePresentation["tone"];
  draftState?: CanvasDraftStatus;
  facts?: TopologyResourcePresentation["facts"];
};
export type TopologyOnboardingState = {
  kind: "connect" | "bootstrap" | "retry" | "application" | "placement" | "inspect";
  title: string;
  description: string;
  action: "Connect server" | "Inspect progress" | "Retry bootstrap" | "Add application" | "Plan placement" | "Inspect topology";
  progress?: ReturnType<typeof bootstrapProgress>;
  sessionID?: string;
};
export type ServerLifecycle = {
  status: "Waiting" | "Connecting" | "Bootstrapping" | "Ready" | "Offline" | "Failed" | "Unknown";
  runtime?: PlacementFacts["runtimes"][number];
  node?: PlacementFacts["nodes"][number];
  agent?: PlacementFacts["agents"][number];
  session?: BootstrapSession;
};

const activeBootstrapStatuses = new Set(["created", "pending", "waiting", "retry_wait", "preflight", "validating", "connecting", "installing", "installing_k3s", "installing_agent", "registering_agent", "waiting_agent", "verifying_agent", "verifying"]);
const connectingBootstrapStatuses = new Set(["created", "pending", "retry_wait", "validating", "connecting"]);
const usableNodeStatuses = new Set(["healthy", "ready", "active"]);
const defaultCPURequestMillicores = 100;
const defaultMemoryRequestBytes = 128 * 1024 * 1024;
export const bootstrapPollInterval = 4_000;

const topologyResourceKinds = {
  server: { kindLabel: "Server", supported: true, capabilities: { acceptsPlacement: true, connectable: false, movable: false } },
  application: { kindLabel: "Application", supported: true, capabilities: { acceptsPlacement: false, connectable: true, movable: true } },
  "managed-service": { kindLabel: "Managed service", supported: false, capabilities: { acceptsPlacement: false, connectable: false, movable: false } },
  "external-resource": { kindLabel: "External resource", supported: false, capabilities: { acceptsPlacement: false, connectable: false, movable: false } },
} as const;

export function topologyResourcePresentation(input: TopologyResourcePresentationInput): TopologyResourcePresentation {
  const definition = topologyResourceKinds[input.kind as keyof typeof topologyResourceKinds];
  if (!definition?.supported) {
    return {
      kind: definition ? input.kind as TopologyResourceKind : "unsupported",
      sourceKind: input.kind,
      supported: false,
      kindLabel: definition?.kindLabel ?? "Unsupported resource",
      name: input.name,
      status: "Unsupported",
      badge: "Unsupported",
      tone: "neutral",
      state: "unsupported",
      context: `No factual ${input.kind} presentation is backed by the topology domain.`,
      ariaLabel: `Unsupported resource ${input.name}, kind ${input.kind}`,
      facts: [],
      capabilities: definition?.capabilities ?? { acceptsPlacement: false, connectable: false, movable: false },
    };
  }
  return {
    kind: input.kind as "server" | "application",
    sourceKind: input.kind,
    supported: true,
    kindLabel: definition.kindLabel,
    name: input.name,
    status: input.status,
    badge: input.badge ?? input.status,
    tone: input.tone ?? "neutral",
    state: input.draftState && input.draftState !== "unchanged" ? "draft" : "factual",
    context: input.context,
    ariaLabel: `${definition.kindLabel} ${input.name}, ${input.status}${input.ariaDetail ? `, ${input.ariaDetail}` : ""}`,
    notice: input.notice,
    draftState: input.draftState,
    facts: input.facts ?? [],
    capabilities: definition.capabilities,
  };
}

export function assignmentFor(plan: TopologyPlan | null, serviceKey: string): TopologyAssignment | undefined {
  return plan?.assignments.find((item) => item.service_key === serviceKey);
}

export function deploymentAssignmentFor(plan: TopologyPlan | null, serviceKey: string, environmentID: string): TopologyAssignment | undefined {
  return plan?.assignments.find((item) => item.service_key === serviceKey && item.environment_id === environmentID);
}

export function currentEnvironment(facts: PlacementFacts | null | undefined, environmentID: string) {
  if (!facts?.environments.length) return undefined;
  if (environmentID) return facts.environments.find((item) => item.id === environmentID);
  return facts.environments.length === 1 ? facts.environments[0] : undefined;
}

export function canvasPlacement(plan: TopologyPlan | null, draft: CanvasDraft, serviceKey: string): CanvasPlacement {
  if (Object.hasOwn(draft, serviceKey)) return draft[serviceKey];
  const assignment = assignmentFor(plan, serviceKey);
  return assignment ? { ...assignment } : { runtime_id: null };
}

export function moveCanvasPlacement(plan: TopologyPlan | null, draft: CanvasDraft, serviceKey: string, runtime?: PlacementFacts["runtimes"][number]): CanvasDraft {
  const current = canvasPlacement(plan, draft, serviceKey);
  return updateCanvasPlacement(plan, draft, serviceKey, runtime ? {
    runtime_id: runtime.id,
    environment_id: runtime.environment_id,
    replicas: current.replicas ?? 1,
    cpu_request_millicores: current.cpu_request_millicores ?? defaultCPURequestMillicores,
    memory_request_bytes: current.memory_request_bytes ?? defaultMemoryRequestBytes,
    exposure: current.exposure ?? { mode: "none" },
  } : { runtime_id: null });
}

export function updateCanvasPlacement(plan: TopologyPlan | null, draft: CanvasDraft, serviceKey: string, patch: Partial<CanvasPlacement>): CanvasDraft {
  const next = { ...canvasPlacement(plan, draft, serviceKey), ...patch };
  const updated = { ...draft };
  if (placementMatches(next, assignmentFor(plan, serviceKey))) delete updated[serviceKey];
  else updated[serviceKey] = next;
  return updated;
}

export function compileCanvasDraft(projectID: string, plan: TopologyPlan | null, draft: CanvasDraft): TopologyDraft {
  const assignments = new Map((plan?.assignments ?? []).map((assignment) => [assignment.service_key, assignment]));
  for (const [serviceKey, placement] of Object.entries(draft)) {
    if (!placement.runtime_id) {
      assignments.delete(serviceKey);
      continue;
    }
    const rationale = placement.rationale?.summary ? { rationale: { summary: placement.rationale.summary } } : {};
    assignments.set(serviceKey, {
      service_key: serviceKey,
      environment_id: placement.environment_id ?? "",
      runtime_id: placement.runtime_id,
      replicas: finiteInteger(placement.replicas),
      cpu_request_millicores: finiteInteger(placement.cpu_request_millicores),
      memory_request_bytes: finiteInteger(placement.memory_request_bytes),
      exposure: { mode: placement.exposure?.mode ?? "none" },
      ...rationale,
    });
  }
  return { schema_version: "opsi.topology_plan/v1", project_id: projectID, assignments: [...assignments.values()].sort((a, b) => a.service_key < b.service_key ? -1 : a.service_key > b.service_key ? 1 : 0) };
}

export function canvasDraftStatus(plan: TopologyPlan | null, draft: CanvasDraft, serviceKey: string): CanvasDraftStatus {
  const applied = assignmentFor(plan, serviceKey)?.runtime_id ?? null;
  const target = canvasPlacement(plan, draft, serviceKey).runtime_id;
  if (Object.hasOwn(draft, serviceKey) && applied === target) return "edited";
  if (applied === target) return "unchanged";
  if (!applied) return "new placement";
  if (!target) return "pending removal";
  return "moved";
}

export function canvasDraftIssues(placement: CanvasPlacement): string[] {
  if (!placement.runtime_id) return [];
  const issues: string[] = [];
  if (!validInteger(placement.replicas, 100)) issues.push("Replicas must be between 1 and 100.");
  if (!validInteger(placement.cpu_request_millicores, 1_000_000)) issues.push("CPU request must be between 1 and 1,000,000 millicores.");
  if (!validInteger(placement.memory_request_bytes, 2 ** 50)) issues.push("Memory request must be between 1 byte and 1 PiB.");
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
    const match = readyServer(facts.nodes.filter((node) => node.runtime_id === runtime.id), facts.agents.filter((agent) => agent.runtime_id === runtime.id));
    if (match) return { status: "Ready", runtime, ...match, session: active ? undefined : latest };
  }
  if (active) return { status: active.status === "waiting" ? "Waiting" : connectingBootstrapStatuses.has(active.status) ? "Connecting" : "Bootstrapping", session: active };
  const runtime = facts.runtimes[0];
  const node = facts.nodes.find((item) => item.runtime_id === runtime?.id) ?? facts.nodes[0];
  const agent = facts.agents.find((item) => item.runtime_id === runtime?.id && (!node || item.node_id === node.id)) ?? facts.agents[0];
  if (latest && ["failed", "dead_letter"].includes(latest.status)) return { status: "Failed", runtime, node, agent, session: latest };
  if (runtime || node || agent) return { status: "Offline", runtime, node, agent, session: latest };
  return { status: "Unknown", session: latest };
}

export function serverStatus(nodes: PlacementFacts["nodes"], agents: PlacementFacts["agents"]): "Ready" | "Offline" | "Unknown" {
  if (readyServer(nodes, agents)) return "Ready";
  return nodes.length || agents.length ? "Offline" : "Unknown";
}

export function topologyOnboarding(facts: PlacementFacts, plan: TopologyPlan | null, sessions: BootstrapSession[]): TopologyOnboardingState {
  const lifecycle = serverLifecycle(facts, sessions);
  if (lifecycle.status === "Waiting" || lifecycle.status === "Connecting" || lifecycle.status === "Bootstrapping") return { kind: "bootstrap", title: lifecycle.status === "Waiting" ? "Waiting for connection" : "Server connection in progress", description: `Bootstrap ${lifecycle.session?.id} is ${lifecycle.session?.status}.`, action: "Inspect progress", progress: bootstrapProgress(lifecycle.session?.checkpoint), sessionID: lifecycle.session?.id };
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

function readyServer(nodes: PlacementFacts["nodes"], agents: PlacementFacts["agents"]) {
  const agent = agents.find((item) => item.status === "active" && nodes.some((node) => usableNodeStatuses.has(node.status) && node.id === item.node_id && node.runtime_id === item.runtime_id));
  const node = agent && nodes.find((item) => item.id === agent.node_id);
  return agent && node ? { agent, node } : undefined;
}

function placementMatches(placement: CanvasPlacement, assignment?: TopologyAssignment) {
  if (!assignment) return !placement.runtime_id;
  return placement.runtime_id === assignment.runtime_id
    && placement.environment_id === assignment.environment_id
    && placement.replicas === assignment.replicas
    && placement.cpu_request_millicores === assignment.cpu_request_millicores
    && placement.memory_request_bytes === assignment.memory_request_bytes
    && placement.exposure?.mode === assignment.exposure.mode
    && (placement.rationale?.summary ?? "") === (assignment.rationale?.summary ?? "");
}

function finiteInteger(value?: number) {
  return Number.isSafeInteger(value) ? value as number : 0;
}

function validInteger(value: number | undefined, max: number) {
  return Number.isSafeInteger(value) && (value as number) >= 1 && (value as number) <= max;
}
