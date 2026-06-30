import { Metric, StatusBadge } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";

export function ReadinessPanel({ console }: { console: ConsoleController }) {
  const status = console.state.readiness?.status ?? console.state.project?.status ?? "no_project";
  const text =
    status === "ready"
      ? "Ready to deploy. Healthy server node, Agent connected, K3s ready."
      : status === "bootstrapping"
        ? "Bootstrap is running. Watch progress before deploying."
        : "This project is not ready to deploy. Add your first server so Opsi can install K3s and the Agent.";

  return (
    <div className="panel hero">
      <div>
        <h1>{console.state.project?.name ?? "No project"}</h1>
        <p className="muted">{text}</p>
        <StatusBadge value={status} />
      </div>
      <div className="actions">
        <button className="primary" onClick={() => console.setActive("Servers / Nodes")} type="button">
          Add first server
        </button>
        <button disabled={!console.state.readiness?.can_deploy} onClick={() => console.setActive("Services")} type="button">
          Deploy service
        </button>
        <button disabled={!console.state.nodes.length || !console.state.services.length} onClick={() => console.setActive("Topology")} type="button">
          View topology
        </button>
      </div>
    </div>
  );
}

export function Metrics({ console }: { console: ConsoleController }) {
  return (
    <div className="metrics">
      <Metric label="Nodes" value={console.state.nodes.length} />
      <Metric label="Services" value={console.state.services.length} />
      <Metric label="Deployments" value={console.state.deployments.length} />
      <Metric label="Open incidents" value={0} />
    </div>
  );
}
