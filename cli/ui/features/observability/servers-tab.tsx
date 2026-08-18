"use client";

import { useState } from "react";
import { Button, Empty, Icon, StatusBadge } from "@/components/ui/primitives";
import { routeHref } from "@/features/console/navigation";
import type { ConsoleController } from "@/features/console/types";
import type { ObservabilityModel } from "@/features/observability/observability-view";
import { formatObserved } from "@/features/observability/shared";
import type { ServerRuntimeSummary } from "@/lib/presentation/observability/model";

export function ServersTab({
  console,
  model,
}: {
  console: ConsoleController;
  model: ObservabilityModel;
}) {
  const projectID = console.route.projectID || console.state.project?.id || "";
  const servers = model.data.servers;
  const selectedServerID = console.route.server || console.route.node || "";
  const selectedServer = servers.find((s) => s.id === selectedServerID) ?? null;

  const [detailTab, setDetailTab] = useState<"overview" | "runtime" | "applications" | "events">("overview");

  function selectServer(server: ServerRuntimeSummary | null) {
    console.navigate({
      server: server ? server.id : "",
    });
  }

  return (
    <div className="space-y-6" data-testid="observability-servers">
      <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-4">
        <div>
          <p className="font-label-sm text-xs text-primary uppercase tracking-wider">Physical / VM Capacity</p>
          <h2 className="font-headline-md text-xl font-bold text-on-surface">Server Observability</h2>
          <p className="text-xs text-on-surface-variant mt-0.5">
            Server nodes, live Agent connectivity, hardware capacity, and workloads.
          </p>
        </div>
        <div>
          <Button
            disabled={model.data.sources.nodes === "loading"}
            onClick={() => void model.load()}
            size="sm"
            variant="secondary"
          >
            <Icon name="refresh" className="text-[16px]" />
            Refresh Servers
          </Button>
        </div>
      </div>

      {servers.length > 0 ? (
        <div className="bg-surface-container-low border border-outline-variant/20 rounded-2xl overflow-hidden shadow-sm">
          <div className="overflow-x-auto">
            <table className="w-full text-left border-collapse" aria-label="Servers runtime inventory">
              <thead>
                <tr className="bg-surface-container/60 border-b border-outline-variant/20 text-[11px] font-label-sm uppercase tracking-wider text-on-surface-variant">
                  <th className="py-3 px-4 font-semibold">Server</th>
                  <th className="py-3 px-4 font-semibold">Status</th>
                  <th className="py-3 px-4 font-semibold">Agent</th>
                  <th className="py-3 px-4 font-semibold">CPU Capacity</th>
                  <th className="py-3 px-4 font-semibold">Memory</th>
                  <th className="py-3 px-4 font-semibold">Placed Workloads</th>
                  <th className="py-3 px-4 font-semibold">Public Host</th>
                  <th className="py-3 px-4 font-semibold">Observed</th>
                  <th className="py-3 px-4 text-right"><span className="sr-only">Actions</span></th>
                </tr>
              </thead>
              <tbody className="divide-y divide-outline-variant/15 text-xs text-on-surface">
                {servers.map((server) => {
                  const isSelected = selectedServer?.id === server.id;
                  return (
                    <tr
                      className={`hover:bg-surface-container/60 transition-colors cursor-pointer ${
                        isSelected ? "bg-primary-container/30 ring-1 ring-inset ring-primary/40" : ""
                      }`}
                      key={server.id}
                      onClick={() => selectServer(server)}
                      data-testid={`server-row-${server.name}`}
                    >
                      <td className="py-3.5 px-4 font-semibold">
                        <span className="text-on-surface font-body-md block">{server.name}</span>
                        <span className="text-[11px] text-on-surface-variant font-code-md block">{server.role}</span>
                      </td>
                      <td className="py-3.5 px-4">
                        <StatusBadge
                          label={server.statusLabel}
                          value={server.status === "ready" ? "healthy" : server.status === "offline" ? "degraded" : server.status}
                        />
                      </td>
                      <td className="py-3.5 px-4">
                        <span className={`inline-flex items-center gap-1.5 font-code-md ${server.agentConnected ? "text-status-ready" : "text-on-surface-variant"}`}>
                          <span className={`w-1.5 h-1.5 rounded-full ${server.agentConnected ? "bg-status-ready" : "bg-outline-variant"}`} />
                          {server.agentConnected ? `Active (${server.agentVersion || "v1"})` : "Disconnected"}
                        </span>
                      </td>
                      <td className="py-3.5 px-4 font-code-md">
                        {server.cpuCores !== undefined ? `${server.cpuCores} cores` : "—"}
                      </td>
                      <td className="py-3.5 px-4 font-code-md">
                        {server.memoryMB !== undefined ? `${server.memoryMB} MB` : "—"}
                      </td>
                      <td className="py-3.5 px-4">
                        <span className="font-semibold">{server.placedWorkloadCount} placed</span>
                        {server.degradedWorkloadCount > 0 ? (
                          <span className="text-status-warning block text-[11px]">({server.degradedWorkloadCount} degraded)</span>
                        ) : null}
                      </td>
                      <td className="py-3.5 px-4 font-code-md text-on-surface-variant">
                        {server.publicHost}
                      </td>
                      <td className="py-3.5 px-4 text-on-surface-variant font-code-md text-[11px]">
                        {server.lastSeenFreshness}
                      </td>
                      <td className="py-3.5 px-4 text-right">
                        <Button
                          onClick={(e) => {
                            e.stopPropagation();
                            selectServer(server);
                          }}
                          size="sm"
                          variant="ghost"
                        >
                          Inspect →
                        </Button>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </div>
      ) : (
        <Empty
          title="No servers registered"
          text="Connect or bootstrap a server node in Infrastructure to observe server telemetry."
        />
      )}

      {/* Server Detail Drawer */}
      {selectedServer ? (
        <ServerDetailDrawer
          console={console}
          detailTab={detailTab}
          model={model}
          onClose={() => selectServer(null)}
          onTabChange={setDetailTab}
          projectID={projectID}
          server={selectedServer}
        />
      ) : null}
    </div>
  );
}

function ServerDetailDrawer({
  console,
  detailTab,
  model,
  onClose,
  onTabChange,
  projectID,
  server,
}: {
  console: ConsoleController;
  detailTab: "overview" | "runtime" | "applications" | "events";
  model: ObservabilityModel;
  onClose: () => void;
  onTabChange: (tab: "overview" | "runtime" | "applications" | "events") => void;
  projectID: string;
  server: ServerRuntimeSummary;
}) {
  const auditEvents = model.data.audit.filter(
    (a) => a.resource_id === server.id || a.resource_type === "node",
  );

  return (
    <div className="fixed inset-0 z-50 bg-black/60 backdrop-blur-sm flex justify-end" onClick={onClose} role="presentation">
      <section
        aria-label={`Server diagnostics for ${server.name}`}
        aria-modal="true"
        className="w-full max-w-2xl h-full bg-surface-container-low border-l border-outline-variant/30 shadow-2xl flex flex-col text-on-surface overflow-hidden"
        data-testid="server-detail-drawer"
        onClick={(e) => e.stopPropagation()}
        role="dialog"
      >
        {/* Drawer Header */}
        <header className="p-6 border-b border-outline-variant/20 flex items-start justify-between gap-4 bg-surface-container/50">
          <div>
            <span className="font-label-sm text-xs text-primary font-bold uppercase tracking-wider block mb-1">
              Server Node Diagnostics • {server.role}
            </span>
            <h2 className="font-headline-md text-2xl font-bold text-on-surface">{server.name}</h2>
            <div className="flex items-center gap-2 mt-2">
              <StatusBadge
                label={`Status: ${server.statusLabel}`}
                value={server.status === "ready" ? "healthy" : server.status === "offline" ? "degraded" : server.status}
              />
              <span className={`text-xs px-2.5 py-0.5 rounded-md font-mono ${server.agentConnected ? "bg-status-ready/15 text-status-ready" : "bg-surface-container-high text-on-surface-variant"}`}>
                Agent: {server.agentConnected ? "Active" : "Offline"}
              </span>
            </div>
          </div>

          <div className="flex items-center gap-2">
            <Button
              onClick={() => console.navigate({ projectID, view: "infrastructure", tab: "servers", server: server.id })}
              size="sm"
              variant="outline"
            >
              Infrastructure
            </Button>
            <button
              aria-label="Close diagnostics"
              className="p-1.5 text-on-surface-variant hover:text-on-surface hover:bg-surface-container-highest rounded-lg transition-colors cursor-pointer"
              onClick={onClose}
              type="button"
            >
              <Icon name="close" className="text-[20px]" />
            </button>
          </div>
        </header>

        {/* Drawer Tabs */}
        <nav aria-label="Server sections" className="flex items-center gap-1 border-b border-outline-variant/20 px-6 pt-2 bg-surface-container/20">
          {(["overview", "runtime", "applications", "events"] as const).map((t) => {
            const active = detailTab === t;
            return (
              <button
                aria-selected={active}
                className={`px-4 py-2.5 text-xs font-label-sm uppercase font-bold border-b-2 transition-all cursor-pointer ${
                  active ? "border-primary text-primary" : "border-transparent text-on-surface-variant hover:text-on-surface"
                }`}
                key={t}
                onClick={() => onTabChange(t)}
                role="tab"
                type="button"
              >
                {t === "applications" ? `Applications (${server.placedWorkloadCount})` : t === "events" ? `Events (${auditEvents.length})` : t}
              </button>
            );
          })}
        </nav>

        {/* Drawer Body */}
        <div className="flex-1 overflow-y-auto p-6 space-y-6">
          {detailTab === "overview" ? (
            <div className="space-y-6">
              <div className="grid grid-cols-2 sm:grid-cols-3 gap-3 bg-surface-container p-4 rounded-2xl border border-outline-variant/15 text-xs">
                <div>
                  <span className="text-on-surface-variant text-[11px] block mb-0.5">Node ID</span>
                  <strong className="text-on-surface font-code-md truncate block">{server.id}</strong>
                </div>
                <div>
                  <span className="text-on-surface-variant text-[11px] block mb-0.5">Public Host</span>
                  <strong className="text-on-surface font-code-md">{server.publicHost}</strong>
                </div>
                <div>
                  <span className="text-on-surface-variant text-[11px] block mb-0.5">Role</span>
                  <strong className="text-on-surface capitalize">{server.role}</strong>
                </div>
                <div>
                  <span className="text-on-surface-variant text-[11px] block mb-0.5">Agent Status</span>
                  <strong className={server.agentConnected ? "text-status-ready" : "text-error"}>
                    {server.agentConnected ? "Connected" : "Disconnected"}
                  </strong>
                </div>
                <div>
                  <span className="text-on-surface-variant text-[11px] block mb-0.5">Agent Version</span>
                  <strong className="text-on-surface font-code-md">{server.agentVersion || "—"}</strong>
                </div>
                <div>
                  <span className="text-on-surface-variant text-[11px] block mb-0.5">Last Heartbeat</span>
                  <strong className="text-on-surface font-code-md">{server.lastSeenFreshness}</strong>
                </div>
              </div>
            </div>
          ) : detailTab === "runtime" ? (
            <div className="space-y-6">
              <div className="grid grid-cols-2 gap-3 bg-surface-container p-4 rounded-2xl border border-outline-variant/15 text-xs">
                <div>
                  <span className="text-on-surface-variant text-[11px] block mb-0.5">CPU Cores</span>
                  <strong className="text-on-surface font-code-md text-base">{server.cpuCores !== undefined ? `${server.cpuCores} cores` : "Not reported"}</strong>
                </div>
                <div>
                  <span className="text-on-surface-variant text-[11px] block mb-0.5">Memory Capacity</span>
                  <strong className="text-on-surface font-code-md text-base">{server.memoryMB !== undefined ? `${server.memoryMB} MB` : "Not reported"}</strong>
                </div>
                <div>
                  <span className="text-on-surface-variant text-[11px] block mb-0.5">Placed Workloads</span>
                  <strong className="text-on-surface">{server.placedWorkloadCount} workloads</strong>
                </div>
                <div>
                  <span className="text-on-surface-variant text-[11px] block mb-0.5">Degraded Workloads</span>
                  <strong className={server.degradedWorkloadCount > 0 ? "text-status-warning" : "text-on-surface"}>
                    {server.degradedWorkloadCount} workloads
                  </strong>
                </div>
              </div>
            </div>
          ) : detailTab === "applications" ? (
            <div className="space-y-4">
              {server.placedServices.length > 0 ? (
                <div className="bg-surface-container rounded-2xl border border-outline-variant/15 p-4 space-y-2">
                  <h3 className="font-headline-md text-sm font-bold text-on-surface mb-3">Placed Applications</h3>
                  <ul className="divide-y divide-outline-variant/15 text-xs">
                    {server.placedServices.map((svcName) => (
                      <li className="py-2.5 flex items-center justify-between" key={svcName}>
                        <strong className="text-on-surface font-semibold">{svcName}</strong>
                        <span className="text-on-surface-variant font-code-md">Assigned to {server.name}</span>
                      </li>
                    ))}
                  </ul>
                </div>
              ) : (
                <div className="p-8 text-center text-xs text-on-surface-variant bg-surface-container rounded-2xl border border-outline-variant/15">
                  No applications are currently assigned to this server node.
                </div>
              )}
            </div>
          ) : (
            <div className="space-y-4">
              {auditEvents.length > 0 ? (
                <div className="space-y-2">
                  {auditEvents.map((evt) => (
                    <div
                      className="p-3 bg-surface-container/60 rounded-xl border border-outline-variant/15 flex items-start justify-between gap-3 text-xs"
                      key={evt.id}
                    >
                      <div className="space-y-0.5">
                        <strong className="text-on-surface font-semibold block capitalize">
                          {evt.action.replaceAll("_", " ")}
                        </strong>
                        <p className="text-on-surface-variant">Actor {evt.actor_user_id || "system"} • result: {evt.result}</p>
                      </div>
                      <time className="text-[11px] font-code-md text-on-surface-variant/70 shrink-0">
                        {formatObserved(Date.parse(evt.created_at) / 1000)}
                      </time>
                    </div>
                  ))}
                </div>
              ) : (
                <div className="p-8 text-center text-xs text-on-surface-variant bg-surface-container rounded-2xl border border-outline-variant/15">
                  No audit or lifecycle events recorded for this server.
                </div>
              )}
            </div>
          )}
        </div>
      </section>
    </div>
  );
}
