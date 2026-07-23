import { Empty, Panel } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";

export function IncidentsView({ console }: { console: ConsoleController }) {
  const incident = console.state.incidentDetail;

  return (
    <section className="grid">
      {console.state.incidentError ? <p role="alert">{console.state.incidentError}</p> : null}
      <Panel title="Incidents">
        <form className="grid" onSubmit={console.actions.incidentList}>
          <select className="select" defaultValue="" name="status">
            <option value="">All statuses</option>
            <option value="open">Open</option>
            <option value="resolved">Resolved</option>
          </select>
          <button disabled={console.state.busy === "incident-list"} type="submit">
            {console.state.busy === "incident-list" ? "Loading..." : "Load incidents"}
          </button>
        </form>
        {console.state.incidents.length ? (
          <div className="grid">
            {console.state.incidents.map((item) => (
              <div className="incidentRow" key={item.incident_id}>
                <b>{item.incident_id}</b>
                <span className="muted">
                  {item.severity || "unknown severity"} · {item.status} · {item.anomaly_type || "unspecified anomaly"}
                </span>
                <span className="muted">Service {item.service_id || "unassigned"}</span>
              </div>
            ))}
          </div>
        ) : (
          <Empty text="Load Agent incident records for the selected project." />
        )}
      </Panel>
      <Panel title="Incident detail">
        <form className="grid" onSubmit={console.actions.incidentGet}>
          <input aria-label="Incident ID" className="field" name="incident_id" required />
          <button disabled={console.state.busy === "incident-get"} type="submit">
            {console.state.busy === "incident-get" ? "Loading..." : "Get detail"}
          </button>
        </form>
        {incident ? (
          <div className="incidentRow">
            <b>{incident.incident_id}</b>
            <span className="muted">Status {incident.status} · Severity {incident.severity || "unknown"}</span>
            <span className="muted">Anomaly {incident.anomaly_type || "unspecified"}</span>
            <span className="muted">Node {incident.node_id || "unassigned"} · Service {incident.service_id || "unassigned"} · Pod {incident.pod_id || "unassigned"}</span>
            <span className="muted">Created {formatIncidentTime(incident.created_at_unix)}</span>
            <span className="muted">Resolved {formatIncidentTime(incident.resolved_at_unix)} · MTTR {incident.mttr_seconds ?? 0}s</span>
          </div>
        ) : (
          <Empty text="Select an incident ID to inspect factual runtime state." />
        )}
      </Panel>
      <Panel title="Resolve incident">
        <form className="grid" onSubmit={console.actions.incidentResolve}>
          <input aria-label="Incident ID" className="field" name="incident_id" required />
          <button disabled={console.state.busy === "incident-resolve"} type="submit">
            Resolve
          </button>
        </form>
      </Panel>
    </section>
  );
}

function formatIncidentTime(value?: number) {
  return value ? new Date(value * 1000).toLocaleString() : "not recorded";
}
