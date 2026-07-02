import { Empty, Metric, Panel } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";
import type { SupportSignal } from "@/lib/contracts/registry";

export function SupportView({ console }: { console: ConsoleController }) {
  const summary = console.state.support;
  if (!summary) return <Empty text="Support data is unavailable until a project is loaded." />;
  const counts = summary.counts;

  return (
    <div className="grid">
      <section className="panel">
        <div className="hero">
          <div>
            <p className="muted">Observability and support</p>
            <h1>SRE support</h1>
            <p className="muted">
              Live project readiness, SLO signals, alert rules, runbooks, and request IDs. No secret values or raw credentials are exposed.
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

function SignalBadge({ signal }: { signal: SupportSignal }) {
  const cls = signal.status === "ok" || signal.status === "ready" ? "ok" : signal.status === "critical" ? "bad" : "warn";
  return <span className={`status ${cls}`}>{signal.status}</span>;
}

function label(value: string) {
  return value.replaceAll("_", " ");
}
