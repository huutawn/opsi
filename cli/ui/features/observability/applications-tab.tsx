"use client";

import { useEffect, useState } from "react";
import { Empty, StatusBadge, Surface } from "@/components/ui/primitives";
import { routeHref } from "@/features/console/navigation";
import type { ConsoleController } from "@/features/console/types";
import type { ObservabilityModel } from "@/features/observability/observability-view";
import { safeLogMessage } from "@/features/observability/data";
import { Fact, formatObserved } from "@/features/observability/shared";
import { formatShortDigest, type ApplicationRuntimeSummary, type RuntimeEvent } from "@/lib/presentation/observability/model";
import type { TelemetryLogEntry, TelemetryQueryResponse } from "@/lib/contracts/registry";

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

  const [detailTab, setDetailTab] = useState<"overview" | "workload" | "logs" | "events" | "exposure">("overview");

  function selectApp(app: ApplicationRuntimeSummary | null) {
    console.navigate({
      service: app ? app.id : "",
    });
  }

  return (
    <div className="observabilityStack" data-testid="observability-applications">
      <div className="observabilityHero">
        <div>
          <p className="eyebrow">Runtime Inventory · Factual Workloads</p>
          <h2>Applications Runtime</h2>
          <p>
            Current workload health, replica readiness, server placement, immutable digests, and bounded diagnostic logs.
          </p>
        </div>
        <div className="heroStatus">
          <button
            className="secondaryAction"
            disabled={model.data.sources.telemetry === "loading"}
            onClick={() => void model.load()}
            type="button"
          >
            Refresh Inventory
          </button>
        </div>
      </div>

      {applications.length > 0 ? (
        <div className="tableWrap">
          <table className="dataTable" aria-label="Applications runtime inventory">
            <thead>
              <tr>
                <th scope="col">Application</th>
                <th scope="col">Workload Status</th>
                <th scope="col">Replicas</th>
                <th scope="col">Server</th>
                <th scope="col">Revision</th>
                <th scope="col">Image Digest</th>
                <th scope="col">Exposure</th>
                <th scope="col">Last Deployment</th>
                <th scope="col">Freshness</th>
                <th scope="col"><span className="srOnly">Actions</span></th>
              </tr>
            </thead>
            <tbody>
              {applications.map((app) => {
                const isSelected = selectedApp?.id === app.id;
                return (
                  <tr
                    className={isSelected ? "selectedRow" : ""}
                    key={app.id}
                    onClick={() => selectApp(app)}
                    style={{ cursor: "pointer" }}
                    data-testid={`app-row-${app.key || app.id}`}
                  >
                    <td>
                      <strong>{app.name}</strong>
                      <small className="cellSubtext">{app.environment}</small>
                    </td>
                    <td>
                      <StatusBadge
                        label={app.workloadLabel}
                        value={app.workloadStatus === "ready" ? "healthy" : app.workloadStatus}
                      />
                    </td>
                    <td>
                      <span className="mono">{app.replicasLabel}</span>
                    </td>
                    <td>
                      <span>{app.serverPlacement}</span>
                    </td>
                    <td>
                      <span className="mono">rev {app.configurationRevision}</span>
                    </td>
                    <td>
                      <span className="mono" title={app.deployedDigest || "Not reported"}>
                        {app.shortDigest}
                      </span>
                    </td>
                    <td>
                      <StatusBadge
                        label={app.exposureLabel}
                        value={app.exposureStatus === "ready" ? "healthy" : app.exposureStatus === "not_configured" ? "unknown" : app.exposureStatus}
                      />
                    </td>
                    <td>
                      <StatusBadge
                        label={app.lastDeploymentLabel}
                        value={app.lastDeploymentOutcome === "succeeded" ? "healthy" : app.lastDeploymentOutcome}
                      />
                    </td>
                    <td>
                      <small className="muted">{app.lastSeenFreshness}</small>
                    </td>
                    <td>
                      <button
                        className="textButton"
                        onClick={(e) => {
                          e.stopPropagation();
                          selectApp(app);
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
          title="No applications in this project"
          text="Deploy an Application through Delivery or Topology to observe runtime diagnostics."
        />
      )}

      {/* Application Detail Drawer / Surface */}
      {selectedApp ? (
        <ApplicationDetailSurface
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

function ApplicationDetailSurface({
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
  detailTab: "overview" | "workload" | "logs" | "events" | "exposure";
  model: ObservabilityModel;
  onClose: () => void;
  onTabChange: (tab: "overview" | "workload" | "logs" | "events" | "exposure") => void;
  projectID: string;
}) {
  const events = model.getApplicationEvents(app.id, app.key);

  return (
    <div className="modalBackdrop" onClick={onClose} role="presentation">
      <section
        aria-label={`Application runtime diagnostics for ${app.name}`}
        aria-modal="true"
        className="diagnosticDrawer"
        data-testid="application-detail-drawer"
        onClick={(e) => e.stopPropagation()}
        role="dialog"
      >
        <header className="drawerHeader">
          <div>
            <p className="eyebrow">Application Diagnostics · {app.environment}</p>
            <h2>{app.name}</h2>
            <div className="drawerBadges">
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
          <div className="drawerHeaderActions">
            <a
              className="secondaryAction textLink"
              href={routeHref({ projectID, view: "delivery", tab: "deployments", service: app.id })}
              onClick={(e) => {
                if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
                e.preventDefault();
                console.navigate({ projectID, view: "delivery", tab: "deployments", service: app.id });
              }}
            >
              Open in Delivery
            </a>
            <a
              className="secondaryAction textLink"
              href={routeHref({ projectID, view: "topology", service: app.id })}
              onClick={(e) => {
                if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
                e.preventDefault();
                console.navigate({ projectID, view: "topology", service: app.id });
              }}
            >
              Open in Topology
            </a>
            <button aria-label="Close diagnostics" className="closeButton" onClick={onClose} type="button">
              ✕
            </button>
          </div>
        </header>

        <nav aria-label="Diagnostic sections" className="consoleTabs drawerTabs">
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
            aria-selected={detailTab === "workload"}
            className={detailTab === "workload" ? "active" : ""}
            onClick={() => onTabChange("workload")}
            role="tab"
            type="button"
          >
            Workload
          </button>
          <button
            aria-selected={detailTab === "logs"}
            className={detailTab === "logs" ? "active" : ""}
            onClick={() => onTabChange("logs")}
            role="tab"
            type="button"
          >
            Logs
          </button>
          <button
            aria-selected={detailTab === "events"}
            className={detailTab === "events" ? "active" : ""}
            onClick={() => onTabChange("events")}
            role="tab"
            type="button"
          >
            Events ({events.length})
          </button>
          <button
            aria-selected={detailTab === "exposure"}
            className={detailTab === "exposure" ? "active" : ""}
            onClick={() => onTabChange("exposure")}
            role="tab"
            type="button"
          >
            Exposure
          </button>
        </nav>

        <div className="drawerContent">
          {detailTab === "overview" ? (
            <AppOverviewSection app={app} />
          ) : detailTab === "workload" ? (
            <AppWorkloadSection app={app} />
          ) : detailTab === "logs" ? (
            <AppLogsSection app={app} model={model} projectID={projectID} />
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

function AppOverviewSection({ app }: { app: ApplicationRuntimeSummary }) {
  return (
    <div className="drawerSectionStack">
      <div className="drawerFactsGrid">
        <div className="drawerFactItem">
          <span>Runtime status</span>
          <strong>{app.workloadLabel}</strong>
        </div>
        <div className="drawerFactItem">
          <span>Ready replicas</span>
          <strong>{app.replicasLabel}</strong>
        </div>
        <div className="drawerFactItem">
          <span>Server node</span>
          <strong>{app.serverPlacement}</strong>
        </div>
        <div className="drawerFactItem">
          <span>Active revision</span>
          <strong>rev {app.configurationRevision}</strong>
        </div>
        <div className="drawerFactItem">
          <span>Image digest</span>
          <strong className="hashString" title={app.deployedDigest || "Not reported"}>
            {formatShortDigest(app.deployedDigest)}
          </strong>
        </div>
        <div className="drawerFactItem">
          <span>Exposure route</span>
          <strong>{app.exposureLabel}</strong>
        </div>
      </div>

      <section className="drawerSubsection">
        <h3>Health & Availability</h3>
        <dl className="diagnosticDl">
          <div>
            <dt>Container restarts</dt>
            <dd>{app.restartCount} restart{app.restartCount === 1 ? "" : "s"}</dd>
          </div>
          <div>
            <dt>Recent errors</dt>
            <dd>{app.recentErrorCount} error{app.recentErrorCount === 1 ? "" : "s"}</dd>
          </div>
          <div>
            <dt>Last observation</dt>
            <dd>{app.lastSeenFreshness}</dd>
          </div>
          {app.failureReason ? (
            <div>
              <dt>Failure context</dt>
              <dd className="failureContext">{app.failureReason}</dd>
            </div>
          ) : null}
        </dl>
      </section>

      {app.boundResourceCount > 0 ? (
        <section className="drawerSubsection">
          <h3>Bound Resources ({app.boundResourceCount})</h3>
          <ul className="compactList">
            {app.boundResourceNames.map((resName, idx) => (
              <li key={resName}>
                <span>{resName}</span>
                <span className="muted">({app.boundResourceTypes[idx] ?? "managed"})</span>
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
    <div className="drawerSectionStack">
      <div className="workloadBanner">
        <StatusBadge value={app.workloadStatus === "ready" ? "healthy" : app.workloadStatus} />
        <div>
          <strong>{app.workloadLabel} ({app.replicasLabel})</strong>
          <p>
            {app.workloadStatus === "ready"
              ? "All configured replicas are passing container health checks."
              : app.workloadStatus === "degraded"
              ? `${app.readyReplicas} of ${app.desiredReplicas} replicas ready. Workload is serving with reduced capacity.`
              : "No workload replicas are ready to accept traffic."}
          </p>
        </div>
      </div>

      <dl className="diagnosticDl">
        <div>
          <dt>Ready replicas</dt>
          <dd>{app.readyReplicas}</dd>
        </div>
        <div>
          <dt>Desired replicas</dt>
          <dd>{app.desiredReplicas}</dd>
        </div>
        <div>
          <dt>Server placement</dt>
          <dd>{app.serverPlacement}</dd>
        </div>
        <div>
          <dt>Observed restarts</dt>
          <dd>{app.restartCount}</dd>
        </div>
        <div>
          <dt>Recent error counter</dt>
          <dd>{app.recentErrorCount}</dd>
        </div>
        <div>
          <dt>Last telemetry observation</dt>
          <dd>{app.lastSeenFreshness}</dd>
        </div>
      </dl>
    </div>
  );
}

function AppLogsSection({
  app,
  projectID,
}: {
  app: ApplicationRuntimeSummary;
  model?: ObservabilityModel;
  projectID: string;
}) {
  const [query, setQuery] = useState("");
  const [level, setLevel] = useState("");
  const [logs, setLogs] = useState<TelemetryLogEntry[]>([]);
  const [status, setStatus] = useState<"loading" | "ready" | "error">("loading");
  const [error, setError] = useState("");
  const [refreshToken, setRefreshToken] = useState(0);

  const appId = app.id;
  const effectiveProjectID = projectID || "proj-1";

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setStatus("loading");
    setError("");
    fetch(`/api/local/projects/${effectiveProjectID}/logs?service_id=${encodeURIComponent(appId)}&limit=100`)
      .then(async (res) => {
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        const data = (await res.json()) as TelemetryQueryResponse;
        setLogs(data.logs ?? []);
        setStatus("ready");
      })
      .catch((err) => {
        setStatus("error");
        setError((err as Error).message);
      });
  }, [appId, effectiveProjectID, refreshToken]);

  const filteredRows = logs.filter((row) => {
    if (level && row.level !== level) return false;
    if (query && !`${row.pod_id ?? ""} ${row.message}`.toLowerCase().includes(query.toLowerCase())) return false;
    return true;
  });

  const levels = Array.from(new Set(logs.map((r) => r.level))).sort();

  return (
    <div className="drawerSectionStack" data-logs-status={status} data-logs-count={logs.length}>
      <div className="logToolbar">
        <div className="logFilters">
          <label>
            Level
            <select
              className="select"
              onChange={(e) => setLevel(e.target.value)}
              value={level}
            >
              <option value="">All levels</option>
              {levels.map((l) => (
                <option key={l} value={l}>{l.toUpperCase()}</option>
              ))}
            </select>
          </label>
          <label>
            Search messages
            <input
              autoComplete="off"
              className="field"
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Search log output…"
              type="search"
              value={query}
            />
          </label>
        </div>
        <div className="logToolbarActions">
          <button
            className="secondaryAction"
            disabled={status === "loading"}
            onClick={() => setRefreshToken((r) => r + 1)}
            type="button"
          >
            Refresh Logs
          </button>
        </div>
      </div>

      <div className="logSecurityNotice" role="note">
        <small>
          <strong>Security boundary:</strong> Logs represent recent bounded application output. Opsi applies redaction to recognized tokens and credentials, but runtime application output is treated as untrusted text. Arbitrary exec or remote shells are not supported.
        </small>
      </div>

      {error ? (
        <p className="inlineError" role="alert">
          {error}. Last factual log tail is preserved.
        </p>
      ) : null}

      {filteredRows.length > 0 ? (
        <ol className="logViewerList" aria-label="Recent application runtime logs">
          {filteredRows.map((row, idx) => (
            <li className={`logRow ${row.level}`} key={`${row.observed_unix}-${row.fingerprint}-${idx}`}>
              <time dateTime={new Date(row.observed_unix * 1000).toISOString()}>
                {formatObserved(row.observed_unix)}
              </time>
              <StatusBadge value={row.level === "error" ? "failed" : row.level === "warn" ? "degraded" : "healthy"} label={row.level.toUpperCase()} />
              {row.pod_id ? <span className="replicaBadge" title="Workload replica">{row.pod_id}</span> : null}
              <code className="logMessage">{safeLogMessage(row.message)}</code>
            </li>
          ))}
        </ol>
      ) : status === "loading" ? (
        <Empty title="Loading logs…" text="Fetching recent bounded log tail from Agent authority." />
      ) : (
        <Empty title="No logs available" text="No recent logs are available for this workload." />
      )}
    </div>
  );
}

function AppEventsSection({ events }: { events: RuntimeEvent[] }) {
  return (
    <div className="drawerSectionStack">
      {events.length > 0 ? (
        <ol className="eventTimeline" aria-label="Application runtime and rollout event timeline">
          {events.map((evt) => (
            <li key={evt.id}>
              <span className={`timelineDot ${evt.status}`} aria-hidden="true" />
              <div>
                <div className="eventHeader">
                  <strong>{evt.title}</strong>
                  <time dateTime={typeof evt.timestamp === "number" ? new Date(evt.timestamp).toISOString() : evt.timestamp}>
                    {evt.formattedTime}
                  </time>
                </div>
                <p className="eventDetail">{evt.detail}</p>
                <small className="muted">{evt.freshness}</small>
              </div>
            </li>
          ))}
        </ol>
      ) : (
        <Empty title="No recent events" text="No recent runtime or rollout events reported for this application." />
      )}
    </div>
  );
}

function AppExposureSection({ app }: { app: ApplicationRuntimeSummary }) {
  return (
    <div className="drawerSectionStack">
      <Surface title="Exposure & Public Routing">
        <dl className="evidenceGrid">
          <Fact label="Exposure Status" value={app.exposureLabel} />
          <Fact label="Hostname" value={app.exposureHostname || "Not configured"} />
          <Fact label="Path" value={app.exposurePath || "/"} />
          {app.exposureURL ? (
            <div>
              <dt>Endpoint URL</dt>
              <dd>
                <a href={app.exposureURL} rel="noreferrer" target="_blank">
                  {app.exposureURL} ↗
                </a>
              </dd>
            </div>
          ) : null}
        </dl>
      </Surface>
    </div>
  );
}
