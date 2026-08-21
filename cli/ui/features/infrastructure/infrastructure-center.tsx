"use client";

import { useState } from "react";
import type { ConsoleController } from "@/features/console/types";
import type { RetainedStorage } from "@/lib/contracts/registry";
import {
  formatBytes,
  retainedStorageLifecyclePresentation,
  resourceLifecyclePresentation,
} from "@/lib/presentation/resources/model";
import { serverStatus } from "@/lib/presentation/infrastructure/model";
import { useInfrastructureCenterData } from "./center-data";
import { AddResourceDialog } from "./add-resource-dialog";
import { ResourceDetail } from "./resource-detail";
import { ServerDetail } from "./server-detail";
import { DestroyStorageDialog } from "./destroy-storage-dialog";
import { BootstrapDialog } from "./infrastructure-view";
import { Button, Empty, Icon, StatusBadge } from "@/components/ui/primitives";

export function InfrastructureCenterView({ console }: { console: ConsoleController }) {
  const projectID = console.state.project?.id ?? "";
  const environmentID = console.route.environment ?? "";
  const { data, error, reload } = useInfrastructureCenterData(console);

  const activeTab: "servers" | "resources" | "storage" =
    console.route.tab === "storage"
      ? "storage"
      : console.route.tab === "resources"
      ? "resources"
      : "servers";

  const [showAddResource, setShowAddResource] = useState(false);
  const [showAddServer, setShowAddServer] = useState(false);
  const [searchQuery, setSearchQuery] = useState("");
  const [selectedResourceID, setSelectedResourceID] = useState<string>(
    (console.route as { resource?: string }).resource ?? ""
  );
  const [selectedNodeID, setSelectedNodeID] = useState<string>(
    (console.route as { server?: string; node?: string }).server ??
      (console.route as { server?: string; node?: string }).node ??
      (data.nodes[0]?.id ?? "")
  );
  const [destroyingStorage, setDestroyingStorage] = useState<RetainedStorage | null>(null);

  const selectedResource = data.resources.find((r) => r.id === selectedResourceID) ?? null;
  const effectiveNodeID = selectedNodeID || data.nodes[0]?.id || "";
  const selectedNode = data.nodes.find((n) => n.id === effectiveNodeID) ?? null;

  const filteredNodes = data.nodes.filter((node) => {
    const q = searchQuery.trim().toLowerCase();
    if (!q) return true;
    return (
      (node.name && node.name.toLowerCase().includes(q)) ||
      (node.public_host && node.public_host.toLowerCase().includes(q)) ||
      node.id.toLowerCase().includes(q)
    );
  });

  return (
    <div className="space-y-6">
      {error ? (
        <div className="bg-status-warning/10 border border-status-warning/30 p-4 rounded-xl text-status-warning text-xs flex items-center gap-2" role="alert">
          <Icon name="warning" className="text-[18px] shrink-0" />
          <span>{error}</span>
        </div>
      ) : null}

      {/* Tab 1: Servers Master-Detail View */}
      {activeTab === "servers" ? (
        data.nodes.length === 0 ? (
          <Empty
            action={
              <Button onClick={() => setShowAddServer(true)} variant="primary">
                <Icon name="add" className="text-[18px]" />
                Connect First Server
              </Button>
            }
            text="Connect physical or virtual compute nodes via one-time bootstrap token."
            title="No Servers Connected"
          />
        ) : (
          <div className="grid grid-cols-1 lg:grid-cols-12 gap-6 items-start">
            {/* Left Column: Server Nodes Master List */}
            <div className="lg:col-span-4 bg-surface-container-low border border-outline-variant/20 rounded-xl p-4 flex flex-col gap-4 shadow-sm">
              <div className="flex items-center justify-between gap-2">
                <h2 className="font-headline-md text-sm font-semibold text-on-surface">Execution Capacity (Servers)</h2>
                <Button onClick={() => setShowAddServer(true)} size="sm" variant="outline">
                  <Icon name="add" className="text-[16px]" />
                  Connect
                </Button>
              </div>

              <div className="relative">
                <Icon name="search" className="absolute left-3 top-1/2 -translate-y-1/2 text-on-surface-variant text-[18px] pointer-events-none" />
                <input
                  className="w-full bg-surface-container-highest border border-outline-variant/30 rounded-lg py-2 pl-9 pr-3 text-xs font-body-md text-on-surface focus:outline-none focus:border-primary/50"
                  onChange={(e) => setSearchQuery(e.target.value)}
                  placeholder="Filter nodes..."
                  type="text"
                  value={searchQuery}
                />
              </div>

              <div className="flex flex-col gap-2 max-h-[600px] overflow-y-auto">
                {filteredNodes.map((node) => {
                  const factNode = data.facts?.nodes.find((fn) => fn.id === node.id);
                  const agent = data.facts?.agents.find((a) => a.node_id === node.id || (factNode && a.runtime_id === factNode.runtime_id));
                  const runtime = data.facts?.runtimes.find((r) => factNode && r.id === factNode.runtime_id);
                  const status = serverStatus(
                    factNode ? [factNode] : [{ id: node.id, project_id: projectID, runtime_id: "", status: node.status, cpu_cores: node.cpu_cores, memory_mb: node.memory_mb }],
                    agent ? [agent] : [],
                    runtime?.status
                  );
                  const isSelected = (selectedNode?.id ?? "") === node.id;

                  return (
                    <div
                      key={node.id}
                      onClick={() => setSelectedNodeID(node.id)}
                      className={`p-3.5 rounded-xl border cursor-pointer transition-all flex flex-col gap-2 ${
                        isSelected
                          ? "bg-primary-container/80 border-primary text-on-surface shadow-sm"
                          : "bg-surface-container border-outline-variant/20 hover:bg-surface-container-high"
                      }`}
                    >
                      <div className="flex items-center justify-between">
                        <div className="flex items-center gap-2 min-w-0">
                          <span
                            className={`w-2 h-2 rounded-full shrink-0 ${
                              status === "Ready" ? "bg-status-ready" : status === "Offline" ? "bg-status-failed" : "bg-status-warning animate-pulse"
                            }`}
                          />
                          <span className="font-body-md text-sm font-semibold text-on-surface truncate">
                            {node.name || node.public_host || `node-${node.id.slice(0, 8)}`}
                          </span>
                        </div>
                        <StatusBadge value={status === "Ready" ? "healthy" : status === "Offline" ? "failed" : "in_progress"} label={status} />
                      </div>

                      <div className="flex items-center justify-between text-[11px] font-code-md text-on-surface-variant">
                        <span>{node.public_host || "127.0.0.1"}</span>
                        <span>{node.cpu_cores ? `${node.cpu_cores} vCPU` : "—"} • {node.memory_mb ? `${node.memory_mb} MB` : "—"}</span>
                      </div>
                    </div>
                  );
                })}
              </div>
            </div>

            {/* Right Column: Detailed Node Inspector */}
            <div className="lg:col-span-8">
              {selectedNode ? (
                <ServerDetail
                  diagnostics={console.state.nodeDetail}
                  facts={data.facts}
                  node={selectedNode}
                  onClose={() => {}}
                  onDrain={(node) => console.actions.nodeAction(node.id, "drain")}
                  onOffline={(node) => console.actions.nodeAction(node.id, "offline")}
                  onRemove={(node) => console.actions.nodeAction(node.id, "remove")}
                  onRetryBootstrap={(sessionID) => console.actions.retryBootstrap(sessionID, reload)}
                  services={data.services}
                  sessions={data.sessions}
                />
              ) : (
                <Empty text="Select a server node from the list to view telemetry and assigned workloads." title="No Server Selected" />
              )}
            </div>
          </div>
        )
      ) : null}

      {/* Tab 2: Managed Resources Grid */}
      {activeTab === "resources" ? (
        data.resources.length === 0 ? (
          <Empty
            action={
              <Button onClick={() => setShowAddResource(true)} variant="primary">
                <Icon name="add" className="text-[18px]" />
                Provision Managed Resource
              </Button>
            }
            text="Provision PostgreSQL 18.6, Valkey, or NATS instances managed by Cloud authority."
            title="No Managed Resources"
          />
        ) : (
          <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-6">
            {data.resources.map((res) => {
              const pres = resourceLifecyclePresentation(res.lifecycle);
              const bindingsCount = data.bindings.filter((b) => b.target.id === res.id).length;
              const isPostgres = res.type === "postgres";

              return (
                <article
                  className="flex flex-col bg-surface-container-low rounded-xl shadow-md hover:shadow-lg transition-all group overflow-hidden border border-outline-variant/20 border-t-4 border-primary"
                  key={res.id}
                >
                  <div className="p-6 flex-1 flex flex-col justify-between space-y-4">
                    <div className="flex items-start justify-between gap-3">
                      <div className="flex items-center gap-3">
                        <div className="w-10 h-10 rounded-lg bg-surface-container-high flex items-center justify-center border border-outline-variant/20 text-primary">
                          <Icon name={isPostgres ? "database" : res.type === "valkey" ? "memory" : "hub"} className="text-[22px]" />
                        </div>
                        <div>
                          <h3
                            className="font-headline-md text-base font-bold text-on-surface group-hover:text-primary transition-colors cursor-pointer"
                            onClick={() => setSelectedResourceID(res.id)}
                          >
                            {res.name}
                          </h3>
                          <span className="font-label-sm text-[11px] text-on-surface-variant uppercase tracking-wider block">
                            {res.type} {res.managed?.version || (res.type === "postgres" ? "18.6" : "")}
                          </span>
                        </div>
                      </div>
                      <StatusBadge value={pres.tone === "ready" ? "healthy" : pres.tone === "failed" ? "failed" : "in_progress"} label={pres.label} />
                    </div>

                    <div className="bg-surface-container-highest/60 p-3.5 rounded-lg border border-outline-variant/20 grid grid-cols-2 gap-3 text-xs">
                      <div>
                        <span className="font-label-sm text-[10px] text-on-surface-variant uppercase tracking-wider block">Active Bindings</span>
                        <span className="font-body-md text-on-surface font-medium">{bindingsCount} apps</span>
                      </div>
                      <div>
                        <span className="font-label-sm text-[10px] text-on-surface-variant uppercase tracking-wider block">CPU / RAM</span>
                        <span className="font-body-md text-on-surface font-medium">
                          {res.managed?.cpu_millicores ? `${res.managed.cpu_millicores}m` : "250m"} • {formatBytes(res.managed?.memory_bytes || 268435456)}
                        </span>
                      </div>
                      {isPostgres ? (
                        <div className="col-span-2">
                          <span className="font-label-sm text-[10px] text-on-surface-variant uppercase tracking-wider block">Persistent Storage</span>
                          <span className="font-body-md text-on-surface font-medium">
                            {formatBytes(res.managed?.storage?.size_bytes || 10737418240)} ({res.runtime?.evidence?.storage_retained ? "Retained" : "Bound"})
                          </span>
                        </div>
                      ) : null}
                    </div>
                  </div>

                  <div className="bg-surface-container px-6 py-3 border-t border-outline-variant/20 flex items-center justify-between">
                    <span className="font-code-md text-[11px] text-on-surface-variant truncate max-w-[180px]">ID: {res.id}</span>
                    <Button onClick={() => setSelectedResourceID(res.id)} size="sm" variant="outline">
                      Inspect & Operations →
                    </Button>
                  </div>
                </article>
              );
            })}
          </div>
        )
      ) : null}

      {/* Tab 3: Retained Storage Inventory */}
      {activeTab === "storage" ? (
        data.retainedStorages.length === 0 ? (
          <Empty
            text="When stateful PostgreSQL resources are deleted, persistent data volumes are retained here for disaster recovery and safe cleanup."
            title="No Retained Storage Volumes"
          />
        ) : (
          <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-6">
            {data.retainedStorages.map((storage) => {
              const pres = retainedStorageLifecyclePresentation(storage.lifecycle);
              return (
                <article
                  className="flex flex-col bg-surface-container-low rounded-xl shadow-md p-6 border border-outline-variant/20 justify-between space-y-4"
                  key={storage.id}
                >
                  <div className="space-y-3">
                    <div className="flex items-start justify-between">
                      <div>
                        <span className="font-label-sm text-[10px] text-on-surface-variant uppercase tracking-wider block">Persistent Volume Claim</span>
                        <h3 className="font-headline-md text-base font-bold text-on-surface">{storage.pvc_name}</h3>
                        <small className="text-xs text-on-surface-variant block mt-0.5">Origin: {storage.resource_name}</small>
                      </div>
                      <StatusBadge value={pres.tone === "ready" ? "healthy" : "warning"} label={pres.label} />
                    </div>

                    <dl className="bg-surface-container-highest/60 p-3.5 rounded-lg border border-outline-variant/20 grid grid-cols-2 gap-3 text-xs">
                      <div>
                        <dt className="font-label-sm text-[10px] text-on-surface-variant uppercase">Storage Size</dt>
                        <dd className="font-body-md text-on-surface font-semibold">{storage.actual_size || formatBytes(storage.requested_bytes)}</dd>
                      </div>
                      <div>
                        <dt className="font-label-sm text-[10px] text-on-surface-variant uppercase">Storage Class</dt>
                        <dd className="font-body-md text-on-surface font-semibold">{storage.storage_class}</dd>
                      </div>
                      <div className="col-span-2">
                        <dt className="font-label-sm text-[10px] text-on-surface-variant uppercase">Retained Timestamp</dt>
                        <dd className="font-code-md text-on-surface-variant">{storage.retained_at}</dd>
                      </div>
                    </dl>
                  </div>

                  <div className="pt-3 border-t border-outline-variant/20 flex items-center justify-between">
                    <span className="font-code-md text-[10px] text-on-surface-variant truncate max-w-[140px]">ID: {storage.id}</span>
                    {storage.lifecycle === "retained" ? (
                      <Button onClick={() => setDestroyingStorage(storage)} size="sm" variant="danger">
                        Review & Destroy Storage
                      </Button>
                    ) : (
                      <span className="font-label-sm text-xs text-on-surface-variant">{storage.lifecycle}</span>
                    )}
                  </div>
                </article>
              );
            })}
          </div>
        )
      ) : null}

      {/* Selected Resource Drawer */}
      {selectedResource ? (
        <ResourceDetail
          allResources={data.resources}
          backups={data.backups}
          bindings={data.bindings}
          cutovers={data.cutovers}
          finalizations={data.finalizations}
          onClose={() => setSelectedResourceID("")}
          onReload={reload}
          projectID={projectID}
          resource={selectedResource}
          restores={data.restores}
          retainedStorages={data.retainedStorages}
          rollbacks={data.rollbacks}
          services={data.services}
        />
      ) : null}

      {/* Add Resource Dialog */}
      {showAddResource ? (
        <AddResourceDialog
          environmentID={environmentID}
          onClose={() => setShowAddResource(false)}
          onCreated={async (res) => {
            await reload();
            setSelectedResourceID(res.id);
          }}
          projectID={projectID}
        />
      ) : null}

      {/* Add Server Dialog */}
      {showAddServer ? (
        <BootstrapDialog
          console={console}
          onClose={() => setShowAddServer(false)}
          onCreated={reload}
        />
      ) : null}

      {/* Destroy Storage Modal */}
      {destroyingStorage ? (
        <DestroyStorageDialog
          onClose={() => setDestroyingStorage(null)}
          onDestroyed={async () => {
            await reload();
            setDestroyingStorage(null);
          }}
          projectID={projectID}
          storage={destroyingStorage}
        />
      ) : null}
    </div>
  );
}
