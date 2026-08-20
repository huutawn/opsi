"use client";

import { useState } from "react";
import { Button, Empty, Icon, StatusBadge } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";
import type { ObservabilityModel } from "@/features/observability/observability-view";
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
    <div className="space-y-6" data-testid="observability-resources">
      <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-4">
        <div>
          <p className="font-label-sm text-xs text-primary uppercase tracking-wider">Data Services & Cache</p>
          <h2 className="font-headline-md text-xl font-bold text-on-surface">Managed Resources Observability</h2>
          <p className="text-xs text-on-surface-variant mt-0.5">
            Operational readiness for PostgreSQL, Valkey, and NATS.
          </p>
        </div>
        <div>
          <Button
            disabled={model.data.sources.resources === "loading"}
            onClick={() => void model.load()}
            size="sm"
            variant="secondary"
          >
            <Icon name="refresh" className="text-[16px]" />
            Refresh Resources
          </Button>
        </div>
      </div>

      {resources.length > 0 ? (
        <div className="bg-surface-container-low border border-outline-variant/20 rounded-2xl overflow-hidden shadow-sm">
          <div className="overflow-x-auto">
            <table className="w-full text-left border-collapse" aria-label="Managed resources runtime inventory">
              <thead>
                <tr className="bg-surface-container/60 border-b border-outline-variant/20 text-[11px] font-label-sm uppercase tracking-wider text-on-surface-variant">
                  <th className="py-3 px-4 font-semibold">Resource</th>
                  <th className="py-3 px-4 font-semibold">Engine</th>
                  <th className="py-3 px-4 font-semibold">Version</th>
                  <th className="py-3 px-4 font-semibold">Readiness</th>
                  <th className="py-3 px-4 font-semibold">Bound Apps</th>
                  <th className="py-3 px-4 font-semibold">Server</th>
                  <th className="py-3 px-4 font-semibold">Storage</th>
                  <th className="py-3 px-4 text-right"><span className="sr-only">Actions</span></th>
                </tr>
              </thead>
              <tbody className="divide-y divide-outline-variant/15 text-xs text-on-surface">
                {resources.map((res) => {
                  const isSelected = selectedResource?.id === res.id;
                  return (
                    <tr
                      className={`hover:bg-surface-container/60 transition-colors cursor-pointer ${
                        isSelected ? "bg-primary-container/30 ring-1 ring-inset ring-primary/40" : ""
                      }`}
                      key={res.id}
                      onClick={() => selectResource(res)}
                      data-testid={`resource-row-${res.name}`}
                    >
                      <td className="py-3.5 px-4 font-semibold text-on-surface font-body-md">
                        {res.name}
                      </td>
                      <td className="py-3.5 px-4">
                        <span className="bg-surface-container px-2 py-0.5 rounded text-[11px] font-medium border border-outline-variant/20">
                          {res.typeLabel}
                        </span>
                      </td>
                      <td className="py-3.5 px-4 font-code-md text-on-surface-variant">
                        {res.version || "Not reported"}
                      </td>
                      <td className="py-3.5 px-4">
                        <StatusBadge
                          label={res.statusLabel}
                          value={res.status === "ready" ? "healthy" : res.status === "degraded" ? "degraded" : res.status === "failed" ? "failed" : "unknown"}
                        />
                      </td>
                      <td className="py-3.5 px-4 font-semibold">
                        {res.applicationBindingCount} bound
                      </td>
                      <td className="py-3.5 px-4 font-code-md text-on-surface-variant">
                        {res.serverPlacement}
                      </td>
                      <td className="py-3.5 px-4 font-code-md text-on-surface-variant">
                        {res.storageBytes ? formatBytes(res.storageBytes) : res.persistentStorage ? "Persistent" : "In-memory"}
                      </td>
                      <td className="py-3.5 px-4 text-right">
                        <Button
                          onClick={(e) => {
                            e.stopPropagation();
                            selectResource(res);
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
          title="No managed resources provisioned"
          text="Create a PostgreSQL database, Valkey cache, or NATS broker in Infrastructure to observe resource readiness."
        />
      )}

      {/* Resource Detail Drawer */}
      {selectedResource ? (
        <ResourceDetailDrawer
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

function ResourceDetailDrawer({
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
    <div className="fixed inset-0 z-50 bg-black/60 backdrop-blur-sm flex justify-end" onClick={onClose} role="presentation">
      <section
        aria-label={`Resource diagnostics for ${resource.name}`}
        aria-modal="true"
        className="w-full max-w-2xl h-full bg-surface-container-low border-l border-outline-variant/30 shadow-2xl flex flex-col text-on-surface overflow-hidden"
        data-testid="resource-detail-drawer"
        onClick={(e) => e.stopPropagation()}
        role="dialog"
      >
        {/* Header */}
        <header className="p-6 border-b border-outline-variant/20 flex items-start justify-between gap-4 bg-surface-container/50">
          <div>
            <span className="font-label-sm text-xs text-primary font-bold uppercase tracking-wider block mb-1">
              Managed Resource Diagnostics • {resource.typeLabel}
            </span>
            <h2 className="font-headline-md text-2xl font-bold text-on-surface">{resource.name}</h2>
            <div className="flex items-center gap-2 mt-2">
              <StatusBadge
                label={`Status: ${resource.statusLabel}`}
                value={resource.status === "ready" ? "healthy" : resource.status === "degraded" ? "degraded" : resource.status === "failed" ? "failed" : "unknown"}
              />
              <span className="text-xs px-2.5 py-0.5 rounded-md bg-surface-container-high font-mono text-on-surface-variant">
                Engine: {resource.typeLabel} {resource.version ? `v${resource.version}` : ""}
              </span>
            </div>
          </div>

          <div className="flex items-center gap-2">
            <Button
              onClick={() => console.navigate({ projectID, view: "infrastructure", tab: "resources", resource: resource.id })}
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

        {/* Tabs */}
        <nav aria-label="Resource sections" className="flex items-center gap-1 border-b border-outline-variant/20 px-6 pt-2 bg-surface-container/20">
          {(["overview", "runtime", "applications"] as const).map((t) => {
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
                {t === "applications" ? `Bound Applications (${resource.applicationBindingCount})` : t}
              </button>
            );
          })}
        </nav>

        {/* Content */}
        <div className="flex-1 overflow-y-auto p-6 space-y-6">
          {detailTab === "overview" ? (
            <div className="space-y-6">
              <div className="grid grid-cols-2 sm:grid-cols-3 gap-3 bg-surface-container p-4 rounded-2xl border border-outline-variant/15 text-xs">
                <div>
                  <span className="text-on-surface-variant text-[11px] block mb-0.5">Resource Name</span>
                  <strong className="text-on-surface font-semibold block">{resource.name}</strong>
                </div>
                <div>
                  <span className="text-on-surface-variant text-[11px] block mb-0.5">Resource ID</span>
                  <strong className="text-on-surface font-code-md truncate block">{resource.id}</strong>
                </div>
                <div>
                  <span className="text-on-surface-variant text-[11px] block mb-0.5">Engine</span>
                  <strong className="text-on-surface">{resource.typeLabel}</strong>
                </div>
                <div>
                  <span className="text-on-surface-variant text-[11px] block mb-0.5">Engine Version</span>
                  <strong className="text-on-surface font-code-md">{resource.version || "Not reported"}</strong>
                </div>
                <div>
                  <span className="text-on-surface-variant text-[11px] block mb-0.5">Readiness</span>
                  <strong className="text-on-surface">{resource.statusLabel}</strong>
                </div>
                <div>
                  <span className="text-on-surface-variant text-[11px] block mb-0.5">Server Placement</span>
                  <strong className="text-on-surface font-code-md">{resource.serverPlacement}</strong>
                </div>
              </div>

              {resource.type.toLowerCase().includes("postgres") ? (
                <div className="bg-surface-container/60 p-4 rounded-2xl border border-outline-variant/15 space-y-2 text-xs">
                  <h3 className="font-headline-md text-sm font-bold text-on-surface">PostgreSQL Safe Runtime Facts</h3>
                  <p className="text-on-surface-variant">
                    Volume Mount: {resource.persistentStorage ? "Mounted persistent volume" : "Ephemeral"}
                  </p>
                  <div className="space-y-1">
                    <strong className="text-on-surface block">Credentials Safety</strong>
                    <p className="text-on-surface-variant text-[11px]">
                      Passwords & connection strings protected; never exposed in Observability.
                    </p>
                  </div>
                </div>
              ) : null}
            </div>
          ) : detailTab === "runtime" ? (
            <div className="space-y-6">
              <div className="grid grid-cols-2 gap-3 bg-surface-container p-4 rounded-2xl border border-outline-variant/15 text-xs">
                <div>
                  <span className="text-on-surface-variant text-[11px] block mb-0.5">Allocated CPU</span>
                  <strong className="text-on-surface font-code-md text-base">{resource.allocatedCPU !== undefined ? `${resource.allocatedCPU} cores` : "Standard profile"}</strong>
                </div>
                <div>
                  <span className="text-on-surface-variant text-[11px] block mb-0.5">Allocated Memory</span>
                  <strong className="text-on-surface font-code-md text-base">{resource.allocatedMemoryBytes ? formatBytes(resource.allocatedMemoryBytes) : "Standard profile"}</strong>
                </div>
                <div>
                  <span className="text-on-surface-variant text-[11px] block mb-0.5">Storage Size</span>
                  <strong className="text-on-surface font-code-md">{resource.storageBytes ? formatBytes(resource.storageBytes) : resource.persistentStorage ? "Persistent storage" : "In-memory"}</strong>
                </div>
                <div>
                  <span className="text-on-surface-variant text-[11px] block mb-0.5">Last Operation</span>
                  <strong className="text-on-surface">{resource.lastOperation || "Normal operation"}</strong>
                </div>
              </div>
            </div>
          ) : (
            <div className="space-y-4">
              {resource.boundServiceKeys.length > 0 ? (
                <div className="bg-surface-container rounded-2xl border border-outline-variant/15 p-4 space-y-2">
                  <h3 className="font-headline-md text-sm font-bold text-on-surface mb-3">Bound Applications</h3>
                  <ul className="divide-y divide-outline-variant/15 text-xs">
                    {resource.boundServiceKeys.map((svcKey) => (
                      <li className="py-2.5 flex items-center justify-between" key={svcKey}>
                        <strong className="text-on-surface font-semibold">{svcKey}</strong>
                        <span className="text-on-surface-variant font-code-md">Bound to {resource.name} ({resource.typeLabel})</span>
                      </li>
                    ))}
                  </ul>
                </div>
              ) : (
                <div className="p-8 text-center text-xs text-on-surface-variant bg-surface-container rounded-2xl border border-outline-variant/15">
                  No applications are currently connected to this managed resource.
                </div>
              )}
            </div>
          )}
        </div>
      </section>
    </div>
  );
}
