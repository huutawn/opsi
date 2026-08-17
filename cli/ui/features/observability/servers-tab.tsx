"use client";

import { useState } from "react";
import { Empty, StatusBadge, Surface } from "@/components/ui/primitives";
import { routeHref } from "@/features/console/navigation";
import type { ConsoleController } from "@/features/console/types";
import type { ObservabilityModel } from "@/features/observability/observability-view";
import { Fact, formatObserved } from "@/features/observability/shared";
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
    <div className="observabilityStack" data-testid="observability-servers">
      <div className="observabilityHero">
        <div>
          <p className="eyebrow">Physical / VM Capacity · Node Telemetry</p>
          <h2>Server Observability</h2>
          <p>
            Inspect server nodes, live Agent connectivity, factual hardware capacity, and placed workload allocations.
          </p>
        </div>
        <div className="heroStatus">
          <button
            className="secondaryAction"
            disabled={model.data.sources.nodes === "loading"}
            onClick={() => void model.load()}
            type="button"
          >
            Refresh Servers
          </button>
        </div>
      </div>

      {servers.length > 0 ? (
        <div className="tableWrap">
          <table className="dataTable" aria-label="Servers runtime inventory">
            <thead>
              <tr>
                <th scope="col">Server</th>
                <th scope="col">Status</th>
                <th scope="col">Agent</th>
                <th scope="col">CPU Capacity</th>
                <th scope="col">Memory</th>
                <th scope="col">Placed Workloads</th>
                <th scope="col">Public Host</th>
                <th scope="col">Freshness</th>
                <th scope="col"><span className="srOnly">Actions</span></th>
              </tr>
            </thead>
            <tbody>
              {servers.map((server) => {
                const isSelected = selectedServer?.id === server.id;
                return (
                  <tr
                    className={isSelected ? "selectedRow" : ""}
                    key={server.id}
                    onClick={() => selectServer(server)}
                    style={{ cursor: "pointer" }}
                    data-testid={`server-row-${server.name}`}
                  >
                    <td>
                      <strong>{server.name}</strong>
                      <small className="cellSubtext">{server.role}</small>
                    </td>
                    <td>
                      <StatusBadge
                        label={server.statusLabel}
                        value={server.status === "ready" ? "healthy" : server.status === "offline" ? "degraded" : server.status}
                      />
                    </td>
                    <td>
                      <span>{server.agentConnected ? `Active (${server.agentVersion})` : "Disconnected"}</span>
                    </td>
                    <td>
                      <span>{server.cpuCores !== undefined ? `${server.cpuCores} cores` : "Not reported"}</span>
                    </td>
                    <td>
                      <span>{server.memoryMB !== undefined ? `${server.memoryMB} MB` : "Not reported"}</span>
                    </td>
                    <td>
                      <span>
                        {server.placedWorkloadCount} placed
                        {server.degradedWorkloadCount > 0 ? (
                          <small className="warningText"> ({server.degradedWorkloadCount} degraded)</small>
                        ) : null}
                      </span>
                    </td>
                    <td>
                      <span className="mono">{server.publicHost}</span>
                    </td>
                    <td>
                      <small className="muted">{server.lastSeenFreshness}</small>
                    </td>
                    <td>
                      <button
                        className="textButton"
                        onClick={(e) => {
                          e.stopPropagation();
                          selectServer(server);
                        }}
                        type="button"
                      >
                        Inspect
                      </button>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      ) : (
        <Empty
          title="No servers registered"
          text="Connect or bootstrap a server node in Infrastructure to observe server telemetry."
        />
      )}

      {/* Server Detail Drawer */}
      {selectedServer ? (
        <ServerDetailSurface
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

function ServerDetailSurface({
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
    <div className="modalBackdrop" onClick={onClose} role="presentation">
      <section
        aria-label={`Server diagnostics for ${server.name}`}
        aria-modal="true"
        className="diagnosticDrawer"
        data-testid="server-detail-drawer"
        onClick={(e) => e.stopPropagation()}
        role="dialog"
      >
        <header className="drawerHeader">
          <div>
            <p className="eyebrow">Server Node Diagnostics · {server.role}</p>
            <h2>{server.name}</h2>
            <div className="drawerBadges">
              <StatusBadge
                label={`Status: ${server.statusLabel}`}
                value={server.status === "ready" ? "healthy" : server.status === "offline" ? "degraded" : server.status}
              />
              <span className="infoBadge">Agent: {server.agentConnected ? "Active" : "Offline"}</span>
            </div>
          </div>
          <div className="drawerHeaderActions">
            <a
              className="secondaryAction textLink"
              href={routeHref({ projectID, view: "infrastructure", tab: "servers", server: server.id })}
              onClick={(e) => {
                if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
                e.preventDefault();
                console.navigate({ projectID, view: "infrastructure", tab: "servers", server: server.id });
              }}
            >
              Open in Infrastructure
            </a>
            <button aria-label="Close diagnostics" className="closeButton" onClick={onClose} type="button">
              ✕
            </button>
          </div>
        </header>

        <nav aria-label="Server sections" className="consoleTabs drawerTabs">
          <button
            aria-selected={detailTab === "overview"}
            className={detailTab === "overview" ? "active" : ""}
            onClick={() => onTabChange("overview")}
            role="tab"
            type="button"
          >
            Overview
          </button>
          <button
            aria-selected={detailTab === "runtime"}
            className={detailTab === "runtime" ? "active" : ""}
            onClick={() => onTabChange("runtime")}
            role="tab"
            type="button"
          >
            Runtime
          </button>
          <button
            aria-selected={detailTab === "applications"}
            className={detailTab === "applications" ? "active" : ""}
            onClick={() => onTabChange("applications")}
            role="tab"
            type="button"
          >
            Applications ({server.placedWorkloadCount})
          </button>
          <button
            aria-selected={detailTab === "events"}
            className={detailTab === "events" ? "active" : ""}
            onClick={() => onTabChange("events")}
            role="tab"
            type="button"
          >
            Events ({auditEvents.length})
          </button>
        </nav>

        <div className="drawerContent">
          {detailTab === "overview" ? (
            <div className="drawerSectionStack">
              <Surface title="Server Identity & Agent Connectivity">
                <dl className="evidenceGrid">
                  <Fact label="Node ID" value={server.id} />
                  <Fact label="Public Host" value={server.publicHost} />
                  <Fact label="Role" value={server.role} />
                  <Fact label="Agent Status" value={server.agentConnected ? "Connected" : "Disconnected / Offline"} />
                  <Fact label="Agent Version" value={server.agentVersion || "Not reported"} />
                  <Fact label="Last Heartbeat" value={server.lastSeenFreshness} />
                </dl>
              </Surface>
            </div>
          ) : detailTab === "runtime" ? (
            <div className="drawerSectionStack">
              <Surface title="Reported Hardware Capacity">
                <dl className="evidenceGrid">
                  <Fact label="CPU Cores" value={server.cpuCores !== undefined ? `${server.cpuCores} cores` : "Not reported"} />
                  <Fact label="Memory Capacity" value={server.memoryMB !== undefined ? `${server.memoryMB} MB` : "Not reported"} />
                  <Fact label="Placed Workloads" value={`${server.placedWorkloadCount} workloads`} />
                  <Fact label="Degraded Workloads" value={`${server.degradedWorkloadCount} workloads`} />
                </dl>
              </Surface>
            </div>
          ) : detailTab === "applications" ? (
            <div className="drawerSectionStack">
              <Surface title="Placed Applications">
                {server.placedServices.length > 0 ? (
                  <ul className="compactList">
                    {server.placedServices.map((svcName) => (
                      <li key={svcName}>
                        <strong>{svcName}</strong>
                        <small>Assigned to node {server.name}</small>
                      </li>
                    ))}
                  </ul>
                ) : (
                  <Empty title="No placed applications" text="No applications are currently assigned to this server node." />
                )}
              </Surface>
            </div>
          ) : (
            <div className="drawerSectionStack">
              <Surface title="Server Events">
                {auditEvents.length > 0 ? (
                  <ol className="eventTimeline">
                    {auditEvents.map((evt) => (
                      <li key={evt.id}>
                        <span className="timelineDot info" aria-hidden="true" />
                        <div>
                          <div className="eventHeader">
                            <strong>{evt.action.replaceAll("_", " ")}</strong>
                            <time dateTime={evt.created_at}>
                              {formatObserved(Date.parse(evt.created_at) / 1000)}
                            </time>
                          </div>
                          <p className="eventDetail">
                            Actor {evt.actor_user_id || "system"} · result: {evt.result}
                          </p>
                        </div>
                      </li>
                    ))}
                  </ol>
                ) : (
                  <Empty title="No recent events" text="No audit or lifecycle events recorded for this server." />
                )}
              </Surface>
            </div>
          )}
        </div>
      </section>
    </div>
  );
}
