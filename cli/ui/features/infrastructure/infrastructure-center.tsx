"use client";

import { useState } from "react";
import type { ConsoleController } from "@/features/console/types";
import type { RetainedStorage } from "@/lib/contracts/registry";
import {
  formatBytes,
  retainedStorageLifecyclePresentation,
  resourceLifecyclePresentation,
  serverLifecyclePresentation,
} from "@/lib/presentation/resources/model";
import { serverStatus } from "@/lib/presentation/infrastructure/model";
import { useInfrastructureCenterData } from "./center-data";
import { AddResourceDialog } from "./add-resource-dialog";
import { ResourceDetail } from "./resource-detail";
import { ServerDetail } from "./server-detail";
import { DestroyStorageDialog } from "./destroy-storage-dialog";
import { BootstrapDialog } from "./infrastructure-view";

export function InfrastructureCenterView({ console }: { console: ConsoleController }) {
  const projectID = console.state.project?.id ?? "";
  const environmentID = console.route.environment ?? "";
  const { data, error, loading, reload } = useInfrastructureCenterData(console);

  const [activeTab, setActiveTab] = useState<"servers" | "resources" | "storage">(
    console.route.tab === "storage"
      ? "storage"
      : console.route.tab === "resources"
      ? "resources"
      : "servers",
  );

  const [showAddResource, setShowAddResource] = useState(false);
  const [showAddServer, setShowAddServer] = useState(false);
  const [selectedResourceID, setSelectedResourceID] = useState<string>(
    (console.route as { resource?: string }).resource ?? "",
  );
  const [selectedNodeID, setSelectedNodeID] = useState<string>(
    (console.route as { server?: string; node?: string }).server ??
      (console.route as { server?: string; node?: string }).node ??
      "",
  );
  const [destroyingStorage, setDestroyingStorage] = useState<RetainedStorage | null>(null);

  const selectedResource = data.resources.find((r) => r.id === selectedResourceID) ?? null;
  const selectedNode = data.nodes.find((n) => n.id === selectedNodeID) ?? null;

  function handleTabChange(tab: "servers" | "resources" | "storage") {
    setActiveTab(tab);
    console.navigate({ view: "infrastructure", tab });
  }

  return (
    <div className="infrastructureCenter">
      <header className="pageHeader">
        <div>
          <p className="eyebrow">Production Control · Factual Infrastructure</p>
          <h1>Infrastructure Resource Center</h1>
          <p className="subtitle">
            Inspect and operate physical/VM server capacity, cloud-managed resources, and persistent database storage.
          </p>
        </div>
        <div className="pageHeaderActions">
          {activeTab === "servers" ? (
            <button className="primary" onClick={() => setShowAddServer(true)} type="button">
              + Add Server
            </button>
          ) : activeTab === "resources" ? (
            <button className="primary" onClick={() => setShowAddResource(true)} type="button">
              + Add Managed Resource
            </button>
          ) : null}
        </div>
      </header>

      <nav aria-label="Infrastructure sections" className="consoleTabs">
        <button
          aria-selected={activeTab === "servers"}
          className={activeTab === "servers" ? "active" : ""}
          onClick={() => handleTabChange("servers")}
          role="tab"
          type="button"
        >
          Servers ({data.nodes.length})
        </button>
        <button
          aria-selected={activeTab === "resources"}
          className={activeTab === "resources" ? "active" : ""}
          onClick={() => handleTabChange("resources")}
          role="tab"
          type="button"
        >
          Managed Resources ({data.resources.length})
        </button>
        <button
          aria-selected={activeTab === "storage"}
          className={activeTab === "storage" ? "active" : ""}
          onClick={() => handleTabChange("storage")}
          role="tab"
          type="button"
        >
          Retained Storage ({data.retainedStorages.length})
        </button>
      </nav>

      {error ? (
        <div className="truthCallout warning" role="alert">
          <b>Authority Warning:</b>
          <p>{error}</p>
        </div>
      ) : null}

      {loading && data.nodes.length === 0 && data.resources.length === 0 ? (
        <div className="centerLoading">
          <p>Loading factual infrastructure resources from Cloud authority…</p>
        </div>
      ) : null}

      {activeTab === "servers" ? (
        <section aria-labelledby="servers-heading" className="centerSection">
          <div className="sectionHeader">
            <div>
              <h2 id="servers-heading">Execution Capacity (Servers)</h2>
              <p>Physical machines and virtual compute nodes connected via Local Edge agent.</p>
            </div>
          </div>

          {data.nodes.length === 0 ? (
            <div className="emptyStateCard">
              <div className="emptyStateIcon">🖥️</div>
              <h3>No servers connected</h3>
              <p>Connect your first physical or virtual server to start deploying workloads.</p>
              <button className="primary" onClick={() => setShowAddServer(true)} type="button">
                Connect Server
              </button>
            </div>
          ) : (
            <div className="resourceGrid">
              {data.nodes.map((node) => {
                const factNode = data.facts?.nodes.find((fn) => fn.id === node.id);
                const agent = data.facts?.agents.find((a) => a.node_id === node.id || (factNode && a.runtime_id === factNode.runtime_id));
                const runtime = data.facts?.runtimes.find((r) => factNode && r.id === factNode.runtime_id);
                const status = serverStatus(
                  factNode ? [factNode] : [{ id: node.id, project_id: projectID, runtime_id: "", status: node.status, cpu_cores: node.cpu_cores, memory_mb: node.memory_mb }],
                  agent ? [agent] : [],
                  runtime?.status,
                );
                const pres = serverLifecyclePresentation(status);

                return (
                  <article
                    className={`resourceCard ${selectedNodeID === node.id ? "selected" : ""}`}
                    key={node.id}
                    onClick={() => setSelectedNodeID(node.id)}
                    role="button"
                    tabIndex={0}
                  >
                    <div className="cardHeader">
                      <div>
                        <span className="cardKind">Server Node</span>
                        <h3>{node.name || node.public_host || `Server ${node.id.slice(0, 8)}`}</h3>
                      </div>
                      <span className={`statusTag ${pres.statusValue}`}>{pres.label}</span>
                    </div>

                    <dl className="cardFacts">
                      <div>
                        <dt>Host / IP</dt>
                        <dd>{node.public_host || "127.0.0.1"}</dd>
                      </div>
                      <div>
                        <dt>Capacity</dt>
                        <dd>
                          {node.cpu_cores ? `${node.cpu_cores} CPU` : "—"} · {node.memory_mb ? `${node.memory_mb} MiB` : "—"}
                        </dd>
                      </div>
                      <div>
                        <dt>Agent Connectivity</dt>
                        <dd className={agent?.status === "active" ? "statusPass" : "statusNeutral"}>
                          {agent ? `${agent.status}` : "Disconnected"}
                        </dd>
                      </div>
                      <div>
                        <dt>K3s Status</dt>
                        <dd>{node.k3s_status || node.status || "Ready"}</dd>
                      </div>
                    </dl>

                    <div className="cardFooter">
                      <small>ID: <code>{node.id}</code></small>
                      <span className="cardActionLink">Inspect Server →</span>
                    </div>
                  </article>
                );
              })}
            </div>
          )}
        </section>
      ) : null}

      {activeTab === "resources" ? (
        <section aria-labelledby="resources-heading" className="centerSection">
          <div className="sectionHeader">
            <div>
              <h2 id="resources-heading">Managed Resources</h2>
              <p>PostgreSQL, Valkey, and NATS instances provisioned and monitored by Cloud.</p>
            </div>
          </div>

          {data.resources.length === 0 ? (
            <div className="emptyStateCard">
              <div className="emptyStateIcon">🗄️</div>
              <h3>No managed resources provisioned</h3>
              <p>Provision managed databases, caches, or messaging instances for your project.</p>
              <button className="primary" onClick={() => setShowAddResource(true)} type="button">
                + Provision First Resource
              </button>
            </div>
          ) : (
            <div className="resourceGrid">
              {data.resources.map((res) => {
                const pres = resourceLifecyclePresentation(res.lifecycle);
                const bindingsCount = data.bindings.filter((b) => b.target.id === res.id).length;
                const isPostgres = res.type === "postgres";

                return (
                  <article
                    className={`resourceCard ${selectedResourceID === res.id ? "selected" : ""}`}
                    key={res.id}
                    onClick={() => setSelectedResourceID(res.id)}
                    role="button"
                    tabIndex={0}
                  >
                    <div className="cardHeader">
                      <div>
                        <span className="cardKind">{res.type.toUpperCase()}</span>
                        <h3>{res.name}</h3>
                      </div>
                      <span className={`statusTag ${pres.tone}`}>{pres.label}</span>
                    </div>

                    <dl className="cardFacts">
                      <div>
                        <dt>Environment</dt>
                        <dd>{res.environment_id || "Default"}</dd>
                      </div>
                      <div>
                        <dt>Active Bindings</dt>
                        <dd>{bindingsCount} {bindingsCount === 1 ? "application" : "applications"}</dd>
                      </div>
                      <div>
                        <dt>CPU / Memory</dt>
                        <dd>
                          {res.managed?.cpu_millicores ? `${res.managed.cpu_millicores}m` : "—"} ·{" "}
                          {formatBytes(res.managed?.memory_bytes)}
                        </dd>
                      </div>
                      <div>
                        <dt>Runtime Readiness</dt>
                        <dd className={res.runtime?.evidence?.workload_ready ? "statusPass" : "statusNeutral"}>
                          {res.runtime?.evidence?.workload_ready ? "Workload Ready" : "Provisioning / Pending"}
                        </dd>
                      </div>
                      {isPostgres ? (
                        <div>
                          <dt>Persistent Storage</dt>
                          <dd>
                            {formatBytes(res.managed?.storage?.size_bytes)} ({res.runtime?.evidence?.storage_retained ? "Retained" : "Bound"})
                          </dd>
                        </div>
                      ) : null}
                    </dl>

                    <div className="cardFooter">
                      <small>ID: <code>{res.id}</code></small>
                      <span className="cardActionLink">Inspect Resource →</span>
                    </div>
                  </article>
                );
              })}
            </div>
          )}
        </section>
      ) : null}

      {activeTab === "storage" ? (
        <section aria-labelledby="storage-heading" className="centerSection">
          <div className="sectionHeader">
            <div>
              <h2 id="storage-heading">Retained Storage Inventory</h2>
              <p>Persistent database volumes retained following PostgreSQL resource deletion.</p>
            </div>
          </div>

          {data.retainedStorages.length === 0 ? (
            <div className="emptyStateCard">
              <div className="emptyStateIcon">💾</div>
              <h3>No retained storage records</h3>
              <p>When stateful PostgreSQL resources are deleted, their persistent data volumes are safely preserved here.</p>
            </div>
          ) : (
            <div className="resourceGrid">
              {data.retainedStorages.map((storage) => {
                const pres = retainedStorageLifecyclePresentation(storage.lifecycle);

                return (
                  <article className="resourceCard storageCard" key={storage.id}>
                    <div className="cardHeader">
                      <div>
                        <span className="cardKind">Persistent Volume Claim</span>
                        <h3>{storage.pvc_name}</h3>
                        <small className="storageSource">Original Resource: {storage.resource_name}</small>
                      </div>
                      <span className={`statusTag ${pres.tone}`}>{pres.label}</span>
                    </div>

                    <dl className="cardFacts">
                      <div>
                        <dt>Storage Size</dt>
                        <dd>{storage.actual_size || formatBytes(storage.requested_bytes)}</dd>
                      </div>
                      <div>
                        <dt>Storage Class</dt>
                        <dd>{storage.storage_class}</dd>
                      </div>
                      <div>
                        <dt>Retained Date</dt>
                        <dd>{storage.retained_at}</dd>
                      </div>
                      <div>
                        <dt>PVC UID</dt>
                        <dd><code>{storage.pvc_uid || "Not reported"}</code></dd>
                      </div>
                    </dl>

                    <div className="cardFooter">
                      <small>Storage ID: <code>{storage.id}</code></small>
                      {storage.lifecycle === "retained" ? (
                        <button
                          className="secondary destructive"
                          onClick={() => setDestroyingStorage(storage)}
                          type="button"
                        >
                          Review & Destroy Storage
                        </button>
                      ) : (
                        <span className="statusTag neutral">{storage.lifecycle}</span>
                      )}
                    </div>
                  </article>
                );
              })}
            </div>
          )}
        </section>
      ) : null}

      {/* Selected Resource Drawer/Panel */}
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

      {/* Selected Server Drawer/Panel */}
      {selectedNode ? (
        <ServerDetail
          diagnostics={console.state.nodeDetail}
          facts={data.facts}
          node={selectedNode}
          onClose={() => setSelectedNodeID("")}
          onDrain={(node) => console.actions.nodeAction(node.id, "drain")}
          onOffline={(node) => console.actions.nodeAction(node.id, "offline")}
          onRemove={(node) => console.actions.nodeAction(node.id, "remove")}
          onRetryBootstrap={(sessionID) => console.actions.retryBootstrap(sessionID, reload)}
          services={data.services}
          sessions={data.sessions}
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

      {/* Add Server Dialog (reusing BootstrapDialog) */}
      {showAddServer ? (
        <BootstrapDialog
          console={console}
          onClose={() => setShowAddServer(false)}
          onCreated={reload}
        />
      ) : null}

      {/* Destroy Retained Storage Modal */}
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
