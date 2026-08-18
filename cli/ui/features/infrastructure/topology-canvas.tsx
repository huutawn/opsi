"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { Background, BaseEdge, EdgeLabelRenderer, getBezierPath, Handle, Position, ReactFlow, type Connection, type Edge, type EdgeProps, type EdgeTypes, type Node, type NodeProps, type NodeTypes, type ReactFlowInstance } from "@xyflow/react";
import { StatusBadge } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";
import { liveDeploymentHealth } from "@/features/infrastructure/deployment-review-model";
import { LocalAPIError, LocalClient } from "@/lib/api/local-client";
import type { BuildRecord, DeploymentJob, GitHubBinding, GitHubRepository, PlacementFacts, ServiceBinding, ServiceConfigurationDiff, ServiceConfigurationDraft, ServiceConfigurationPreview, ServiceConfigurationValidation, ServiceRecord, TopologyDiff, TopologyPlan, TopologyPreview, TopologyValidation } from "@/lib/contracts/registry";
import { assignmentFor, canvasDraftIssues, canvasDraftStatus, canvasPlacement, compileCanvasDraft, currentEnvironment, moveCanvasPlacement, serverStatus, topologyResourcePresentation, updateCanvasPlacement, type CanvasDraft, type CanvasPlacement, type TopologyResourcePresentation } from "@/lib/presentation/infrastructure/model";

type SelectData = { onSelect: () => void };
type ResourceData = SelectData & { canvasTarget?: string; deployment?: DeploymentJob; mode?: "design" | "live"; onPointerDown?: (e: React.PointerEvent | React.MouseEvent) => void; presentation: TopologyResourcePresentation; serviceKey?: string };
type UnplacedData = SelectData & { count: number };
type ResourceFlowNode = Node<ResourceData, "resource">;
type UnplacedFlowNode = Node<UnplacedData, "unplaced">;
type CanvasNode = ResourceFlowNode | UnplacedFlowNode;
type DraftReview = { preview: TopologyPreview; validation: TopologyValidation; diff: TopologyDiff; idempotencyKey: string; topologyRevision: number; topologyStateHash: string };
type ConfigurationReview = { serviceID: string; preview: ServiceConfigurationPreview; validation: ServiceConfigurationValidation; diff: ServiceConfigurationDiff; idempotencyKey: string };
type ConfigurationDrafts = Record<string, ServiceConfigurationDraft>;
type SelectedConnection = { sourceID: string; key: string };
type PlacementResource = { id: string; key: string; kind: "application" | "managed_service"; type: string; lifecycle: string; name: string; version?: string; replicas?: number; cpuMillicores?: number; memoryBytes?: number };

function CustomConnectionEdge({ id, label, selected, sourcePosition, sourceX, sourceY, style, targetPosition, targetX, targetY }: EdgeProps) {
  const [edgePath, labelX, labelY] = getBezierPath({ sourcePosition, sourceX, sourceY, targetPosition, targetX, targetY });
  return <>
    <BaseEdge id={id} path={edgePath} style={style} />
    {label ? <EdgeLabelRenderer>
      <div className="nodrag nopan react-flow__edge-label" style={{ position: "absolute", transform: `translate(-50%, -50%) translate(${labelX}px,${labelY}px)`, pointerEvents: "all", zIndex: 1000 }}>
        <span style={{ background: "var(--opsi-surface-lowest)", border: selected ? "1px solid var(--opsi-secondary)" : "1px solid var(--opsi-outline-variant)", borderRadius: "var(--opsi-radius-pill)", padding: "2px 8px", fontSize: "11px", fontFamily: "var(--opsi-font-mono)", fontWeight: 700, color: "var(--opsi-on-surface)", whiteSpace: "nowrap", display: "inline-block" }}>
          {label}
        </span>
      </div>
    </EdgeLabelRenderer> : null}
  </>;
}

const nodeTypes = { resource: TopologyResourceNode, unplaced: UnplacedGroup } satisfies NodeTypes;
const edgeTypes = { default: CustomConnectionEdge } satisfies EdgeTypes;
const groupWidth = 292;
const appHeight = 126;

export function TopologyDesignCanvas({ bindings, builds, console, draft, facts, onDraft, onReload, repositories, topology }: { bindings: GitHubBinding[]; builds: BuildRecord[]; console: ConsoleController; draft: CanvasDraft; facts: PlacementFacts; onDraft: (draft: CanvasDraft) => void; onReload: () => Promise<void>; repositories: GitHubRepository[]; topology: TopologyPlan | null }) {
  const client = useMemo(() => new LocalClient(), []);
  const [review, setReview] = useState<DraftReview | null>(null);
  const [busy, setBusy] = useState<"" | "review" | "apply">("");
  const [message, setMessage] = useState("");
	const [configurationDrafts, setConfigurationDrafts] = useState<ConfigurationDrafts>({});
	const [configurationReview, setConfigurationReview] = useState<ConfigurationReview | null>(null);
	const [selectedConnection, setSelectedConnection] = useState<SelectedConnection | null>(null);
  const projectID = console.state.project?.id ?? "";
  const selectedID = resolveSelection(console.route.topology, facts);
  const changeCount = Object.keys(draft).length;
  const configurationChangeCount = Object.keys(configurationDrafts).length;
  const unpublishedCount = changeCount + configurationChangeCount;
  const environment = currentEnvironment(facts, console.route.environment ?? "");
  const select = (id: string) => {
		setSelectedConnection(null);
    console.navigate({ topology: id });
    window.requestAnimationFrame(() => document.getElementById("topology-inspector-heading")?.focus());
  };

  const draftRef = useRef(draft);
  useEffect(() => { draftRef.current = draft; }, [draft]);

  function move(serviceKey: string, runtimeID?: string) {
    const runtime = facts.runtimes.find((item) => item.id === runtimeID);
    const managed = facts.resources?.find((resource) => resource.id === serviceKey && resource.kind === "managed_service");
    const next = moveCanvasPlacement(topology, draftRef.current, serviceKey, runtime);
    changeDraft(runtime && managed ? updateCanvasPlacement(topology, next, serviceKey, { replicas: managed.replicas, cpu_request_millicores: managed.cpu_millicores, memory_request_bytes: managed.memory_bytes, exposure: { mode: "none" } }) : next);
    select(facts.resources?.some((resource) => resource.id === serviceKey) ? `resource:${serviceKey}` : `service:${serviceKey}`);
  }

  useEffect(() => {
    let activeKey: string | null = null;
    let startX = 0;
    let startY = 0;

    function onDown(e: MouseEvent | PointerEvent | TouchEvent) {
      if ("button" in e && e.button !== 0) return;
      const touch = "touches" in e && e.touches[0] ? e.touches[0] : undefined;
      const clientX = touch ? touch.clientX : "clientX" in e ? e.clientX : 0;
      const clientY = touch ? touch.clientY : "clientY" in e ? e.clientY : 0;
      const target = e.target as HTMLElement | null;
      if (target?.closest(".react-flow__handle")) return;
      let nodeEl = target?.closest<HTMLElement>(".topologyResourceNode[data-service-key]");
      if (!nodeEl && typeof document !== "undefined") {
        nodeEl = [...document.querySelectorAll<HTMLElement>(".topologyResourceNode[data-service-key]")].find((el) => {
          const box = el.getBoundingClientRect();
          return clientX >= box.left && clientX <= box.right && clientY >= box.top && clientY <= box.bottom;
        }) ?? null;
      }
      const rawKey = nodeEl?.dataset.serviceKey;
      if (!rawKey) return;
      activeKey = rawKey;
      startX = clientX;
      startY = clientY;
    }

    function onUp(e: MouseEvent | PointerEvent | TouchEvent) {
      if (!activeKey) return;
      const touch = "changedTouches" in e && e.changedTouches[0] ? e.changedTouches[0] : undefined;
      const endX = touch ? touch.clientX : "clientX" in e ? e.clientX : 0;
      const endY = touch ? touch.clientY : "clientY" in e ? e.clientY : 0;
      const serviceKey = activeKey;
      if (Math.hypot(endX - startX, endY - startY) < 10) {
        activeKey = null;
        return;
      }
      activeKey = null;
      let targetID: string | null | undefined = null;
      if (typeof document !== "undefined") {
        const elements = typeof document.elementsFromPoint === "function" ? document.elementsFromPoint(endX, endY) : [];
        targetID = elements.find((el) => el.hasAttribute("data-canvas-target"))?.getAttribute("data-canvas-target");
        if (!targetID) {
          targetID = [...document.querySelectorAll<HTMLElement>("[data-canvas-target]")].find((element) => {
            const box = element.getBoundingClientRect();
            return endX >= box.left && endX <= box.right && endY >= box.top && endY <= box.bottom;
          })?.dataset.canvasTarget;
        }
        if (!targetID) {
          const unplacedEl = document.querySelector<HTMLElement>('[data-canvas-target="unplaced"]');
          if (unplacedEl) {
            const box = unplacedEl.getBoundingClientRect();
            if (endX >= box.left && endX <= box.right + 20) {
              targetID = "unplaced";
            } else if (endX > box.right + 20) {
              const colIndex = Math.floor((endX - box.right - 20) / (box.width + 28));
              const runtime = facts.runtimes[colIndex] ?? facts.runtimes[0];
              if (runtime) targetID = `runtime:${runtime.id}`;
            }
          }
        }
      }
      if (targetID && targetID.startsWith("runtime:")) {
        move(serviceKey, targetID.slice(8));
      } else if (targetID === "unplaced") {
        move(serviceKey);
      }
    }

    window.addEventListener("mousedown", onDown, true);
    window.addEventListener("pointerdown", onDown, true);
    window.addEventListener("mouseup", onUp, true);
    window.addEventListener("pointerup", onUp, true);
    return () => {
      window.removeEventListener("mousedown", onDown, true);
      window.removeEventListener("pointerdown", onDown, true);
      window.removeEventListener("mouseup", onUp, true);
      window.removeEventListener("pointerup", onUp, true);
    };
  });

  const placements = new Map([...facts.services.map((service) => [service.key, canvasPlacement(topology, draft, service.key)] as const), ...(facts.resources ?? []).filter((resource) => resource.kind === "managed_service").map((resource) => [resource.id, canvasPlacement(topology, draft, resource.id)] as const)]);
  const nodes = buildNodes(console, facts, topology, draft, placements, selectedID, select);
  const edges = buildConnectionEdges(console.state.services, configurationDrafts);
  const canvasKey = `${topology?.revision ?? 0}:${topology?.state_hash ?? "none"}:${nodes.map((node) => node.type === "resource" ? `${node.id}:${node.parentId ?? "root"}:${node.data.presentation.status}:${node.data.presentation.draftState ?? "factual"}` : `${node.id}:${node.data.count}`).join("|")}:${edges.map((e) => `${e.id}:${e.label}`).join("|")}`;
  const selectedService = selectedID.startsWith("service:") ? facts.services.find((service) => service.key === selectedID.slice(8)) : undefined;
  const selectedManagedResource = selectedID.startsWith("resource:") ? facts.resources?.find((resource) => resource.id === selectedID.slice(9) && resource.kind === "managed_service") : undefined;
  const selectedRuntime = selectedID.startsWith("runtime:") ? facts.runtimes.find((runtime) => runtime.id === selectedID.slice(8)) : undefined;

  useEffect(() => {
    if (!review || review.topologyRevision === (topology?.revision ?? 0) && review.topologyStateHash === (topology?.state_hash ?? "")) return;
    // The CanvasDraft remains authoritative local work; only its stale Cloud review closes.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setReview(null);
    setMessage("Topology changed. Review draft again.");
  }, [review, topology?.revision, topology?.state_hash]);

  function changeDraft(next: CanvasDraft) {
    onDraft(next);
    setReview(null);
    setMessage("");
  }


  function reset() {
    changeDraft({});
  }

	function changeConfiguration(service: ServiceRecord, next: ServiceConfigurationDraft) {
		setConfigurationDrafts((current) => ({ ...current, [service.id]: next }));
		setConfigurationReview(null);
		setMessage("");
	}

	function connectApplications(connection: Connection) {
		const source = serviceForNode(console.state.services, connection.source);
		const target = serviceForNode(console.state.services, connection.target);
		if (!source || !target || source.id === target.id) return;
		const current = configurationDraft(source, configurationDrafts);
		const binding: ServiceBinding = { kind: "internal_http", target_service_id: target.id, target_service_key: target.name, env_prefix: envPrefix(target.name) };
		if (!current.bindings?.some((item) => connectionKey(item) === connectionKey(binding))) changeConfiguration(source, { ...current, bindings: [...(current.bindings ?? []), binding] });
		select(`service:${source.name}`);
		setSelectedConnection({ sourceID: source.id, key: connectionKey(binding) });
	}

	function removeConnection(sourceID: string, key: string) {
		const source = console.state.services.find((service) => service.id === sourceID);
		if (!source) return;
		const current = configurationDraft(source, configurationDrafts);
		changeConfiguration(source, { ...current, bindings: (current.bindings ?? []).filter((binding) => connectionKey(binding) !== key) });
		setSelectedConnection(null);
	}

	function selectConnection(edge: Edge) {
		const source = serviceForNode(console.state.services, edge.source);
		if (!source) return;
		select(`service:${source.name}`);
		setSelectedConnection({ sourceID: source.id, key: String(edge.data?.connectionKey ?? "") });
	}

	async function reviewConfiguration(service: ServiceRecord) {
		const next = configurationDraft(service, configurationDrafts);
		setBusy("review");
		setMessage("");
		try {
			const [preview, validation, diff] = await Promise.all([client.serviceConfigurationPreview(projectID, service.id, next), client.serviceConfigurationValidate(projectID, service.id, next), client.serviceConfigurationDiff(projectID, service.id, next)]);
			setConfigurationReview({ serviceID: service.id, preview, validation, diff, idempotencyKey: crypto.randomUUID() });
		} catch (error) {
			setConfigurationReview(null);
			setMessage((error as Error).message);
		} finally {
			setBusy("");
		}
	}

	async function applyConfiguration(service: ServiceRecord) {
		if (!configurationReview?.validation.valid || configurationReview.serviceID !== service.id) return;
		const reviewed = configurationReview;
		setBusy("apply");
		try {
			const result = await client.serviceConfigurationApply(projectID, service.id, { draft: reviewed.preview.configuration, expected_revision: reviewed.preview.current_revision, expected_state_hash: reviewed.preview.current_state_hash }, reviewed.idempotencyKey);
			await Promise.all([console.actions.load(), onReload()]);
			if (result.configuration.state_hash === reviewed.preview.draft_state_hash) {
				setConfigurationDrafts((current) => { const next = { ...current }; delete next[service.id]; return next; });
				setConfigurationReview(null);
				setSelectedConnection(null);
				setMessage(`Service configuration r${result.configuration.revision} applied${result.reused ? " from replay" : ""}.`);
			} else {
				setMessage("Cloud returned a different configuration hash; the local draft was preserved.");
			}
		} catch (error) {
			if (error instanceof LocalAPIError && error.status === 409) await Promise.all([console.actions.load(), onReload()]);
			setConfigurationReview(null);
			setMessage((error as Error).message);
		} finally {
			setBusy("");
		}
	}

  async function reviewDraft() {
    if (!projectID || !changeCount) return;
    setBusy("review");
    setMessage("");
    try {
      const preview = await client.topologyPlan(projectID, compileCanvasDraft(projectID, topology, draft));
      const [validation, diff] = await Promise.all([client.topologyValidate(projectID, preview.draft), client.topologyDiff(projectID, preview.draft)]);
      setReview({ preview, validation, diff, idempotencyKey: crypto.randomUUID(), topologyRevision: diff.current_revision, topologyStateHash: diff.current_hash ?? preview.state_hash });
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
        setMessage("Topology changed. Review draft again.");
      } else {
        setMessage((error as Error).message);
      }
    } finally {
      setBusy("");
    }
  }

  return (
    <section className="relative w-full h-[640px] lg:h-[700px] rounded-2xl overflow-hidden border border-outline-variant/20 bg-surface-container-lowest flex flex-col lg:flex-row shadow-xl" aria-labelledby="topology-design-heading">
      {/* Canvas Area */}
      <div className="flex-1 relative h-full flex flex-col bg-[radial-gradient(#334155_1px,transparent_1px)] [background-size:24px_24px] bg-[position:-12px_-12px]">
        {/* Floating Action Controls Overlay */}
        <div className="absolute top-4 left-4 right-4 flex flex-wrap items-center justify-between gap-3 z-10 pointer-events-none">
          <div className="flex items-center gap-3 bg-surface-container/90 backdrop-blur-md px-4 py-2 rounded-xl border border-outline-variant/20 shadow-md pointer-events-auto">
            <div className="flex items-center gap-2">
              <span className={`w-2 h-2 rounded-full ${unpublishedCount ? "bg-status-warning animate-pulse" : "bg-status-ready"}`} />
              <span className="text-xs font-label-sm font-semibold text-on-surface">
                {unpublishedCount} {unpublishedCount === 1 ? "draft change" : "draft changes"}
              </span>
            </div>
            <div className="w-px h-4 bg-outline-variant/30" />
            <div className="flex items-center gap-2">
              <button
                disabled={!changeCount || Boolean(busy)}
                onClick={reset}
                className="px-2.5 py-1 text-xs text-on-surface-variant hover:text-on-surface hover:bg-surface-container-highest rounded-lg transition-colors disabled:opacity-40 cursor-pointer"
                type="button"
              >
                Reset
              </button>
              <button
                disabled={!changeCount || Boolean(busy)}
                onClick={() => void reviewDraft()}
                className="px-2.5 py-1 text-xs bg-surface-container-highest hover:bg-surface-container-high text-on-surface rounded-lg transition-colors font-medium disabled:opacity-40 cursor-pointer"
                type="button"
              >
                {busy === "review" ? "Reviewing…" : "Review draft"}
              </button>
              <button
                disabled={!review?.validation.valid || Boolean(busy)}
                onClick={() => void applyTopology()}
                className="px-3 py-1 text-xs bg-primary text-on-primary font-bold rounded-lg transition-colors disabled:opacity-40 shadow-sm cursor-pointer"
                type="button"
              >
                {busy === "apply" ? "Applying…" : "Apply topology"}
              </button>
            </div>
          </div>

          <div className="hidden sm:flex items-center gap-2 bg-surface-container/90 backdrop-blur-md px-3 py-1.5 rounded-full border border-outline-variant/20 shadow-md pointer-events-auto text-xs font-label-sm text-on-surface-variant">
            <span className="text-primary font-semibold">{topology ? `Plan r${topology.revision}` : "Draft"}</span>
            <span>•</span>
            <span className="font-code-md text-[11px] truncate max-w-[120px]">{topology?.state_hash?.slice(0, 8) ?? "Clean"}</span>
          </div>
        </div>

        {message ? (
          <div className="absolute top-18 left-4 z-10 bg-surface-container-high/90 backdrop-blur-md border border-outline-variant/30 text-xs px-3 py-1.5 rounded-lg text-primary shadow-md" role="status">
            {message}
          </div>
        ) : null}

        {/* Flow Canvas */}
        <div className="w-full h-full flex-1">
          <CanvasFlow
            edges={edges}
            facts={facts}
            key={canvasKey}
            nodes={nodes}
            onConnect={connectApplications}
            onEdgeSelect={selectConnection}
            onMove={move}
            onRemoveEdge={(edge) => {
              const source = serviceForNode(console.state.services, edge.source);
              if (source) removeConnection(source.id, String(edge.data?.connectionKey ?? ""));
            }}
          />
        </div>
      </div>

      {/* Inspector Sidebar */}
      <TopologyInspector
        bindings={bindings}
        builds={builds}
        busy={busy}
        configurationDrafts={configurationDrafts}
        configurationReview={configurationReview}
        console={console}
        draft={draft}
        facts={facts}
        onApplyConfiguration={applyConfiguration}
        onConfiguration={changeConfiguration}
        onDraft={changeDraft}
        onRemoveConnection={removeConnection}
        onReviewConfiguration={reviewConfiguration}
        repositories={repositories}
        selectedConnection={selectedConnection}
        selectedManagedResource={selectedManagedResource}
        selectedRuntime={selectedRuntime}
        selectedService={selectedService}
        topology={topology}
      />
    </section>
  );
}

export function LiveTopologyCanvas({ console, environment, facts }: { console: ConsoleController; environment: PlacementFacts["environments"][number]; facts: PlacementFacts }) {
  const deployments = [...console.state.deployments].filter((deployment) => deployment.environment_id === environment.id).sort((a, b) => b.created_at.localeCompare(a.created_at));
  const latest = new Map<string, DeploymentJob>();
  for (const deployment of deployments) if (!latest.has(deployment.service_id)) latest.set(deployment.service_id, deployment);
  const select = (id: string) => {
    console.navigate({ topology: id });
    window.requestAnimationFrame(() => document.getElementById("live-topology-inspector-heading")?.focus());
  };
  const nodes = buildLiveNodes(console, facts, environment.id, latest, select);
  const selectedID = nodes.some((node) => node.id === console.route.topology) ? console.route.topology : nodes[0]?.id ?? "";
  const renderedNodes = nodes.map((node) => ({ ...node, selected: node.id === selectedID }));
  const selected = renderedNodes.find((node) => node.id === selectedID);
  const edges = buildLiveEdges(console.state.services, renderedNodes);
  return (
    <div className="relative w-full h-[640px] lg:h-[700px] rounded-2xl overflow-hidden border border-outline-variant/20 bg-surface-container-lowest flex flex-col lg:flex-row shadow-xl">
      <div className="flex-1 relative h-full flex flex-col bg-[radial-gradient(#334155_1px,transparent_1px)] [background-size:24px_24px] bg-[position:-12px_-12px]">
        <div className="absolute top-4 left-4 right-4 flex items-center justify-between z-10 pointer-events-none">
          <div className="flex items-center gap-3 bg-surface-container/90 backdrop-blur-md px-4 py-2 rounded-xl border border-outline-variant/20 shadow-md pointer-events-auto">
            <div className="flex items-center gap-2 text-xs font-label-sm text-status-ready font-semibold">
              <span className="material-symbols-outlined text-[16px]">wifi</span>
              <span>Agent Connected</span>
            </div>
            <div className="w-px h-4 bg-outline-variant/30" />
            <span className="text-[11px] text-on-surface-variant font-code-md">Live Telemetry Active</span>
          </div>
        </div>

        <div className="w-full h-full flex-1" aria-label="Read-only factual topology canvas">
          {renderedNodes.length ? (
            <ReactFlow<ResourceFlowNode>
              defaultViewport={{ x: 60, y: 60, zoom: 0.95 }}
              edges={edges}
              elementsSelectable
              fitView
              fitViewOptions={{ padding: 0.2 }}
              maxZoom={1.2}
              minZoom={0.55}
              nodes={renderedNodes}
              nodesConnectable={false}
              nodesDraggable={false}
              nodeTypes={nodeTypes}
              panOnDrag={[1, 2]}
              zoomOnDoubleClick={false}
            >
              <Background color="var(--opsi-outline-variant)" gap={24} size={1} />
            </ReactFlow>
          ) : (
            <div className="flex items-center justify-center h-full text-on-surface-variant text-sm font-body-md">
              No factual runtime or deployment resources reported for {environment.name}.
            </div>
          )}
        </div>
      </div>

      <LiveResourceInspector environment={environment} selected={selected} />
    </div>
  );
}

function buildLiveNodes(console: ConsoleController, facts: PlacementFacts, environmentID: string, latest: Map<string, DeploymentJob>, select: (id: string) => void): ResourceFlowNode[] {
  const runtimes = facts.runtimes.filter((runtime) => runtime.environment_id === environmentID);
  const nodes: ResourceFlowNode[] = [];
  runtimes.forEach((runtime, index) => {
    const runtimeNodes = facts.nodes.filter((node) => node.runtime_id === runtime.id);
    const agents = facts.agents.filter((agent) => agent.runtime_id === runtime.id);
    const node = runtimeNodes[0];
    const agent = agents.find((item) => item.status === "active") ?? agents[0];
    const record = console.state.nodes.find((item) => item.id === node?.id);
    const status = serverStatus(runtimeNodes, agents, runtime.status);
    const presentation = topologyResourcePresentation({
      kind: "server",
      name: runtime.name || record?.public_host || runtime.id,
      status,
      context: `${runtime.type} · ${runtime.id}`,
      ariaDetail: `Runtime ${runtime.status}, Agent ${agent?.status ?? "not reported"}`,
      notice: status === "Unknown" ? "Facts are insufficient to establish Ready or Offline." : undefined,
      tone: status === "Ready" ? "ready" : status === "Offline" ? "failed" : "neutral",
      facts: [
        { label: "Runtime", value: runtime.status },
        { label: "Node", value: node ? `${node.id} · ${node.status}` : "Not reported" },
        { label: "Agent", value: agent ? `${agent.id} · ${agent.status}` : "Not reported" },
        { label: "Capacity", value: node?.cpu_cores === undefined || node.memory_mb === undefined ? "Not reported" : `${node.cpu_cores} cores · ${node.memory_mb} MiB` },
        { label: "Heartbeat", value: agent?.last_seen_at || node?.last_seen_at || record?.last_seen_at || "Not reported" },
      ],
    });
    nodes.push({ id: `runtime:${runtime.id}`, type: "resource", position: { x: 24, y: 28 + index * 230 }, data: { mode: "live", onSelect: () => select(`runtime:${runtime.id}`), presentation }, draggable: false, focusable: false, style: { width: 310, height: 190 } });
  });

  const serviceSlots = new Map<string, number>();
  facts.services.forEach((service) => {
    const deployment = latest.get(service.id) ?? latest.get(service.key);
    const serviceRecord = console.state.services.find((item) => item.id === service.id || item.name === service.key);
    const sourceKind = serviceRecord?.type || "application";
    const runtimeIndex = sourceKind === "application" ? runtimes.findIndex((runtime) => runtime.id === deployment?.runtime_id) : -1;
    const lane = runtimeIndex >= 0 ? runtimeIndex : runtimes.length;
    const slot = serviceSlots.get(String(lane)) ?? 0;
    serviceSlots.set(String(lane), slot + 1);
    const state = deployment ? liveDeploymentHealth(deployment) : "Unknown";
    const digest = deployment?.current_digest || deployment?.terminal_result?.current_digest || deployment?.desired_digest || deployment?.snapshot?.image.digest;
    const endpoint = deployment?.exposure_spec ? `${deployment.exposure_spec.hostname}${deployment.exposure_spec.path}` : "Not reported";
    const runtime = runtimes.find((item) => item.id === deployment?.runtime_id);
    const presentation = topologyResourcePresentation({
      kind: sourceKind,
      name: service.key,
      status: state,
      context: deployment ? `Reported on ${runtime?.name || deployment.runtime_id || "unknown runtime"}` : "No factual deployment reported",
      ariaDetail: deployment ? `Deployment ${deployment.id}` : "deployment unknown",
      notice: deployment?.failure_message_redacted || (deployment ? undefined : "Design placement is intentionally not shown in Live."),
      tone: state === "Running" ? "ready" : state === "Failed" ? "failed" : state === "Unknown" ? "neutral" : "warning",
      facts: [
        { label: "Workload", value: deployment?.rollout_state || deployment?.status || "Not reported" },
        { label: "Image digest", value: digest || "Not reported" },
        { label: "Deployment", value: deployment?.id || "Not reported" },
        { label: "Exposure", value: endpoint },
      ],
    });
    const id = `service:${service.key}`;
    nodes.push({ id, type: "resource", position: { x: 380 + slot * 280, y: 48 + lane * 230 }, data: { deployment: presentation.supported ? deployment : undefined, mode: "live", onSelect: () => select(id), presentation }, draggable: false, focusable: false, style: { width: 250, height: presentation.notice ? 144 : 132 } });
  });
  return nodes;
}

function buildLiveEdges(services: ServiceRecord[], nodes: ResourceFlowNode[]): Edge[] {
  const nodeIDs = new Set(nodes.map((node) => node.id));
  const edges: Edge[] = [];
  for (const node of nodes) {
    const deployment = node.data.deployment;
    if (deployment?.runtime_id && nodeIDs.has(`runtime:${deployment.runtime_id}`)) edges.push({ id: `placement:${deployment.id}`, source: `runtime:${deployment.runtime_id}`, target: node.id, label: "reported placement", selectable: false, style: { stroke: "var(--opsi-live-border)" } });
  }
  for (const service of services) for (const binding of service.configuration?.bindings ?? []) {
    const source = `service:${service.name}`;
    const target = `service:${binding.target_service_key}`;
    if (!nodeIDs.has(source) || !nodeIDs.has(target)) continue;
    edges.push({ id: `binding:${service.id}:${connectionKey(binding)}`, source, target, label: [binding.kind, binding.path || binding.env_prefix].filter(Boolean).join(" · "), selectable: false, animated: false });
  }
  return edges;
}

function LiveResourceInspector({ environment, selected }: { environment: PlacementFacts["environments"][number]; selected?: ResourceFlowNode }) {
  const resource = selected?.data.presentation;
  const deployment = selected?.data.deployment;
  return (
    <aside className="w-full lg:w-96 bg-surface-container-low/95 backdrop-blur-xl border-t lg:border-t-0 lg:border-l border-outline-variant/20 p-5 overflow-y-auto space-y-4 shrink-0 shadow-xl" aria-labelledby="live-topology-inspector-heading">
      <div className="flex items-start justify-between gap-3 pb-3 border-b border-outline-variant/20">
        <div className="min-w-0">
          <p className="text-[11px] font-code-md text-primary truncate">{environment.name} / Live</p>
          <h3 className="font-headline-md text-base font-bold text-on-surface truncate mt-0.5" id="live-topology-inspector-heading" tabIndex={-1}>
            {resource?.name || "No resource selected"}
          </h3>
        </div>
        {resource ? <StatusBadge label={resource.badge || resource.status} value={resource.status} /> : null}
      </div>
      {resource ? (
        <>
          <section className="bg-surface-container-high/60 p-4 rounded-xl border border-outline-variant/10 space-y-2">
            <h4 className="font-label-sm text-xs text-on-surface-variant uppercase tracking-wider">Reported facts</h4>
            <dl className="space-y-1">
              {resource.facts.map((fact) => (
                <InspectorFact key={fact.label} label={fact.label} value={fact.value} />
              ))}
            </dl>
            {resource.notice ? (
              <p className="text-xs text-status-warning bg-status-warning/10 p-2.5 rounded-lg border border-status-warning/20 mt-2">
                {resource.notice}
              </p>
            ) : null}
          </section>
          {deployment ? (
            <section className="bg-surface-container-high/60 p-4 rounded-xl border border-outline-variant/10 space-y-2">
              <h4 className="font-label-sm text-xs text-on-surface-variant uppercase tracking-wider">Deployment state</h4>
              <dl className="space-y-1">
                <InspectorFact label="Phase" value={deployment.action === "rollback" ? "Rollback" : deployment.base_deployment_id ? "Exposure" : "Workload"} />
                <InspectorFact label="Failure code" value={deployment.failure_code || deployment.terminal_result?.failure_code || "None"} />
                <InspectorFact label="Rollback" value={deployment.rollback_eligible ? "Eligible" : "Unavailable"} />
              </dl>
            </section>
          ) : null}
        </>
      ) : (
        <p className="text-xs text-on-surface-variant">No factual resource selected.</p>
      )}
    </aside>
  );
}

function CanvasFlow({ edges, nodes: initialNodes, onConnect, onEdgeSelect, onMove, onRemoveEdge }: { edges: Edge[]; facts?: PlacementFacts; nodes: CanvasNode[]; onConnect: (connection: Connection) => void; onEdgeSelect: (edge: Edge) => void; onMove: (serviceKey: string, runtimeID?: string) => void; onRemoveEdge: (edge: Edge) => void }) {
  void onMove;
  const instance = useRef<ReactFlowInstance<CanvasNode>>(null);
  return (
    <ReactFlow<CanvasNode>
      defaultEdgeOptions={{ selectable: true, type: "default" }}
      edgeTypes={edgeTypes}
      edges={edges}
      elementsSelectable
      fitView
      fitViewOptions={{ padding: 0.08 }}
      maxZoom={1.25}
      minZoom={0.65}
      nodeTypes={nodeTypes}
      nodes={initialNodes}
      nodesConnectable
      nodesDraggable={false}
      onConnect={onConnect}
      onEdgeClick={(_, edge) => onEdgeSelect(edge)}
      onEdgesDelete={(removed) => removed.forEach(onRemoveEdge)}
      onInit={(flow) => {
        instance.current = flow;
      }}
      panOnDrag={[1, 2]}
    >
      <Background color="var(--opsi-outline-variant)" gap={24} size={1} />
    </ReactFlow>
  );
}

function DraftReviewPanel({ review }: { review: DraftReview }) {
  return (
    <section className="bg-surface-container-low p-4 rounded-xl border border-outline-variant/20 space-y-3" aria-labelledby="draft-review-heading">
      <div className="flex items-center justify-between">
        <h3 className="font-headline-md text-sm font-bold text-on-surface" id="draft-review-heading">Cloud Review</h3>
        <span className={`text-xs font-semibold px-2 py-0.5 rounded ${review.validation.valid ? "bg-status-ready/15 text-status-ready" : "bg-status-failed/15 text-status-failed"}`}>
          {review.validation.valid ? "Valid" : "Issues Found"}
        </span>
      </div>
      <div className="grid grid-cols-2 gap-2 text-xs font-code-md bg-surface-container p-2.5 rounded-lg">
        <div><span className="text-on-surface-variant">Revision:</span> <strong className="text-on-surface">{review.diff.current_revision}</strong></div>
        <div><span className="text-on-surface-variant">Proposed:</span> <strong className="text-primary">{review.diff.proposed_hash?.slice(0, 8)}</strong></div>
      </div>
      {review.validation.issues.length ? (
        <ul className="text-xs text-error space-y-1">
          {review.validation.issues.map((issue, index) => (
            <li key={`${issue.code}:${index}`}>{issue.message}</li>
          ))}
        </ul>
      ) : null}
    </section>
  );
}

function buildNodes(console: ConsoleController, facts: PlacementFacts, topology: TopologyPlan | null, draft: CanvasDraft, placements: Map<string, CanvasPlacement>, selectedID: string, select: (id: string) => void, onPointerDown?: (serviceKey: string, e: React.PointerEvent | React.MouseEvent) => void): CanvasNode[] {
  const groups: CanvasNode[] = [];
  const applications: CanvasNode[] = [];
  const managedResources = (facts.resources ?? []).filter((resource): resource is typeof resource & { kind: "managed_service" } => resource.kind === "managed_service");
  const resources: PlacementResource[] = [...facts.services.map((service) => ({ id: service.id, key: service.key, kind: "application" as const, type: "application", lifecycle: "", name: service.key })), ...managedResources.map((resource) => ({ id: resource.id, key: resource.id, kind: resource.kind, type: resource.type, lifecycle: resource.lifecycle, name: resource.name, version: resource.version, replicas: resource.replicas, cpuMillicores: resource.cpu_millicores, memoryBytes: resource.memory_bytes }))];
  const groupServices = new Map<string, typeof resources>();
  const runtimeIDs = new Set(facts.runtimes.map((runtime) => runtime.id));
  groupServices.set("unplaced", resources.filter((service) => { const runtimeID = placements.get(service.key)?.runtime_id; return !runtimeID || !runtimeIDs.has(runtimeID); }));
  for (const runtime of facts.runtimes) groupServices.set(runtime.id, resources.filter((service) => placements.get(service.key)?.runtime_id === runtime.id));
  const maxItems = Math.max(1, ...groupServices.values().map((services) => services.length));
  const groupHeight = Math.max(300, 150 + maxItems * appHeight);
  groups.push({ id: "unplaced", type: "unplaced", position: { x: 24, y: 24 }, data: { count: groupServices.get("unplaced")?.length ?? 0, onSelect: () => select("unplaced") }, draggable: false, focusable: false, selected: selectedID === "unplaced", style: { width: groupWidth, height: groupHeight }, zIndex: 0 });
  facts.runtimes.forEach((runtime, index) => {
    const factualNodes = facts.nodes.filter((node) => node.runtime_id === runtime.id);
    const agents = facts.agents.filter((agent) => agent.runtime_id === runtime.id);
    const assigned = groupServices.get(runtime.id) ?? [];
    const requestedCPU = assigned.reduce((sum, service) => sum + (placements.get(service.key)?.cpu_request_millicores ?? 0), 0);
    const requestedMemory = assigned.reduce((sum, service) => sum + (placements.get(service.key)?.memory_request_bytes ?? 0), 0);
    const node = factualNodes[0];
    const agent = agents.find((item) => item.status === "active") ?? agents[0];
    const record = console.state.nodes.find((item) => item.id === node?.id);
    const status = serverStatus(factualNodes, agents, runtime.status);
    const id = `runtime:${runtime.id}`;
    const presentation = topologyResourcePresentation({
      kind: "server",
      name: runtime.name || record?.public_host || runtime.id,
      status,
      context: `${assigned.length} placed ${assigned.length === 1 ? "app" : "apps"}`,
      ariaDetail: `Agent ${agent?.status ?? "Not reported"}`,
      tone: status === "Ready" ? "ready" : status === "Offline" ? "failed" : "neutral",
      facts: [
        { label: "CPU", value: node?.cpu_cores === undefined ? "Ready" : `${node.cpu_cores} cores` },
        { label: "RAM", value: node?.memory_mb === undefined ? "Ready" : `${node.memory_mb} MiB` },
        { label: "Agent", value: agent?.status ?? "Active" },
      ],
    });
    groups.push({ id, type: "resource", position: { x: 24 + (index + 1) * (groupWidth + 28), y: 24 }, data: { canvasTarget: id, onSelect: () => select(id), presentation }, draggable: false, focusable: false, selected: selectedID === id, style: { width: groupWidth, height: groupHeight }, zIndex: 0 });
  });
  for (const [parent, services] of groupServices) {
    const parentIndex = parent === "unplaced" ? -1 : facts.runtimes.findIndex((r) => r.id === parent);
    const parentX = 24 + (parentIndex + 1) * (groupWidth + 28);
    const parentY = 24;
    services.forEach((service, index) => {
      const placement = placements.get(service.key) ?? { runtime_id: null };
      const runtime = facts.runtimes.find((item) => item.id === placement.runtime_id);
      const sourceKind = service.kind === "managed_service" ? "managed_service" : console.state.services.find((item) => item.id === service.id || item.name === service.key)?.type || "application";
      const status = canvasDraftStatus(topology, draft, service.key);
      const id = service.kind === "managed_service" ? `resource:${service.key}` : `service:${service.key}`;
      const assignment = placement.runtime_id ? "Assigned" : "Unplaced";
      const issues = canvasDraftIssues(placement).length;
      const presentation = topologyResourcePresentation({
        kind: sourceKind,
        name: service.name,
        status: assignment,
        badge: status,
        context: `${runtime?.name || runtime?.id || (placement.runtime_id ? `${placement.runtime_id}` : "Unplaced")}`,
        ariaDetail: status,
        draftState: status,
        notice: issues ? `${issues} missing fields` : undefined,
        tone: status === "pending removal" ? "failed" : status === "unchanged" ? "neutral" : "warning",
        facts: service.kind === "managed_service" ? [
          { label: "Type", value: service.type },
          { label: "Lifecycle", value: service.lifecycle },
          { label: "Version", value: service.version || "default" },
        ] : [
          { label: "Replicas", value: String(placement.replicas ?? 1) },
          { label: "CPU", value: `${placement.cpu_request_millicores ?? 100}m` },
          { label: "Memory", value: `${mib(placement.memory_request_bytes ?? 128 * 1024 * 1024)} MiB` },
        ],
      });
      applications.push({ id, type: "resource", position: { x: parentX + 20, y: parentY + 136 + index * appHeight }, data: { onPointerDown: onPointerDown ? (e) => onPointerDown(service.key, e) : undefined, onSelect: () => select(id), presentation, serviceKey: service.key }, draggable: presentation.capabilities.movable, focusable: false, selected: selectedID === id, style: { width: groupWidth - 40, height: issues ? 112 : 108 }, zIndex: 1 });
    });
  }
  return [...groups, ...applications];
}

function TopologyInspector({ bindings, builds, busy, configurationDrafts, configurationReview, console, draft, facts, onApplyConfiguration, onConfiguration, onDraft, onRemoveConnection, onReviewConfiguration, repositories, selectedConnection, selectedManagedResource, selectedRuntime, selectedService, topology }: { bindings: GitHubBinding[]; builds: BuildRecord[]; busy: "" | "review" | "apply"; configurationDrafts: ConfigurationDrafts; configurationReview: ConfigurationReview | null; console: ConsoleController; draft: CanvasDraft; facts: PlacementFacts; onApplyConfiguration: (service: ServiceRecord) => Promise<void>; onConfiguration: (service: ServiceRecord, draft: ServiceConfigurationDraft) => void; onDraft: (draft: CanvasDraft) => void; onRemoveConnection: (sourceID: string, key: string) => void; onReviewConfiguration: (service: ServiceRecord) => Promise<void>; repositories: GitHubRepository[]; selectedConnection: SelectedConnection | null; selectedManagedResource?: NonNullable<PlacementFacts["resources"]>[number]; selectedRuntime?: PlacementFacts["runtimes"][number]; selectedService?: PlacementFacts["services"][number]; topology: TopologyPlan | null }) {
  if (selectedConnection) {
    const source = console.state.services.find((service) => service.id === selectedConnection.sourceID);
    if (source) return <ConnectionInspector busy={busy} drafts={configurationDrafts} onApply={onApplyConfiguration} onChange={onConfiguration} onRemove={onRemoveConnection} onReview={onReviewConfiguration} review={configurationReview} selected={selectedConnection} services={console.state.services} source={source} />;
  }
  if (selectedManagedResource) {
    const placement = canvasPlacement(topology, draft, selectedManagedResource.id);
    const runtime = facts.runtimes.find((item) => item.id === placement.runtime_id);
    const status = canvasDraftStatus(topology, draft, selectedManagedResource.id);
    return (
      <aside className="w-full lg:w-96 bg-surface-container-low/95 backdrop-blur-xl border-t lg:border-t-0 lg:border-l border-outline-variant/20 p-5 overflow-y-auto space-y-4 shrink-0 shadow-xl" aria-labelledby="topology-inspector-heading">
        <div className="flex items-start justify-between gap-3 pb-3 border-b border-outline-variant/20">
          <div className="min-w-0">
            <p className="text-[11px] font-code-md text-primary truncate">{runtime?.name ?? "Unplaced"}</p>
            <h3 className="font-headline-md text-base font-bold text-on-surface truncate mt-0.5" id="topology-inspector-heading" tabIndex={-1}>
              {selectedManagedResource.name}
            </h3>
          </div>
          <StatusBadge label={status} value={status === "unchanged" ? "healthy" : "in_progress"} />
        </div>
        <section className="bg-surface-container-high/60 p-4 rounded-xl border border-outline-variant/10 space-y-2">
          <h4 className="font-label-sm text-xs text-on-surface-variant uppercase tracking-wider">Managed intent</h4>
          <dl className="space-y-1">
            <InspectorFact label="Type" value={selectedManagedResource.type} />
            <InspectorFact label="Version" value={selectedManagedResource.version || "default"} />
            <InspectorFact label="Lifecycle" value={selectedManagedResource.lifecycle} />
            <InspectorFact label="Runtime" value={runtimeLabel(facts, placement.runtime_id)} />
            <InspectorFact label="Replicas" value={String(selectedManagedResource.replicas ?? 1)} />
            <InspectorFact label="CPU" value={selectedManagedResource.cpu_millicores === undefined ? "Not reported" : `${selectedManagedResource.cpu_millicores}m`} />
            <InspectorFact label="Memory" value={selectedManagedResource.memory_bytes === undefined ? "Not reported" : `${mib(selectedManagedResource.memory_bytes)} MiB`} />
          </dl>
          <div className="pt-3">
            <button onClick={() => console.navigate({ view: "infrastructure", tab: "resources", resource: selectedManagedResource.id })} className="w-full py-2 px-3 bg-surface-container hover:bg-surface-container-high text-on-surface text-xs font-medium rounded-lg border border-outline-variant/20 transition-colors" type="button">
              Open in Infrastructure Center →
            </button>
          </div>
        </section>
      </aside>
    );
  }
  if (selectedService) {
    const live = assignmentFor(topology, selectedService.key);
    const placement = canvasPlacement(topology, draft, selectedService.key);
    const runtime = facts.runtimes.find((item) => item.id === placement.runtime_id);
    const environment = facts.environments.find((item) => item.id === runtime?.environment_id);
    const issues = canvasDraftIssues(placement);
    const status = canvasDraftStatus(topology, draft, selectedService.key);
    const edit = (patch: Partial<CanvasPlacement>) => onDraft(updateCanvasPlacement(topology, draft, selectedService.key, patch));
    const service = console.state.services.find((item) => item.id === selectedService.id && item.name === selectedService.key);
    const binding = bindings.find((item) => item.status === "active" && item.project_id === facts.project_id && item.service_id === selectedService.id && item.service_key === selectedService.key);
    const repository = repositories.find((item) => item.repository_id === binding?.repository_id);
    const build = builds.filter((item) => item.project_id === facts.project_id && item.service_id === selectedService.id && item.service_key === selectedService.key && (!binding || item.active_binding_id === binding.id)).sort((a, b) => b.created_at.localeCompare(a.created_at))[0];
    return (
      <aside className="w-full lg:w-96 bg-surface-container-low/95 backdrop-blur-xl border-t lg:border-t-0 lg:border-l border-outline-variant/20 p-5 overflow-y-auto space-y-4 shrink-0 shadow-xl" aria-labelledby="topology-inspector-heading">
        <div className="flex items-start justify-between gap-3 pb-3 border-b border-outline-variant/20">
          <div className="min-w-0">
            <p className="text-[11px] font-code-md text-primary truncate">{environment?.name ?? (placement.runtime_id ? "Unknown env" : "Unplaced")} / {runtime?.name ?? placement.runtime_id ?? "Unplaced"}</p>
            <h3 className="font-headline-md text-base font-bold text-on-surface truncate mt-0.5" id="topology-inspector-heading" tabIndex={-1}>
              {selectedService.key}
            </h3>
          </div>
          <StatusBadge label={status} value={status === "unchanged" ? "healthy" : "in_progress"} />
        </div>

        <section className="bg-surface-container-high/60 p-4 rounded-xl border border-outline-variant/10 space-y-3">
          <h4 className="font-label-sm text-xs text-on-surface-variant uppercase tracking-wider">Placement intent</h4>
          <dl className="space-y-1">
            <InspectorFact label="Applied" value={runtimeLabel(facts, live?.runtime_id)} />
            <InspectorFact label="Draft" value={runtimeLabel(facts, placement.runtime_id)} />
          </dl>
          <form className="space-y-3 pt-2 border-t border-outline-variant/10" onSubmit={(event) => event.preventDefault()}>
            <div>
              <label className="text-[11px] font-label-sm text-on-surface-variant block mb-1">Replicas</label>
              <input className="w-full bg-surface-container-highest border border-outline-variant/30 rounded-lg px-3 py-1.5 text-xs text-on-surface focus:outline-none focus:border-primary/50" disabled={!placement.runtime_id} max="100" min="1" onChange={(event) => edit({ replicas: numberValue(event) })} required step="1" type="number" value={placement.replicas ?? ""} />
            </div>
            <div className="grid grid-cols-2 gap-2">
              <div>
                <label className="text-[11px] font-label-sm text-on-surface-variant block mb-1">CPU (m)</label>
                <input className="w-full bg-surface-container-highest border border-outline-variant/30 rounded-lg px-3 py-1.5 text-xs text-on-surface focus:outline-none focus:border-primary/50" disabled={!placement.runtime_id} max="1000000" min="1" onChange={(event) => edit({ cpu_request_millicores: numberValue(event) })} required step="1" type="number" value={placement.cpu_request_millicores ?? ""} />
              </div>
              <div>
                <label className="text-[11px] font-label-sm text-on-surface-variant block mb-1">Memory (MiB)</label>
                <input className="w-full bg-surface-container-highest border border-outline-variant/30 rounded-lg px-3 py-1.5 text-xs text-on-surface focus:outline-none focus:border-primary/50" disabled={!placement.runtime_id} max="1073741824" min="1" onChange={(event) => edit({ memory_request_bytes: mibValue(event) })} required step="1" type="number" value={placement.memory_request_bytes === undefined ? "" : Math.round(placement.memory_request_bytes / 1024 / 1024)} />
              </div>
            </div>
            <div>
              <label className="text-[11px] font-label-sm text-on-surface-variant block mb-1">Exposure</label>
              <select className="w-full bg-surface-container-highest border border-outline-variant/30 rounded-lg px-3 py-1.5 text-xs text-on-surface focus:outline-none focus:border-primary/50 cursor-pointer" disabled={!placement.runtime_id} onChange={(event) => edit({ exposure: { mode: event.target.value as "none" | "internal" | "public" } })} value={placement.exposure?.mode ?? "none"}>
                <option value="none">None</option>
                <option value="internal">Internal</option>
                <option value="public">Public</option>
              </select>
            </div>
          </form>
        </section>

        {issues.length ? (
          <section className="bg-error-container/20 border border-error/30 p-3 rounded-xl space-y-1">
            <h4 className="font-label-sm text-xs text-error uppercase font-semibold">Validation issues</h4>
            <ul className="text-xs text-error space-y-0.5 list-disc list-inside">
              {issues.map((issue) => (
                <li key={issue}>{issue}</li>
              ))}
            </ul>
          </section>
        ) : null}

        <section className="bg-surface-container-high/60 p-4 rounded-xl border border-outline-variant/10 space-y-2">
          <h4 className="font-label-sm text-xs text-on-surface-variant uppercase tracking-wider">Source & Build</h4>
          <dl className="space-y-1">
            <InspectorFact label="Repository" value={repository?.full_name ?? service?.repo_url ?? "Not reported"} />
            <InspectorFact label="Branch" value={binding?.selected_ref || service?.branch || "main"} />
            <InspectorFact label="Build" value={build ? `${build.id.slice(0, 8)} · ${build.build.status}` : "No build yet"} />
          </dl>
        </section>
      </aside>
    );
  }
  if (selectedRuntime) {
    const nodes = facts.nodes.filter((node) => node.runtime_id === selectedRuntime.id);
    const agents = facts.agents.filter((agent) => agent.runtime_id === selectedRuntime.id);
    const node = nodes[0];
    const agent = agents.find((item) => item.status === "active") ?? agents[0];
    const record = console.state.nodes.find((item) => item.id === node?.id);
    const environment = facts.environments.find((item) => item.id === selectedRuntime.environment_id);
    const status = serverStatus(nodes, agents, selectedRuntime.status);
    return (
      <aside className="w-full lg:w-96 bg-surface-container-low/95 backdrop-blur-xl border-t lg:border-t-0 lg:border-l border-outline-variant/20 p-5 overflow-y-auto space-y-4 shrink-0 shadow-xl" aria-labelledby="topology-inspector-heading">
        <div className="flex items-start justify-between gap-3 pb-3 border-b border-outline-variant/20">
          <div className="min-w-0">
            <p className="text-[11px] font-code-md text-primary truncate">{environment?.name ?? selectedRuntime.environment_id}</p>
            <h3 className="font-headline-md text-base font-bold text-on-surface truncate mt-0.5" id="topology-inspector-heading" tabIndex={-1}>
              {selectedRuntime.name || record?.public_host || selectedRuntime.id}
            </h3>
          </div>
          <StatusBadge label={status} value={status === "Ready" ? "healthy" : "unknown"} />
        </div>
        <section className="bg-surface-container-high/60 p-4 rounded-xl border border-outline-variant/10 space-y-2">
          <h4 className="font-label-sm text-xs text-on-surface-variant uppercase tracking-wider">Server capacity</h4>
          <dl className="space-y-1">
            <InspectorFact label="Runtime" value={selectedRuntime.id} />
            <InspectorFact label="Node" value={node?.id ?? "Not reported"} />
            <InspectorFact label="CPU" value={node?.cpu_cores === undefined ? "Not reported" : `${node.cpu_cores} cores`} />
            <InspectorFact label="RAM" value={node?.memory_mb === undefined ? "Not reported" : `${node.memory_mb} MiB`} />
            <InspectorFact label="Agent" value={agent ? `${agent.id.slice(0, 8)} · ${agent.status}` : "Not connected"} />
          </dl>
        </section>
      </aside>
    );
  }
  const unplacedApplications = facts.services.filter((service) => { const runtimeID = canvasPlacement(topology, draft, service.key).runtime_id; return !runtimeID || !facts.runtimes.some((runtime) => runtime.id === runtimeID); }).length;
  const unplacedManaged = (facts.resources ?? []).filter((resource) => resource.kind === "managed_service" && !canvasPlacement(topology, draft, resource.id).runtime_id).length;
  return (
    <aside className="w-full lg:w-96 bg-surface-container-low/95 backdrop-blur-xl border-t lg:border-t-0 lg:border-l border-outline-variant/20 p-5 overflow-y-auto space-y-4 shrink-0 shadow-xl" aria-labelledby="topology-inspector-heading">
      <div className="pb-3 border-b border-outline-variant/20">
        <p className="text-[11px] font-code-md text-primary">Design / Unplaced</p>
        <h3 className="font-headline-md text-base font-bold text-on-surface mt-0.5" id="topology-inspector-heading" tabIndex={-1}>
          Unplaced Resources
        </h3>
      </div>
      <div className="bg-surface-container-high/60 p-4 rounded-xl border border-outline-variant/10 text-xs text-on-surface-variant space-y-2">
        <p><strong className="text-on-surface">{unplacedApplications}</strong> applications and <strong className="text-on-surface">{unplacedManaged}</strong> managed services are in queue.</p>
        <p className="text-[11px]">Drag resources onto a server to plan deployment placement.</p>
      </div>
    </aside>
  );
}

function TopologyResourceNode({ data, selected }: NodeProps<ResourceFlowNode>) {
  const resource = data.presentation;
  const connectable = data.mode !== "live" && resource.capabilities.connectable && data.serviceKey;
  const isServer = resource.kind === "server";
  const isReady = resource.status === "Ready" || resource.status === "Running" || resource.status === "Assigned";
  const isFailed = resource.status === "Failed" || resource.status === "Offline";
  
  if (isServer) {
    return (
      <div
        aria-label={resource.ariaLabel}
        aria-pressed={selected}
        className={`w-full h-full rounded-2xl bg-surface-container-low/95 backdrop-blur-md border p-4 shadow-xl flex flex-col justify-between transition-all cursor-pointer ${
          selected ? "border-primary ring-1 ring-primary/40 shadow-primary/10" : "border-outline-variant/20 hover:border-outline-variant/50"
        }`}
        data-canvas-target={data.canvasTarget}
        onClick={data.onSelect}
        onKeyDown={(event) => selectKeyDown(event, data.onSelect)}
        onKeyUp={(event) => selectKeyUp(event, data.onSelect)}
        role="button"
        tabIndex={0}
      >
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-2">
            <span className="material-symbols-outlined text-[16px] text-on-surface-variant">dns</span>
            <h4 className="font-headline-md text-sm font-bold text-on-surface truncate">{resource.name}</h4>
          </div>
          <span className={`px-2 py-0.5 rounded text-[10px] font-label-sm uppercase font-semibold ${
            isReady ? "bg-status-ready/15 text-status-ready border border-status-ready/30" : isFailed ? "bg-status-failed/15 text-status-failed border border-status-failed/30" : "bg-surface-container-highest text-on-surface-variant"
          }`}>
            {resource.status}
          </span>
        </div>

        <div className="text-[11px] font-code-md text-on-surface-variant truncate">
          {resource.context}
        </div>

        <div className="space-y-2 mt-2 pt-2 border-t border-outline-variant/10">
          <div className="space-y-1">
            <div className="flex justify-between text-[10px] font-label-sm text-on-surface-variant">
              <span>CPU</span>
              <span className="font-code-md text-on-surface">{resource.facts.find(f => f.label.includes("CPU"))?.value ?? "Ready"}</span>
            </div>
            <div className="w-full bg-surface-container-highest rounded-full h-1.5 overflow-hidden">
              <div className="bg-secondary h-full rounded-full w-1/2"></div>
            </div>
          </div>
          <div className="space-y-1">
            <div className="flex justify-between text-[10px] font-label-sm text-on-surface-variant">
              <span>RAM</span>
              <span className="font-code-md text-on-surface">{resource.facts.find(f => f.label.includes("RAM"))?.value ?? "Ready"}</span>
            </div>
            <div className="w-full bg-surface-container-highest rounded-full h-1.5 overflow-hidden">
              <div className="bg-tertiary h-full rounded-full w-1/3"></div>
            </div>
          </div>
        </div>
      </div>
    );
  }

  const borderTone = isFailed ? "border-l-status-failed" : isReady ? "border-l-status-ready" : "border-l-status-warning";
  return (
    <div
      aria-label={resource.ariaLabel}
      aria-pressed={selected}
      className={`w-full h-full rounded-xl bg-surface-container border border-outline-variant/20 border-l-4 ${borderTone} p-3 shadow-md hover:bg-surface-container-high transition-all flex items-center justify-between cursor-pointer ${
        selected ? "ring-1 ring-primary/50 shadow-lg" : ""
      }`}
      data-canvas-target={data.canvasTarget}
      data-draft-state={resource.draftState?.replace(" ", "-")}
      data-resource-kind={resource.kind}
      data-resource-mode={data.mode ?? "design"}
      data-resource-state={resource.state}
      data-service-key={data.serviceKey}
      onClick={data.onSelect}
      onKeyDown={(event) => selectKeyDown(event, data.onSelect)}
      onKeyUp={(event) => selectKeyUp(event, data.onSelect)}
      role="button"
      tabIndex={0}
    >
      {connectable ? <Handle aria-label={`Connect into ${data.serviceKey}`} position={Position.Left} type="target" className="!w-2 !h-2 !bg-primary" /> : null}
      <div className="flex items-center gap-2.5 min-w-0 flex-1">
        <div className="w-8 h-8 rounded-lg bg-surface flex items-center justify-center text-primary shrink-0 shadow-sm border border-outline-variant/10">
          <span className="material-symbols-outlined text-[16px]">
            {resource.kind === "managed-service" ? "database" : "layers"}
          </span>
        </div>
        <div className="min-w-0 flex-1">
          <span className="font-headline-md text-xs font-bold text-on-surface truncate block">{resource.name}</span>
          <span className="font-code-md text-[10px] text-on-surface-variant truncate block">{resource.context}</span>
        </div>
      </div>
      <div className="flex flex-col items-end gap-1 shrink-0 ml-2">
        <span className={`px-2 py-0.5 rounded text-[10px] font-label-sm uppercase font-semibold flex items-center gap-1 ${
          isReady ? "bg-status-ready/10 text-status-ready" : isFailed ? "bg-status-failed/10 text-status-failed" : "bg-status-warning/10 text-status-warning"
        }`}>
          {isReady ? <span className="material-symbols-outlined text-[12px]">check_circle</span> : null}
          {resource.badge || resource.status}
        </span>
      </div>
      {connectable ? <Handle aria-label={`Connect from ${data.serviceKey}`} position={Position.Right} type="source" className="!w-2 !h-2 !bg-primary" /> : null}
    </div>
  );
}

function UnplacedGroup({ data, selected }: NodeProps<UnplacedFlowNode>) {
  return (
    <div
      aria-label={`Unplaced applications, ${data.count} applications and managed resources`}
      aria-pressed={selected}
      className={`w-full h-full rounded-2xl border-2 border-dashed border-outline-variant/30 bg-surface-container-low/40 p-4 flex flex-col justify-between text-center transition-all cursor-pointer ${
        selected ? "border-primary ring-1 ring-primary/30" : "hover:border-outline-variant/60"
      }`}
      data-canvas-target="unplaced"
      onClick={data.onSelect}
      onKeyDown={(event) => selectKeyDown(event, data.onSelect)}
      onKeyUp={(event) => selectKeyUp(event, data.onSelect)}
      role="button"
      tabIndex={0}
    >
      <div className="flex items-center justify-between pb-2 border-b border-outline-variant/10">
        <span className="font-label-sm text-xs text-on-surface-variant uppercase tracking-wider">Unplaced Queue</span>
        <span className="px-2 py-0.5 rounded-full bg-surface-container-highest text-xs font-code-md font-bold text-on-surface">{data.count}</span>
      </div>
      <div className="py-4">
        <span className="material-symbols-outlined text-3xl text-on-surface-variant/50">layers</span>
        <p className="text-xs text-on-surface font-medium mt-1">Resource Queue</p>
        <p className="text-[11px] text-on-surface-variant mt-0.5">Drag to place on server</p>
      </div>
      <div className="text-[10px] font-code-md text-on-surface-variant/60">Drop here to unplace</div>
    </div>
  );
}

function ServiceRuntimeInspector({ busy, draft, onApply, onChange, onReview, review }: { busy: "" | "review" | "apply"; draft: ServiceConfigurationDraft; onApply: () => void; onChange: (draft: ServiceConfigurationDraft) => void; onReview: () => void; review: ConfigurationReview | null }) {
	const environment = (draft.environment ?? []).map((item) => `${item.name}=${item.value}`).join("\n");
	return <section className="configurationInspector" aria-labelledby="runtime-configuration-heading"><h4 id="runtime-configuration-heading">Runtime intent</h4><label>Non-secret environment<textarea className="field" onChange={(event) => onChange({ ...draft, environment: parseEnvironment(event.target.value) })} placeholder={'LOG_LEVEL=info\nFEATURE_FLAG=true'} rows={4} value={environment} /></label><p className="muted">Secret-like names are rejected by Cloud. Generated connection keys cannot be overridden.</p><div className="form"><label>Public hostname<input className="field" onChange={(event) => onChange(routeDraft(draft, event.target.value, draft.public_route?.path ?? "/"))} placeholder="apps.example.com" value={draft.public_route?.hostname ?? ""} /></label><label>Public path<input className="field" onChange={(event) => onChange(routeDraft(draft, draft.public_route?.hostname ?? "", event.target.value))} placeholder="/" value={draft.public_route?.path ?? ""} /></label></div><p>{draft.bindings?.length ?? 0} connection intent{draft.bindings?.length === 1 ? "" : "s"} in this service draft.</p><button disabled={Boolean(busy)} onClick={onReview} type="button">{busy === "review" ? "Reviewing…" : "Review service configuration"}</button>{review ? <ConfigurationReviewPanel busy={busy === "apply"} onApply={onApply} review={review} /> : null}</section>;
}

function ConnectionInspector({ busy, drafts, onApply, onChange, onRemove, onReview, review, selected, services, source }: { busy: "" | "review" | "apply"; drafts: ConfigurationDrafts; onApply: (service: ServiceRecord) => Promise<void>; onChange: (service: ServiceRecord, draft: ServiceConfigurationDraft) => void; onRemove: (sourceID: string, key: string) => void; onReview: (service: ServiceRecord) => Promise<void>; review: ConfigurationReview | null; selected: SelectedConnection; services: ServiceRecord[]; source: ServiceRecord }) {
	const draft = configurationDraft(source, drafts);
	const applied = source.configuration?.bindings?.find((binding) => connectionKey(binding) === selected.key);
	const binding = draft.bindings?.find((item) => connectionKey(item) === selected.key);
	const target = services.find((service) => service.id === (binding ?? applied)?.target_service_id);
	const update = (next: ServiceBinding) => onChange(source, { ...draft, bindings: (draft.bindings ?? []).map((item) => connectionKey(item) === selected.key ? next : item) });
	if (!binding && applied) return <aside className="canvasInspector" aria-labelledby="topology-inspector-heading"><p className="canvasPath">{source.name} → {target?.name ?? applied.target_service_key}</p><h3 id="topology-inspector-heading" tabIndex={-1}>Pending connection removal</h3><p>The applied connection remains visible until this service configuration is reviewed and applied.</p><button disabled={Boolean(busy)} onClick={() => void onReview(source)} type="button">Review removal</button>{review?.serviceID === source.id ? <ConfigurationReviewPanel busy={busy === "apply"} onApply={() => void onApply(source)} review={review} /> : null}</aside>;
	if (!binding) return null;
	return <aside className="canvasInspector" aria-labelledby="topology-inspector-heading"><p className="canvasPath">{source.name} → {target?.name ?? binding.target_service_key}</p><h3 id="topology-inspector-heading" tabIndex={-1}>HTTP connection</h3><form className="form" onSubmit={(event) => event.preventDefault()}><label className="span2">Runtime intent<select className="select" onChange={(event) => update(bindingForKind(binding, event.target.value as ServiceBinding["kind"]))} value={binding.kind}><option value="internal_http">Internal HTTP</option><option value="browser_http">Browser HTTP</option></select></label>{binding.kind === "internal_http" ? <label className="span2">Environment prefix<input className="field" onChange={(event) => update({ ...binding, env_prefix: event.target.value })} placeholder="API" value={binding.env_prefix ?? ""} /></label> : <><label>Environment name<input className="field" onChange={(event) => update({ ...binding, env_name: event.target.value })} placeholder="API_BASE_URL" value={binding.env_name ?? ""} /></label><label>Same-origin path<input className="field" onChange={(event) => update({ ...binding, path: event.target.value })} placeholder="/api" value={binding.path ?? "/api"} /></label></>}</form><p className="muted">{binding.kind === "internal_http" ? "Cloud generates HOST, PORT, and URL from factual runtime DNS and target port." : "Browser HTTP emits a path only; source and target must share a public hostname."}</p><div className="dialogActions"><button onClick={() => onRemove(source.id, selected.key)} type="button">Remove connection</button><button disabled={Boolean(busy)} onClick={() => void onReview(source)} type="button">{busy === "review" ? "Reviewing…" : "Review connection"}</button></div>{review?.serviceID === source.id ? <ConfigurationReviewPanel busy={busy === "apply"} onApply={() => void onApply(source)} review={review} /> : null}</aside>;
}

function ConfigurationReviewPanel({ busy, onApply, review }: { busy: boolean; onApply: () => void; review: ConfigurationReview }) {
	return <section className="draftReview configurationReview" aria-label="Cloud service configuration review"><p>{review.validation.valid ? "Cloud validation passed." : "Cloud validation failed."}</p><div className="hashPair"><div><span>Current revision</span><strong>{review.preview.current_revision}</strong><code>{review.preview.current_state_hash}</code></div><div><span>Draft hash</span><code>{review.preview.draft_state_hash}</code></div></div><h4>Semantic diff</h4>{review.diff.changes.length ? <ul>{review.diff.changes.map((change, index) => <li key={`${change.kind}:${change.name}:${index}`}><strong>{change.kind.replaceAll("_", " ")}</strong> · {change.action}{change.name ? ` · ${change.name}` : ""}{change.before || change.after ? <><br /><code>{change.before || "none"} → {change.after || "none"}</code></> : null}</li>)}</ul> : <p>No semantic changes.</p>}{review.validation.issues?.length ? <ul className="draftIssues">{review.validation.issues.map((issue) => <li key={`${issue.code}:${issue.field}`}><strong>{issue.code}</strong> · {issue.message}</li>)}</ul> : null}<button disabled={!review.validation.valid || busy} onClick={onApply} type="button">{busy ? "Applying…" : "Apply service configuration"}</button></section>;
}

function buildConnectionEdges(services: ServiceRecord[], drafts: ConfigurationDrafts): Edge[] {
	const edges: Edge[] = [];
	for (const source of services) {
		const applied = source.configuration?.bindings ?? [];
		const current = configurationDraft(source, drafts).bindings ?? [];
		const appliedByKey = new Map(applied.map((binding) => [connectionKey(binding), binding]));
		const currentByKey = new Map(current.map((binding) => [connectionKey(binding), binding]));
		for (const key of new Set([...appliedByKey.keys(), ...currentByKey.keys()])) {
			const before = appliedByKey.get(key);
			const after = currentByKey.get(key);
			const binding = after ?? before;
			const target = services.find((service) => service.id === binding?.target_service_id);
			if (!binding || !target) continue;
			const status = !before ? "pending add" : !after ? "pending removal" : JSON.stringify(before) === JSON.stringify(after) ? "applied" : "pending change";
			edges.push({ id: `connection:${source.id}:${key}`, source: `service:${source.name}`, target: `service:${target.name}`, label: `${binding.kind === "internal_http" ? "Internal" : "Browser"} · ${status}`, data: { connectionKey: key, status }, animated: status === "pending add" || status === "pending change", style: { stroke: status === "pending removal" ? "var(--color-error)" : status === "applied" ? "var(--color-status-ready)" : "var(--color-status-warning)", strokeDasharray: status === "pending removal" ? "6 5" : undefined, strokeWidth: 2 }, labelStyle: { fill: "var(--color-on-surface)", fontSize: 11, fontWeight: 700 } });
		}
	}
	return edges;
}

function configurationDraft(service: ServiceRecord, drafts: ConfigurationDrafts): ServiceConfigurationDraft {
	if (Object.hasOwn(drafts, service.id)) return drafts[service.id];
	const configuration = service.configuration;
	return { schema_version: "opsi.service_configuration/v1", environment: configuration?.environment ?? [], public_route: configuration?.public_route, bindings: configuration?.bindings ?? [] };
}

function serviceForNode(services: ServiceRecord[], nodeID: string | null) { return services.find((service) => `service:${service.name}` === nodeID || `service:${service.id}` === nodeID || service.id === nodeID || service.name === nodeID); }
function connectionKey(binding: ServiceBinding) { return binding.target_service_id || binding.target_service_key; }
function envPrefix(value: string) { return value.toUpperCase().replace(/[^A-Z0-9]+/g, "_").replace(/^_+|_+$/g, "") || "SERVICE"; }
function bindingForKind(binding: ServiceBinding, kind: ServiceBinding["kind"]): ServiceBinding { return kind === "internal_http" ? { kind, target_service_id: binding.target_service_id, target_service_key: binding.target_service_key, env_prefix: envPrefix(binding.target_service_key) } : { kind, target_service_id: binding.target_service_id, target_service_key: binding.target_service_key, env_name: `${envPrefix(binding.target_service_key)}_BASE_URL`, path: "/api" }; }
function parseEnvironment(value: string) { return value.split("\n").map((line) => line.trim()).filter(Boolean).map((line) => { const separator = line.indexOf("="); return { name: separator < 0 ? line : line.slice(0, separator).trim(), value: separator < 0 ? "" : line.slice(separator + 1) }; }); }
function routeDraft(draft: ServiceConfigurationDraft, hostname: string, path: string): ServiceConfigurationDraft { return { ...draft, public_route: hostname || path ? { hostname, path } : undefined }; }

function InspectorFact({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-center justify-between py-1 border-b border-outline-variant/10 text-xs">
      <dt className="text-on-surface-variant font-label-sm">{label}</dt>
      <dd className="font-code-md text-on-surface font-medium truncate max-w-[160px]">{value}</dd>
    </div>
  );
}
function assignmentSummary(assignment?: TopologyDiff["changes"][number]["before"]) { return assignment ? `${assignment.runtime_id}, ${assignment.replicas} replicas, ${assignment.cpu_request_millicores}m, ${mib(assignment.memory_request_bytes)} MiB, ${assignment.exposure.mode}` : "unplaced"; }
function issueScope(issue: TopologyValidation["issues"][number]) { return [issue.service_key, issue.runtime_id].filter(Boolean).join(" / ") ? `${[issue.service_key, issue.runtime_id].filter(Boolean).join(" / ")}: ` : ""; }
function mib(bytes: number) { return Math.round(bytes / 1024 / 1024); }
function capacityCPU(value: number | undefined, unknown: boolean) { return unknown || value === undefined ? "Unknown CPU" : `${value}m CPU`; }
function capacityMemory(value: number | undefined, unknown: boolean) { return unknown || value === undefined ? "Unknown memory" : `${mib(value)} MiB memory`; }
function runtimeLabel(facts: PlacementFacts, runtimeID?: string | null) { const runtime = facts.runtimes.find((item) => item.id === runtimeID); return runtime ? `${runtime.name} · ${runtime.id}` : runtimeID ? `${runtimeID} · not reported` : "Unplaced"; }
function selectKeyDown(event: React.KeyboardEvent, select: () => void) { if (event.key === "Enter") select(); if (event.key === " ") event.preventDefault(); }
function selectKeyUp(event: React.KeyboardEvent, select: () => void) { if (event.key === " ") select(); }
function resolveSelection(id: string | undefined, facts: PlacementFacts) { if (!id) return facts.services[0] ? `service:${facts.services[0].key}` : facts.resources?.find((resource) => resource.kind === "managed_service") ? `resource:${facts.resources.find((resource) => resource.kind === "managed_service")!.id}` : facts.runtimes[0] ? `runtime:${facts.runtimes[0].id}` : "unplaced"; if (id.startsWith("node:")) return `runtime:${facts.nodes.find((node) => node.id === id.slice(5))?.runtime_id ?? ""}`; if (id.startsWith("agent:")) return `runtime:${facts.agents.find((agent) => agent.id === id.slice(6))?.runtime_id ?? ""}`; if (id.startsWith("environment:")) return `runtime:${facts.runtimes.find((runtime) => runtime.environment_id === id.slice(12))?.id ?? ""}`; return id; }
function numberValue(event: React.ChangeEvent<HTMLInputElement>) { return Number.isFinite(event.target.valueAsNumber) ? event.target.valueAsNumber : undefined; }
function mibValue(event: React.ChangeEvent<HTMLInputElement>) { const value = numberValue(event); return value === undefined ? undefined : value * 1024 * 1024; }

