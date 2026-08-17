"use client";

import { useState } from "react";
import { Empty, StatusBadge, Surface } from "@/components/ui/primitives";
import { routeHref } from "@/features/console/navigation";
import type { ConsoleController } from "@/features/console/types";
import type { ObservabilityModel } from "@/features/observability/observability-view";
import { Fact } from "@/features/observability/shared";
import type { ResourceRuntimeSummary } from "@/lib/presentation/observability/model";
import { formatBytes } from "@/lib/presentation/resources/model";

export function ResourcesTab({
  console,
  model,
}: {
  console: ConsoleController;
  model: ObservabilityModel;
}) {
  const projectID = console.route.projectID || console.state.project?.id || "";
  const resources = model.data.managedResources;
  const selectedResourceID = console.route.resource || "";
  const selectedResource = resources.find((r) => r.id === selectedResourceID) ?? null;

  const [detailTab, setDetailTab] = useState<"overview" | "runtime" | "applications">("overview");

  function selectResource(resource: ResourceRuntimeSummary | null) {
    console.navigate({
      resource: resource ? resource.id : "",
    });
  }

  return (
    <div className="observabilityStack" data-testid="observability-resources">
      <div className="observabilityHero">
        <div>
          <p className="eyebrow">Data Services & In-Memory Infrastructure</p>
          <h2>Managed Resources Observability</h2>
          <p>
            Factual operational readiness for PostgreSQL databases, Valkey caching layers, and NATS event brokers.
          </p>
        </div>
        <div className="heroStatus">
          <button
            className="secondaryAction"
            disabled={model.data.sources.resources === "loading"}
            onClick={() => void model.load()}
            type="button"
          >
            Refresh Resources
          </button>
        </div>
      </div>

      {resources.length > 0 ? (
        <div className="tableWrap">
          <table className="dataTable" aria-label="Managed resources runtime inventory">
            <thead>
              <tr>
                <th scope="col">Resource</th>
                <th scope="col">Engine</th>
                <th scope="col">Version</th>
                <th scope="col">Readiness</th>
                <th scope="col">Bound Applications</th>
                <th scope="col">Server</th>
                <th scope="col">Storage</th>
                <th scope="col"><span className="srOnly">Actions</span></th>
              </tr>
            </thead>
            <tbody>
              {resources.map((res) => {
                const isSelected = selectedResource?.id === res.id;
                return (
                  <tr
                    className={isSelected ? "selectedRow" : ""}
                    key={res.id}
                    onClick={() => selectResource(res)}
                    style={{ cursor: "pointer" }}
                    data-testid={`resource-row-${res.name}`}
                  >
                    <td>
                      <strong>{res.name}</strong>
                    </td>
                    <td>
                      <span className="typePill">{res.typeLabel}</span>
                    </td>
                    <td>
                      <span className="mono">{res.version || "Not reported"}</span>
                    </td>
                    <td>
                      <StatusBadge
                        label={res.statusLabel}
                        value={res.status === "ready" ? "healthy" : res.status === "degraded" ? "degraded" : res.status === "failed" ? "failed" : "unknown"}
                      />
                    </td>
                    <td>
                      <span>{res.applicationBindingCount} bound</span>
                    </td>
                    <td>
                      <span>{res.serverPlacement}</span>
                    </td>
                    <td>
                      <span>{res.storageBytes ? formatBytes(res.storageBytes) : res.persistentStorage ? "Persistent" : "In-memory"}</span>
                    </td>
                    <td>
                      <button
                        className="textButton"
                        onClick={(e) => {
                          e.stopPropagation();
                          selectResource(res);
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
          title="No managed resources provisioned"
          text="Create a PostgreSQL database, Valkey cache, or NATS broker in Infrastructure to observe resource readiness."
        />
      )}

      {/* Resource Detail Drawer */}
      {selectedResource ? (
        <ResourceDetailSurface
          console={console}
          detailTab={detailTab}
          onClose={() => selectResource(null)}
          onTabChange={setDetailTab}
          projectID={projectID}
          resource={selectedResource}
        />
      ) : null}
    </div>
  );
}

function ResourceDetailSurface({
  console,
  detailTab,
  onClose,
  onTabChange,
  projectID,
  resource,
}: {
  console: ConsoleController;
  detailTab: "overview" | "runtime" | "applications";
  onClose: () => void;
  onTabChange: (tab: "overview" | "runtime" | "applications") => void;
  projectID: string;
  resource: ResourceRuntimeSummary;
}) {
  return (
    <div className="modalBackdrop" onClick={onClose} role="presentation">
      <section
        aria-label={`Resource diagnostics for ${resource.name}`}
        aria-modal="true"
        className="diagnosticDrawer"
        data-testid="resource-detail-drawer"
        onClick={(e) => e.stopPropagation()}
        role="dialog"
      >
        <header className="drawerHeader">
          <div>
            <p className="eyebrow">Managed Resource Diagnostics · {resource.typeLabel}</p>
            <h2>{resource.name}</h2>
            <div className="drawerBadges">
              <StatusBadge
                label={`Status: ${resource.statusLabel}`}
                value={resource.status === "ready" ? "healthy" : resource.status === "degraded" ? "degraded" : resource.status === "failed" ? "failed" : "unknown"}
              />
              <span className="infoBadge">Engine: {resource.typeLabel} {resource.version ? `v${resource.version}` : ""}</span>
            </div>
          </div>
          <div className="drawerHeaderActions">
            <a
              className="secondaryAction textLink"
              href={routeHref({ projectID, view: "infrastructure", tab: "resources", resource: resource.id })}
              onClick={(e) => {
                if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
                e.preventDefault();
                console.navigate({ projectID, view: "infrastructure", tab: "resources", resource: resource.id });
              }}
            >
              Open in Infrastructure
            </a>
            <button aria-label="Close diagnostics" className="closeButton" onClick={onClose} type="button">
              ✕
            </button>
          </div>
        </header>

        <nav aria-label="Resource sections" className="consoleTabs drawerTabs">
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
            Bound Applications ({resource.applicationBindingCount})
          </button>
        </nav>

        <div className="drawerContent">
          {detailTab === "overview" ? (
            <div className="drawerSectionStack">
              <Surface title="Resource Identity & Engine">
                <dl className="evidenceGrid">
                  <Fact label="Resource Name" value={resource.name} />
                  <Fact label="Resource ID" value={resource.id} />
                  <Fact label="Engine" value={resource.typeLabel} />
                  <Fact label="Engine Version" value={resource.version || "Not reported"} />
                  <Fact label="Readiness Status" value={resource.statusLabel} />
                  <Fact label="Server Placement" value={resource.serverPlacement} />
                </dl>
              </Surface>

              {resource.type.toLowerCase().includes("postgres") ? (
                <Surface title="PostgreSQL Safe Runtime Facts">
                  <dl className="evidenceGrid">
                    <Fact label="Service Readiness" value={resource.statusLabel} />
                    <Fact label="PostgreSQL Version" value={resource.version || "16"} />
                    <Fact label="Volume Mount" value={resource.persistentStorage ? "Mounted persistent volume" : "Ephemeral"} />
                    <Fact label="Credentials Safety" value="Passwords & connection strings protected; never exposed in Observability." />
                  </dl>
                </Surface>
              ) : null}
            </div>
          ) : detailTab === "runtime" ? (
            <div className="drawerSectionStack">
              <Surface title="Allocated Capacity">
                <dl className="evidenceGrid">
                  <Fact label="Allocated CPU" value={resource.allocatedCPU !== undefined ? `${resource.allocatedCPU} cores` : "Standard profile"} />
                  <Fact label="Allocated Memory" value={resource.allocatedMemoryBytes ? formatBytes(resource.allocatedMemoryBytes) : "Standard profile"} />
                  <Fact label="Storage Size" value={resource.storageBytes ? formatBytes(resource.storageBytes) : resource.persistentStorage ? "Persistent storage" : "In-memory / Ephemeral"} />
                  <Fact label="Last Operation" value={resource.lastOperation || "Normal operation"} />
                  {resource.lastFailure ? <Fact label="Last Failure Detail" value={resource.lastFailure} /> : null}
                </dl>
              </Surface>
            </div>
          ) : (
            <div className="drawerSectionStack">
              <Surface title="Bound Applications">
                {resource.boundServiceKeys.length > 0 ? (
                  <ul className="compactList">
                    {resource.boundServiceKeys.map((svcKey) => (
                      <li key={svcKey}>
                        <strong>{svcKey}</strong>
                        <small>Bound to {resource.name} ({resource.typeLabel})</small>
                      </li>
                    ))}
                  </ul>
                ) : (
                  <Empty title="No bound applications" text="No applications are currently connected to this managed resource." />
                )}
              </Surface>
            </div>
          )}
        </div>
      </section>
    </div>
  );
}
