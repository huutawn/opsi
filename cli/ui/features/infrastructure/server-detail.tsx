"use client";

import { useState } from "react";
import type {
  BootstrapSession,
  NodeDiagnostics,
  NodeRecord,
  PlacementFacts,
  ServiceRecord,
} from "@/lib/contracts/registry";
import { serverLifecyclePresentation } from "@/lib/presentation/resources/model";
import { serverStatus } from "@/lib/presentation/infrastructure/model";

export type ServerDetailTab = "overview" | "capacity" | "runtime" | "workloads" | "events";

export function ServerDetail({
  diagnostics,
  facts,
  node,
  onClose,
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
  onClose: () => void;
  onDrain: (node: NodeRecord) => void;
  onOffline: (node: NodeRecord) => void;
  onRemove: (node: NodeRecord) => void;
  onRetryBootstrap?: (sessionID: string) => void;
  services: ServiceRecord[];
  sessions: BootstrapSession[];
}) {
  const [activeTab, setActiveTab] = useState<ServerDetailTab>("overview");

  const factNode = facts?.nodes.find((fn) => fn.id === node.id);
  const matchingAgent = facts?.agents.find((a) => a.node_id === node.id || (factNode && a.runtime_id === factNode.runtime_id));
  const matchingRuntime = facts?.runtimes.find((r) => factNode && r.id === factNode.runtime_id);
  const matchingSession = sessions.find((s) => s.public_host && (s.public_host === node.public_host || s.public_host === node.name));

  const status = serverStatus(
    factNode ? [factNode] : [{ id: node.id, project_id: "", runtime_id: "", status: node.status, cpu_cores: node.cpu_cores, memory_mb: node.memory_mb }],
    matchingAgent ? [matchingAgent] : [],
    matchingRuntime?.status,
  );

  const pres = serverLifecyclePresentation(status);

  // Placed workloads from services
  const placedServices = services.filter((s) => Boolean(s.name));

  return (
    <aside aria-label={`Server details for ${node.name || node.public_host || node.id}`} className="canvasInspector serverDetailPanel">
      <div className="inspectorHeading">
        <div>
          <p className="canvasPath">Infrastructure / Servers / {node.public_host || node.id}</p>
          <span className="canvasNodeKind">Server Capacity</span>
          <h2 tabIndex={-1}>{node.name || node.public_host || `Server ${node.id.slice(0, 8)}`}</h2>
        </div>
        <div className="inspectorHeaderActions">
          <span className={`reportedState ${pres.statusValue}`}>{pres.label}</span>
          <button aria-label="Close server detail" className="iconButton" onClick={onClose} type="button">
            <svg aria-hidden="true" viewBox="0 0 20 20">
              <path d="m5 5 10 10M15 5 5 15" />
            </svg>
          </button>
        </div>
      </div>

      <nav aria-label="Server sections" className="resourceTabs">
        <button
          aria-selected={activeTab === "overview"}
          className={activeTab === "overview" ? "active" : ""}
          onClick={() => setActiveTab("overview")}
          role="tab"
          type="button"
        >
          Overview
        </button>
        <button
          aria-selected={activeTab === "capacity"}
          className={activeTab === "capacity" ? "active" : ""}
          onClick={() => setActiveTab("capacity")}
          role="tab"
          type="button"
        >
          Capacity
        </button>
        <button
          aria-selected={activeTab === "runtime"}
          className={activeTab === "runtime" ? "active" : ""}
          onClick={() => setActiveTab("runtime")}
          role="tab"
          type="button"
        >
          Runtime & K3s
        </button>
        <button
          aria-selected={activeTab === "workloads"}
          className={activeTab === "workloads" ? "active" : ""}
          onClick={() => setActiveTab("workloads")}
          role="tab"
          type="button"
        >
          Workloads
        </button>
        <button
          aria-selected={activeTab === "events"}
          className={activeTab === "events" ? "active" : ""}
          onClick={() => setActiveTab("events")}
          role="tab"
          type="button"
        >
          Events
        </button>
      </nav>

      {activeTab === "overview" ? (
        <div className="detailSection">
          <section className="inspectorSection">
            <h4>Server Identity & Connectivity</h4>
            <dl className="factsGrid">
              <div>
                <dt>Node ID</dt>
                <dd><code>{node.id}</code></dd>
              </div>
              <div>
                <dt>Public Host / IP</dt>
                <dd>{node.public_host || "Local Edge"}</dd>
              </div>
              <div>
                <dt>Agent Connectivity</dt>
                <dd className={matchingAgent?.status === "active" ? "statusPass" : "statusNeutral"}>
                  {matchingAgent ? `${matchingAgent.status} (${matchingAgent.id})` : "Not reported"}
                </dd>
              </div>
              <div>
                <dt>Last Heartbeat</dt>
                <dd>{matchingAgent?.last_seen_at || node.last_seen_at || "Not reported"}</dd>
              </div>
              <div>
                <dt>K3s Node Role</dt>
                <dd>{node.k3s_role || "server/agent"}</dd>
              </div>
              <div>
                <dt>K3s Readiness</dt>
                <dd className={node.k3s_status === "Ready" || node.status === "ready" ? "statusPass" : "statusNeutral"}>
                  {node.k3s_status || node.status || "Unknown"}
                </dd>
              </div>
            </dl>
          </section>

          <section className="inspectorSection">
            <h4>Operational Actions</h4>
            <div className="buttonRow">
              {(pres.label === "Failed" || matchingSession?.status === "failed" || matchingSession?.status === "dead_letter") && matchingSession && onRetryBootstrap ? (
                <button
                  className="primary"
                  onClick={() => onRetryBootstrap(matchingSession.id)}
                  type="button"
                >
                  Retry Bootstrap
                </button>
              ) : null}
              <button className="secondary" onClick={() => onOffline(node)} type="button">
                Mark Offline
              </button>
              <button className="secondary" onClick={() => onDrain(node)} type="button">
                Drain Workloads
              </button>
              <button className="secondary destructive" onClick={() => onRemove(node)} type="button">
                Remove Server
              </button>
            </div>
          </section>
        </div>
      ) : null}

      {activeTab === "capacity" ? (
        <div className="detailSection">
          <section className="inspectorSection">
            <h4>Hardware Capacity Facts</h4>
            <dl className="factsGrid">
              <div>
                <dt>CPU Cores</dt>
                <dd><strong>{node.cpu_cores ? `${node.cpu_cores} cores` : "Not reported"}</strong></dd>
              </div>
              <div>
                <dt>Total Memory</dt>
                <dd><strong>{node.memory_mb ? `${node.memory_mb} MiB` : "Not reported"}</strong></dd>
              </div>
              <div>
                <dt>Disk Capacity</dt>
                <dd>{node.disk_total_gb ? `${node.disk_total_gb} GiB` : "Not reported"}</dd>
              </div>
              <div>
                <dt>Provider / Region</dt>
                <dd>{[node.provider, node.region].filter(Boolean).join(" / ") || "Local / Bare Metal"}</dd>
              </div>
              <div>
                <dt>Agent Version</dt>
                <dd>{node.agent_version || "Not reported"}</dd>
              </div>
              <div>
                <dt>Role</dt>
                <dd>{node.role || "Primary Node"}</dd>
              </div>
            </dl>
          </section>
        </div>
      ) : null}

      {activeTab === "runtime" ? (
        <div className="detailSection">
          <section className="inspectorSection">
            <h4>Runtime Authority</h4>
            <dl className="factsGrid">
              <div>
                <dt>Runtime ID</dt>
                <dd><code>{matchingRuntime?.id || "None"}</code></dd>
              </div>
              <div>
                <dt>Runtime Type</dt>
                <dd>{matchingRuntime?.type || "k3s"}</dd>
              </div>
              <div>
                <dt>Runtime Status</dt>
                <dd>{matchingRuntime?.status || "active"}</dd>
              </div>
              <div>
                <dt>K3s Role & Status</dt>
                <dd>{[node.k3s_role, node.k3s_status].filter(Boolean).join(" · ") || "Ready"}</dd>
              </div>
              <div>
                <dt>Agent Status</dt>
                <dd>{matchingAgent?.status || (node.agent_id ? "Active" : "Not connected")}</dd>
              </div>
            </dl>
          </section>
        </div>
      ) : null}

      {activeTab === "workloads" ? (
        <div className="detailSection">
          <h4>Placed Workloads</h4>
          {placedServices.length === 0 ? (
            <p className="emptyStateText">No application workloads currently assigned to this server.</p>
          ) : (
            <div className="workloadsList">
              {placedServices.map((svc) => (
                <div className="workloadItem" key={svc.id}>
                  <div>
                    <strong>{svc.name}</strong>
                    <small>ID: {svc.id}</small>
                  </div>
                  <span className="statusTag ready">{svc.status || "active"}</span>
                </div>
              ))}
            </div>
          )}
        </div>
      ) : null}

      {activeTab === "events" ? (
        <div className="detailSection">
          <h4>Recent Server Events & Diagnostics</h4>
          {diagnostics?.open_bootstrap_events && diagnostics.open_bootstrap_events.length > 0 ? (
            <div className="operationsTimeline">
              {diagnostics.open_bootstrap_events.map((evt) => (
                <div className="timelineItem" key={evt.id}>
                  <div className="timelineDot" data-tone="neutral" />
                  <div className="timelineContent">
                    <div className="timelineHeader">
                      <strong>{evt.step}</strong>
                      <span>{evt.progress_percent}%</span>
                    </div>
                    <p className="timelineDetails">{evt.message_redacted}</p>
                    <small className="timelineTime">{evt.created_at}</small>
                  </div>
                </div>
              ))}
            </div>
          ) : (
            <p className="emptyStateText">No active bootstrap diagnostics recorded for this node.</p>
          )}
        </div>
      ) : null}
    </aside>
  );
}
