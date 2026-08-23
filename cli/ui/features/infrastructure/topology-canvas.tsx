"use client";

import { memo, useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Background,
  BaseEdge,
  EdgeLabelRenderer,
  getBezierPath,
  Handle,
  Position,
  ReactFlow,
  type Connection,
  type Edge,
  type EdgeProps,
  type EdgeTypes,
  type Node,
  type NodeProps,
  type NodeTypes,
  type ReactFlowInstance,
} from "@xyflow/react";
import { Button, Icon, StatusBadge } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";
import { DependencyDialog } from "@/features/dependencies/dependency-dialog";
import { RealizationReviewDialog } from "@/features/dependencies/realization-review-panel";
import { formatSymbolicSource } from "@/features/dependencies/types";
import { liveDeploymentHealth } from "@/features/infrastructure/deployment-review-model";
import { LocalAPIError, LocalClient } from "@/lib/api/local-client";
import { applicationConnectionEdgeID, applicationTopologyNodeID, dependencyTopologyEdgeID, topologyHandleIDs } from "@/features/infrastructure/topology-graph-identity";
import type {
  ApplicationDependency,
  BuildRecord,
  DeploymentJob,
  GitHubBinding,
  GitHubRepository,
  PlacementFacts,
  ServiceBinding,
  ServiceConfigurationDiff,
  ServiceConfigurationDraft,
  ServiceConfigurationPreview,
  ServiceConfigurationValidation,
  ServiceRecord,
  TopologyDiff,
  TopologyPlan,
  TopologyPreview,
  TopologyValidation,
} from "@/lib/contracts/registry";
import {
  assignmentFor,
  canvasDraftIssues,
  canvasDraftStatus,
  canvasPlacement,
  compileCanvasDraft,
  moveCanvasPlacement,
  serverStatus,
  topologyResourcePresentation,
  updateCanvasPlacement,
} from "@/lib/presentation/infrastructure/model";

type CanvasPlacement = ReturnType<typeof canvasPlacement>;
type CanvasDraft = Record<string, CanvasPlacement>;
type ConfigurationDrafts = Record<string, ServiceConfigurationDraft>;
type DraftReview = {
  preview: TopologyPreview;
  validation: TopologyValidation;
  diff: TopologyDiff;
  topologyRevision: number;
  topologyStateHash: string;
  idempotencyKey: string;
};
type ConfigurationReview = {
  serviceID: string;
  preview: ServiceConfigurationPreview;
  validation: ServiceConfigurationValidation;
  diff: ServiceConfigurationDiff;
  idempotencyKey: string;
};

type ResourceFlowNode = Node<
  {
    canvasTarget?: string;
    deployment?: DeploymentJob;
    mode?: "design" | "live";
    onPointerDown?: (e: React.PointerEvent | React.MouseEvent) => void;
    onSelect?: () => void;
    presentation: ReturnType<typeof topologyResourcePresentation>;
    serviceKey?: string;
  },
  "resource"
>;
type UnplacedFlowNode = Node<{ count: number; onSelect?: () => void }, "unplaced">;
type CanvasNode = ResourceFlowNode | UnplacedFlowNode;
type SelectedDependency = {
  sourceID: string;
  logicalName: string;
  dependency: ApplicationDependency;
  status: string;
  isManaged: boolean;
  hasBinding?: boolean;
};

type PlacementResource = {
  id: string;
  key: string;
  kind: string;
  type: string;
  lifecycle: string;
  name: string;
  version?: string;
  replicas?: number;
  cpuMillicores?: number;
  memoryBytes?: number;
};

const CustomConnectionEdge = memo(function CustomConnectionEdge({
  id,
  label,
  selected,
  sourcePosition,
  sourceX,
  sourceY,
  style,
  targetPosition,
  targetX,
  targetY,
}: EdgeProps) {
  const [edgePath, labelX, labelY] = getBezierPath({
    sourcePosition,
    sourceX,
    sourceY,
    targetPosition,
    targetX,
    targetY,
  });
  return (
    <>
      <BaseEdge id={id} interactionWidth={30} path={edgePath} style={style} />
      {label ? (
        <EdgeLabelRenderer>
          <div
            className="nodrag nopan react-flow__edge-label"
            style={{
              position: "absolute",
              transform: "translate(-50%, -50%) translate(" + labelX + "px," + labelY + "px)",
              pointerEvents: "none",
              zIndex: 1000,
            }}
          >
            <span
              data-edge-id={id}
              style={{
                background: "var(--opsi-surface-lowest)",
                border: selected
                  ? "1px solid var(--opsi-secondary)"
                  : "1px solid var(--opsi-outline-variant)",
                borderRadius: "var(--opsi-radius-pill)",
                padding: "2px 8px",
                fontSize: "11px",
                fontFamily: "var(--opsi-font-mono)",
                fontWeight: 700,
                color: "var(--opsi-on-surface)",
                whiteSpace: "nowrap",
                display: "inline-block",
                pointerEvents: "none",
              }}
            >
              {label}
            </span>
          </div>
        </EdgeLabelRenderer>
      ) : null}
    </>
  );
});

const TopologyResourceNode = memo(function TopologyResourceNode({ data, selected }: NodeProps<ResourceFlowNode>) {
  const { canvasTarget, mode, onPointerDown, onSelect, presentation, serviceKey } = data;
  const isServer = presentation.kind === "server";
  const isManaged = presentation.kind === "managed-service";

  return (
    <div
      aria-label={presentation.ariaLabel || presentation.name}
      className={"topologyResourceNode " + (selected ? "selected " : "") + (isServer ? "serverNode " : isManaged ? "managedNode " : "appNode ")}
      data-canvas-target={canvasTarget}
      data-resource-kind={presentation.kind}
      data-resource-mode={mode || "design"}
      data-resource-state={presentation.supported === false || presentation.kind === "unsupported" ? "unsupported" : "factual"}
      data-draft-state={mode === "live" ? undefined : presentation.draftState}
      data-service-key={serviceKey}
      onClick={onSelect}
      onKeyDown={(e) => onSelect && selectKeyDown(e, onSelect)}
      onKeyUp={(e) => onSelect && selectKeyUp(e, onSelect)}
      onPointerDown={onPointerDown}
      role="button"
      tabIndex={0}
    >
      <Handle id={topologyHandleIDs.target} type="target" position={Position.Left} style={{ background: "var(--opsi-primary)" }} />
      <div className="nodeHeader">
        <div className="min-w-0">
          <span className="nodeKind">{presentation.kind.replace("_", " ")}</span>
          <strong className="nodeTitle">{presentation.name}</strong>
        </div>
        <StatusBadge label={presentation.badge || presentation.status} value={presentation.status} />
      </div>

      <p className="nodeContext">{presentation.context}</p>

      {presentation.facts.length ? (
        <div className="nodeFacts">
          {presentation.facts.slice(0, 3).map((f) => (
            <span key={f.label} className="nodeFact">
              <small>{f.label}:</small> <strong>{f.value}</strong>
            </span>
          ))}
        </div>
      ) : null}

      {presentation.notice ? <p className="nodeNotice">{presentation.notice}</p> : null}
      <Handle id={topologyHandleIDs.source} type="source" position={Position.Right} style={{ background: "var(--opsi-primary)" }} />
    </div>
  );
});

const UnplacedGroup = memo(function UnplacedGroup({ data, selected }: NodeProps<UnplacedFlowNode>) {
  return (
    <div
      aria-label={`Unplaced applications, ${data.count} applications`}
      aria-pressed={selected}
      className={"unplacedGroup " + (selected ? "selected" : "")}
      data-canvas-target="unplaced"
      onClick={data.onSelect}
      onKeyDown={(e) => data.onSelect && selectKeyDown(e, data.onSelect)}
      onKeyUp={(e) => data.onSelect && selectKeyUp(e, data.onSelect)}
      role="button"
      tabIndex={0}
    >
      <div className="groupHeader">
        <h4 className="groupTitle">Unplaced Resources</h4>
        <span className="countBadge">{data.count}</span>
      </div>
      <p className="groupHint">Drag resources here to unassign them from servers.</p>
    </div>
  );
});

const nodeTypes = { resource: TopologyResourceNode, unplaced: UnplacedGroup } satisfies NodeTypes;
const edgeTypes = { default: CustomConnectionEdge } satisfies EdgeTypes;
const groupWidth = 292;
const appHeight = 148;

export function TopologyDesignCanvas({
  bindings,
  builds,
  console,
  draft,
  facts,
  onDraft,
  onReload,
  onUnpublishedChanges,
  repositories,
  topology,
}: {
  bindings: GitHubBinding[];
  builds: BuildRecord[];
  console: ConsoleController;
  draft: CanvasDraft;
  facts: PlacementFacts;
  onDraft: (draft: CanvasDraft) => void;
  onReload: () => Promise<void>;
  onUnpublishedChanges: (count: number) => void;
  repositories: GitHubRepository[];
  topology: TopologyPlan | null;
}) {
  const client = useMemo(() => new LocalClient(), []);
  const [review, setReview] = useState<DraftReview | null>(null);
  const [busy, setBusy] = useState<"" | "review" | "apply">("");
  const [message, setMessage] = useState("");
  const [configurationDrafts, setConfigurationDrafts] = useState<ConfigurationDrafts>({});
  const [configurationReview, setConfigurationReview] = useState<ConfigurationReview | null>(null);
  const [selectedDependency, setSelectedDependency] = useState<SelectedDependency | null>(null);
  const [depModal, setDepModal] = useState<{
    consumer: ServiceRecord;
    existingDependency?: ApplicationDependency;
    targetIdentityHint?: string;
    targetKindHint?: "managed_service" | "application";
  } | null>(null);
  const [realizationModal, setRealizationModal] = useState<ServiceRecord | null>(null);

  const projectID = console.state.project?.id ?? "";
  const selectedID = resolveSelection(console.route.topology, facts);
  const changeCount = Object.keys(draft).length;
  const configurationChangeCount = Object.keys(configurationDrafts).length;
  const unpublishedCount = changeCount + configurationChangeCount;

  useEffect(() => {
    onUnpublishedChanges(unpublishedCount);
  }, [onUnpublishedChanges, unpublishedCount]);
  useEffect(() => () => onUnpublishedChanges(0), [onUnpublishedChanges]);

  const navigateRef = useRef(console.navigate);
  useEffect(() => {
    navigateRef.current = console.navigate;
  }, [console.navigate]);

  const select = useCallback((id: string) => {
    setSelectedDependency(null);
    navigateRef.current({ topology: id });
    window.requestAnimationFrame(() => document.getElementById("topology-inspector-heading")?.focus());
  }, []);

  const draftRef = useRef(draft);
  useEffect(() => {
    draftRef.current = draft;
  }, [draft]);

  function move(serviceKey: string, runtimeID?: string) {
    const runtime = facts.runtimes.find((item) => item.id === runtimeID);
    const managed = facts.resources?.find(
      (resource) => resource.id === serviceKey && resource.kind === "managed_service"
    );
    const next = moveCanvasPlacement(topology, draftRef.current, serviceKey, runtime);
    changeDraft(
      runtime && managed
        ? updateCanvasPlacement(topology, next, serviceKey, {
            replicas: managed.replicas,
            cpu_request_millicores: managed.cpu_millicores,
            memory_request_bytes: managed.memory_bytes,
            exposure: { mode: "none" },
          })
        : next
    );
    select(
      facts.resources?.some((resource) => resource.id === serviceKey)
        ? "resource:" + serviceKey
        : "service:" + serviceKey
    );
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
        nodeEl =
          [...document.querySelectorAll<HTMLElement>(".topologyResourceNode[data-service-key]")].find((el) => {
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
              if (runtime) targetID = "runtime:" + runtime.id;
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

  const placements = useMemo(
    () =>
      new Map([
        ...facts.services.map((service) => [service.key, canvasPlacement(topology, draft, service.key)] as const),
        ...(facts.resources ?? [])
          .filter((resource) => resource.kind === "managed_service")
          .map((resource) => [resource.id, canvasPlacement(topology, draft, resource.id)] as const),
      ]),
    [facts.resources, facts.services, topology, draft]
  );
  const nodes = useMemo(
    () => buildNodes(console.state.services, console.state.nodes, facts, topology, draft, placements, selectedID, select),
    [console.state.services, console.state.nodes, facts, topology, draft, placements, selectedID, select]
  );
  const edges = useMemo(
    () => buildConnectionEdges(console.state.services, configurationDrafts, selectedID),
    [console.state.services, configurationDrafts, selectedID]
  );

  const selectedService = selectedID.startsWith("service:")
    ? facts.services.find((service) => service.key === selectedID.slice(8))
    : undefined;
  const selectedManagedResource = selectedID.startsWith("resource:")
    ? facts.resources?.find((resource) => resource.id === selectedID.slice(9) && resource.kind === "managed_service")
    : undefined;
  const selectedRuntime = selectedID.startsWith("runtime:")
    ? facts.runtimes.find((runtime) => runtime.id === selectedID.slice(8))
    : undefined;

  useEffect(() => {
    if (
      !review ||
      (review.topologyRevision === (topology?.revision ?? 0) &&
        review.topologyStateHash === (topology?.state_hash ?? ""))
    )
      return;
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

  async function reviewConfiguration(service: ServiceRecord) {
    if (!projectID) return;
    setBusy("review");
    setMessage("");
    try {
      const draft = configurationDraft(service, configurationDrafts);
      const preview = await client.serviceConfigurationPreview(projectID, service.id, draft);
      const [validation, diff] = await Promise.all([
        client.serviceConfigurationValidate(projectID, service.id, preview.configuration),
        client.serviceConfigurationDiff(projectID, service.id, preview.configuration),
      ]);
      setConfigurationReview({
        serviceID: service.id,
        preview,
        validation,
        diff,
        idempotencyKey: crypto.randomUUID(),
      });
    } catch (error) {
      setConfigurationReview(null);
      setMessage((error as Error).message);
    } finally {
      setBusy("");
    }
  }

  async function applyConfiguration(service: ServiceRecord) {
    if (!configurationReview?.validation.valid || !projectID) return;
    const reviewed = configurationReview;
    setBusy("apply");
    setMessage("");
    try {
      await client.serviceConfigurationApply(
        projectID,
        service.id,
        {
          draft: reviewed.preview.configuration,
          expected_revision: reviewed.preview.current_revision,
          expected_state_hash: reviewed.preview.current_state_hash,
        },
        reviewed.idempotencyKey
      );
      await console.actions.load();
      setConfigurationDrafts((current) => {
        const next = { ...current };
        delete next[service.id];
        return next;
      });
      setConfigurationReview(null);
      setMessage(`Service configuration applied for ${service.name}.`);
    } catch (error) {
      setMessage((error as Error).message);
    } finally {
      setBusy("");
    }
  }

  function connectApplications(connection: Connection) {
    const source = serviceForNode(console.state.services, connection.source);
    if (!source) return;

    const targetService = serviceForNode(console.state.services, connection.target);
    const targetResource = (facts.resources ?? []).find(
      (r) => "resource:" + r.id === connection.target || r.id === connection.target
    );

    if (targetResource) {
      setDepModal({
        consumer: source,
        targetIdentityHint: targetResource.id,
        targetKindHint: "managed_service",
      });
    } else if (targetService && targetService.id !== source.id) {
      const draft = configurationDraft(source, configurationDrafts);
      const newBinding: ServiceBinding = {
        kind: "internal_http",
        target_service_id: targetService.id,
        target_service_key: targetService.name,
        env_prefix: envPrefix(targetService.name),
      };
      changeConfiguration(source, {
        ...draft,
        bindings: [...(draft.bindings ?? []).filter((b) => connectionKey(b) !== targetService.id), newBinding],
      });
      select("connection:" + source.id + ":" + targetService.id);
    }
  }

  function selectEdge(edge: Edge) {
    const data = edge.data as SelectedDependency | undefined;
    if (data && data.dependency) {
      setSelectedDependency(data);
      select("service:" + data.sourceID);
    } else if (edge.id.startsWith("connection:")) {
      select(edge.id);
    }
  }

  async function reviewDraft() {
    if (!projectID || !changeCount) return;
    setBusy("review");
    setMessage("");
    try {
      const preview = await client.topologyPlan(projectID, compileCanvasDraft(projectID, topology, draft));
      const [validation, diff] = await Promise.all([
        client.topologyValidate(projectID, preview.draft),
        client.topologyDiff(projectID, preview.draft),
      ]);
      setReview({
        preview,
        validation,
        diff,
        idempotencyKey: crypto.randomUUID(),
        topologyRevision: diff.current_revision,
        topologyStateHash: diff.current_hash ?? preview.state_hash,
      });
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
      const result = await client.topologyApply(
        projectID,
        {
          draft: reviewed.preview.draft,
          expected_revision: reviewed.diff.current_revision,
          expected_state_hash: reviewed.diff.current_hash ?? reviewed.preview.state_hash,
        },
        reviewed.idempotencyKey
      );
      await onReload();
      if (result.plan.plan_hash === reviewed.preview.plan_hash && result.plan.plan_hash === reviewed.diff.proposed_hash) {
        onDraft({});
        setReview(null);
        setMessage("TopologyPlan r" + result.plan.revision + " applied" + (result.reused ? " from the idempotent replay" : "") + ".");
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
    <section
      className="topologyCanvas relative w-full rounded-2xl overflow-hidden border border-outline-variant/20 bg-surface-container-lowest flex flex-col shadow-xl"
      aria-labelledby="topology-design-heading"
    >
      <div className="flex-1 flex flex-col min-w-0 relative">
        <div className="absolute top-4 left-4 right-4 z-10 flex items-center justify-between pointer-events-none">
          <div className="flex items-center gap-2 bg-surface-container/90 backdrop-blur-md p-1.5 rounded-xl border border-outline-variant/20 shadow-md pointer-events-auto">
            <div className="flex items-center gap-2 px-2">
              <span
                className={"w-2 h-2 rounded-full " + (unpublishedCount ? "bg-status-warning animate-pulse" : "bg-status-ready")}
              />
              <span className="text-xs font-label-sm font-semibold text-on-surface">
                {unpublishedCount} {unpublishedCount === 1 ? "unpublished change" : "unpublished changes"}
              </span>
            </div>
            <div className="w-px h-4 bg-outline-variant/30" />
            <div className="designActions flex items-center gap-2">
              <button
                aria-label="Reset changes"
                disabled={!changeCount || Boolean(busy)}
                onClick={reset}
                className="px-3 py-2 min-h-[40px] min-w-[40px] text-xs text-on-surface-variant hover:text-on-surface hover:bg-surface-container-highest rounded-lg transition-colors disabled:opacity-40 cursor-pointer"
                type="button"
              >
                Reset changes
              </button>
              <button
                disabled={!changeCount || Boolean(busy)}
                onClick={() => void reviewDraft()}
                className="px-3 py-2 min-h-[40px] min-w-[40px] text-xs bg-surface-container-highest hover:bg-surface-container-high text-on-surface rounded-lg transition-colors font-medium disabled:opacity-40 cursor-pointer"
                type="button"
              >
                {busy === "review" ? "Reviewing…" : "Review draft"}
              </button>
              <button
                disabled={!review?.validation.valid || Boolean(busy)}
                onClick={() => void applyTopology()}
                className="px-3 py-2 min-h-[40px] min-w-[40px] text-xs bg-primary text-on-primary font-bold rounded-lg transition-colors disabled:opacity-40 shadow-sm cursor-pointer"
                type="button"
              >
                {busy === "apply" ? "Applying…" : "Apply topology"}
              </button>
            </div>
          </div>

          <div className="hidden sm:flex items-center gap-2 bg-surface-container/90 backdrop-blur-md px-3 py-1.5 rounded-full border border-outline-variant/20 shadow-md pointer-events-auto text-xs font-label-sm text-on-surface-variant">
            <span className="text-primary font-semibold">{topology ? "TopologyPlan r" + topology.revision : "Draft"}</span>
            <span>•</span>
            <span className="font-code-md text-[11px] truncate max-w-[120px]">
              {topology?.state_hash?.slice(0, 8) ?? "Clean"}
            </span>
          </div>
        </div>

        {message ? (
          <div
            className="absolute top-18 left-4 z-10 bg-surface-container-high/90 backdrop-blur-md border border-outline-variant/30 text-xs px-3 py-1.5 rounded-lg text-primary shadow-md"
            role="status"
          >
            {message}
          </div>
        ) : null}

        <div className="topologyFlow w-full flex-1">
          <CanvasFlow
            edges={edges}
            facts={facts}
            nodes={nodes}
            onConnect={connectApplications}
            onEdgeSelect={selectEdge}
            onMove={move}
            onRemoveEdge={(edge) => {
              const data = edge.data as SelectedDependency | undefined;
              if (data) {
                const source = console.state.services.find((s) => s.id === data.sourceID);
                if (source && data.dependency) {
                  setDepModal({
                    consumer: source,
                    existingDependency: data.dependency,
                  });
                }
              }
            }}
          />
        </div>
      </div>

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
        onApplyTopology={() => void applyTopology()}
        onConfiguration={changeConfiguration}
        onDraft={changeDraft}
        onEditDependency={(dep, consumer) => {
          setDepModal({ consumer, existingDependency: dep });
        }}
        onRealizeDependency={(consumer) => {
          setRealizationModal(consumer);
        }}
        onRemoveDependency={async (consumer, depLogicalName) => {
          const current = consumer.configuration || {
            schema_version: "opsi.service_configuration/v1",
            dependencies: [],
          };
          const nextDraft: ServiceConfigurationDraft = {
            ...current,
            dependencies: (current.dependencies ?? []).filter((d) => d.logical_name !== depLogicalName),
          };
          try {
            const preview = await client.serviceConfigurationPreview(projectID, consumer.id, nextDraft);
            await client.serviceConfigurationApply(
              projectID,
              consumer.id,
              {
                draft: preview.configuration,
                expected_revision: preview.current_revision,
                expected_state_hash: preview.current_state_hash,
              },
              crypto.randomUUID()
            );
            await console.actions.load();
            await onReload();
            setSelectedDependency(null);
          } catch (cause) {
            setMessage((cause as Error).message);
          }
        }}
        onReviewConfiguration={reviewConfiguration}
        repositories={repositories}
        review={review}
        selectedDependency={selectedDependency}
        selectedID={selectedID}
        selectedManagedResource={selectedManagedResource}
        selectedRuntime={selectedRuntime}
        selectedService={selectedService}
        topology={topology}
      />

      {depModal ? (
        <DependencyDialog
          consumer={depModal.consumer}
          existingDependency={depModal.existingDependency}
          facts={facts}
          onApply={async () => {
            await console.actions.load();
            await onReload();
            setDepModal(null);
          }}
          onClose={() => setDepModal(null)}
          projectID={projectID}
          targetIdentityHint={depModal.targetIdentityHint}
          targetKindHint={depModal.targetKindHint}
        />
      ) : null}

      {realizationModal ? (
        <RealizationReviewDialog
          consumer={realizationModal}
          onApplied={async () => {
            await console.actions.load();
            await onReload();
          }}
          onClose={() => setRealizationModal(null)}
          projectID={projectID}
        />
      ) : null}
    </section>
  );
}

export function LiveTopologyCanvas({
  console,
  environment,
  facts,
}: {
  console: ConsoleController;
  environment: PlacementFacts["environments"][number];
  facts: PlacementFacts;
}) {
  const deployments = [...console.state.deployments]
    .filter((deployment) => deployment.environment_id === environment.id)
    .sort((a, b) => b.created_at.localeCompare(a.created_at));
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
    <div className="topologyCanvas relative w-full rounded-2xl overflow-hidden border border-outline-variant/20 bg-surface-container-lowest flex flex-col shadow-xl">
      <div className="flex-1 relative h-full flex flex-col bg-[radial-gradient(#334155_1px,transparent_1px)] [background-size:24px_24px] bg-[position:-12px_-12px]">
        <div className="absolute top-4 left-4 right-4 flex items-center justify-between z-10 pointer-events-none">
          <div className="flex items-center gap-3 bg-surface-container/90 backdrop-blur-md px-4 py-2 rounded-xl border border-outline-variant/20 shadow-md pointer-events-auto">
            <div className="flex items-center gap-2 text-xs font-label-sm text-on-surface font-semibold">
              <Icon name="network_check" className="text-[16px]" />
              <span>Observed runtime facts</span>
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

function buildLiveNodes(
  console: ConsoleController,
  facts: PlacementFacts,
  environmentID: string,
  latest: Map<string, DeploymentJob>,
  select: (id: string) => void
): ResourceFlowNode[] {
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
      context: runtime.type + " · " + runtime.id,
      ariaDetail: "Runtime " + runtime.status + ", Agent " + (agent?.status ?? "not reported"),
      notice: status === "Unknown" ? "Facts are insufficient to establish Ready or Offline." : undefined,
      tone: status === "Ready" ? "ready" : status === "Offline" ? "failed" : "neutral",
      facts: [
        { label: "Runtime", value: runtime.status },
        { label: "Node", value: node ? node.id + " · " + node.status : "Not reported" },
        { label: "Agent", value: agent ? agent.id + " · " + agent.status : "Not reported" },
        {
          label: "Capacity",
          value:
            node?.cpu_cores === undefined || node.memory_mb === undefined
              ? "Not reported"
              : node.cpu_cores + " cores · " + node.memory_mb + " MiB",
        },
        {
          label: "Heartbeat",
          value: agent?.last_seen_at || node?.last_seen_at || record?.last_seen_at || "Not reported",
        },
      ],
    });
    nodes.push({
      id: "runtime:" + runtime.id,
      type: "resource",
      position: { x: 24, y: 28 + index * 230 },
      data: { mode: "live", onSelect: () => select("runtime:" + runtime.id), presentation },
      draggable: false,
      focusable: false,
      style: { width: 310, height: 190 },
    });
  });

  const serviceSlots = new Map<string, number>();
  facts.services.forEach((service) => {
    const deployment = latest.get(service.id) ?? latest.get(service.key);
    const serviceRecord = console.state.services.find(
      (item) => item.id === service.id || item.name === service.key
    );
    const sourceKind = serviceRecord?.type || "application";
    const runtimeIndex =
      sourceKind === "application"
        ? runtimes.findIndex((runtime) => runtime.id === deployment?.runtime_id)
        : -1;
    const lane = runtimeIndex >= 0 ? runtimeIndex : runtimes.length;
    const slot = serviceSlots.get(String(lane)) ?? 0;
    serviceSlots.set(String(lane), slot + 1);
    const state = deployment ? liveDeploymentHealth(deployment) : "Unknown";
    const digest =
      deployment?.current_digest ||
      deployment?.terminal_result?.current_digest ||
      deployment?.desired_digest ||
      deployment?.snapshot?.image.digest;
    const endpoint = deployment?.exposure_spec
      ? "" + deployment.exposure_spec.hostname + deployment.exposure_spec.path
      : "Not reported";
    const runtime = runtimes.find((item) => item.id === deployment?.runtime_id);
    const presentation = topologyResourcePresentation({
      kind: sourceKind,
      name: service.key,
      status: state,
      context: deployment
        ? "Reported on " + (runtime?.name || deployment.runtime_id || "unknown runtime")
        : "No factual deployment reported",
      ariaDetail: deployment ? "Deployment " + deployment.id : "deployment unknown",
      notice:
        deployment?.failure_message_redacted ||
        (deployment ? undefined : "Design placement is intentionally not shown in Live."),
      tone: state === "Running" ? "ready" : state === "Failed" ? "failed" : state === "Unknown" ? "neutral" : "warning",
      facts: [
        { label: "Workload", value: deployment?.rollout_state || deployment?.status || "Not reported" },
        { label: "Image digest", value: digest || "Not reported" },
        { label: "Deployment", value: deployment?.id || "Not reported" },
        { label: "Exposure", value: endpoint },
      ],
    });
    const id = "service:" + service.key;
    nodes.push({
      id,
      type: "resource",
      position: { x: 380 + slot * 280, y: 48 + lane * 230 },
      data: {
        deployment: presentation.supported ? deployment : undefined,
        mode: "live",
        onSelect: () => select(id),
        presentation,
      },
      draggable: false,
      focusable: false,
      style: { width: 250, height: presentation.notice ? 144 : 132 },
    });
  });
  return nodes;
}

function buildLiveEdges(services: ServiceRecord[], nodes: ResourceFlowNode[]): Edge[] {
  const nodeIDs = new Set(nodes.map((node) => node.id));
  const edges: Edge[] = [];
  for (const node of nodes) {
    const deployment = node.data.deployment;
    if (deployment?.runtime_id && nodeIDs.has("runtime:" + deployment.runtime_id)) {
      edges.push({
        id: "placement:" + deployment.id,
        source: "runtime:" + deployment.runtime_id,
        target: node.id,
        label: "reported placement",
        selectable: false,
        style: { stroke: "var(--opsi-live-border)" },
      });
    }
  }
  for (const service of services) {
    for (const dep of service.configuration?.dependencies ?? []) {
      const source = "service:" + service.name;
      const target =
        dep.target_kind === "managed_service" ? "resource:" + dep.target_identity : "service:" + dep.target_identity;
      if (!nodeIDs.has(source) || !nodeIDs.has(target)) continue;
      edges.push({
        id: "dep:" + service.id + ":" + dep.logical_name,
        source,
        target,
        label: dep.protocol === "postgres" ? "PostgreSQL" : dep.protocol === "redis" ? "Valkey" : "HTTP",
        selectable: false,
        animated: false,
      });
    }
  }
  return edges;
}

function LiveResourceInspector({
  environment,
  selected,
}: {
  environment: PlacementFacts["environments"][number];
  selected?: ResourceFlowNode;
}) {
  const resource = selected?.data.presentation;
  const deployment = selected?.data.deployment;
  return (
    <aside
      className="liveInspector w-full lg:w-96 bg-surface-container-low/95 backdrop-blur-xl border-t lg:border-t-0 lg:border-l border-outline-variant/20 p-5 overflow-y-auto space-y-4 shrink-0 shadow-xl"
      aria-labelledby="live-topology-inspector-heading"
      data-resource-state={resource?.supported === false || resource?.kind === "unsupported" ? "unsupported" : "factual"}
    >
      <div className="flex items-start justify-between gap-3 pb-3 border-b border-outline-variant/20">
        <div className="min-w-0">
          <p className="text-[11px] font-code-md text-primary truncate">{environment.name} / Live</p>
          <h3
            className="font-headline-md text-base font-bold text-on-surface truncate mt-0.5"
            id="live-topology-inspector-heading"
            tabIndex={-1}
          >
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
              <h4 className="font-label-sm text-xs text-on-surface-variant uppercase tracking-wider">
                Deployment state
              </h4>
              <dl className="space-y-1">
                <InspectorFact
                  label="Phase"
                  value={
                    deployment.action === "rollback"
                      ? "Rollback"
                      : deployment.base_deployment_id
                      ? "Exposure"
                      : "Workload"
                  }
                />
                <InspectorFact
                  label="Failure code"
                  value={deployment.failure_code || deployment.terminal_result?.failure_code || "None"}
                />
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

function CanvasFlow({
  edges,
  nodes: initialNodes,
  onConnect,
  onEdgeSelect,
  onMove,
  onRemoveEdge,
}: {
  edges: Edge[];
  facts?: PlacementFacts;
  nodes: CanvasNode[];
  onConnect: (connection: Connection) => void;
  onEdgeSelect: (edge: Edge) => void;
  onMove: (serviceKey: string, runtimeID?: string) => void;
  onRemoveEdge: (edge: Edge) => void;
}) {
  void onMove;
  const instance = useRef<ReactFlowInstance<CanvasNode>>(null);
  return (
    <ReactFlow<CanvasNode>
      defaultEdgeOptions={{ selectable: true, type: "default" }}
      edgeTypes={edgeTypes}
      edges={edges}
      elementsSelectable
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
        flow.fitView({ padding: 0.08 });
      }}
      panOnDrag={[1, 2]}
    >
      <Background color="var(--opsi-outline-variant)" gap={24} size={1} />
    </ReactFlow>
  );
}

function buildNodes(
  services: ServiceRecord[],
  nodeRecords: Array<{ id: string; public_host?: string }>,
  facts: PlacementFacts,
  topology: TopologyPlan | null,
  draft: CanvasDraft,
  placements: Map<string, CanvasPlacement>,
  selectedID: string,
  select: (id: string) => void,
  onPointerDown?: (serviceKey: string, e: React.PointerEvent | React.MouseEvent) => void
): CanvasNode[] {
  const groups: CanvasNode[] = [];
  const applications: CanvasNode[] = [];
  const managedResources = (facts.resources ?? []).filter(
    (resource): resource is typeof resource & { kind: "managed_service" } => resource.kind === "managed_service"
  );
  const resources: PlacementResource[] = [
    ...facts.services.map((service) => {
      const svc = services.find((item) => item.id === service.id || item.name === service.key);
      return {
        id: service.id,
        key: service.key,
        kind: svc?.type || "application",
        type: svc?.type || "application",
        lifecycle: "",
        name: service.key,
      };
    }),
    ...managedResources.map((resource) => ({
      id: resource.id,
      key: resource.id,
      kind: resource.kind,
      type: resource.type,
      lifecycle: resource.lifecycle,
      name: resource.name,
      version: resource.version,
      replicas: resource.replicas,
      cpuMillicores: resource.cpu_millicores,
      memoryBytes: resource.memory_bytes,
    })),
  ];
  const groupServices = new Map<string, typeof resources>();
  const runtimeIDs = new Set(facts.runtimes.map((runtime) => runtime.id));
  groupServices.set(
    "unplaced",
    resources.filter((service) => {
      const runtimeID = placements.get(service.key)?.runtime_id;
      return !runtimeID || !runtimeIDs.has(runtimeID);
    })
  );
  for (const runtime of facts.runtimes)
    groupServices.set(
      runtime.id,
      resources.filter((service) => placements.get(service.key)?.runtime_id === runtime.id)
    );
  const maxItems = Math.max(1, ...groupServices.values().map((services) => services.length));
  const groupHeight = Math.max(300, 150 + maxItems * appHeight);
  const unplacedCount = groupServices.get("unplaced")?.length ?? 0;
  groups.push({
    id: "unplaced",
    type: "unplaced",
    position: { x: 24, y: 24 },
    width: groupWidth,
    height: groupHeight,
    initialWidth: groupWidth,
    initialHeight: groupHeight,
    measured: { width: groupWidth, height: groupHeight },
    data: { count: unplacedCount, onSelect: () => select("unplaced") },
    draggable: false,
    focusable: false,
    selected: selectedID === "unplaced",
    style: { width: groupWidth, height: groupHeight },
    zIndex: 0,
  });

  facts.runtimes.forEach((runtime, index) => {
    const factualNodes = facts.nodes.filter((node) => node.runtime_id === runtime.id);
    const agents = facts.agents.filter((agent) => agent.runtime_id === runtime.id);
    const assigned = groupServices.get(runtime.id) ?? [];
    const node = factualNodes[0];
    const agent = agents.find((item) => item.status === "active") ?? agents[0];
    const record = nodeRecords.find((item) => item.id === node?.id);
    const status = serverStatus(factualNodes, agents, runtime.status);
    const id = "runtime:" + runtime.id;
    const presentation = topologyResourcePresentation({
      kind: "server",
      name: runtime.name || record?.public_host || runtime.id,
      status,
      context: assigned.length + " placed " + (assigned.length === 1 ? "app" : "apps"),
      ariaDetail: "Agent " + (agent?.status ?? "Not reported"),
      tone: status === "Ready" ? "ready" : status === "Offline" ? "failed" : "neutral",
      facts: [
        { label: "CPU", value: node?.cpu_cores === undefined ? "Ready" : node.cpu_cores + " cores" },
        { label: "RAM", value: node?.memory_mb === undefined ? "Ready" : node.memory_mb + " MiB" },
        { label: "Agent", value: agent?.status ?? "Active" },
      ],
    });
    const posX = 24 + (index + 1) * (groupWidth + 28);
    groups.push({
      id,
      type: "resource",
      position: { x: posX, y: 24 },
      width: groupWidth,
      height: groupHeight,
      initialWidth: groupWidth,
      initialHeight: groupHeight,
      measured: { width: groupWidth, height: groupHeight },
      handles: [
        { id: "target", type: "target", position: Position.Left, x: 0, y: Math.floor(groupHeight / 2) - 6, width: 12, height: 12 },
        { id: "source", type: "source", position: Position.Right, x: groupWidth - 12, y: Math.floor(groupHeight / 2) - 6, width: 12, height: 12 },
      ],
      data: { canvasTarget: id, onSelect: () => select(id), presentation },
      draggable: false,
      focusable: false,
      selected: selectedID === id,
      style: { width: groupWidth, height: groupHeight },
      zIndex: 0,
    });
  });

  for (const [parent, servicesList] of groupServices) {
    const parentIndex = parent === "unplaced" ? -1 : facts.runtimes.findIndex((r) => r.id === parent);
    const parentX = 24 + (parentIndex + 1) * (groupWidth + 28);
    const parentY = 24;
    servicesList.forEach((service, index) => {
      const placement = placements.get(service.key) ?? { runtime_id: null };
      const runtime = facts.runtimes.find((item) => item.id === placement.runtime_id);
      const sourceKind =
        service.kind === "managed_service"
          ? "managed_service"
          : services.find((item) => item.id === service.id || item.name === service.key)?.type ||
            "application";
      const status = canvasDraftStatus(topology, draft, service.key);
      const id = service.kind === "managed_service" ? "resource:" + service.key : "service:" + service.key;
      const assignment = placement.runtime_id ? "Assigned" : "Unplaced";
      const issues = canvasDraftIssues(placement).length;
      const presentation = topologyResourcePresentation({
        kind: sourceKind,
        name: service.name,
        status: assignment,
        badge: status,
        context: "" + (runtime?.name || runtime?.id || (placement.runtime_id ? "" + placement.runtime_id : "Unplaced")),
        ariaDetail: status,
        draftState: status,
        notice: issues ? issues + " missing fields" : undefined,
        tone: status === "pending removal" ? "failed" : status === "unchanged" ? "neutral" : "warning",
        facts:
          service.kind === "managed_service"
            ? [
                { label: "Type", value: service.type },
                { label: "Lifecycle", value: service.lifecycle },
                { label: "Version", value: service.version || "default" },
              ]
            : [
                { label: "Replicas", value: String(placement.replicas ?? 1) },
                { label: "CPU", value: (placement.cpu_request_millicores ?? 100) + "m" },
                { label: "Memory", value: mib(placement.memory_request_bytes ?? 128 * 1024 * 1024) + " MiB" },
              ],
      });
      const posX = parentX + 20;
      const posY = parentY + 136 + index * appHeight;
      const nodeHeight = issues ? 112 : 108;
      const nodeWidth = groupWidth - 40;
      const isSelected = selectedID === id;
      applications.push({
        id,
        type: "resource",
        position: { x: posX, y: posY },
        width: nodeWidth,
        height: nodeHeight,
        initialWidth: nodeWidth,
        initialHeight: nodeHeight,
        measured: { width: nodeWidth, height: nodeHeight },
        handles: [
          { id: "target", type: "target", position: Position.Left, x: 0, y: Math.floor(nodeHeight / 2) - 6, width: 12, height: 12 },
          { id: "source", type: "source", position: Position.Right, x: nodeWidth - 12, y: Math.floor(nodeHeight / 2) - 6, width: 12, height: 12 },
        ],
        data: {
          onPointerDown: onPointerDown ? (e) => onPointerDown(service.key, e) : undefined,
          onSelect: () => select(id),
          presentation,
          serviceKey: service.key,
        },
        draggable: presentation.capabilities.movable,
        focusable: false,
        selected: isSelected,
        style: { width: nodeWidth, height: nodeHeight },
        zIndex: 1,
      });
    });
  }
  return [...groups, ...applications];
}

function buildConnectionEdges(
  services: ServiceRecord[],
  drafts: ConfigurationDrafts = {},
  selectedID = ""
): Edge[] {
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
      const target = services.find((service) => service.id === binding?.target_service_id || service.name === binding?.target_service_key);
      if (!binding || !target) continue;
      const status = !before ? "pending add" : !after ? "pending removal" : sameBinding(before, after) ? "applied" : "pending change";
      const edgeId = applicationConnectionEdgeID(source.id, key);
      const label = `${binding.kind === "internal_http" ? "Internal" : "Browser"} · ${status}`;
      const animated = status === "pending add" || status === "pending change";
      const stroke = status === "pending removal" ? "var(--color-error)" : status === "applied" ? "var(--color-status-ready)" : "var(--color-status-warning)";
      const strokeDasharray = status === "pending removal" ? "6 5" : undefined;
      const sourceNodeId = applicationTopologyNodeID(source.name);
      const targetNodeId = applicationTopologyNodeID(target.name);
      edges.push({
        id: edgeId,
        source: sourceNodeId,
        target: targetNodeId,
        sourceHandle: topologyHandleIDs.source,
        targetHandle: topologyHandleIDs.target,
        label,
        selected: selectedID === edgeId,
        data: { connectionKey: key, status, sourceID: source.id, targetID: target.id },
        animated,
        style: {
          stroke,
          strokeDasharray,
          strokeWidth: 2,
        },
        labelStyle: { fill: "var(--color-on-surface)", fontSize: 11, fontWeight: 700 },
      });
    }

    const config = source.configuration;
    const dependencies = config?.dependencies ?? [];
    const boundLogicalNames = new Set(
      (config?.resource_bindings ?? []).filter((b) => Boolean(b.binding_id)).map((b) => b.logical_name)
    );

    if (dependencies.length > 0) {
      for (const dep of dependencies) {
        if (dep.target_kind === "managed_service") {
          const targetNodeId = "resource:" + dep.target_identity;
          const isBound = boundLogicalNames.has(dep.logical_name);
          const status = isBound ? "Ready" : "Needs setup";
          const label =
            dep.protocol === "postgres"
              ? "PostgreSQL · " + dep.logical_name
              : "Valkey · " + dep.logical_name;
          const depEdgeId = dependencyTopologyEdgeID(source.id, dep.logical_name);
          const stroke = isBound ? "var(--color-status-ready)" : "var(--color-status-warning)";
          const strokeDasharray = isBound ? undefined : "6 4";
          const sourceNodeId = "service:" + source.name;
          edges.push({
            id: depEdgeId,
            source: sourceNodeId,
            target: targetNodeId,
            sourceHandle: "source",
            targetHandle: "target",
            label: label + " · " + status,
            selected: selectedID === depEdgeId,
            data: {
              sourceID: source.id,
              logicalName: dep.logical_name,
              dependency: dep,
              status,
              isManaged: true,
              hasBinding: isBound,
            },
            animated: !isBound,
            style: {
              stroke,
              strokeDasharray,
              strokeWidth: 2,
            },
            labelStyle: { fill: "var(--color-on-surface)", fontSize: 11, fontWeight: 700 },
          });
        } else if (dep.target_kind === "application") {
          const target = services.find((s) => s.id === dep.target_identity || s.name === dep.target_identity);
          if (target) {
            const label =
              dep.strategy === "same_origin"
                ? "Same origin (" + (dep.path || "/api") + ")"
                : dep.strategy === "internal_http"
                ? "Internal HTTP"
                : "Public HTTP";
            const depEdgeId = dependencyTopologyEdgeID(source.id, dep.logical_name);
            const sourceNodeId = "service:" + source.name;
            const targetNodeId = "service:" + target.name;
            edges.push({
              id: depEdgeId,
              source: sourceNodeId,
              target: targetNodeId,
              sourceHandle: "source",
              targetHandle: "target",
              label: "HTTP · " + label,
              selected: selectedID === depEdgeId,
              data: {
                sourceID: source.id,
                logicalName: dep.logical_name,
                dependency: dep,
                status: "Ready",
                isManaged: false,
              },
              style: {
                stroke: "var(--color-status-ready)",
                strokeWidth: 2,
              },
              labelStyle: { fill: "var(--color-on-surface)", fontSize: 11, fontWeight: 700 },
            });
          }
        }
      }
    }
  }
  return edges;
}

function TopologyInspector({
  busy,
  configurationDrafts,
  configurationReview,
  console,
  draft,
  facts,
  onApplyConfiguration,
  onApplyTopology,
  onConfiguration,
  onDraft,
  onEditDependency,
  onRealizeDependency,
  onRemoveDependency,
  onReviewConfiguration,
  review,
  selectedDependency,
  selectedID,
  selectedManagedResource,
  selectedService,
  topology,
}: {
  bindings: GitHubBinding[];
  builds: BuildRecord[];
  busy: "" | "review" | "apply";
  configurationDrafts: ConfigurationDrafts;
  configurationReview: ConfigurationReview | null;
  console: ConsoleController;
  draft: CanvasDraft;
  facts: PlacementFacts;
  onApplyConfiguration: (service: ServiceRecord) => Promise<void>;
  onApplyTopology?: () => void;
  onConfiguration: (service: ServiceRecord, draft: ServiceConfigurationDraft) => void;
  onDraft: (draft: CanvasDraft) => void;
  onEditDependency: (dep: ApplicationDependency, consumer: ServiceRecord) => void;
  onRealizeDependency: (consumer: ServiceRecord) => void;
  onRemoveDependency: (consumer: ServiceRecord, logicalName: string) => Promise<void>;
  onReviewConfiguration: (service: ServiceRecord) => Promise<void>;
  repositories: GitHubRepository[];
  review?: DraftReview | null;
  selectedDependency: SelectedDependency | null;
  selectedID?: string;
  selectedManagedResource?: NonNullable<PlacementFacts["resources"]>[number];
  selectedRuntime?: PlacementFacts["runtimes"][number];
  selectedService?: PlacementFacts["services"][number];
  topology: TopologyPlan | null;
}) {
  if (review) {
    return (
      <aside
        className="w-full lg:w-96 bg-surface-container-low/95 backdrop-blur-xl border-t lg:border-t-0 lg:border-l border-outline-variant/20 p-5 overflow-y-auto space-y-4 shrink-0 shadow-xl"
        aria-labelledby="draft-review-heading"
      >
        <DraftReviewPanel busy={busy === "apply"} onApply={() => onApplyTopology?.()} review={review} />
      </aside>
    );
  }

  if (selectedID?.startsWith("connection:")) {
    const parts = selectedID.split(":");
    const sourceID = parts[1];
    const targetID = parts[2];
    const source = console.state.services.find((s) => s.id === sourceID || s.name === sourceID);
    if (source) {
      const currentDraft = configurationDraft(source, configurationDrafts);
      const applied = source.configuration?.bindings?.find((b) => connectionKey(b) === targetID);
      const binding = currentDraft.bindings?.find((b) => connectionKey(b) === targetID);
      const target = console.state.services.find((s) => s.id === (binding ?? applied)?.target_service_id || s.name === (binding ?? applied)?.target_service_key);
      const update = (next: ServiceBinding) =>
        onConfiguration(source, {
          ...currentDraft,
          bindings: (currentDraft.bindings ?? []).map((item) => (connectionKey(item) === targetID ? next : item)),
        });

      if (!binding && applied) {
        return (
          <aside
            className="w-full lg:w-96 bg-surface-container-low/95 backdrop-blur-xl border-t lg:border-t-0 lg:border-l border-outline-variant/20 p-5 overflow-y-auto space-y-4 shrink-0 shadow-xl"
            aria-labelledby="topology-inspector-heading"
          >
            <div className="flex items-start justify-between gap-3 pb-3 border-b border-outline-variant/20">
              <div className="min-w-0">
                <p className="text-[11px] font-code-md text-primary truncate">
                  {source.name} → {target?.name ?? applied.target_service_key}
                </p>
                <h3
                  className="font-headline-md text-base font-bold text-on-surface truncate mt-0.5"
                  id="topology-inspector-heading"
                  tabIndex={-1}
                >
                  Pending connection removal
                </h3>
              </div>
            </div>
            <p className="text-xs text-on-surface-variant">
              The applied connection remains visible until this service configuration is reviewed and applied.
            </p>
            <Button
              disabled={Boolean(busy)}
              onClick={() => void onReviewConfiguration(source)}
              size="sm"
              variant="primary"
              className="w-full"
            >
              Review removal
            </Button>
            {configurationReview?.serviceID === source.id ? (
              <ConfigurationReviewPanel
                busy={busy === "apply"}
                onApply={() => void onApplyConfiguration(source)}
                review={configurationReview}
              />
            ) : null}
          </aside>
        );
      }

      if (binding) {
        return (
          <aside
            className="w-full lg:w-96 bg-surface-container-low/95 backdrop-blur-xl border-t lg:border-t-0 lg:border-l border-outline-variant/20 p-5 overflow-y-auto space-y-4 shrink-0 shadow-xl"
            aria-labelledby="topology-inspector-heading"
          >
            <div className="flex items-start justify-between gap-3 pb-3 border-b border-outline-variant/20">
              <div className="min-w-0">
                <p className="text-[11px] font-code-md text-primary truncate">
                  {source.name} → {target?.name ?? binding.target_service_key}
                </p>
                <h3
                  className="font-headline-md text-base font-bold text-on-surface truncate mt-0.5"
                  id="topology-inspector-heading"
                  tabIndex={-1}
                >
                  HTTP connection
                </h3>
              </div>
            </div>
            <form className="space-y-3" onSubmit={(event) => event.preventDefault()}>
              <label className="flex flex-col gap-1.5 font-medium text-xs text-on-surface-variant">
                <span>Runtime intent</span>
                <select
                  aria-label="Runtime intent"
                  className="w-full bg-surface-container border border-outline-variant/30 rounded-xl px-3 py-2 text-xs text-on-surface focus:outline-none focus:border-primary/50"
                  onChange={(event) => update(bindingForKind(binding, event.target.value as ServiceBinding["kind"]))}
                  value={binding.kind}
                >
                  <option value="internal_http">Internal HTTP</option>
                  <option value="browser_http">Browser HTTP</option>
                </select>
              </label>
              {binding.kind === "internal_http" ? (
                <label className="flex flex-col gap-1.5 font-medium text-xs text-on-surface-variant">
                  <span>Environment prefix</span>
                  <input
                    aria-label="Environment prefix"
                    className="w-full bg-surface-container border border-outline-variant/30 rounded-xl px-3 py-2 text-xs text-on-surface focus:outline-none focus:border-primary/50"
                    onChange={(event) => update({ ...binding, env_prefix: event.target.value })}
                    placeholder="API"
                    value={binding.env_prefix ?? ""}
                  />
                </label>
              ) : (
                <>
                  <label className="flex flex-col gap-1.5 font-medium text-xs text-on-surface-variant">
                    <span>Environment name</span>
                    <input
                      aria-label="Environment name"
                      className="w-full bg-surface-container border border-outline-variant/30 rounded-xl px-3 py-2 text-xs text-on-surface focus:outline-none focus:border-primary/50"
                      onChange={(event) => update({ ...binding, env_name: event.target.value })}
                      placeholder="API_BASE_URL"
                      value={binding.env_name ?? ""}
                    />
                  </label>
                  <label className="flex flex-col gap-1.5 font-medium text-xs text-on-surface-variant">
                    <span>Same-origin path</span>
                    <input
                      aria-label="Same-origin path"
                      className="w-full bg-surface-container border border-outline-variant/30 rounded-xl px-3 py-2 text-xs text-on-surface focus:outline-none focus:border-primary/50"
                      onChange={(event) => update({ ...binding, path: event.target.value })}
                      placeholder="/api"
                      value={binding.path ?? "/api"}
                    />
                  </label>
                </>
              )}
            </form>
            <p className="text-[11px] text-on-surface-variant">
              {binding.kind === "internal_http"
                ? "Cloud generates HOST, PORT, and URL from factual runtime DNS and target port."
                : "Browser HTTP emits a path only; source and target must share a public hostname."}
            </p>
            <div className="pt-2 flex flex-col gap-2">
              <Button
                onClick={() => {
                  onConfiguration(source, {
                    ...currentDraft,
                    bindings: (currentDraft.bindings ?? []).filter((item) => connectionKey(item) !== targetID),
                  });
                }}
                size="sm"
                variant="ghost"
                className="w-full text-status-failed"
              >
                Remove connection
              </Button>
              <Button
                disabled={Boolean(busy)}
                onClick={() => void onReviewConfiguration(source)}
                size="sm"
                variant="primary"
                className="w-full"
              >
                {busy === "review" ? "Reviewing…" : "Review connection"}
              </Button>
            </div>
            {configurationReview?.serviceID === source.id ? (
              <ConfigurationReviewPanel
                busy={busy === "apply"}
                onApply={() => void onApplyConfiguration(source)}
                review={configurationReview}
              />
            ) : null}
          </aside>
        );
      }
    }
  }

  if (selectedDependency) {
    const consumer = console.state.services.find((s) => s.id === selectedDependency.sourceID);
    if (consumer && selectedDependency.dependency) {
      const dep = selectedDependency.dependency;
      return (
        <aside
          className="w-full lg:w-96 bg-surface-container-low/95 backdrop-blur-xl border-t lg:border-t-0 lg:border-l border-outline-variant/20 p-5 overflow-y-auto space-y-4 shrink-0 shadow-xl"
          aria-labelledby="topology-inspector-heading"
        >
          <div className="flex items-start justify-between gap-3 pb-3 border-b border-outline-variant/20">
            <div className="min-w-0">
              <p className="text-[11px] font-code-md text-primary truncate">
                {consumer.name} → {dep.target_identity}
              </p>
              <h3
                className="font-headline-md text-base font-bold text-on-surface truncate mt-0.5"
                id="topology-inspector-heading"
                tabIndex={-1}
              >
                {dep.logical_name}
              </h3>
            </div>
            <StatusBadge label={selectedDependency.status} value={selectedDependency.hasBinding ? "healthy" : "in_progress"} />
          </div>

          <section className="bg-surface-container-high/60 p-4 rounded-xl border border-outline-variant/10 space-y-2">
            <h4 className="font-label-sm text-xs text-on-surface-variant uppercase tracking-wider">
              Contract Specification
            </h4>
            <dl className="space-y-1">
              <InspectorFact label="Protocol" value={dep.protocol} />
              <InspectorFact label="Target Kind" value={dep.target_kind.replace("_", " ")} />
              <InspectorFact label="Target" value={dep.target_identity} />
              <InspectorFact label="Phase" value={dep.injection_phase} />
              <InspectorFact label="Requirement" value={dep.required ? "Required" : "Optional"} />
              {dep.strategy ? <InspectorFact label="Strategy" value={dep.strategy.replace("_", " ")} /> : null}
            </dl>
          </section>

          {dep.injection_mappings && dep.injection_mappings.length > 0 ? (
            <section className="bg-surface-container-high/60 p-4 rounded-xl border border-outline-variant/10 space-y-2">
              <h4 className="font-label-sm text-xs text-on-surface-variant uppercase tracking-wider">
                Environment Blueprint
              </h4>
              <ul className="space-y-1 font-code-md text-xs">
                {dep.injection_mappings.map((m, idx) => (
                  <li key={idx} className="flex items-center justify-between py-0.5">
                    <span className="text-on-surface font-bold">{m.env_name}</span>
                    <span className="text-on-surface-variant">← {formatSymbolicSource(m.symbolic_source, dep.protocol)}</span>
                  </li>
                ))}
              </ul>
            </section>
          ) : null}

          <div className="pt-2 flex flex-col gap-2">
            {dep.target_kind === "managed_service" && !selectedDependency.hasBinding ? (
              <Button
                onClick={() => onRealizeDependency(consumer)}
                size="sm"
                variant="primary"
                className="w-full"
              >
                Review Connection & Realize
              </Button>
            ) : null}
            <Button
              onClick={() => onEditDependency(dep, consumer)}
              size="sm"
              variant="secondary"
              className="w-full"
            >
              Edit Dependency Contract
            </Button>
            <Button
              onClick={() => void onRemoveDependency(consumer, dep.logical_name)}
              size="sm"
              variant="ghost"
              className="w-full text-status-failed"
            >
              Remove Dependency
            </Button>
          </div>
        </aside>
      );
    }
  }

  if (selectedManagedResource) {
    const placement = canvasPlacement(topology, draft, selectedManagedResource.id);
    const runtime = facts.runtimes.find((item) => item.id === placement.runtime_id);
    const status = canvasDraftStatus(topology, draft, selectedManagedResource.id);
    return (
      <aside
        className="w-full lg:w-96 bg-surface-container-low/95 backdrop-blur-xl border-t lg:border-t-0 lg:border-l border-outline-variant/20 p-5 overflow-y-auto space-y-4 shrink-0 shadow-xl"
        aria-labelledby="topology-inspector-heading"
      >
        <div className="flex items-start justify-between gap-3 pb-3 border-b border-outline-variant/20">
          <div className="min-w-0">
            <p className="text-[11px] font-code-md text-primary truncate">{runtime?.name ?? "Unplaced"}</p>
            <h3
              className="font-headline-md text-base font-bold text-on-surface truncate mt-0.5"
              id="topology-inspector-heading"
              tabIndex={-1}
            >
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
            <InspectorFact
              label="CPU"
              value={
                selectedManagedResource.cpu_millicores === undefined
                  ? "Not reported"
                  : selectedManagedResource.cpu_millicores + "m"
              }
            />
            <InspectorFact
              label="Memory"
              value={
                selectedManagedResource.memory_bytes === undefined
                  ? "Not reported"
                  : mib(selectedManagedResource.memory_bytes) + " MiB"
              }
            />
          </dl>
          <div className="pt-3">
            <button
              onClick={() =>
                console.navigate({
                  view: "infrastructure",
                  tab: "resources",
                  resource: selectedManagedResource.id,
                })
              }
              className="w-full py-2 px-3 bg-surface-container hover:bg-surface-container-high text-on-surface text-xs font-medium rounded-lg border border-outline-variant/20 transition-colors"
              type="button"
            >
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
    const status = canvasDraftStatus(topology, draft, selectedService.key);
    const serviceRecord = console.state.services.find((s) => s.id === selectedService.id || s.name === selectedService.key);
    const deps = serviceRecord?.configuration?.dependencies ?? [];
    const edit = (patch: Partial<CanvasPlacement>) => onDraft(updateCanvasPlacement(topology, draft, selectedService.key, patch));

    return (
      <aside
        className="w-full lg:w-96 bg-surface-container-low/95 backdrop-blur-xl border-t lg:border-t-0 lg:border-l border-outline-variant/20 p-5 overflow-y-auto space-y-4 shrink-0 shadow-xl"
        aria-labelledby="topology-inspector-heading"
      >
        <div className="flex items-start justify-between gap-3 pb-3 border-b border-outline-variant/20">
          <div className="min-w-0">
            <p className="text-[11px] font-code-md text-primary truncate">
              {environment?.name ?? (placement.runtime_id ? "Unknown environment" : "Unplaced")} / {runtime?.name ?? placement.runtime_id ?? "Unplaced"} / {selectedService.key}
            </p>
            <h3
              className="font-headline-md text-base font-bold text-on-surface truncate mt-0.5"
              id="topology-inspector-heading"
              tabIndex={-1}
            >
              {serviceRecord?.name || selectedService.key}
            </h3>
          </div>
          <StatusBadge label={status} value={status === "unchanged" ? "healthy" : "in_progress"} />
        </div>

        <dl className="space-y-1 bg-surface-container-high/60 p-4 rounded-xl border border-outline-variant/10">
          <InspectorFact label="Live assignment" value={runtimeLabel(facts, live?.runtime_id)} />
          <InspectorFact label="Draft assignment" value={runtimeLabel(facts, placement.runtime_id)} />
        </dl>

        <form className="space-y-3" onSubmit={(event) => event.preventDefault()}>
          <label className="flex flex-col gap-1.5 font-medium text-xs text-on-surface-variant">
            <span>Replicas</span>
            <input
              aria-label="Replicas"
              className="w-full bg-surface-container border border-outline-variant/30 rounded-xl px-3 py-2 text-xs text-on-surface focus:outline-none focus:border-primary/50"
              disabled={!placement.runtime_id}
              max="100"
              min="1"
              onChange={(event) => edit({ replicas: numberValue(event) })}
              required
              step="1"
              type="number"
              value={placement.replicas ?? ""}
            />
          </label>
          <label className="flex flex-col gap-1.5 font-medium text-xs text-on-surface-variant">
            <span>CPU request (millicores)</span>
            <input
              aria-label="CPU request (millicores)"
              className="w-full bg-surface-container border border-outline-variant/30 rounded-xl px-3 py-2 text-xs text-on-surface focus:outline-none focus:border-primary/50"
              disabled={!placement.runtime_id}
              max="1000000"
              min="1"
              onChange={(event) => edit({ cpu_request_millicores: numberValue(event) })}
              required
              step="1"
              type="number"
              value={placement.cpu_request_millicores ?? ""}
            />
          </label>
          <label className="flex flex-col gap-1.5 font-medium text-xs text-on-surface-variant">
            <span>Memory (MiB)</span>
            <input
              aria-label="Memory (MiB)"
              className="w-full bg-surface-container border border-outline-variant/30 rounded-xl px-3 py-2 text-xs text-on-surface focus:outline-none focus:border-primary/50"
              disabled={!placement.runtime_id}
              max="1073741824"
              min="1"
              onChange={(event) => edit({ memory_request_bytes: mibValue(event) })}
              required
              step="1"
              type="number"
              value={
                placement.memory_request_bytes === undefined
                  ? ""
                  : Math.round(placement.memory_request_bytes / 1024 / 1024)
              }
            />
          </label>
          <label className="flex flex-col gap-1.5 font-medium text-xs text-on-surface-variant">
            <span>Exposure</span>
            <select
              aria-label="Exposure"
              className="w-full bg-surface-container border border-outline-variant/30 rounded-xl px-3 py-2 text-xs text-on-surface focus:outline-none focus:border-primary/50"
              disabled={!placement.runtime_id}
              onChange={(event) =>
                edit({ exposure: { mode: event.target.value as "none" | "internal" | "public" } })
              }
              value={placement.exposure?.mode ?? "none"}
            >
              <option value="none">None</option>
              <option value="internal">Internal</option>
              <option value="public">Public</option>
            </select>
          </label>
        </form>

        {deps.length > 0 ? (
          <section className="bg-surface-container-high/60 p-4 rounded-xl border border-outline-variant/10 space-y-2">
            <div className="flex items-center justify-between">
              <h4 className="font-label-sm text-xs text-on-surface-variant uppercase tracking-wider">
                Dependencies ({deps.length})
              </h4>
            </div>
            <ul className="space-y-1.5 font-code-md text-xs">
              {deps.map((dep) => (
                <li
                  key={dep.logical_name}
                  className="p-2 rounded bg-surface-container flex items-center justify-between cursor-pointer hover:bg-surface-container-highest transition-colors"
                  onClick={() =>
                    onEditDependency(dep, serviceRecord!)
                  }
                >
                  <span className="text-on-surface font-semibold">{dep.logical_name}</span>
                  <span className="text-on-surface-variant text-[11px]">{dep.protocol} → {dep.target_identity}</span>
                </li>
              ))}
            </ul>
          </section>
        ) : null}

        <div className="pt-2">
          {serviceRecord ? (
            <Button
              onClick={() => onEditDependency({
                logical_name: "",
                protocol: "postgres",
                target_kind: "managed_service",
                target_identity: "",
                required: true,
                injection_phase: "runtime",
                injection_mappings: [],
              }, serviceRecord)}
              size="sm"
              variant="primary"
              className="w-full"
            >
              Add Dependency Contract
            </Button>
          ) : null}
        </div>
      </aside>
    );
  }

  return (
    <aside
      className="w-full lg:w-96 bg-surface-container-low/95 backdrop-blur-xl border-t lg:border-t-0 lg:border-l border-outline-variant/20 p-5 overflow-y-auto space-y-4 shrink-0 shadow-xl"
      aria-labelledby="topology-inspector-heading"
    >
      <div className="flex items-start justify-between gap-3 pb-3 border-b border-outline-variant/20">
        <div className="min-w-0">
          <h3
            className="font-headline-md text-base font-bold text-on-surface truncate mt-0.5"
            id="topology-inspector-heading"
            tabIndex={-1}
          >
            Topology Inspector
          </h3>
        </div>
      </div>
      <p className="text-xs text-on-surface-variant">
        Select a server, application, managed database/cache, or connection edge on the canvas to inspect facts and edit contracts.
      </p>
    </aside>
  );
}

function configurationDraft(service: ServiceRecord, drafts: ConfigurationDrafts): ServiceConfigurationDraft {
  if (Object.hasOwn(drafts, service.id)) return drafts[service.id];
  const configuration = service.configuration;
  return {
    schema_version: "opsi.service_configuration/v1",
    environment: configuration?.environment ?? [],
    public_route: configuration?.public_route,
    bindings: configuration?.bindings ?? [],
    resource_bindings: configuration?.resource_bindings ?? [],
    dependencies: configuration?.dependencies ?? [],
  };
}

function serviceForNode(services: ServiceRecord[], nodeID: string | null) {
  return services.find(
    (service) =>
      "service:" + service.name === nodeID ||
      "service:" + service.id === nodeID ||
      service.id === nodeID ||
      service.name === nodeID
  );
}

function InspectorFact({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-center justify-between py-1 border-b border-outline-variant/10 text-xs">
      <dt className="text-on-surface-variant font-label-sm">{label}</dt>
      <dd className="font-code-md text-on-surface font-medium truncate max-w-[160px]">{value}</dd>
    </div>
  );
}

function mib(bytes: number) {
  return Math.round(bytes / 1024 / 1024);
}

function runtimeLabel(facts: PlacementFacts, runtimeID?: string | null) {
  const runtime = facts.runtimes.find((item) => item.id === runtimeID);
  return runtime ? runtime.name + " · " + runtime.id : runtimeID ? runtimeID + " · not reported" : "Unplaced";
}

function selectKeyDown(event: React.KeyboardEvent, select: () => void) {
  if (event.key === "Enter") {
    select();
    window.requestAnimationFrame(() => {
      document.getElementById("topology-inspector-heading")?.focus();
    });
  }
  if (event.key === " ") event.preventDefault();
}

function selectKeyUp(event: React.KeyboardEvent, select: () => void) {
  if (event.key === " ") {
    select();
    window.requestAnimationFrame(() => {
      document.getElementById("topology-inspector-heading")?.focus();
    });
  }
}

function resolveSelection(id: string | undefined, facts: PlacementFacts) {
  const managed = facts.resources?.find((resource) => resource.kind === "managed_service");
  if (!id)
    return facts.services[0]
      ? "service:" + facts.services[0].key
      : managed
      ? "resource:" + managed.id
      : facts.runtimes[0]
      ? "runtime:" + facts.runtimes[0].id
      : "unplaced";
  if (id.startsWith("node:")) return "runtime:" + (facts.nodes.find((node) => node.id === id.slice(5))?.runtime_id ?? "");
  if (id.startsWith("agent:")) return "runtime:" + (facts.agents.find((agent) => agent.id === id.slice(6))?.runtime_id ?? "");
  if (id.startsWith("environment:"))
    return "runtime:" + (facts.runtimes.find((runtime) => runtime.environment_id === id.slice(12))?.id ?? "");
  return id;
}

function numberValue(event: React.ChangeEvent<HTMLInputElement>) {
  return Number.isFinite(event.target.valueAsNumber) ? event.target.valueAsNumber : undefined;
}

function mibValue(event: React.ChangeEvent<HTMLInputElement>) {
  const value = numberValue(event);
  return value === undefined ? undefined : value * 1024 * 1024;
}

function assignmentSummary(assignment?: TopologyDiff["changes"][number]["before"]) {
  return assignment
    ? `${assignment.runtime_id}, ${assignment.replicas} replicas, ${assignment.cpu_request_millicores}m, ${mib(assignment.memory_request_bytes)} MiB, ${assignment.exposure.mode}`
    : "unplaced";
}

function issueScope(issue: TopologyValidation["issues"][number]) {
  return [issue.service_key, issue.runtime_id].filter(Boolean).join(" / ")
    ? `${[issue.service_key, issue.runtime_id].filter(Boolean).join(" / ")}: `
    : "";
}

function capacityCPU(value: number | undefined, unknown: boolean) {
  return unknown || value === undefined ? "Unknown CPU" : `${value}m CPU`;
}

function capacityMemory(value: number | undefined, unknown: boolean) {
  return unknown || value === undefined ? "Unknown memory" : `${mib(value)} MiB memory`;
}

function connectionKey(binding: ServiceBinding) {
  return binding.target_service_id || binding.target_service_key;
}

function sameBinding(a?: ServiceBinding, b?: ServiceBinding) {
  if (!a || !b) return false;
  if (a.kind !== b.kind) return false;
  if ((a.target_service_id || a.target_service_key) !== (b.target_service_id || b.target_service_key)) return false;
  if (a.kind === "internal_http") return (a.env_prefix ?? "") === (b.env_prefix ?? "");
  if (a.kind === "browser_http") return (a.env_name ?? "") === (b.env_name ?? "") && (a.path ?? "/api") === (b.path ?? "/api");
  return false;
}

function envPrefix(value: string) {
  return value.toUpperCase().replace(/[^A-Z0-9]+/g, "_").replace(/^_+|_+$/g, "") || "SERVICE";
}

function bindingForKind(binding: ServiceBinding, kind: ServiceBinding["kind"]): ServiceBinding {
  return kind === "internal_http"
    ? { kind, target_service_id: binding.target_service_id, target_service_key: binding.target_service_key, env_prefix: envPrefix(binding.target_service_key) }
    : { kind, target_service_id: binding.target_service_id, target_service_key: binding.target_service_key, env_name: `${envPrefix(binding.target_service_key)}_BASE_URL`, path: "/api" };
}

function DraftReviewPanel({ busy, onApply, review }: { busy: boolean; onApply: () => void; review: DraftReview }) {
  return (
    <section className="draftReview bg-surface-container-low border border-outline-variant/20 rounded-2xl p-5 shadow-xl space-y-4" aria-label="Cloud topology review" aria-labelledby="draft-review-heading">
      <h3 className="font-headline-md text-lg font-bold text-on-surface" id="draft-review-heading">Cloud topology review</h3>
      <p className="text-xs text-on-surface-variant">
        {review.validation.valid ? (
          <>
            <span>Cloud validation passed</span>. The reviewed canonical draft is eligible to apply.
          </>
        ) : (
          <>
            <span>Cloud validation failed</span>. Apply remains disabled until the draft is reviewed as valid.
          </>
        )}
      </p>
      <div className="grid grid-cols-2 gap-3 bg-surface-container p-3 rounded-xl border border-outline-variant/10 text-xs font-code-md">
        <div>
          <span className="text-[10px] text-on-surface-variant uppercase font-semibold block">Current revision</span>
          <strong className="text-on-surface font-bold">{review.diff.current_revision}</strong>
          <code className="block text-[10px] opacity-70 truncate">{review.diff.current_hash || review.preview.state_hash || "No current state hash"}</code>
        </div>
        <div>
          <span className="text-[10px] text-on-surface-variant uppercase font-semibold block">Proposed hash</span>
          <code className="block text-[10px] font-bold text-primary truncate">{review.diff.proposed_hash}</code>
        </div>
      </div>
      <h4 className="font-label-sm text-xs font-semibold text-on-surface uppercase tracking-wider">Cloud semantic diff</h4>
      {review.diff.changes.length ? (
        <ul className="space-y-2 text-xs">
          {review.diff.changes.map((change) => (
            <li key={change.service_key} className="bg-surface-container/60 p-2.5 rounded-lg border border-outline-variant/10">
              <strong className="text-on-surface font-bold">{change.service_key}</strong> · {change.change}
              <br />
              <code className="font-code-md text-[11px] text-on-surface-variant">{assignmentSummary(change.before)} → {assignmentSummary(change.after)}</code>
            </li>
          ))}
        </ul>
      ) : (
        <p className="text-xs text-on-surface-variant">No semantic changes.</p>
      )}
      <h4 className="font-label-sm text-xs font-semibold text-on-surface uppercase tracking-wider">Validation issues</h4>
      {review.validation.issues.length ? (
        <ul className="draftIssues space-y-1.5 text-xs text-error">
          {review.validation.issues.map((issue, index) => (
            <li key={`${issue.code}:${index}`} className="flex items-center gap-1.5">
              <strong className="font-bold">{issue.code}</strong> · {issueScope(issue)}{issue.message}
            </li>
          ))}
        </ul>
      ) : (
        <p className="text-xs text-on-surface-variant">No service-level validation issues.</p>
      )}
      <div className="space-y-2">
        {review.validation.runtimes.map((runtime) => (
          <div key={runtime.runtime_id} className="bg-surface-container/40 p-2.5 rounded-lg border border-outline-variant/10 text-xs">
            <b className="text-on-surface font-semibold">{runtime.runtime_id} · {runtime.eligible ? "eligible" : "ineligible"}</b>
            <p className="text-on-surface-variant text-[11px]">
              Requested {runtime.capacity.requested_cpu_millicores}m / {mib(runtime.capacity.requested_memory_bytes)} MiB<br />
              Available {capacityCPU(runtime.capacity.available_cpu_millicores, runtime.capacity.unknown_capacity)} / {capacityMemory(runtime.capacity.available_memory_bytes, runtime.capacity.unknown_capacity)}
            </p>
            {runtime.issues.length ? (
              <ul className="draftIssues space-y-1 text-xs text-error mt-1">
                {runtime.issues.map((issue, index) => (
                  <li key={`${issue.code}:${index}`}>{issue.code}: {issue.message}</li>
                ))}
              </ul>
            ) : (
              <small className="text-on-surface-variant/70 text-[10px]">No runtime issues.</small>
            )}
          </div>
        ))}
      </div>
      <div className="pt-2 flex items-center justify-between">
        <span className="text-xs text-on-surface-variant">{review.validation.valid ? "Validated by Cloud" : "Resolve validation issues and review again"}</span>
        <Button disabled={!review.validation.valid || busy} onClick={onApply} size="sm" type="button" variant="primary">
          {busy ? "Applying…" : "Apply canonical draft"}
        </Button>
      </div>
    </section>
  );
}

function ConfigurationReviewPanel({ busy, onApply, review }: { busy: boolean; onApply: () => void; review: ConfigurationReview }) {
  return (
    <section className="draftReview configurationReview bg-surface-container-low border border-outline-variant/20 rounded-2xl p-5 shadow-xl space-y-4" aria-label="Cloud service configuration review">
      <p className="text-xs text-on-surface-variant">{review.validation.valid ? "Cloud validation passed." : "Cloud validation failed."}</p>
      <div className="grid grid-cols-2 gap-3 bg-surface-container p-3 rounded-xl border border-outline-variant/10 text-xs font-code-md">
        <div>
          <span className="text-[10px] text-on-surface-variant uppercase font-semibold block">Current revision</span>
          <strong className="text-on-surface font-bold">{review.preview.current_revision}</strong>
          <code className="block text-[10px] opacity-70 truncate">{review.preview.current_state_hash}</code>
        </div>
        <div>
          <span className="text-[10px] text-on-surface-variant uppercase font-semibold block">Draft hash</span>
          <code className="block text-[10px] font-bold text-primary truncate">{review.preview.draft_state_hash}</code>
        </div>
      </div>
      <h4 className="font-label-sm text-xs font-semibold text-on-surface uppercase tracking-wider">Semantic diff</h4>
      {review.diff.changes.length ? (
        <ul className="space-y-2 text-xs">
          {review.diff.changes.map((change, index) => (
            <li key={`${change.kind}:${change.name}:${index}`} className="bg-surface-container/60 p-2.5 rounded-lg border border-outline-variant/10">
              <strong className="text-on-surface font-bold">{change.kind.replaceAll("_", " ")}</strong> · {change.action}{change.name ? ` · ${change.name}` : ""}
              {change.before || change.after ? (
                <>
                  <br />
                  <code className="font-code-md text-[11px] text-on-surface-variant">{change.before || "none"} → {change.after || "none"}</code>
                </>
              ) : null}
            </li>
          ))}
        </ul>
      ) : (
        <p className="text-xs text-on-surface-variant">No semantic changes.</p>
      )}
      {review.validation.issues?.length ? (
        <ul className="draftIssues space-y-1.5 text-xs text-error">
          {review.validation.issues.map((issue) => (
            <li key={`${issue.code}:${issue.field}`} className="flex items-center gap-1.5">
              <strong className="font-bold">{issue.code}</strong> · {issue.message}
            </li>
          ))}
        </ul>
      ) : null}
      <div className="pt-2 flex items-center justify-end">
        <Button disabled={!review.validation.valid || busy} onClick={onApply} size="sm" type="button" variant="primary">
          {busy ? "Applying…" : "Apply service configuration"}
        </Button>
      </div>
    </section>
  );
}
