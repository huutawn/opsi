"use client";

import type { MouseEvent } from "react";
import { Button, Empty, Icon, StatusBadge } from "@/components/ui/primitives";
import { routeHref } from "@/features/console/navigation";
import type { ConsoleController } from "@/features/console/types";
import type { ObservabilityModel } from "@/features/observability/observability-view";
import { SourceBadge, formatObserved } from "@/features/observability/shared";

export function OverviewTab({
  console,
  model,
}: {
  console: ConsoleController;
  model: ObservabilityModel;
}) {
  const projectID = console.route.projectID || console.state.project?.id || "";
  const overview = model.data.overview;
  const sources = model.data.sources;

  function follow(
    event: MouseEvent<HTMLAnchorElement>,
    target: {
      view?: string;
      tab?: string;
      service?: string;
      server?: string;
      resource?: string;
      deployment?: string;
    }
  ) {
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    console.navigate({
      projectID,
      view: (target.view as "observability" | "delivery" | "infrastructure" | "services") ?? "observability",
      tab: target.tab,
      service: target.service,
      server: target.server,
      resource: target.resource,
      deployment: target.deployment,
    });
  }

  const apps = overview.applications;
  const srvs = overview.servers;
  const res = overview.resources;
  const del = overview.delivery;

  return (
    <div className="space-y-6" data-testid="observability-overview">
      <div className="flex items-center justify-between">
        <div>
          <p className="font-label-sm text-xs text-primary uppercase tracking-wider">Telemetry & Health</p>
          <h2 className="font-headline-md text-xl font-bold text-on-surface">Observability Overview</h2>
        </div>
      </div>

      {/* Top Controls & Refresh Bar */}
      <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-4 bg-surface-container-low p-4 rounded-xl border border-outline-variant/20 shadow-sm">
        <div className="flex items-center gap-3">
          <div className="w-2.5 h-2.5 rounded-full bg-status-ready animate-pulse" />
          <span className="font-label-sm text-xs font-semibold text-on-surface">Live Factual Telemetry</span>
          <span className="text-xs text-on-surface-variant font-code-md">• {overview.freshness}</span>
        </div>

        <div className="flex items-center gap-3">
          <span className="text-xs text-on-surface-variant font-code-md">Window: Last 1h</span>
          <Button
            disabled={sources.telemetry === "loading"}
            onClick={() => void model.load()}
            size="sm"
            variant="secondary"
          >
            <Icon name="refresh" className="text-[16px]" />
            Refresh
          </Button>
        </div>
      </div>

      {model.data.error ? (
        <div className="bg-error-container/20 border border-error/30 p-4 rounded-xl text-error text-xs flex items-center gap-2" role="alert">
          <Icon name="error" className="text-[18px] shrink-0" />
          <span>{model.data.error}</span>
        </div>
      ) : null}

      {/* 4 Top KPI Cards matching docs/ui_html/observability_opsi_dashboard/code.html */}
      <div className="statusStrip grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-6" aria-label="System status summary">
        {/* KPI 1: Applications */}
        <a
          className="bg-surface-container-low border border-outline-variant/20 rounded-xl p-6 shadow-sm hover:shadow-md transition-all flex flex-col justify-between space-y-4 cursor-pointer"
          href={routeHref({ projectID, view: "observability", tab: "applications" })}
          onClick={(e) => follow(e, { view: "observability", tab: "applications" })}
        >
          <div className="flex items-center justify-between">
            <span className="font-label-sm text-xs text-on-surface-variant uppercase tracking-wider">Applications</span>
            <div className="w-8 h-8 rounded-lg bg-surface-container-high flex items-center justify-center text-primary">
              <Icon name="layers" className="text-[18px]" />
            </div>
          </div>
          <div>
            <div className="font-headline-lg text-2xl font-bold text-on-surface">
              {apps.total ? `${apps.ready}/${apps.total} Ready` : "0 Apps"}
            </div>
            <p className="text-xs text-on-surface-variant mt-1">
              {apps.degraded > 0 || apps.failed > 0
                ? `${apps.degraded} degraded • ${apps.failed} failed`
                : apps.total
                  ? "All workloads healthy"
                  : "Deploy an app to observe"}
            </p>
          </div>
          <StatusBadge
            label={apps.failed ? "Failed" : apps.degraded ? "Degraded" : apps.ready ? "Healthy" : "Unknown"}
            value={apps.failed ? "failed" : apps.degraded ? "degraded" : apps.ready ? "healthy" : "unknown"}
          />
        </a>

        {/* KPI 2: Server Nodes */}
        <a
          className="bg-surface-container-low border border-outline-variant/20 rounded-xl p-6 shadow-sm hover:shadow-md transition-all flex flex-col justify-between space-y-4 cursor-pointer"
          href={routeHref({ projectID, view: "observability", tab: "servers" })}
          onClick={(e) => follow(e, { view: "observability", tab: "servers" })}
        >
          <div className="flex items-center justify-between">
            <span className="font-label-sm text-xs text-on-surface-variant uppercase tracking-wider">Servers</span>
            <div className="w-8 h-8 rounded-lg bg-surface-container-high flex items-center justify-center text-primary">
              <Icon name="dns" className="text-[18px]" />
            </div>
          </div>
          <div>
            <div className="font-headline-lg text-2xl font-bold text-on-surface">
              {srvs.total ? `${srvs.ready}/${srvs.total} Active` : "0 Servers"}
            </div>
            <p className="text-xs text-on-surface-variant mt-1">
              {srvs.offline > 0 || srvs.failed > 0
                ? `${srvs.offline} offline • ${srvs.failed} failed`
                : srvs.total
                  ? "All execution nodes ready"
                  : "No servers connected"}
            </p>
          </div>
          <StatusBadge
            label={srvs.failed ? "Failed" : srvs.offline ? "Offline" : srvs.ready ? "Healthy" : "Unknown"}
            value={srvs.failed ? "failed" : srvs.offline ? "degraded" : srvs.ready ? "healthy" : "unknown"}
          />
        </a>

        {/* KPI 3: Managed Resources */}
        <a
          className="bg-surface-container-low border border-outline-variant/20 rounded-xl p-6 shadow-sm hover:shadow-md transition-all flex flex-col justify-between space-y-4 cursor-pointer"
          href={routeHref({ projectID, view: "observability", tab: "resources" })}
          onClick={(e) => follow(e, { view: "observability", tab: "resources" })}
        >
          <div className="flex items-center justify-between">
            <span className="font-label-sm text-xs text-on-surface-variant uppercase tracking-wider">Managed Resources</span>
            <div className="w-8 h-8 rounded-lg bg-surface-container-high flex items-center justify-center text-primary">
              <Icon name="database" className="text-[18px]" />
            </div>
          </div>
          <div>
            <div className="font-headline-lg text-2xl font-bold text-on-surface">
              {res.total ? `${res.ready}/${res.total} Ready` : "0 Resources"}
            </div>
            <p className="text-xs text-on-surface-variant mt-1">
              {res.degraded > 0 || res.failed > 0
                ? `${res.degraded} degraded • ${res.failed} failed`
                : res.total
                  ? "Postgres / Valkey / NATS ready"
                  : "No resources provisioned"}
            </p>
          </div>
          <StatusBadge
            label={res.failed ? "Failed" : res.degraded ? "Degraded" : res.ready ? "Healthy" : "Unknown"}
            value={res.failed ? "failed" : res.degraded ? "degraded" : res.ready ? "healthy" : "unknown"}
          />
        </a>

        {/* KPI 4: Delivery Ops */}
        <a
          className="bg-surface-container-low border border-outline-variant/20 rounded-xl p-6 shadow-sm hover:shadow-md transition-all flex flex-col justify-between space-y-4 cursor-pointer"
          href={routeHref({ projectID, view: "deploy" })}
          onClick={(e) => follow(e, { view: "deploy" })}
        >
          <div className="flex items-center justify-between">
            <span className="font-label-sm text-xs text-on-surface-variant uppercase tracking-wider">Delivery Rollouts</span>
            <div className="w-8 h-8 rounded-lg bg-surface-container-high flex items-center justify-center text-primary">
              <Icon name="rocket_launch" className="text-[18px]" />
            </div>
          </div>
          <div>
            <div className="font-headline-lg text-2xl font-bold text-on-surface">
              {del.active > 0 ? `${del.active} Active` : `${del.succeeded} Completed`}
            </div>
            <p className="text-xs text-on-surface-variant mt-1">
              {del.failed > 0 ? `${del.failed} recent failures` : "Rollout state verified"}
            </p>
          </div>
          <StatusBadge
            label={del.failed > 0 ? "Failures" : del.active > 0 ? "Active" : "Stable"}
            value={del.failed > 0 ? "failed" : del.active > 0 ? "in_progress" : "healthy"}
          />
        </a>
      </div>

      {/* Actionable Failures & Attention Section */}
      <div className="bg-surface-container-low border border-outline-variant/20 rounded-xl p-6 shadow-sm space-y-4">
        <div className="flex items-center justify-between border-b border-outline-variant/20 pb-3">
          <div className="flex items-center gap-2">
            <Icon name="warning" className="text-status-warning text-[20px]" />
            <h3 className="font-headline-md text-base font-bold text-on-surface">Actionable Failures & Incident Attention</h3>
          </div>
          <span className="text-xs text-on-surface-variant">{overview.actionableFailures.length} active items</span>
        </div>

        {overview.actionableFailures.length > 0 ? (
          <div className="space-y-3" data-testid="actionable-failures">
            {overview.actionableFailures.map((failure) => (
              <a
                className="p-4 bg-surface-container rounded-xl border border-outline-variant/20 hover:border-outline-variant/50 transition-all flex items-center justify-between gap-4 cursor-pointer"
                href={routeHref({ projectID, ...failure.target })}
                key={failure.id}
                onClick={(e) => follow(e, failure.target)}
              >
                <div className="flex items-start gap-3 min-w-0">
                  <div className="p-2 rounded-lg bg-surface-container-high text-status-warning shrink-0 mt-0.5">
                    <Icon name="error" className="text-[18px]" />
                  </div>
                  <div className="min-w-0">
                    <div className="flex items-center gap-2">
                      <span className="font-label-sm text-[10px] text-primary uppercase bg-primary/10 px-2 py-0.5 rounded border border-primary/20">
                        {failure.categoryLabel}
                      </span>
                      <strong className="font-body-md text-sm text-on-surface truncate">{failure.title}</strong>
                    </div>
                    <p className="text-xs text-on-surface-variant mt-1 leading-relaxed">{failure.explanation}</p>
                    <span className="text-[10px] text-on-surface-variant/70 font-code-md mt-1 block">{failure.freshness}</span>
                  </div>
                </div>
                <Icon name="arrow_forward" className="text-on-surface-variant text-[20px] shrink-0" />
              </a>
            ))}
          </div>
        ) : (
          <Empty
            text="All observed applications, server execution nodes, and managed database resources report factual healthy state."
            title="No Current Runtime Failures"
          />
        )}
      </div>

      {/* Source Coverage Grid */}
      <div className="bg-surface-container-low border border-outline-variant/20 rounded-xl p-6 shadow-sm space-y-4">
        <span className="font-label-sm text-xs text-on-surface-variant uppercase tracking-wider block">Source Authority Coverage</span>
        <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-6 gap-3" aria-label="Source coverage">
          <SourceBadge label="Cloud Registry" state={sources.registry} />
          <SourceBadge label="Agent Telemetry" state={sources.telemetry} />
          <SourceBadge label="Server Nodes" state={sources.nodes} />
          <SourceBadge label="Managed Resources" state={sources.resources} />
          <SourceBadge label="Delivery Rollouts" state={sources.deployments} />
          <div className="bg-surface-container p-3.5 rounded-xl border border-outline-variant/20 flex flex-col justify-between">
            <span className="font-label-sm text-[10px] text-on-surface-variant uppercase">Observation</span>
            <span className="font-code-md text-xs font-semibold text-on-surface truncate mt-1">
              {formatObserved(overview.lastObservationUnix)}
            </span>
          </div>
        </div>
      </div>
    </div>
  );
}
