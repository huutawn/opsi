"use client";

import { useMemo, useRef, useState } from "react";
import { applyNodeChanges, Background, ReactFlow, type Node, type NodeChange, type NodeProps, type NodeTypes, type ReactFlowInstance } from "@xyflow/react";
import type { ConsoleController } from "@/features/console/types";
import { LocalAPIError, LocalClient } from "@/lib/api/local-client";
import type { PlacementFacts, TopologyDiff, TopologyPlan, TopologyPreview, TopologyValidation } from "@/lib/contracts/registry";
import { assignmentFor, canvasDraftIssues, canvasDraftStatus, canvasPlacement, compileCanvasDraft, moveCanvasPlacement, serverStatus, updateCanvasPlacement, type CanvasDraft, type CanvasDraftStatus, type CanvasPlacement } from "@/lib/presentation/infrastructure/model";

type SelectData = { onSelect: () => void };
type ServerData = SelectData & { agent: string; cpu: string; label: string; memory: string; runtimeID: string; status: "Ready" | "Offline" | "Unknown" };
type ApplicationData = SelectData & { assignment: "Assigned" | "Unplaced"; draftStatus: CanvasDraftStatus; issues: number; runtime: string; serviceKey: string };
type UnplacedData = SelectData & { count: number };
type ServerFlowNode = Node<ServerData, "server">;
type ApplicationFlowNode = Node<ApplicationData, "application">;
type UnplacedFlowNode = Node<UnplacedData, "unplaced">;
type CanvasNode = ServerFlowNode | ApplicationFlowNode | UnplacedFlowNode;
type DraftReview = { preview: TopologyPreview; validation: TopologyValidation; diff: TopologyDiff; idempotencyKey: string };

const nodeTypes = { server: ServerNode, application: ApplicationNode, unplaced: UnplacedGroup } satisfies NodeTypes;
const groupWidth = 292;
const appHeight = 98;

export function TopologyDesignCanvas({ console, draft, facts, onDraft, onReload, topology }: { console: ConsoleController; draft: CanvasDraft; facts: PlacementFacts; onDraft: (draft: CanvasDraft) => void; onReload: () => Promise<void>; topology: TopologyPlan | null }) {
  const client = useMemo(() => new LocalClient(), []);
  const [review, setReview] = useState<DraftReview | null>(null);
  const [busy, setBusy] = useState<"" | "review" | "apply">("");
  const [message, setMessage] = useState("");
  const projectID = console.state.project?.id ?? "";
  const selectedID = resolveSelection(console.route.topology, facts);
  const changeCount = Object.keys(draft).length;
  const select = (id: string) => {
    console.navigate({ topology: id });
    window.requestAnimationFrame(() => document.getElementById("topology-inspector-heading")?.focus());
  };
  const placements = new Map(facts.services.map((service) => [service.key, canvasPlacement(topology, draft, service.key)]));
  const nodes = buildNodes(console, facts, topology, draft, placements, selectedID, select);
  const canvasKey = `${topology?.revision ?? 0}:${topology?.state_hash ?? "none"}:${nodes.map((node) => `${node.id}:${node.parentId ?? "root"}:${node.type === "server" ? node.data.status : node.type === "application" ? `${node.data.draftStatus}:${node.data.issues}` : node.data.count}`).join("|")}`;
  const selectedService = selectedID.startsWith("service:") ? facts.services.find((service) => service.key === selectedID.slice(8)) : undefined;
  const selectedRuntime = selectedID.startsWith("runtime:") ? facts.runtimes.find((runtime) => runtime.id === selectedID.slice(8)) : undefined;

  function changeDraft(next: CanvasDraft) {
    onDraft(next);
    setReview(null);
    setMessage("");
  }

  function move(serviceKey: string, runtimeID?: string) {
    const runtime = facts.runtimes.find((item) => item.id === runtimeID);
    changeDraft(moveCanvasPlacement(topology, draft, serviceKey, runtime));
    select(`service:${serviceKey}`);
  }

  function reset() {
    changeDraft({});
  }

  async function reviewDraft() {
    if (!projectID || !changeCount) return;
    setBusy("review");
    setMessage("");
    try {
      const preview = await client.topologyPlan(projectID, compileCanvasDraft(projectID, topology, draft));
      const [validation, diff] = await Promise.all([client.topologyValidate(projectID, preview.draft), client.topologyDiff(projectID, preview.draft)]);
      setReview({ preview, validation, diff, idempotencyKey: crypto.randomUUID() });
    } catch (error) {
      setReview(null);
      setMessage((error as Error).message);
    } finally {
      setBusy("");
    }
  }

  async function applyTopology() {
    if (!review?.validation.valid || !projectID) return;
    const reviewed = review;
    setBusy("apply");
    setMessage("");
    try {
      const result = await client.topologyApply(projectID, { draft: reviewed.preview.draft, expected_revision: reviewed.diff.current_revision, expected_state_hash: reviewed.diff.current_hash ?? reviewed.preview.state_hash }, reviewed.idempotencyKey);
      await onReload();
      if (result.plan.plan_hash === reviewed.preview.plan_hash && result.plan.plan_hash === reviewed.diff.proposed_hash) {
        onDraft({});
        setReview(null);
        setMessage(`TopologyPlan r${result.plan.revision} applied${result.reused ? " from the idempotent replay" : ""}.`);
      } else {
        setReview(null);
        setMessage("Cloud returned a different plan hash; local changes were preserved for review.");
      }
    } catch (error) {
      if (error instanceof LocalAPIError && error.status === 409 && error.code === "TOPOLOGY_STATE_CONFLICT") {
        await onReload();
        setReview(null);
        setMessage("Topology changed in Cloud. Facts were refreshed and local changes were preserved; review the draft again.");
      } else {
        setMessage((error as Error).message);
      }
    } finally {
      setBusy("");
    }
  }

  return <>
    <div className="draftToolbar" aria-live="polite">
      <strong>{changeCount} unpublished {changeCount === 1 ? "change" : "changes"}</strong>
      <span>{review ? review.validation.valid ? "Cloud validated" : "Cloud validation failed" : "Local draft"}</span>
      <button aria-expanded={Boolean(review)} disabled={!changeCount || Boolean(busy)} onClick={() => void reviewDraft()} type="button">{busy === "review" ? "Reviewing…" : "Review draft"}</button>
      <button disabled={!changeCount || Boolean(busy)} onClick={reset} type="button">Reset changes</button>
    </div>
    {review ? <DraftReviewPanel busy={busy === "apply"} onApply={() => void applyTopology()} review={review} /> : null}
    {message ? <p className={message.includes("applied") ? "notice" : "placementError"} role="status">{message}</p> : null}
    <div className="designWorkspace">
      <div className="topologyFlow" aria-label="Editable topology placement canvas">
        <CanvasFlow key={canvasKey} nodes={nodes} onMove={move} />
      </div>
      <TopologyInspector console={console} draft={draft} facts={facts} onDraft={changeDraft} selectedRuntime={selectedRuntime} selectedService={selectedService} topology={topology} />
    </div>
  </>;
}

function CanvasFlow({ nodes: initialNodes, onMove }: { nodes: CanvasNode[]; onMove: (serviceKey: string, runtimeID?: string) => void }) {
  const [nodes, setNodes] = useState(initialNodes);
  const instance = useRef<ReactFlowInstance<CanvasNode>>(null);
  const selected = new Map(initialNodes.map((node) => [node.id, node.selected]));
  const renderedNodes = nodes.map((node) => ({ ...node, selected: selected.get(node.id) } as CanvasNode));
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
  return <ReactFlow<CanvasNode> defaultEdgeOptions={{ selectable: false }} edges={[]} fitView fitViewOptions={{ padding: 0.08 }} maxZoom={1.25} minZoom={0.65} nodeTypes={nodeTypes} nodes={renderedNodes} nodesConnectable={false} onInit={(flow) => { instance.current = flow; }} onNodeDragStop={dragStopped} onNodesChange={changed} panOnDrag={[1, 2]} selectionOnDrag>
    <Background color="#d9d5ca" gap={22} size={1} />
  </ReactFlow>;
}

function DraftReviewPanel({ busy, onApply, review }: { busy: boolean; onApply: () => void; review: DraftReview }) {
  return <section className="draftReview" aria-labelledby="draft-review-heading">
    <h3 id="draft-review-heading">Cloud topology review</h3>
    <p>{review.validation.valid ? "Cloud validation passed. The reviewed canonical draft is eligible to apply." : "Cloud validation failed. Apply remains disabled until the draft is reviewed as valid."}</p>
    <div className="hashPair"><div><span>Current revision</span><strong>{review.diff.current_revision}</strong><code>{review.diff.current_hash || review.preview.state_hash || "No current state hash"}</code></div><div><span>Proposed hash</span><code>{review.diff.proposed_hash}</code></div></div>
    <h4>Cloud semantic diff</h4>
    {review.diff.changes.length ? <ul>{review.diff.changes.map((change) => <li key={change.service_key}><strong>{change.service_key}</strong> · {change.change}<br /><code>{assignmentSummary(change.before)} → {assignmentSummary(change.after)}</code></li>)}</ul> : <p>No semantic changes.</p>}
    <h4>Validation issues</h4>
    {review.validation.issues.length ? <ul className="draftIssues">{review.validation.issues.map((issue, index) => <li key={`${issue.code}:${index}`}><strong>{issue.code}</strong> · {issueScope(issue)}{issue.message}</li>)}</ul> : <p>No service-level validation issues.</p>}
    <div className="reviewGrid">{review.validation.runtimes.map((runtime) => <div key={runtime.runtime_id}><b>{runtime.runtime_id} · {runtime.eligible ? "eligible" : "ineligible"}</b><p>Requested {runtime.capacity.requested_cpu_millicores}m / {mib(runtime.capacity.requested_memory_bytes)} MiB<br />Available {capacityCPU(runtime.capacity.available_cpu_millicores, runtime.capacity.unknown_capacity)} / {capacityMemory(runtime.capacity.available_memory_bytes, runtime.capacity.unknown_capacity)}</p>{runtime.issues.length ? <ul className="draftIssues">{runtime.issues.map((issue, index) => <li key={`${issue.code}:${index}`}>{issue.code}: {issue.message}</li>)}</ul> : <small>No runtime issues.</small>}</div>)}</div>
    <div className="dialogActions"><span>{review.validation.valid ? "Validated by Cloud" : "Resolve validation issues and review again"}</span><button disabled={!review.validation.valid || busy} onClick={onApply} type="button">{busy ? "Applying…" : "Apply topology"}</button></div>
  </section>;
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

function TopologyInspector({ console, draft, facts, onDraft, selectedRuntime, selectedService, topology }: { console: ConsoleController; draft: CanvasDraft; facts: PlacementFacts; onDraft: (draft: CanvasDraft) => void; selectedRuntime?: PlacementFacts["runtimes"][number]; selectedService?: PlacementFacts["services"][number]; topology: TopologyPlan | null }) {
  if (selectedService) {
    const live = assignmentFor(topology, selectedService.key);
    const placement = canvasPlacement(topology, draft, selectedService.key);
    const runtime = facts.runtimes.find((item) => item.id === placement.runtime_id);
    const environment = facts.environments.find((item) => item.id === runtime?.environment_id);
    const issues = canvasDraftIssues(placement);
    const status = canvasDraftStatus(topology, draft, selectedService.key);
    const edit = (patch: Partial<CanvasPlacement>) => onDraft(updateCanvasPlacement(topology, draft, selectedService.key, patch));
    return <aside className="canvasInspector" aria-labelledby="topology-inspector-heading"><p className="canvasPath">{environment?.name ?? (placement.runtime_id ? "Unknown environment" : "Unplaced")} / {runtime?.name ?? placement.runtime_id ?? "Unplaced"} / {selectedService.key}</p><h3 id="topology-inspector-heading" tabIndex={-1}>{selectedService.key}</h3><p><span className={`draftState ${status.replace(" ", "-")}`}>{status}</span></p><dl><InspectorFact label="Live assignment" value={runtimeLabel(facts, live?.runtime_id)} /><InspectorFact label="Draft assignment" value={runtimeLabel(facts, placement.runtime_id)} /></dl><form className="form" onSubmit={(event) => event.preventDefault()}><label>Replicas<input className="field" disabled={!placement.runtime_id} max="100" min="1" onChange={(event) => edit({ replicas: numberValue(event) })} required step="1" type="number" value={placement.replicas ?? ""} /></label><label>CPU request (millicores)<input className="field" disabled={!placement.runtime_id} max="1000000" min="1" onChange={(event) => edit({ cpu_request_millicores: numberValue(event) })} required step="1" type="number" value={placement.cpu_request_millicores ?? ""} /></label><label>Memory (MiB)<input className="field" disabled={!placement.runtime_id} max="1073741824" min="1" onChange={(event) => edit({ memory_request_bytes: mibValue(event) })} required step="1" type="number" value={placement.memory_request_bytes === undefined ? "" : Math.round(placement.memory_request_bytes / 1024 / 1024)} /></label><label>Exposure<select className="select" disabled={!placement.runtime_id} onChange={(event) => edit({ exposure: { mode: event.target.value as "none" | "internal" | "public" } })} value={placement.exposure?.mode ?? "none"}><option value="none">None</option><option value="internal">Internal</option><option value="public">Public</option></select></label></form><h4>Draft validation issues</h4>{issues.length ? <ul className="draftIssues">{issues.map((issue) => <li key={issue}>{issue}</li>)}</ul> : <p>{placement.runtime_id ? "No local boundary issues. Review with Cloud before applying." : "Place this service on a runtime before editing resources."}</p>}</aside>;
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
function assignmentSummary(assignment?: TopologyDiff["changes"][number]["before"]) { return assignment ? `${assignment.runtime_id}, ${assignment.replicas} replicas, ${assignment.cpu_request_millicores}m, ${mib(assignment.memory_request_bytes)} MiB, ${assignment.exposure.mode}` : "unplaced"; }
function issueScope(issue: TopologyValidation["issues"][number]) { return [issue.service_key, issue.runtime_id].filter(Boolean).join(" / ") ? `${[issue.service_key, issue.runtime_id].filter(Boolean).join(" / ")}: ` : ""; }
function mib(bytes: number) { return Math.round(bytes / 1024 / 1024); }
function capacityCPU(value: number | undefined, unknown: boolean) { return unknown || value === undefined ? "Unknown CPU" : `${value}m CPU`; }
function capacityMemory(value: number | undefined, unknown: boolean) { return unknown || value === undefined ? "Unknown memory" : `${mib(value)} MiB memory`; }
function runtimeLabel(facts: PlacementFacts, runtimeID?: string | null) { const runtime = facts.runtimes.find((item) => item.id === runtimeID); return runtime ? `${runtime.name} · ${runtime.id}` : runtimeID ? `${runtimeID} · not reported` : "Unplaced"; }
function selectKeyDown(event: React.KeyboardEvent, select: () => void) { if (event.key === "Enter") select(); if (event.key === " ") event.preventDefault(); }
function selectKeyUp(event: React.KeyboardEvent, select: () => void) { if (event.key === " ") select(); }
function resolveSelection(id: string | undefined, facts: PlacementFacts) { if (!id) return facts.services[0] ? `service:${facts.services[0].key}` : facts.runtimes[0] ? `runtime:${facts.runtimes[0].id}` : "unplaced"; if (id.startsWith("node:")) return `runtime:${facts.nodes.find((node) => node.id === id.slice(5))?.runtime_id ?? ""}`; if (id.startsWith("agent:")) return `runtime:${facts.agents.find((agent) => agent.id === id.slice(6))?.runtime_id ?? ""}`; if (id.startsWith("environment:")) return `runtime:${facts.runtimes.find((runtime) => runtime.environment_id === id.slice(12))?.id ?? ""}`; return id; }
function numberValue(event: React.ChangeEvent<HTMLInputElement>) { return Number.isFinite(event.target.valueAsNumber) ? event.target.valueAsNumber : undefined; }
function mibValue(event: React.ChangeEvent<HTMLInputElement>) { const value = numberValue(event); return value === undefined ? undefined : value * 1024 * 1024; }
