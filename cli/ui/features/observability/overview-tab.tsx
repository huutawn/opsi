"use client";

import { Empty, Surface } from "@/components/ui/primitives";
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
    event: React.MouseEvent<HTMLAnchorElement>,
    target: {
      view?: string;
      tab?: string;
      service?: string;
      server?: string;
      resource?: string;
      deployment?: string;
    },
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
    <div className="observabilityStack" data-testid="observability-overview">
      <div className="observabilityHero">
        <div>
          <p className="eyebrow">Factual Observability · Runtime Diagnostics</p>
          <h2>Observability Overview</h2>
          <p>
            Factual runtime health across applications, server nodes, managed infrastructure, and delivery operations.
          </p>
        </div>
        <div className="heroStatus">
          <div className="freshnessBadge">
            <span className="liveDot" />
            <small>{overview.freshness}</small>
          </div>
          <button
            className="secondaryAction"
            disabled={sources.telemetry === "loading"}
            onClick={() => void model.load()}
            type="button"
          >
            Refresh
          </button>
        </div>
      </div>

      {model.data.error ? (
        <p className="inlineError" role="alert">
          {model.data.error}
        </p>
      ) : null}

      {/* Summary KPI Strip */}
      <div className="statusStrip" aria-label="System status summary">
        <a
          className={`statusLead ${apps.failed ? "failed" : apps.degraded ? "degraded" : apps.ready ? "healthy" : "unknown"}`}
          href={routeHref({ projectID, view: "observability", tab: "applications" })}
          onClick={(e) => follow(e, { view: "observability", tab: "applications" })}
        >
          <span>Applications</span>
          <strong>{apps.total ? `${apps.ready}/${apps.total} Ready` : "No apps"}</strong>
          <small>
            {apps.degraded > 0 || apps.failed > 0
              ? `${apps.degraded} degraded · ${apps.failed} failed`
              : apps.total
              ? "All workloads healthy"
              : "Deploy an Application to observe"}
          </small>
        </a>

        <a
          className={`statusLead ${srvs.failed ? "failed" : srvs.offline ? "degraded" : srvs.ready ? "healthy" : "unknown"}`}
          href={routeHref({ projectID, view: "observability", tab: "servers" })}
          onClick={(e) => follow(e, { view: "observability", tab: "servers" })}
        >
          <span>Servers</span>
          <strong>{srvs.total ? `${srvs.ready}/${srvs.total} Ready` : "No servers"}</strong>
          <small>
            {srvs.offline > 0 || srvs.failed > 0
              ? `${srvs.offline} offline · ${srvs.failed} failed`
              : srvs.total
              ? "All server nodes active"
              : "No servers connected"}
          </small>
        </a>

        <a
          className={`statusLead ${res.failed ? "failed" : res.degraded ? "degraded" : res.ready ? "healthy" : "unknown"}`}
          href={routeHref({ projectID, view: "observability", tab: "resources" })}
          onClick={(e) => follow(e, { view: "observability", tab: "resources" })}
        >
          <span>Managed Resources</span>
          <strong>{res.total ? `${res.ready}/${res.total} Ready` : "No resources"}</strong>
          <small>
            {res.degraded > 0 || res.failed > 0
              ? `${res.degraded} degraded · ${res.failed} failed`
              : res.total
              ? "PostgreSQL / Valkey / NATS ready"
              : "No resources provisioned"}
          </small>
        </a>

        <a
          className="statusLead"
          href={routeHref({ projectID, view: "delivery", tab: "deployments" })}
          onClick={(e) => follow(e, { view: "delivery", tab: "deployments" })}
        >
          <span>Delivery Ops</span>
          <strong>{del.active > 0 ? `${del.active} Active` : `${del.succeeded} Completed`}</strong>
          <small>
            {del.failed > 0 ? `${del.failed} recent failures` : "Rollout state verified"}
          </small>
        </a>
      </div>

      {/* Actionable Failures / Attention */}
      <Surface title="Actionable Failures & Attention">
        {overview.actionableFailures.length > 0 ? (
          <div className="attentionList" data-testid="actionable-failures">
            {overview.actionableFailures.map((failure) => (
              <a
                className={`attentionItem ${failure.category === "workload" || failure.category === "exposure" || failure.category === "server" || failure.category === "resource" ? "degraded" : "failed"}`}
                href={routeHref({ projectID, ...failure.target })}
                key={failure.id}
                onClick={(e) => follow(e, failure.target)}
              >
                <span className="attentionMark" aria-hidden="true" />
                <div>
                  <div className="attentionHeader">
                    <span className="categoryPill">{failure.categoryLabel}</span>
                    <strong>{failure.title}</strong>
                  </div>
                  <p className="attentionDetail">{failure.explanation}</p>
                  <small className="muted">{failure.freshness}</small>
                </div>
                <span className="actionArrow" aria-hidden="true">→</span>
              </a>
            ))}
          </div>
        ) : (
          <Empty
            title="No current runtime failures"
            text="All observed applications, server nodes, and managed resources report factual healthy state."
          />
        )}
      </Surface>

      {/* Source Coverage & Freshness Grid */}
      <section className="coverageGrid" aria-label="Source coverage">
        <SourceBadge label="Cloud Registry" state={sources.registry} />
        <SourceBadge label="Agent Telemetry" state={sources.telemetry} />
        <SourceBadge label="Server Nodes" state={sources.nodes} />
        <SourceBadge label="Managed Resources" state={sources.resources} />
        <SourceBadge label="Delivery Rollouts" state={sources.deployments} />
        <div className="coverageItem">
          <span>Last observation</span>
          <b>{formatObserved(overview.lastObservationUnix)}</b>
        </div>
      </section>
    </div>
  );
}
