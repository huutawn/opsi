import type { TimelineEvent } from "@/lib/contracts/registry";
import { formatTime } from "@/lib/formatting/time";

export function EventsList({ events }: { events: TimelineEvent[] }) {
  return (
    <div className="timeline">
      {events.map((event) => (
        <div className="event" key={event.id}>
          <div>{event.progress_percent}%</div>
          <div>
            <b>{event.step}</b>
            <br />
            <span className="muted">{event.message_redacted}</span>
            <div className="bar">
              <i style={{ width: `${event.progress_percent}%` }} />
            </div>
          </div>
          <div>{formatTime(event.created_at)}</div>
        </div>
      ))}
    </div>
  );
}
