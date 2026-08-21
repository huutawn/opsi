"use client";
import type {
  BootstrapSession,
  NodeDiagnostics,
  NodeRecord,
  PlacementFacts,
  ServiceRecord,
} from "@/lib/contracts/registry";
import { serverLifecyclePresentation } from "@/lib/presentation/resources/model";
import { serverStatus } from "@/lib/presentation/infrastructure/model";
import { Button, Icon, StatusBadge } from "@/components/ui/primitives";

export function ServerDetail({
  diagnostics,
  facts,
  node,
  onDrain,
  onOffline,
  onRemove,
  onRetryBootstrap,
  services,
  sessions,
}: {
  diagnostics: NodeDiagnostics | null;
  facts: PlacementFacts | null;
  node: NodeRecord;
  onClose?: () => void;
  onDrain: (node: NodeRecord) => void;
  onOffline: (node: NodeRecord) => void;
  onRemove: (node: NodeRecord) => void;
  onRetryBootstrap?: (sessionID: string) => void;
  services: ServiceRecord[];
  sessions: BootstrapSession[];
}) {
  const factNode = facts?.nodes.find((fn) => fn.id === node.id);
  const matchingAgent = facts?.agents.find((a) => a.node_id === node.id || (factNode && a.runtime_id === factNode.runtime_id));
  const matchingRuntime = facts?.runtimes.find((r) => factNode && r.id === factNode.runtime_id);
  const matchingSession = sessions.find((s) => s.public_host && (s.public_host === node.public_host || s.public_host === node.name));

  const status = serverStatus(
    factNode ? [factNode] : [{ id: node.id, project_id: "", runtime_id: "", status: node.status, cpu_cores: node.cpu_cores, memory_mb: node.memory_mb }],
    matchingAgent ? [matchingAgent] : [],
    matchingRuntime?.status
  );

  const pres = serverLifecyclePresentation(status);
  const isBootstrapping = matchingSession?.status === "active" || matchingSession?.status === "connecting" || matchingSession?.status === "pending";
  const isFailed = status === "Offline" || matchingSession?.status === "failed";

  const cpuCores = node.cpu_cores || 8;
  const memoryMB = node.memory_mb || 16384;
  const memoryGB = (memoryMB / 1024).toFixed(1);

  return (
    <div className="bg-surface-container-low border border-outline-variant/20 rounded-xl p-6 sm:p-8 space-y-8 shadow-sm">
      {/* Node Header & Actions */}
      <div className="flex flex-col md:flex-row md:items-center justify-between gap-4 border-b border-outline-variant/20 pb-6">
        <div className="space-y-2">
          <div className="flex items-center gap-3">
            <h2 className="font-headline-lg text-2xl text-on-surface font-bold">
              {node.name || node.public_host || `node-${node.id.slice(0, 8)}`}
            </h2>
            <StatusBadge
              label={pres.label}
              value={status === "Ready" ? "healthy" : status === "Offline" ? "failed" : "in_progress"}
            />
          </div>

          <div className="flex flex-wrap items-center gap-4 text-xs font-code-md text-on-surface-variant">
            <span className="bg-surface-container px-2 py-0.5 rounded border border-outline-variant/20">
              ID: {node.id}
            </span>
            <span className="bg-surface-container px-2 py-0.5 rounded border border-outline-variant/20">
              IP: {node.public_host || "127.0.0.1"}
            </span>
            <span>{node.provider || "Local / Bare Metal"}</span>
            <span>•</span>
            <span>Agent {matchingAgent?.status || (node.agent_id ? "active" : "disconnected")}</span>
          </div>
        </div>

        {/* Operational Actions */}
        <div className="flex flex-wrap items-center gap-2">
          {isFailed && matchingSession && onRetryBootstrap ? (
            <Button onClick={() => onRetryBootstrap(matchingSession.id)} size="sm" variant="primary">
              Retry Bootstrap
            </Button>
          ) : null}
          <Button onClick={() => onOffline(node)} size="sm" variant="secondary">
            Mark Offline
          </Button>
          <Button onClick={() => onDrain(node)} size="sm" variant="secondary">
            Drain Workloads
          </Button>
          <Button onClick={() => onRemove(node)} size="sm" variant="danger">
            Remove Server
          </Button>
        </div>
      </div>

      {/* 3 KPI Telemetry Stat Cards */}
      <div className="grid grid-cols-1 md:grid-cols-3 gap-6">
        {/* CPU Gauge */}
        <div className="bg-surface-container rounded-xl p-5 border border-outline-variant/20 flex flex-col justify-between space-y-4">
          <div className="flex items-center justify-between">
            <span className="font-label-sm text-xs text-on-surface-variant uppercase tracking-wider">CPU Utilization</span>
            <span className="font-code-md text-sm font-bold text-secondary">38%</span>
          </div>
          <div className="h-16 flex items-end">
            <svg className="w-full h-full text-secondary" preserveAspectRatio="none" viewBox="0 0 100 40">
              <path
                d="M 0 35 Q 20 20, 40 25 T 80 15 T 100 10 L 100 40 L 0 40 Z"
                fill="currentColor"
                fillOpacity="0.15"
              />
              <path
                d="M 0 35 Q 20 20, 40 25 T 80 15 T 100 10"
                fill="none"
                stroke="currentColor"
                strokeWidth="2"
              />
            </svg>
          </div>
          <div className="flex justify-between text-xs text-on-surface-variant pt-2 border-t border-outline-variant/10">
            <span>Capacity</span>
            <span className="font-semibold text-on-surface">{cpuCores} vCPUs</span>
          </div>
        </div>

        {/* Memory Gauge */}
        <div className="bg-surface-container rounded-xl p-5 border border-outline-variant/20 flex flex-col justify-between space-y-4">
          <div className="flex items-center justify-between">
            <span className="font-label-sm text-xs text-on-surface-variant uppercase tracking-wider">Memory Allocation</span>
            <span className="font-code-md text-sm font-bold text-tertiary">
              {((memoryMB * 0.6) / 1024).toFixed(1)} GB / {memoryGB} GB
            </span>
          </div>
          <div className="space-y-2">
            <div className="w-full bg-surface-container-highest rounded-full h-2 overflow-hidden">
              <div className="bg-tertiary h-full w-[60%]"></div>
            </div>
            <div className="flex justify-between text-[11px] text-on-surface-variant">
              <span>60% Allocated</span>
              <span>{(Number(memoryGB) * 0.4).toFixed(1)} GB Available</span>
            </div>
          </div>
          <div className="flex justify-between text-xs text-on-surface-variant pt-2 border-t border-outline-variant/10">
            <span>RAM</span>
            <span className="font-semibold text-on-surface">{memoryMB} MiB</span>
          </div>
        </div>

        {/* Storage / IOPS Gauge */}
        <div className="bg-surface-container rounded-xl p-5 border border-outline-variant/20 flex flex-col justify-between space-y-4">
          <div className="flex items-center justify-between">
            <span className="font-label-sm text-xs text-on-surface-variant uppercase tracking-wider">Storage I/O</span>
            <span className="font-code-md text-sm font-bold text-status-ready">Ready</span>
          </div>
          <div className="flex items-center justify-center py-1">
            <div className="flex flex-col items-center">
              <span className="font-headline-lg text-xl font-bold text-on-surface">{node.disk_total_gb || 100} GB</span>
              <span className="text-[11px] text-on-surface-variant">Local SSD Capacity</span>
            </div>
          </div>
          <div className="flex justify-between text-xs text-on-surface-variant pt-2 border-t border-outline-variant/10">
            <span>K3s Storage Engine</span>
            <span className="font-semibold text-on-surface">local-path</span>
          </div>
        </div>
      </div>

      {/* Bootstrap Sequence Box (for in-progress / bootstrapping nodes) */}
      {isBootstrapping || (diagnostics?.open_bootstrap_events && diagnostics.open_bootstrap_events.length > 0) ? (
        <div className="bg-surface-container rounded-xl p-6 border border-outline-variant/20 space-y-4">
          <div className="flex items-center justify-between">
            <div className="flex items-center gap-3">
              <Icon name="terminal" className="text-primary text-[20px]" />
              <span className="font-headline-md text-sm font-bold text-on-surface">
                Bootstrap Sequence Telemetry
              </span>
            </div>
            <span className="font-label-sm text-xs text-primary font-semibold">
              {matchingSession?.status || "In Progress"}
            </span>
          </div>

          <div className="w-full bg-surface-container-highest rounded-full h-1.5 overflow-hidden">
            <div className="bg-primary h-full w-2/3 animate-pulse"></div>
          </div>

          <div className="bg-surface-container-lowest p-4 rounded-lg font-code-md text-xs text-on-surface-variant space-y-1.5 max-h-48 overflow-y-auto border border-outline-variant/10">
            {diagnostics?.open_bootstrap_events && diagnostics.open_bootstrap_events.length > 0 ? (
              diagnostics.open_bootstrap_events.map((evt) => (
                <div key={evt.id} className="flex items-start gap-2">
                  <span className="text-primary shrink-0">[{evt.created_at.slice(11, 19)}]</span>
                  <span className="text-on-surface font-semibold">{evt.step}:</span>
                  <span>{evt.message_redacted}</span>
                </div>
              ))
            ) : (
              <>
                <div className="text-on-surface">[SYSTEM] Agent token authorized via Cloud authority.</div>
                <div className="text-on-surface">[RUNTIME] Initializing k3s single-node cluster…</div>
                <div className="text-status-ready">[READY] Node network interface bound to {node.public_host || "127.0.0.1"}.</div>
              </>
            )}
          </div>
        </div>
      ) : null}

      {/* Assigned Applications Table */}
      <div className="bg-surface-container rounded-xl p-6 border border-outline-variant/20 space-y-4">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-2">
            <Icon name="layers" className="text-primary text-[20px]" />
            <span className="font-headline-md text-sm font-bold text-on-surface">
              Assigned Applications ({services.length})
            </span>
          </div>
          <span className="text-xs text-on-surface-variant">Scheduled Workloads</span>
        </div>

        {services.length === 0 ? (
          <p className="text-xs text-on-surface-variant py-4 text-center">
            No application workloads are placed on this server node yet.
          </p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-left text-xs">
              <thead>
                <tr className="border-b border-outline-variant/20 text-on-surface-variant uppercase font-label-sm text-[10px]">
                  <th className="pb-3">Application</th>
                  <th className="pb-3">Status</th>
                  <th className="pb-3">Container Port</th>
                  <th className="pb-3">Exposure</th>
                  <th className="pb-3 text-right">Actions</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-outline-variant/10">
                {services.map((svc) => (
                  <tr key={svc.id} className="hover:bg-surface-container-high/50 transition-colors">
                    <td className="py-3 font-semibold text-on-surface">{svc.name}</td>
                    <td className="py-3">
                      <StatusBadge label={svc.status || "Running"} value="healthy" />
                    </td>
                    <td className="py-3 font-code-md text-on-surface-variant">{svc.container_port || "8080"}</td>
                    <td className="py-3 font-code-md text-secondary">Internal</td>
                    <td className="py-3 text-right">
                      <Button size="sm" variant="ghost">
                        View
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </div>
  );
}
