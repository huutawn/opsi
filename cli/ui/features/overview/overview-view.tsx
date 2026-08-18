"use client";

import type { MouseEvent } from "react";
import { Button, Empty, Icon, PageHeader, StatusBadge } from "@/components/ui/primitives";
import { routeHref } from "@/features/console/navigation";
import type { ConsoleController } from "@/features/console/types";
import {
  deliveryActivity,
  deriveProjectSummary,
  formatTimestamp,
  serviceRows,
  statusLabel,
} from "@/lib/presentation/project";

export function OverviewView({ console }: { console: ConsoleController }) {
  const project = console.state.project;
  if (!project) return <Empty text="Choose a project from the workspace to see operational evidence." title="Select a project" />;

  const projectID = project.id;
  const summary = deriveProjectSummary({
    project,
    readiness: console.state.readiness,
    services: console.state.services,
    deployments: console.state.deployments,
    foundation: console.state.foundation,
  });
  const rows = serviceRows({
    services: console.state.services,
    telemetry: console.state.foundation.telemetry,
    telemetrySource: console.state.foundation.sources.telemetry,
    deployments: console.state.deployments,
    placement: console.state.foundation.placement,
    topology: console.state.foundation.topology,
  });
  const activity = deliveryActivity(console.state.deployments);
  const healthyNodes = console.state.foundation.placement?.nodes.filter((node) => node.status === "healthy").length;
  const totalNodes = console.state.foundation.placement?.nodes.length;

  function follow(
    event: MouseEvent<HTMLAnchorElement>,
    target: { view: "delivery" | "services" | "infrastructure" | "observability"; tab?: string }
  ) {
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    console.navigate({ projectID, view: target.view, tab: target.tab });
  }

  return (
    <div className="p-4 lg:p-margin-desktop max-w-7xl mx-auto space-y-6">
      <PageHeader
        action={
          <Button onClick={() => void console.actions.load()} variant="secondary">
            <Icon name="refresh" className="text-[18px]" />
            Refresh project overview
          </Button>
        }
        description="A concise factual summary of runtime status, delivery rollouts, and infrastructure health."
        eyebrow="Project Overview"
        icon="dashboard"
        title={project.name}
      />

      {/* KPI Status Strip */}
      <div className="statusStrip grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-5 gap-4">
        <div className="statusLead bg-surface-container-low border border-outline-variant/20 rounded-xl p-4 shadow-sm space-y-1">
          <span className="font-label-sm text-[10px] text-on-surface-variant uppercase tracking-wider block">Status</span>
          <div className="font-headline-md text-base font-bold text-on-surface flex items-center gap-2">
            <strong className="sr-only">{statusLabel(summary.overall)}</strong>
            <StatusBadge label={statusLabel(summary.overall)} value={summary.overall} />
          </div>
          <small className="text-[11px] text-on-surface-variant block">
            {summary.attention.length ? `${summary.attention.length} items need attention` : "All systems normal"}
          </small>
        </div>

        <div className="bg-surface-container-low border border-outline-variant/20 rounded-xl p-4 shadow-sm space-y-1">
          <span className="font-label-sm text-[10px] text-on-surface-variant uppercase tracking-wider block">Pod Readiness</span>
          <div className="font-headline-md text-lg font-bold text-on-surface">
            {summary.readiness.desired ? `${summary.readiness.ready}/${summary.readiness.desired} Ready` : "Not reported"}
          </div>
          <small className="text-[11px] text-on-surface-variant block">
            {summary.readiness.desired ? "Ready container pods" : "Telemetry unavailable"}
          </small>
        </div>

        <div className="bg-surface-container-low border border-outline-variant/20 rounded-xl p-4 shadow-sm space-y-1">
          <span className="font-label-sm text-[10px] text-on-surface-variant uppercase tracking-wider block">Latest Delivery</span>
          <div className="flex items-center gap-2">
            {summary.latestBuild ? (
              <StatusBadge value={summary.latestBuild.build.status} />
            ) : summary.latestDeployment ? (
              <StatusBadge value={summary.latestDeployment.status} />
            ) : (
              <span className="font-headline-md text-sm font-bold text-on-surface">No data</span>
            )}
          </div>
          <small className="text-[11px] text-on-surface-variant truncate block">
            {summary.latestBuild ? summary.latestBuild.service_key : "No recent builds"}
          </small>
        </div>

        <div className="bg-surface-container-low border border-outline-variant/20 rounded-xl p-4 shadow-sm space-y-1">
          <span className="font-label-sm text-[10px] text-on-surface-variant uppercase tracking-wider block">Open Incidents</span>
          <div className="font-headline-md text-lg font-bold text-on-surface">
            {console.state.foundation.sources.incidents === "available"
              ? summary.openIncidents
              : console.state.foundation.sources.incidents === "unavailable"
                ? "Unavailable"
                : "None"}
          </div>
          <small className="text-[11px] text-on-surface-variant block">
            {summary.openIncidents ? "Needs investigation" : "No active alarms"}
          </small>
        </div>

        <div className="bg-surface-container-low border border-outline-variant/20 rounded-xl p-4 shadow-sm space-y-1">
          <span className="font-label-sm text-[10px] text-on-surface-variant uppercase tracking-wider block">Nodes Online</span>
          <div className="font-headline-md text-lg font-bold text-on-surface">
            {healthyNodes !== undefined && totalNodes !== undefined ? `${healthyNodes}/${totalNodes} Ready` : "Unknown"}
          </div>
          <small className="text-[11px] text-on-surface-variant block">
            {console.state.foundation.sources.runtime === "available" ? "Server nodes connected" : "Runtime unavailable"}
          </small>
        </div>
      </div>

      {/* 2-Column Split: Delivery Activity & Service Health */}
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
        {/* Delivery Activity */}
        <div className="bg-surface-container-low border border-outline-variant/20 rounded-xl p-6 shadow-sm space-y-4">
          <div className="flex items-center justify-between border-b border-outline-variant/20 pb-3">
            <div className="flex items-center gap-2">
              <Icon name="rocket_launch" className="text-primary text-[20px]" />
              <h3 className="font-headline-md text-base font-bold text-on-surface">Delivery Activity</h3>
            </div>
            <a
              className="text-xs text-primary font-semibold hover:underline"
              href={routeHref({ projectID: project.id, view: "delivery", tab: "deployments" })}
              onClick={(event) => follow(event, { view: "delivery", tab: "deployments" })}
            >
              Open Delivery →
            </a>
          </div>

          {activity.kind === "chart" ? (
            <div className="space-y-3">
              <p className="text-xs text-on-surface-variant">
                {(() => {
                  const totals = activity.buckets.reduce(
                    (acc, b) => ({
                      succeeded: acc.succeeded + b.succeeded,
                      failed: acc.failed + b.failed,
                      rolled_back: acc.rolled_back + b.rolled_back,
                      cancelled: acc.cancelled + b.cancelled,
                      other: acc.other + b.other,
                    }),
                    { succeeded: 0, failed: 0, rolled_back: 0, cancelled: 0, other: 0 }
                  );
                  const parts = [];
                  if (totals.succeeded) parts.push(`${totals.succeeded} succeeded`);
                  if (totals.failed) parts.push(`${totals.failed} failed`);
                  if (totals.rolled_back) parts.push(`${totals.rolled_back} rolled back`);
                  if (totals.cancelled) parts.push(`${totals.cancelled} cancelled`);
                  if (totals.other) parts.push(`${totals.other} other`);
                  return parts.join(", ");
                })()}
              </p>
              <div className="overflow-x-auto">
                <table className="w-full text-xs text-left">
                  <thead>
                    <tr className="border-b border-outline-variant/20 text-on-surface-variant font-label-sm">
                      <th className="py-2 px-3">Day</th>
                      <th className="py-2 px-3">Succeeded</th>
                      <th className="py-2 px-3">Failed</th>
                      <th className="py-2 px-3">Rolled back</th>
                      <th className="py-2 px-3">Cancelled</th>
                      <th className="py-2 px-3">Other</th>
                      <th className="py-2 px-3">Total</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-outline-variant/10">
                    {activity.buckets.map((bucket) => {
                      const total = bucket.succeeded + bucket.failed + bucket.rolled_back + bucket.cancelled + bucket.other;
                      return (
                        <tr key={bucket.day} className="hover:bg-surface-container-high/40">
                          <td className="py-2 px-3 font-code-md">{bucket.day}</td>
                          <td className="py-2 px-3">{bucket.succeeded}</td>
                          <td className="py-2 px-3">{bucket.failed}</td>
                          <td className="py-2 px-3">{bucket.rolled_back}</td>
                          <td className="py-2 px-3">{bucket.cancelled}</td>
                          <td className="py-2 px-3">{bucket.other}</td>
                          <td className="py-2 px-3 font-bold">{total}</td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            </div>
          ) : activity.events && activity.events.length ? (
            <div className="space-y-2">
              {activity.events.slice(-5).reverse().map((deployment) => (
                <div
                  key={deployment.id}
                  className="p-3 bg-surface-container rounded-lg border border-outline-variant/20 flex items-center justify-between gap-3"
                >
                  <div className="min-w-0">
                    <strong className="font-body-md text-sm text-on-surface block truncate">{deployment.service_id}</strong>
                    <span className="text-[11px] text-on-surface-variant">
                      {formatTimestamp(deployment.updated_at ?? deployment.created_at)}
                    </span>
                  </div>
                  <StatusBadge value={deployment.rollout_state ?? deployment.status} />
                </div>
              ))}
            </div>
          ) : (
            <Empty text="Accepted builds and deployments will appear here when reported." title="No Delivery Data Yet" />
          )}
        </div>

        {/* Service Health */}
        <div className="bg-surface-container-low border border-outline-variant/20 rounded-xl p-6 shadow-sm space-y-4">
          <div className="flex items-center justify-between border-b border-outline-variant/20 pb-3">
            <div className="flex items-center gap-2">
              <Icon name="layers" className="text-primary text-[20px]" />
              <h3 className="font-headline-md text-base font-bold text-on-surface">Service Health</h3>
            </div>
            <a
              className="text-xs text-primary font-semibold hover:underline"
              href={routeHref({ projectID: project.id, view: "services" })}
              onClick={(event) => follow(event, { view: "services" })}
            >
              View Services →
            </a>
          </div>

          {rows.length ? (
            <div className="space-y-2">
              {rows.slice(0, 5).map((row) => (
                <a
                  key={row.service.id}
                  className="p-3 bg-surface-container rounded-lg border border-outline-variant/20 flex items-center justify-between gap-3 hover:bg-surface-container-high transition-colors cursor-pointer"
                  href={routeHref({ projectID: project.id, view: "services", service: row.service.id })}
                  onClick={(event) => {
                    if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
                    event.preventDefault();
                    console.navigate({ projectID: project.id, view: "services", service: row.service.id });
                    console.setServiceDetail(row.service);
                  }}
                >
                  <div className="min-w-0">
                    <strong className="font-body-md text-sm text-on-surface block truncate">{row.service.name}</strong>
                    <span className="text-[11px] text-on-surface-variant">
                      {row.environment || "Default"} • {row.runtime || "Ready"}
                    </span>
                  </div>
                  <div className="flex items-center gap-3">
                    <span className="font-code-md text-xs text-on-surface-variant">
                      {row.ready !== undefined && row.desired !== undefined ? `${row.ready}/${row.desired}` : "—"}
                    </span>
                    <StatusBadge label={statusLabel(row.health)} value={row.health} />
                  </div>
                </a>
              ))}
            </div>
          ) : (
            <Empty text="No application services configured for this project yet." title="No Services Found" />
          )}
        </div>
      </div>

      {/* Attention Queue */}
      {summary.attention.length > 0 ? (
        <div className="bg-surface-container-low border border-outline-variant/20 rounded-xl p-6 shadow-sm space-y-4">
          <div className="flex items-center gap-2 border-b border-outline-variant/20 pb-3">
            <Icon name="warning" className="text-status-warning text-[20px]" />
            <h3 className="font-headline-md text-base font-bold text-on-surface">Attention Queue</h3>
          </div>
          <div className="space-y-2">
            {summary.attention.map((item) => (
              <a
                key={item.id}
                className="p-3.5 bg-surface-container rounded-xl border border-outline-variant/20 hover:border-outline-variant/50 transition-all flex items-center justify-between gap-4 cursor-pointer"
                href={routeHref({ projectID: project.id, view: item.target.view, tab: item.target.tab })}
                onClick={(event) => follow(event, item.target)}
              >
                <div>
                  <strong className="font-body-md text-sm text-on-surface block">{item.title}</strong>
                  <p className="text-xs text-on-surface-variant mt-0.5">{item.detail}</p>
                </div>
                <Icon name="arrow_forward" className="text-on-surface-variant text-[18px] shrink-0" />
              </a>
            ))}
          </div>
        </div>
      ) : null}
    </div>
  );
}
