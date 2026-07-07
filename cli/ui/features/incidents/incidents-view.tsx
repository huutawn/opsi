import { Empty, Panel } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";

export function IncidentsView({ console }: { console: ConsoleController }) {
  const failedDeploys = console.state.deployments.filter((item) => ["failed", "cancelled"].includes(item.status));
  const unhealthyNodes = console.state.nodes.filter((item) => !["healthy", "removed"].includes(item.status));
  const incident = console.state.incidentResult?.incident;

  return (
    <section className="grid">
      <Panel title="Incidents & RCA">
        {failedDeploys.length || unhealthyNodes.length ? (
          <div className="grid">
            {failedDeploys.map((item) => (
              <div className="incidentRow" key={item.id}>
                <b>Deployment {item.id}</b>
                <span className="muted">Status {item.status}. RCA requires sanitized Agent context.</span>
                <span className="muted">{item.failure_code ?? "deployment_failed"}</span>
              </div>
            ))}
            {unhealthyNodes.map((item) => (
              <div className="incidentRow" key={item.id}>
                <b>Node {item.name}</b>
                <span className="muted">Status {item.status}. Raw logs stay on Agent.</span>
                <button onClick={() => void console.actions.diagnostics(item.id)} type="button">
                  Open diagnostics
                </button>
              </div>
            ))}
          </div>
        ) : (
          <Empty text="No active incident signals from registry state. RCA uses sanitized Agent context only when an incident exists." />
        )}
      </Panel>
      <Panel title="Agent RCA">
        <form className="grid" onSubmit={console.actions.incidentAnalyze}>
          <input aria-label="Incident ID" className="field" name="incident_id" required />
          <input aria-label="User ID" className="field" name="user_id" required />
          <select className="select" defaultValue="Developer" name="role">
            <option>Owner</option>
            <option>Developer</option>
            <option>Viewer</option>
          </select>
          <button disabled={console.state.busy === "incident-analyze"} type="submit">
            Analyze
          </button>
        </form>
        {incident ? (
          <div className="incidentRow">
            <b>{incident.incident_id}</b>
            <span className="muted">
              {incident.rca_metadata?.provider ?? "unknown"} / {incident.rca_metadata?.model ?? "unknown"}{" "}
              {incident.rca_metadata?.fallback_used ? "fallback" : "provider"}
            </span>
            <span>{incident.root_cause}</span>
            <span className="muted">Confidence {Math.round((incident.confidence ?? 0) * 100)}%</span>
            {(incident.recommended_actions ?? []).map((action) => (
              <span className="muted" key={action.id}>
                {action.id}: {action.type} · {action.action_hash}
              </span>
            ))}
          </div>
        ) : null}
      </Panel>
      <Panel title="Mitigation controls">
        <form className="grid" onSubmit={console.actions.incidentApprove}>
          <input aria-label="Incident ID" className="field" name="incident_id" required />
          <input aria-label="Action ID" className="field" name="action_id" required />
          <input aria-label="Action hash" className="field" name="action_hash" required />
          <input aria-label="User ID" className="field" name="user_id" required />
          <select className="select" defaultValue="Developer" name="role">
            <option>Owner</option>
            <option>Developer</option>
            <option>Viewer</option>
          </select>
          <button disabled={console.state.busy === "incident-approve"} type="submit">
            Approve
          </button>
        </form>
        <form className="grid" onSubmit={console.actions.incidentResolve}>
          <input aria-label="Incident ID" className="field" name="incident_id" required />
          <input aria-label="User ID" className="field" name="user_id" required />
          <select className="select" defaultValue="Developer" name="role">
            <option>Owner</option>
            <option>Developer</option>
            <option>Viewer</option>
          </select>
          <button disabled={console.state.busy === "incident-resolve"} type="submit">
            Resolve
          </button>
        </form>
      </Panel>
    </section>
  );
}
