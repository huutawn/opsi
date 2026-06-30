import { Empty, Panel } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";

export function IncidentsView({ console }: { console: ConsoleController }) {
  const failedDeploys = console.state.deployments.filter((item) => ["failed", "cancelled"].includes(item.status));
  const unhealthyNodes = console.state.nodes.filter((item) => !["healthy", "removed"].includes(item.status));

  return (
    <section className="grid">
      <Panel title="Incidents & RCA">
        {failedDeploys.length || unhealthyNodes.length ? (
          <div className="grid">
            {failedDeploys.map((item) => (
              <div className="incidentRow" key={item.id}>
                <b>Deployment {item.id}</b>
                <span className="muted">Status {item.status}. RCA requires sanitized Agent context.</span>
                <button disabled type="button">
                  Analyze sanitized context
                </button>
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
      <Panel title="Mitigation controls">
        <Empty text="Mitigation actions remain disabled until Agent returns a typed allowlist and the user explicitly approves." />
      </Panel>
    </section>
  );
}
