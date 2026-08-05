"use client";

import { useRef, useState } from "react";
import { applyNodeChanges, Background, ReactFlow, type Node, type NodeChange, type NodeProps, type NodeTypes, type ReactFlowInstance } from "@xyflow/react";
import type { ConsoleController } from "@/features/console/types";
import type { PlacementFacts, TopologyPlan } from "@/lib/contracts/registry";
import { assignmentFor, canvasDraftIssues, canvasDraftStatus, canvasPlacement, moveCanvasPlacement, type CanvasDraft, type CanvasDraftStatus, type CanvasPlacement } from "@/lib/presentation/infrastructure/model";

type SelectData = { onSelect: () => void };
type ServerData = SelectData & { agent: string; cpu: string; label: string; memory: string; runtimeID: string; status: "Ready" | "Offline" | "Unknown" };
type ApplicationData = SelectData & { assignment: "Assigned" | "Unplaced"; draftStatus: CanvasDraftStatus; issues: number; runtime: string; serviceKey: string };
type UnplacedData = SelectData & { count: number };
type ServerFlowNode = Node<ServerData, "server">;
type ApplicationFlowNode = Node<ApplicationData, "application">;
type UnplacedFlowNode = Node<UnplacedData, "unplaced">;
type CanvasNode = ServerFlowNode | ApplicationFlowNode | UnplacedFlowNode;

const nodeTypes = { server: ServerNode, application: ApplicationNode, unplaced: UnplacedGroup } satisfies NodeTypes;
const groupWidth = 292;
const appHeight = 98;

export function TopologyDesignCanvas({ console, draft, facts, onDraft, topology }: { console: ConsoleController; draft: CanvasDraft; facts: PlacementFacts; onDraft: (draft: CanvasDraft) => void; topology: TopologyPlan | null }) {
  const [reviewOpen, setReviewOpen] = useState(false);
  const selectedID = resolveSelection(console.route.topology, facts);
  const changes = facts.services.filter((service) => canvasDraftStatus(topology, draft, service.key) !== "unchanged").map((service) => {
    const before = assignmentFor(topology, service.key)?.runtime_id ?? null;
    const after = canvasPlacement(topology, draft, service.key).runtime_id;
    return { key: service.key, before, after };
  });
  const select = (id: string) => {
    console.navigate({ topology: id });
    window.requestAnimationFrame(() => document.getElementById("topology-inspector-heading")?.focus());
  };
  const placements = new Map(facts.services.map((service) => [service.key, canvasPlacement(topology, draft, service.key)]));
  const nodes = buildNodes(console, facts, topology, draft, placements, selectedID, select);
  const canvasKey = `${topology?.revision ?? 0}:${topology?.state_hash ?? "none"}:${selectedID}:${nodes.map((node) => `${node.id}:${node.parentId ?? "root"}:${node.type === "server" ? node.data.status : node.type === "application" ? node.data.draftStatus : node.data.count}`).join("|")}`;
  const selectedService = selectedID.startsWith("service:") ? facts.services.find((service) => service.key === selectedID.slice(8)) : undefined;
  const selectedRuntime = selectedID.startsWith("runtime:") ? facts.runtimes.find((runtime) => runtime.id === selectedID.slice(8)) : undefined;

  function move(serviceKey: string, runtimeID?: string) {
    const runtime = facts.runtimes.find((item) => item.id === runtimeID);
    onDraft(moveCanvasPlacement(topology, draft, serviceKey, runtime));
    select(`service:${serviceKey}`);
  }

  function reset() {
    onDraft({});
    setReviewOpen(false);
  }

  return <>
    <div className="draftToolbar" aria-live="polite">
      <strong>{changes.length} unpublished {changes.length === 1 ? "change" : "changes"}</strong>
      <span>Local only · not validated or deployable</span>
      <button aria-expanded={reviewOpen} disabled={!changes.length} onClick={() => setReviewOpen((open) => !open)} type="button">Review draft</button>
      <button disabled={!changes.length} onClick={reset} type="button">Reset changes</button>
    </div>
    {reviewOpen ? <section className="draftReview" aria-labelledby="draft-review-heading"><h3 id="draft-review-heading">Local semantic diff</h3><p>This review is not Cloud validation and does not make the draft deployable.</p><ul>{changes.map((change) => <li key={change.key}><code>{change.key}: {change.before ?? "unplaced"} → {change.after ?? "unplaced"}</code></li>)}</ul></section> : null}
    <div className="designWorkspace">
      <div className="topologyFlow" aria-label="Editable topology placement canvas">
        <CanvasFlow key={canvasKey} nodes={nodes} onMove={move} />
      </div>
      <TopologyInspector console={console} draft={draft} facts={facts} selectedRuntime={selectedRuntime} selectedService={selectedService} topology={topology} />
    </div>
  </>;
}

function CanvasFlow({ nodes: initialNodes, onMove }: { nodes: CanvasNode[]; onMove: (serviceKey: string, runtimeID?: string) => void }) {
  const [nodes, setNodes] = useState(initialNodes);
  const instance = useRef<ReactFlowInstance<CanvasNode>>(null);
  function changed(changes: NodeChange<CanvasNode>[]) { setNodes((current) => applyNodeChanges(changes, current)); }
  function dragStopped(event: MouseEvent | TouchEvent, node: CanvasNode) {
    if (node.type !== "application") return;
    const point = "changedTouches" in event ? event.changedTouches[0] : event;
    const targetID = [...document.querySelectorAll<HTMLElement>("[data-canvas-target]")].find((element) => { const box = element.getBoundingClientRect(); return point.clientX >= box.left && point.clientX <= box.right && point.clientY >= box.top && point.clientY <= box.bottom; })?.dataset.canvasTarget;
    const groups = instance.current?.getIntersectingNodes(node, true).filter((item) => item.type === "server" || item.type === "unplaced") ?? [];
    const target = targetID ? initialNodes.find((item) => item.id === targetID) : groups.find((item) => item.id !== node.parentId) ?? groups[0];
    setNodes(initialNodes);
    if (target?.type === "server") onMove(node.data.serviceKey, target.id.slice(8));
    else if (target?.type === "unplaced") onMove(node.data.serviceKey);
  }
  return <ReactFlow<CanvasNode> defaultEdgeOptions={{ selectable: false }} edges={[]} fitView fitViewOptions={{ padding: 0.08 }} maxZoom={1.25} minZoom={0.65} nodeTypes={nodeTypes} nodes={nodes} nodesConnectable={false} onInit={(flow) => { instance.current = flow; }} onNodeDragStop={dragStopped} onNodesChange={changed} panOnDrag={[1, 2]} selectionOnDrag>
    <Background color="#d9d5ca" gap={22} size={1} />
  </ReactFlow>;
}

function buildNodes(console: ConsoleController, facts: PlacementFacts, topology: TopologyPlan | null, draft: CanvasDraft, placements: Map<string, CanvasPlacement>, selectedID: string, select: (id: string) => void): CanvasNode[] {
  const groups: CanvasNode[] = [];
  const applications: CanvasNode[] = [];
  const groupServices = new Map<string, typeof facts.services>();
  const runtimeIDs = new Set(facts.runtimes.map((runtime) => runtime.id));
  groupServices.set("unplaced", facts.services.filter((service) => { const runtimeID = placements.get(service.key)?.runtime_id; return !runtimeID || !runtimeIDs.has(runtimeID); }));
  for (const runtime of facts.runtimes) groupServices.set(runtime.id, facts.services.filter((service) => placements.get(service.key)?.runtime_id === runtime.id));
  const maxItems = Math.max(1, ...groupServices.values().map((services) => services.length));
  const groupHeight = Math.max(260, 142 + maxItems * appHeight);
  groups.push({ id: "unplaced", type: "unplaced", position: { x: 24, y: 24 }, data: { count: groupServices.get("unplaced")?.length ?? 0, onSelect: () => select("unplaced") }, draggable: false, focusable: false, selected: selectedID === "unplaced", style: { width: groupWidth, height: groupHeight }, zIndex: 0 });
  facts.runtimes.forEach((runtime, index) => {
    const factualNodes = facts.nodes.filter((node) => node.runtime_id === runtime.id);
    const agents = facts.agents.filter((agent) => agent.runtime_id === runtime.id);
    const node = factualNodes[0];
    const agent = agents.find((item) => item.status === "active") ?? agents[0];
    const record = console.state.nodes.find((item) => item.id === node?.id);
    const status = serverStatus(factualNodes, agents);
    const id = `runtime:${runtime.id}`;
    groups.push({ id, type: "server", position: { x: 24 + (index + 1) * (groupWidth + 28), y: 24 }, data: { agent: agent?.status ?? "Not reported", cpu: node?.cpu_cores === undefined ? "Not reported" : `${node.cpu_cores} cores`, label: runtime.name || record?.public_host || runtime.id, memory: node?.memory_mb === undefined ? "Not reported" : `${node.memory_mb} MiB`, onSelect: () => select(id), runtimeID: runtime.id, status }, draggable: false, focusable: false, selected: selectedID === id, style: { width: groupWidth, height: groupHeight }, zIndex: 0 });
  });
  for (const [parent, services] of groupServices) services.forEach((service, index) => {
    const placement = placements.get(service.key) ?? { runtime_id: null };
    const runtime = facts.runtimes.find((item) => item.id === placement.runtime_id);
    const status = canvasDraftStatus(topology, draft, service.key);
    const id = `service:${service.key}`;
    applications.push({ id, type: "application", parentId: parent === "unplaced" ? "unplaced" : `runtime:${parent}`, position: { x: 20, y: 124 + index * appHeight }, data: { assignment: placement.runtime_id ? "Assigned" : "Unplaced", draftStatus: status, issues: canvasDraftIssues(placement).length, onSelect: () => select(id), runtime: runtime?.name || runtime?.id || (placement.runtime_id ? `${placement.runtime_id} not reported` : "No runtime"), serviceKey: service.key }, draggable: true, focusable: false, selected: selectedID === id, style: { width: groupWidth - 40, height: 78 }, zIndex: 1 });
  });
  return [...groups, ...applications];
}

function TopologyInspector({ console, draft, facts, selectedRuntime, selectedService, topology }: { console: ConsoleController; draft: CanvasDraft; facts: PlacementFacts; selectedRuntime?: PlacementFacts["runtimes"][number]; selectedService?: PlacementFacts["services"][number]; topology: TopologyPlan | null }) {
  if (selectedService) {
    const live = assignmentFor(topology, selectedService.key);
    const placement = canvasPlacement(topology, draft, selectedService.key);
    const runtime = facts.runtimes.find((item) => item.id === placement.runtime_id);
    const environment = facts.environments.find((item) => item.id === runtime?.environment_id);
    const issues = canvasDraftIssues(placement);
    return <aside className="canvasInspector" aria-labelledby="topology-inspector-heading"><p className="canvasPath">{environment?.name ?? (placement.runtime_id ? "Unknown environment" : "Unplaced")} / {runtime?.name ?? placement.runtime_id ?? "Unplaced"} / {selectedService.key}</p><h3 id="topology-inspector-heading" tabIndex={-1}>{selectedService.key}</h3><p><span className={`draftState ${canvasDraftStatus(topology, draft, selectedService.key).replace(" ", "-")}`}>{canvasDraftStatus(topology, draft, selectedService.key)}</span></p><dl><InspectorFact label="Live assignment" value={runtimeLabel(facts, live?.runtime_id)} /><InspectorFact label="Draft assignment" value={runtimeLabel(facts, placement.runtime_id)} /><InspectorFact label="Replicas" value={field(placement.replicas)} /><InspectorFact label="CPU" value={placement.cpu_request_millicores ? `${placement.cpu_request_millicores} millicores` : "Missing"} /><InspectorFact label="Memory" value={placement.memory_request_bytes ? `${Math.round(placement.memory_request_bytes / 1024 / 1024)} MiB` : "Missing"} /><InspectorFact label="Exposure" value={placement.exposure?.mode ?? "Missing"} /></dl><h4>Draft validation issues</h4>{issues.length ? <ul className="draftIssues">{issues.map((issue) => <li key={issue}>{issue}</li>)}</ul> : <p>No local completeness issues. This draft has not been Cloud validated.</p>}</aside>;
  }
  if (selectedRuntime) {
    const nodes = facts.nodes.filter((node) => node.runtime_id === selectedRuntime.id);
    const agents = facts.agents.filter((agent) => agent.runtime_id === selectedRuntime.id);
    const node = nodes[0];
    const agent = agents.find((item) => item.status === "active") ?? agents[0];
    const record = console.state.nodes.find((item) => item.id === node?.id);
    const environment = facts.environments.find((item) => item.id === selectedRuntime.environment_id);
    return <aside className="canvasInspector" aria-labelledby="topology-inspector-heading"><p className="canvasPath">{environment?.name ?? selectedRuntime.environment_id} / {selectedRuntime.name}</p><h3 id="topology-inspector-heading" tabIndex={-1}>{selectedRuntime.name || record?.public_host || selectedRuntime.id}</h3><dl><InspectorFact label="Status" value={serverStatus(nodes, agents)} /><InspectorFact label="Runtime" value={selectedRuntime.id} /><InspectorFact label="Node" value={node?.id ?? "Not reported"} /><InspectorFact label="CPU" value={node?.cpu_cores === undefined ? "Not reported" : `${node.cpu_cores} cores`} /><InspectorFact label="RAM" value={node?.memory_mb === undefined ? "Not reported" : `${node.memory_mb} MiB`} /><InspectorFact label="Agent" value={agent ? `${agent.id} · ${agent.status}` : "Not reported"} /></dl></aside>;
  }
  return <aside className="canvasInspector" aria-labelledby="topology-inspector-heading"><p className="canvasPath">Unplaced</p><h3 id="topology-inspector-heading" tabIndex={-1}>Unplaced applications</h3><p>{facts.services.filter((service) => { const runtimeID = canvasPlacement(topology, draft, service.key).runtime_id; return !runtimeID || !facts.runtimes.some((runtime) => runtime.id === runtimeID); }).length} applications have no reported draft target.</p></aside>;
}

function ServerNode({ data, selected }: NodeProps<ServerFlowNode>) {
  return <div aria-label={`Server ${data.label}, ${data.status}, Agent ${data.agent}`} aria-pressed={selected} className="canvasServerNode" data-canvas-target={`runtime:${data.runtimeID}`} data-status={data.status.toLowerCase()} onClick={data.onSelect} onKeyDown={(event) => selectKeyDown(event, data.onSelect)} onKeyUp={(event) => selectKeyUp(event, data.onSelect)} role="button" tabIndex={0}><span className="canvasNodeKind">Server</span><strong>{data.label}</strong><span className="canvasStatus">{data.status}</span><dl><InspectorFact label="CPU" value={data.cpu} /><InspectorFact label="RAM" value={data.memory} /><InspectorFact label="Agent" value={data.agent} /></dl></div>;
}

function ApplicationNode({ data, selected }: NodeProps<ApplicationFlowNode>) {
  return <div aria-label={`Application ${data.serviceKey}, ${data.assignment}, ${data.draftStatus}`} aria-pressed={selected} className="canvasApplicationNode" data-draft={data.draftStatus.replace(" ", "-")} onClick={data.onSelect} onKeyDown={(event) => selectKeyDown(event, data.onSelect)} onKeyUp={(event) => selectKeyUp(event, data.onSelect)} role="button" tabIndex={0}><span className="canvasNodeKind">Application</span><strong>{data.serviceKey}</strong><span>{data.assignment} · {data.runtime}</span><small>{data.draftStatus}{data.issues ? ` · ${data.issues} missing fields` : ""}</small></div>;
}

function UnplacedGroup({ data, selected }: NodeProps<UnplacedFlowNode>) {
  return <div aria-label={`Unplaced applications, ${data.count} applications`} aria-pressed={selected} className="canvasUnplacedGroup" data-canvas-target="unplaced" onClick={data.onSelect} onKeyDown={(event) => selectKeyDown(event, data.onSelect)} onKeyUp={(event) => selectKeyUp(event, data.onSelect)} role="button" tabIndex={0}><span className="canvasNodeKind">Unplaced</span><strong>Waiting for assignment</strong><small>{data.count} applications · drop here to remove placement</small></div>;
}

function InspectorFact({ label, value }: { label: string; value: string }) { return <div><dt>{label}</dt><dd>{value}</dd></div>; }
function field(value?: number) { return value ? String(value) : "Missing"; }
function runtimeLabel(facts: PlacementFacts, runtimeID?: string | null) { const runtime = facts.runtimes.find((item) => item.id === runtimeID); return runtime ? `${runtime.name} · ${runtime.id}` : runtimeID ? `${runtimeID} · not reported` : "Unplaced"; }
function serverStatus(nodes: PlacementFacts["nodes"], agents: PlacementFacts["agents"]): "Ready" | "Offline" | "Unknown" { if (nodes.some((node) => ["healthy", "ready", "active"].includes(node.status)) && agents.some((agent) => agent.status === "active")) return "Ready"; return nodes.length || agents.length ? "Offline" : "Unknown"; }
function selectKeyDown(event: React.KeyboardEvent, select: () => void) { if (event.key === "Enter") select(); if (event.key === " ") event.preventDefault(); }
function selectKeyUp(event: React.KeyboardEvent, select: () => void) { if (event.key === " ") select(); }
function resolveSelection(id: string | undefined, facts: PlacementFacts) { if (!id) return facts.services[0] ? `service:${facts.services[0].key}` : facts.runtimes[0] ? `runtime:${facts.runtimes[0].id}` : "unplaced"; if (id.startsWith("node:")) return `runtime:${facts.nodes.find((node) => node.id === id.slice(5))?.runtime_id ?? ""}`; if (id.startsWith("agent:")) return `runtime:${facts.agents.find((agent) => agent.id === id.slice(6))?.runtime_id ?? ""}`; if (id.startsWith("environment:")) return `runtime:${facts.runtimes.find((runtime) => runtime.environment_id === id.slice(12))?.id ?? ""}`; return id; }
