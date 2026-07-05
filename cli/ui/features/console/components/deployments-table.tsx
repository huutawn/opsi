import { Empty, StatusBadge } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";
import type { DeploymentJob } from "@/lib/contracts/registry";
import { formatTime } from "@/lib/formatting/time";

export function DeploymentsTable({ console }: { console: ConsoleController }) {
  const { deployments, services } = console.state;
  if (!deployments.length) return <Empty text="No deployments. Queued jobs will appear after local submit." />;
  return (
    <div className="tableWrap">
      <table>
        <thead>
          <tr>
            <th>Request</th>
            <th>Service</th>
            <th>Status</th>
            <th>Requested by</th>
            <th>Created</th>
            <th>Actions</th>
          </tr>
        </thead>
        <tbody>
          {deployments.map((deployment) => (
            <tr key={deployment.id}>
              <td>{deployment.id}</td>
              <td>{services.find((item) => item.id === deployment.service_id)?.name || deployment.service_id}</td>
              <td>
                <StatusBadge value={deployment.status} />
              </td>
              <td>{deployment.requested_by || "-"}</td>
              <td>{formatTime(deployment.created_at)}</td>
              <td className="actions">
                <button onClick={() => void console.actions.loadDeploymentEvents(deployment.id)} type="button">
                  Events
                </button>
                <button disabled type="button">
                  Rollback
                </button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

export function DeploymentMini({ rows }: { rows: DeploymentJob[] }) {
  return (
    <div className="tableWrap">
      <table>
        <tbody>
          {rows.map((item) => (
            <tr key={item.id}>
              <td>{item.id}</td>
              <td>
                <StatusBadge value={item.status} />
              </td>
              <td>{formatTime(item.created_at)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
