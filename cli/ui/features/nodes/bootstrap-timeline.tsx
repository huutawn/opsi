import { Empty, Panel, StatusBadge } from "@/components/ui/primitives";
import { EventsList } from "@/features/console/shared";
import type { ConsoleController } from "@/features/console/types";
import { formatTime } from "@/lib/formatting/time";

export function BootstrapTimeline({ console }: { console: ConsoleController }) {
  return (
    <Panel title="Bootstrap timeline">
      {console.state.sessions.length ? <SessionsTable console={console} /> : null}
      {console.state.bootstrapEvents.length ? (
        <EventsList events={console.state.bootstrapEvents} />
      ) : (
        <Empty text="Bootstrap event stream appears after Add Server starts." />
      )}
    </Panel>
  );
}

function SessionsTable({ console }: { console: ConsoleController }) {
  return (
    <div className="tableWrap">
      <table>
        <thead>
          <tr>
            <th>Session</th>
            <th>Status</th>
            <th>Host</th>
            <th>Role</th>
            <th>Progress</th>
            <th>Failure</th>
            <th>Created</th>
            <th>Events</th>
          </tr>
        </thead>
        <tbody>
          {console.state.sessions.map((item) => (
            <tr key={item.id}>
              <td>{item.id}</td>
              <td>
                <StatusBadge value={item.status} />
              </td>
              <td>{item.public_host || "-"}</td>
              <td>{item.role}</td>
              <td>
                {item.checkpoint
                  ? `${item.checkpoint.next_step_index}/4${item.checkpoint.last_completed_step ? ` · ${item.checkpoint.last_completed_step}` : ""}`
                  : `${item.attempt_count ?? 0}/${item.max_attempts ?? 0} attempts`}
              </td>
              <td>{item.last_failure_message_redacted || item.last_failure_code || "-"}</td>
              <td>{formatTime(item.created_at)}</td>
              <td>
                <button onClick={() => void console.actions.loadBootstrapEvents(item.id)} type="button">
                  Reconnect
                </button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
