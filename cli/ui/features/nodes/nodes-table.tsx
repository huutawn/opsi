import { Empty, StatusBadge } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";
import { formatTime } from "@/lib/formatting/time";

export function NodesTable({ console }: { console: ConsoleController }) {
  if (!console.state.nodes.length) return <Empty text="No servers connected. Add a VPS/server so Opsi can install K3s and the Agent." />;
  return (
    <div className="tableWrap">
      <table>
        <thead>
          <tr>
            <th>Name</th>
            <th>Role</th>
            <th>Status</th>
            <th>Public host</th>
            <th>Provider/region</th>
            <th>CPU</th>
            <th>Memory</th>
            <th>Disk</th>
            <th>K3s</th>
            <th>Agent</th>
            <th>Last seen</th>
            <th>Actions</th>
          </tr>
        </thead>
        <tbody>
          {console.state.nodes.map((node) => (
            <tr key={node.id}>
              <td>{node.name}</td>
              <td>{node.role}</td>
              <td>
                <StatusBadge value={node.status} />
              </td>
              <td>{node.public_host || "-"}</td>
              <td>{[node.provider, node.region].filter(Boolean).join(" / ") || "-"}</td>
              <td>{node.cpu_cores || "-"}</td>
              <td>{node.memory_mb || "-"}</td>
              <td>{node.disk_total_gb || "-"}</td>
              <td>{node.k3s_status || node.k3s_role || "-"}</td>
              <td>{node.agent_version || node.agent_id || "-"}</td>
              <td>{formatTime(node.last_seen_at)}</td>
              <td className="actions">
                <button onClick={() => void console.actions.diagnostics(node.id)} type="button">
                  Details
                </button>
                <button disabled={node.status === "removed"} onClick={() => void console.actions.nodeAction(node.id, "drain")} type="button">
                  Drain
                </button>
                <button
                  className="danger"
                  disabled={node.status === "removed"}
                  onClick={() => void console.actions.nodeAction(node.id, "remove")}
                  type="button"
                >
                  Remove
                </button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
