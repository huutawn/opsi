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
