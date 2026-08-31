"use client";

import { useEffect, useState } from "react";
import { Button, Empty, Icon, StatusBadge } from "@/components/ui/primitives";
import { routeHref } from "@/features/console/navigation";
import type { ConsoleController } from "@/features/console/types";
import type { ObservabilityModel } from "@/features/observability/observability-view";
import { safeLogMessage } from "@/features/observability/data";
import { formatObserved } from "@/features/observability/shared";
import { LocalClient } from "@/lib/api/local-client";
import { formatShortDigest, type ApplicationRuntimeSummary, type RuntimeEvent } from "@/lib/presentation/observability/model";
import type { TelemetryLogEntry, TelemetryQueryResponse } from "@/lib/contracts/registry";

type DetailTab = "overview" | "workload" | "dependencies" | "logs" | "events" | "exposure";

export function ApplicationsTab({
  console,
  model,
}: {
  console: ConsoleController;
  model: ObservabilityModel;
}) {
  const projectID = console.route.projectID || console.state.project?.id || (console.state.projects && console.state.projects[0]?.id) || "proj-1";
  const applications = model.data.applications;
  const selectedServiceID = console.route.service || "";
  const selectedApp = applications.find((a) => a.id === selectedServiceID || a.key === selectedServiceID) ?? null;

  const [detailTab, setDetailTab] = useState<DetailTab>("overview");

  function selectApp(app: ApplicationRuntimeSummary | null) {
    console.navigate({
      service: app ? app.id : "",
    });
  }

  return (
    <div className="space-y-6" data-testid="observability-applications">
      <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-4">
        <div>
          <p className="font-label-sm text-xs text-primary uppercase tracking-wider">Runtime Inventory</p>
          <h2 className="font-headline-md text-xl font-bold text-on-surface">Applications Runtime</h2>
          <p className="text-xs text-on-surface-variant mt-0.5">
            Workload health, replica readiness, server placement, and logs.
          </p>
        </div>
        <div>
          <Button
            disabled={model.data.sources.telemetry === "loading"}
            onClick={() => void model.load()}
            size="sm"
            variant="secondary"
          >
            <Icon name="refresh" className="text-[16px]" />
            Refresh Inventory
          </Button>
        </div>
      </div>

      {applications.length > 0 ? (
        <div className="bg-surface-container-low border border-outline-variant/20 rounded-2xl overflow-hidden shadow-sm">
          <div className="overflow-x-auto">
            <table className="w-full text-left border-collapse" aria-label="Applications runtime inventory">
              <thead>
                <tr className="bg-surface-container/60 border-b border-outline-variant/20 text-[11px] font-label-sm uppercase tracking-wider text-on-surface-variant">
                  <th className="py-3 px-4 font-semibold">Application</th>
                  <th className="py-3 px-4 font-semibold">Workload Status</th>
                  <th className="py-3 px-4 font-semibold">Replicas</th>
                  <th className="py-3 px-4 font-semibold">Server</th>
                  <th className="py-3 px-4 font-semibold">Revision</th>
                  <th className="py-3 px-4 font-semibold">Image Digest</th>
                  <th className="py-3 px-4 font-semibold">Exposure</th>
                  <th className="py-3 px-4 font-semibold">Last Deployment</th>
                  <th className="py-3 px-4 font-semibold">Observed</th>
                  <th className="py-3 px-4 text-right"><span className="sr-only">Actions</span></th>
                </tr>
              </thead>
              <tbody className="divide-y divide-outline-variant/15 text-xs text-on-surface">
                {applications.map((app) => {
                  const isSelected = selectedApp?.id === app.id;
                  return (
                    <tr
                      className={`hover:bg-surface-container/60 transition-colors cursor-pointer ${
                        isSelected ? "bg-primary-container/30 ring-1 ring-inset ring-primary/40" : ""
                      }`}
                      key={app.id}
                      onClick={() => selectApp(app)}
                      data-testid={`app-row-${app.key || app.id}`}
                    >
                      <td className="py-3.5 px-4 font-semibold">
                        <span className="text-on-surface font-body-md block">{app.name}</span>
                        <span className="text-[11px] text-on-surface-variant font-code-md block">{app.environment}</span>
                      </td>
                      <td className="py-3.5 px-4">
                        <StatusBadge
                          label={app.workloadLabel}
                          value={app.workloadStatus === "ready" ? "healthy" : app.workloadStatus}
                        />
                      </td>
                      <td className="py-3.5 px-4 font-code-md">
                        {app.replicasLabel}
                      </td>
                      <td className="py-3.5 px-4 text-on-surface-variant font-code-md">
                        {app.serverPlacement}
                      </td>
                      <td className="py-3.5 px-4 font-code-md text-on-surface-variant">
                        rev {app.configurationRevision}
                      </td>
                      <td className="py-3.5 px-4 font-code-md text-on-surface-variant" title={app.deployedDigest || "Not reported"}>
                        {app.shortDigest}
                      </td>
                      <td className="py-3.5 px-4">
                        <StatusBadge
                          label={app.exposureLabel}
                          value={app.exposureStatus === "ready" ? "healthy" : app.exposureStatus === "not_configured" ? "unknown" : app.exposureStatus}
                        />
                      </td>
                      <td className="py-3.5 px-4">
                        <StatusBadge
                          label={app.lastDeploymentLabel}
                          value={app.lastDeploymentOutcome === "succeeded" ? "healthy" : app.lastDeploymentOutcome}
                        />
                      </td>
                      <td className="py-3.5 px-4 text-on-surface-variant font-code-md text-[11px]">
                        {app.lastSeenFreshness}
                      </td>
                      <td className="py-3.5 px-4 text-right">
                        <Button
                          onClick={(e) => {
                            e.stopPropagation();
                            selectApp(app);
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
          title="No applications in this project"
          text="Deploy an Application through Delivery or Topology to observe runtime diagnostics."
        />
      )}

      {/* Application Detail Slide-Over Drawer */}
      {selectedApp ? (
        <ApplicationDetailDrawer
          app={selectedApp}
          console={console}
          detailTab={detailTab}
          model={model}
          onClose={() => selectApp(null)}
          onTabChange={setDetailTab}
          projectID={projectID}
        />
      ) : null}
    </div>
  );
}

function ApplicationDetailDrawer({
  app,
  console,
  detailTab,
  model,
  onClose,
  onTabChange,
  projectID,
}: {
  app: ApplicationRuntimeSummary;
  console: ConsoleController;
  detailTab: DetailTab;
  model: ObservabilityModel;
  onClose: () => void;
  onTabChange: (tab: DetailTab) => void;
  projectID: string;
}) {
  const events = model.getApplicationEvents(app.id, app.key);

  return (
    <div className="fixed inset-0 z-50 bg-black/60 backdrop-blur-sm flex justify-end" onClick={onClose} role="presentation">
      <section
        aria-label={`Application runtime diagnostics for ${app.name}`}
        aria-modal="true"
        className="w-full max-w-2xl h-full bg-surface-container-low border-l border-outline-variant/30 shadow-2xl flex flex-col text-on-surface overflow-hidden"
        data-testid="application-detail-drawer"
        onClick={(e) => e.stopPropagation()}
        role="dialog"
      >
        {/* Drawer Header */}
        <header className="p-6 border-b border-outline-variant/20 flex items-start justify-between gap-4 bg-surface-container/50">
          <div>
            <span className="font-label-sm text-xs text-primary font-bold uppercase tracking-wider block mb-1">
              Application Diagnostics • {app.environment}
            </span>
            <h2 className="font-headline-md text-2xl font-bold text-on-surface">{app.name}</h2>
            <div className="flex items-center gap-2 mt-2">
              <StatusBadge
                label={`Runtime: ${app.workloadLabel}`}
                value={app.workloadStatus === "ready" ? "healthy" : app.workloadStatus}
              />
              <StatusBadge
                label={`Deployment: ${app.lastDeploymentLabel}`}
                value={app.lastDeploymentOutcome === "succeeded" ? "healthy" : app.lastDeploymentOutcome}
              />
            </div>
          </div>

          <div className="flex items-center gap-2">
            <a
              className="inline-flex items-center justify-center px-3 py-1.5 text-xs font-medium rounded-lg border border-outline-variant text-on-surface hover:bg-surface-container-high transition-colors"
              href={routeHref({ projectID, view: "deploy", service: app.id })}
              onClick={(e) => {
                if (e.metaKey || e.ctrlKey) return;
                e.preventDefault();
                console.navigate({ projectID, view: "deploy", service: app.id });
              }}
            >
              Open in Deploy
            </a>
            <a
              className="inline-flex items-center justify-center px-3 py-1.5 text-xs font-medium rounded-lg border border-outline-variant text-on-surface hover:bg-surface-container-high transition-colors"
              href={routeHref({ projectID, view: "deploy", service: app.id })}
              onClick={(e) => {
                if (e.metaKey || e.ctrlKey) return;
                e.preventDefault();
                console.navigate({ projectID, view: "deploy", service: app.id });
              }}
            >
              Open in Topology
            </a>
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
        <nav aria-label="Diagnostic sections" className="flex items-center gap-1 border-b border-outline-variant/20 px-6 pt-2 bg-surface-container/20">
          {(["overview", "workload", "dependencies", "logs", "events", "exposure"] as const).map((t) => {
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
                {t === "events" ? `Events (${events.length})` : t}
              </button>
            );
          })}
        </nav>

        {/* Drawer Content Body */}
        <div className="flex-1 overflow-y-auto p-6 space-y-6">
          {detailTab === "overview" ? (
            <AppOverviewSection app={app} />
          ) : detailTab === "workload" ? (
            <AppWorkloadSection app={app} />
          ) : detailTab === "dependencies" ? (
            <AppDependenciesSection app={app} console={console} />
          ) : detailTab === "logs" ? (
            <AppLogsSection app={app} projectID={projectID} />
          ) : detailTab === "events" ? (
            <AppEventsSection events={events} />
          ) : (
            <AppExposureSection app={app} />
          )}
        </div>
      </section>
    </div>
  );
}

function AppDependenciesSection({
  app,
  console,
}: {
  app: ApplicationRuntimeSummary;
  console: ConsoleController;
}) {
  const service = console.state.services.find((s) => s.id === app.id || s.name === app.key);
  const dependencies = service?.configuration?.dependencies ?? [];

  if (!dependencies.length) {
    return (
      <div className="bg-surface-container rounded-xl p-8 border border-outline-variant/20 text-center space-y-3">
        <Icon name="hub" className="text-[32px] text-on-surface-variant/40 mx-auto" />
        <h4 className="font-headline-md text-base text-on-surface font-semibold">No Dependencies Declared</h4>
        <p className="text-xs text-on-surface-variant max-w-md mx-auto">
          This application does not declare any explicit database, cache, or HTTP dependencies in its configuration.
        </p>
      </div>
    );
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h4 className="font-headline-md text-sm font-semibold text-on-surface">
          Declared runtime dependencies ({dependencies.length})
        </h4>
        <span className="text-xs font-code-md text-on-surface-variant">
          Read-only authority facts
        </span>
      </div>

      <div className="space-y-4">
        {dependencies.map((dep) => (
          <article className="rounded-xl border border-outline-variant/20 bg-surface-container p-4" key={dep.logical_name}>
            <div className="flex flex-wrap items-start justify-between gap-3">
              <div>
                <h5 className="font-semibold text-on-surface">{dep.logical_name}</h5>
                <p className="mt-1 text-xs text-on-surface-variant">
                  {dep.protocol} · {dep.target_kind} · {dep.target_identity}
                </p>
              </div>
              <StatusBadge label={dep.required ? "Required" : "Optional"} value={dep.required ? "healthy" : "unknown"} />
            </div>
            <p className="mt-3 text-xs text-on-surface-variant">
              {dep.verification_contract?.path
                ? `Verification contract: ${dep.verification_contract.type || "http"} ${dep.verification_contract.path} → ${dep.verification_contract.expected_status || 200}`
                : "No verification contract is recorded in the current service configuration."}
            </p>
          </article>
        ))}
      </div>
    </div>
  );
}

function AppOverviewSection({ app }: { app: ApplicationRuntimeSummary }) {
  return (
    <div className="space-y-6">
      <div className="grid grid-cols-2 sm:grid-cols-3 gap-3 bg-surface-container p-4 rounded-2xl border border-outline-variant/15 text-xs">
        <div>
          <span className="text-on-surface-variant text-[11px] block mb-0.5">Runtime Status</span>
          <strong className="text-on-surface">{app.workloadLabel}</strong>
        </div>
        <div>
          <span className="text-on-surface-variant text-[11px] block mb-0.5">Ready Replicas</span>
          <strong className="text-on-surface font-code-md">{app.replicasLabel}</strong>
        </div>
        <div>
          <span className="text-on-surface-variant text-[11px] block mb-0.5">Server Node</span>
          <strong className="text-on-surface font-code-md">{app.serverPlacement}</strong>
        </div>
        <div>
          <span className="text-on-surface-variant text-[11px] block mb-0.5">Active Revision</span>
          <strong className="text-on-surface font-code-md">rev {app.configurationRevision}</strong>
        </div>
        <div>
          <span className="text-on-surface-variant text-[11px] block mb-0.5">Image Digest</span>
          <strong className="text-primary font-code-md truncate block" title={app.deployedDigest || "Not reported"}>
            {formatShortDigest(app.deployedDigest)}
          </strong>
        </div>
        <div>
          <span className="text-on-surface-variant text-[11px] block mb-0.5">Exposure Route</span>
          <strong className="text-on-surface">{app.exposureLabel}</strong>
        </div>
      </div>

      <section className="bg-surface-container/60 p-4 rounded-2xl border border-outline-variant/15 space-y-3">
        <h3 className="font-headline-md text-sm font-bold text-on-surface">Health & Availability</h3>
        <div className="grid grid-cols-3 gap-3 text-xs">
          <div>
            <span className="text-on-surface-variant text-[11px] block mb-0.5">Restarts</span>
            <strong className="text-on-surface">{app.restartCount} restart{app.restartCount === 1 ? "" : "s"}</strong>
          </div>
          <div>
            <span className="text-on-surface-variant text-[11px] block mb-0.5">Recent Errors</span>
            <strong className="text-on-surface">{app.recentErrorCount} error{app.recentErrorCount === 1 ? "" : "s"}</strong>
          </div>
          <div>
            <span className="text-on-surface-variant text-[11px] block mb-0.5">Last Seen</span>
            <strong className="text-on-surface">{app.lastSeenFreshness}</strong>
          </div>
        </div>
        {app.failureReason ? (
          <div className="p-3 bg-error-container/20 border border-error/30 rounded-xl text-xs text-error">
            <strong>Failure Context:</strong> {app.failureReason}
          </div>
        ) : null}
      </section>

      {app.boundResourceCount > 0 ? (
        <section className="bg-surface-container/60 p-4 rounded-2xl border border-outline-variant/15 space-y-3">
          <h3 className="font-headline-md text-sm font-bold text-on-surface">Bound Resources ({app.boundResourceCount})</h3>
          <ul className="divide-y divide-outline-variant/15 text-xs">
            {app.boundResourceNames.map((resName, idx) => (
              <li className="py-2 flex items-center justify-between" key={resName}>
                <span className="text-on-surface font-semibold">{resName}</span>
                <span className="text-on-surface-variant font-code-md">({app.boundResourceTypes[idx] ?? "managed"})</span>
              </li>
            ))}
          </ul>
        </section>
      ) : null}
    </div>
  );
}

function AppWorkloadSection({ app }: { app: ApplicationRuntimeSummary }) {
  return (
    <div className="space-y-6">
      <div className="p-4 bg-surface-container rounded-2xl border border-outline-variant/15 flex items-start gap-4">
        <StatusBadge value={app.workloadStatus === "ready" ? "healthy" : app.workloadStatus} />
        <div>
          <strong className="text-sm font-bold text-on-surface block mb-1">
            {app.workloadLabel} ({app.replicasLabel})
          </strong>
          <p className="text-xs text-on-surface-variant">
            {app.workloadStatus === "ready"
              ? "All configured replicas are passing container health checks."
              : app.workloadStatus === "degraded"
              ? `${app.readyReplicas} of ${app.desiredReplicas} replicas ready. Workload is serving with reduced capacity.`
              : "No workload replicas are ready to accept traffic."}
          </p>
        </div>
      </div>

      <div className="grid grid-cols-2 gap-3 bg-surface-container/60 p-4 rounded-2xl border border-outline-variant/15 text-xs">
        <div>
          <span className="text-on-surface-variant block mb-0.5">Ready Replicas</span>
          <strong className="text-on-surface font-code-md">{app.readyReplicas}</strong>
        </div>
        <div>
          <span className="text-on-surface-variant block mb-0.5">Desired Replicas</span>
          <strong className="text-on-surface font-code-md">{app.desiredReplicas}</strong>
        </div>
      </div>
    </div>
  );
}

function AppLogsSection({
  app,
  projectID,
}: {
  app: ApplicationRuntimeSummary;
  projectID: string;
}) {
  const [logs, setLogs] = useState<TelemetryLogEntry[]>([]);
  const [loading, setLoading] = useState(false);
  const [filter, setFilter] = useState<string>("all");

  const [searchQuery, setSearchQuery] = useState("");

  useEffect(() => {
    let active = true;
    async function fetchLogs() {
      setLoading(true);
      try {
        const client = new LocalClient();
        const res: TelemetryQueryResponse = await client.logs(projectID, { serviceID: app.id, limit: 100 });
        if (!active) return;
        setLogs(res.logs || []);
      } catch {
        if (!active) return;
        setLogs([]);
      } finally {
        if (active) setLoading(false);
      }
    }
    void fetchLogs();
    return () => {
      active = false;
    };
  }, [app.id, projectID]);

  const filteredLogs = logs.filter((log) => {
    if (filter === "errors" && !(log.level === "error" || log.level === "warn")) return false;
    if (searchQuery && !log.message?.toLowerCase().includes(searchQuery.toLowerCase())) return false;
    return true;
  });

  return (
    <div className="space-y-4">
      <div className="flex flex-col sm:flex-row items-stretch sm:items-center justify-between gap-3">
        <div className="relative flex-1">
          <Icon name="search" className="absolute left-3 top-1/2 -translate-y-1/2 text-on-surface-variant text-[16px] pointer-events-none" />
          <input
            className="w-full bg-surface-container-highest border border-outline-variant/30 rounded-lg pl-9 pr-3 py-2 text-xs text-on-surface focus:outline-none focus:border-primary/50 min-h-[40px]"
            onChange={(e) => setSearchQuery(e.target.value)}
            placeholder="Search log output…"
            type="search"
            value={searchQuery}
          />
        </div>
        <div className="flex items-center gap-2">
          <Button
            onClick={() => setFilter("all")}
            size="sm"
            variant={filter === "all" ? "primary" : "outline"}
          >
            All Streams
          </Button>
          <Button
            onClick={() => setFilter("errors")}
            size="sm"
            variant={filter === "errors" ? "primary" : "outline"}
          >
            Errors only
          </Button>
        </div>
      </div>

      <div className="text-[11px] text-on-surface-variant bg-surface-container/40 p-2.5 rounded-xl border border-outline-variant/10 flex items-center justify-between">
        <span>Security boundary: Log payloads are bounded and sanitized by Local Edge.</span>
        <span className="font-code-md">{filteredLogs.length} entries</span>
      </div>

      <div
        className="bg-surface-container-lowest p-4 rounded-2xl border border-outline-variant/20 font-code-md text-xs text-on-surface max-h-[400px] overflow-y-auto space-y-1.5"
        data-logs-status={loading ? "loading" : "ready"}
      >
        {loading ? (
          <div className="py-8 text-center text-on-surface-variant">Streaming logs…</div>
        ) : filteredLogs.length === 0 ? (
          <div className="py-8 text-center text-on-surface-variant">No log entries reported in this window.</div>
        ) : (
          filteredLogs.map((entry, index) => (
            <div className="flex items-start gap-2.5 leading-relaxed" key={index}>
              <span className="text-on-surface-variant/60 shrink-0 text-[11px]">
                {formatObserved(entry.observed_unix)}
              </span>
              {entry.pod_id ? (
                <span className="text-primary font-code-md text-[10px] px-1 py-0.2 rounded bg-primary/10 shrink-0">
                  {entry.pod_id}
                </span>
              ) : null}
              <span
                className={`font-bold uppercase text-[10px] px-1.5 py-0.2 rounded shrink-0 ${
                  entry.level === "error" || entry.level === "warn"
                    ? "bg-error/20 text-error"
                    : "bg-surface-container-high text-on-surface-variant"
                }`}
              >
                {entry.level || "info"}
              </span>
              <span className="text-on-surface break-all">{safeLogMessage(entry.message)}</span>
            </div>
          ))
        )}
      </div>
    </div>
  );
}

function AppEventsSection({ events }: { events: RuntimeEvent[] }) {
  return (
    <div className="space-y-3">
      {events.length === 0 ? (
        <div className="p-8 text-center text-xs text-on-surface-variant bg-surface-container rounded-2xl border border-outline-variant/15">
          No runtime events recorded in observation window.
        </div>
      ) : (
        <div className="space-y-2">
          {events.map((ev) => (
            <div
              className="p-3 bg-surface-container/60 rounded-xl border border-outline-variant/15 flex items-start justify-between gap-3 text-xs"
              key={ev.id}
            >
              <div className="space-y-0.5">
                <strong className="text-on-surface font-semibold block">{ev.title}</strong>
                <p className="text-on-surface-variant">{ev.detail}</p>
              </div>
              <span className="text-[11px] font-code-md text-on-surface-variant/70 shrink-0">
                {ev.formattedTime || ev.freshness}
              </span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function AppExposureSection({ app }: { app: ApplicationRuntimeSummary }) {
  return (
    <div className="space-y-4">
      <div className="p-4 bg-surface-container rounded-2xl border border-outline-variant/15 space-y-3">
        <div className="flex items-center justify-between">
          <h3 className="font-headline-md text-sm font-bold text-on-surface">Exposure Status</h3>
          <StatusBadge
            label={app.exposureLabel}
            value={app.exposureStatus === "ready" ? "healthy" : app.exposureStatus === "not_configured" ? "unknown" : app.exposureStatus}
          />
        </div>
        <p className="text-xs text-on-surface-variant">
          Exposure intent is established through Topology and published by Cloud authority upon successful deployment.
        </p>
      </div>
    </div>
  );
}
