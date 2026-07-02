import { Empty, Metric, Panel } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";
import type { GrafanaPanel, GrafanaSeries, SupportSignal } from "@/lib/contracts/registry";

export function SupportView({ console }: { console: ConsoleController }) {
  const summary = console.state.support;
  if (!summary) return <Empty text="Support data is unavailable until a project is loaded." />;
  const counts = summary.counts;

  return (
    <div className="grid">
      <section className="panel grafanaHero">
        <div className="hero">
          <div>
            <p className="muted">Prometheus / Grafana view</p>
            <h1>{summary.dashboard.title}</h1>
            <p className="muted">
              Datasource {summary.dashboard.datasource}. Refresh {summary.dashboard.refresh}. Values come from Cloud registry, Agent heartbeat,
              deployment events, and redacted support state.
            </p>
          </div>
          <button type="button" onClick={() => void console.actions.load()}>
            Refresh
          </button>
        </div>
      </section>

      <div className="metrics">
        <Metric label="Healthy nodes" value={`${counts.healthy_nodes}/${counts.nodes}`} />
        <Metric label="Services" value={counts.services} />
        <Metric label="Deploy jobs" value={counts.deployment_jobs} />
        <Metric label="Audit events" value={counts.audit_events} />
      </div>

      <div className="grafanaGrid">
        {summary.dashboard.panels.map((panel) => (
          <DashboardPanel key={panel.id} panel={panel} />
        ))}
      </div>

      <div className="grid cols">
        <Panel title="SLO signals">
          <div className="tableWrap">
            <table>
              <thead>
                <tr>
                  <th>Signal</th>
                  <th>Status</th>
                  <th>Value</th>
                  <th>Target</th>
                  <th>Detail</th>
                </tr>
              </thead>
              <tbody>
                {summary.signals.map((signal) => (
                  <tr key={signal.name}>
                    <td>{label(signal.name)}</td>
                    <td>
                      <SignalBadge signal={signal} />
                    </td>
                    <td>{signal.value}</td>
                    <td>{signal.target}</td>
                    <td className="muted">{signal.detail || "Current data sufficient"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Panel>

        <Panel title="Active alerts">
          {summary.active_alerts.length === 0 ? (
            <Empty text="No active support alerts." />
          ) : (
            <div className="timeline">
              {summary.active_alerts.map((alert) => (
                <div className="incidentRow" key={alert.id}>
                  <span className="muted">{alert.severity.toUpperCase()}</span>
                  <b>{alert.title}</b>
                  <span className="muted">{alert.resource_id || alert.runbook_id}</span>
                </div>
              ))}
            </div>
          )}
        </Panel>
      </div>

      <div className="grid cols">
        <Panel title="Configured alert rules">
          <div className="tableWrap">
            <table>
              <thead>
                <tr>
                  <th>Rule</th>
                  <th>Severity</th>
                  <th>Metric</th>
                  <th>Runbook</th>
                </tr>
              </thead>
              <tbody>
                {summary.configured_alerts.map((rule) => (
                  <tr key={rule.id}>
                    <td>{rule.title}</td>
                    <td>{rule.severity}</td>
                    <td>{rule.metric}</td>
                    <td>{rule.runbook_id}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Panel>

        <Panel title="Production gates">
          <div className="timeline">
            {summary.production_gates.map((gate) => (
              <div className="incidentRow" key={gate.name}>
                <span className={`status ${gate.passed ? "ok" : "warn"}`}>{gate.passed ? "pass" : "watch"}</span>
                <b>{gate.name}</b>
                <span className="muted">{gate.detail}</span>
              </div>
            ))}
          </div>
        </Panel>
      </div>

      <div className="grid cols">
        <Panel title="Break-glass policy">
          <div className="specList compact">
            <div>
              <span>Time-limited</span>
              <b>{yesNo(summary.break_glass_policy.time_limited)}</b>
            </div>
            <div>
              <span>Approval</span>
              <b>{yesNo(summary.break_glass_policy.approval_required)}</b>
            </div>
            <div>
              <span>Reason</span>
              <b>{yesNo(summary.break_glass_policy.reason_required)}</b>
            </div>
            <div>
              <span>Audited</span>
              <b>{yesNo(summary.break_glass_policy.audited)}</b>
            </div>
            <div>
              <span>Secret reveal default</span>
              <b>{yesNo(summary.break_glass_policy.secret_reveal_by_default)}</b>
            </div>
            <div>
              <span>Owner notification</span>
              <b>{summary.break_glass_policy.owner_notification}</b>
            </div>
          </div>
        </Panel>

        <Panel title="Request ID correlation">
          {summary.recent_request_ids?.length ? (
            <div className="timeline">
              {summary.recent_request_ids.map((id) => (
                <code className="event" key={id}>
                  {id}
                </code>
              ))}
            </div>
          ) : (
            <Empty text="No deployment request IDs recorded yet." />
          )}
        </Panel>
      </div>

      <Panel title="Runbooks">
        <div className="tableWrap">
          <table>
            <thead>
              <tr>
                <th>Runbook</th>
                <th>Symptoms</th>
                <th>Mitigation</th>
                <th>Escalation</th>
              </tr>
            </thead>
            <tbody>
              {summary.runbooks.map((runbook) => (
                <tr key={runbook.id}>
                  <td>
                    <b>{runbook.title}</b>
                    <br />
                    <span className="muted">{runbook.dashboard_query}</span>
                  </td>
                  <td>{runbook.symptoms}</td>
                  <td>{runbook.immediate_mitigation}</td>
                  <td>{runbook.escalation_path}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Panel>
    </div>
  );
}

function DashboardPanel({ panel }: { panel: GrafanaPanel }) {
  const max = Math.max(1, ...panel.series.map((item) => item.value));
  return (
    <section className="grafanaPanel">
      <div className="grafanaPanelHead">
        <div>
          <h2>{panel.title}</h2>
          <p className="muted">{panel.description || panel.query}</p>
        </div>
        <code>{panel.unit}</code>
      </div>
      {panel.series.length === 0 ? (
        <Empty text="No samples for this panel yet." />
      ) : (
        <div className="grafanaSeries">
          {panel.series.map((series) => (
            <SeriesBar key={`${panel.id}-${series.name}`} max={max} series={series} />
          ))}
        </div>
      )}
      <code className="query">{panel.query}</code>
    </section>
  );
}

function SeriesBar({ max, series }: { max: number; series: GrafanaSeries }) {
  const width = Math.max(4, Math.min(100, (series.value / max) * 100));
  return (
    <div className="seriesRow">
      <span>{series.name}</span>
      <div className="seriesTrack" aria-label={`${series.name}: ${formatValue(series.value)}`}>
        <i className={series.status === "ok" || series.status === "healthy" || series.status === "active" ? "ok" : series.status === "stale" || series.status === "failed" ? "bad" : "warn"} style={{ width: `${width}%` }} />
      </div>
      <b>{formatValue(series.value)}</b>
    </div>
  );
}

function SignalBadge({ signal }: { signal: SupportSignal }) {
  const cls = signal.status === "ok" || signal.status === "ready" ? "ok" : signal.status === "critical" ? "bad" : "warn";
  return <span className={`status ${cls}`}>{signal.status}</span>;
}

function label(value: string) {
  return value.replaceAll("_", " ");
}

function yesNo(value: boolean) {
  return value ? "yes" : "no";
}

function formatValue(value: number) {
  if (value >= 100) return value.toFixed(0);
  if (value >= 10) return value.toFixed(1);
  return value.toFixed(2).replace(/\.00$/, "");
}
