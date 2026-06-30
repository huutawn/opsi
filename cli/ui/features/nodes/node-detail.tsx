import { Metric, Panel } from "@/components/ui/primitives";
import { DeploymentMini, EventsList } from "@/features/console/shared";
import type { ConsoleController } from "@/features/console/types";
import type { DeploymentJob, NodeRecord, TimelineEvent } from "@/lib/contracts/registry";

export function NodeDetail({ console }: { console: ConsoleController }) {
  if (!console.state.nodeDetail) return null;
  const node = (console.state.nodeDetail.node ?? {}) as NodeRecord;
  const events = (console.state.nodeDetail.open_bootstrap_events ?? []) as TimelineEvent[];
  const jobs = (console.state.nodeDetail.recent_deployment_jobs ?? []) as DeploymentJob[];
  return (
    <Panel title="Node detail">
      <div className="metrics">
        <Metric label="Health" value={node.status || "-"} />
        <Metric label="K3s" value={node.k3s_status || node.k3s_role || "-"} />
        <Metric label="Agent" value={node.agent_version || node.agent_id || "-"} />
        <Metric label="Capacity" value={`${node.cpu_cores || "-"} CPU`} />
      </div>
      <h3>Bootstrap history</h3>
      {events.length ? <EventsList events={events} /> : <p className="muted">No bootstrap events.</p>}
      <h3>Recent deploys</h3>
      {jobs.length ? <DeploymentMini rows={jobs} /> : <p className="muted">None</p>}
    </Panel>
  );
}
