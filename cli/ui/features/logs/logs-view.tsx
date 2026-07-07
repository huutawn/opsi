"use client";

import { useEffect, useState } from "react";
import { Empty, Panel, StatusBadge } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";
import { LocalClient } from "@/lib/api/local-client";
import type { TelemetryLogEntry } from "@/lib/contracts/registry";

const client = new LocalClient();

export function LogsView({ console }: { console: ConsoleController }) {
  const projectID = console.state.project?.id;
  const [logs, setLogs] = useState<TelemetryLogEntry[]>([]);
  const [message, setMessage] = useState("");

	useEffect(() => {
		if (!projectID) return;
		let cancelled = false;
		client
			.logs(projectID, { limit: 50 })
			.then((resp) => {
				if (!cancelled) {
					setMessage("");
					setLogs(resp.logs ?? []);
				}
			})
      .catch((error: Error) => {
        if (!cancelled) setMessage(error.message);
      });
    return () => {
      cancelled = true;
    };
  }, [projectID]);

  if (!projectID) return <Empty text="Select a project first." />;

  return (
    <Panel title="Logs">
      {message ? <Empty text={message} /> : null}
      {!message && logs.length === 0 ? <Empty text="No recent Agent logs for this project." /> : null}
      {logs.length > 0 ? (
        <table>
          <thead>
            <tr>
              <th>Time</th>
              <th>Level</th>
              <th>Service</th>
              <th>Message</th>
            </tr>
          </thead>
          <tbody>
            {logs.map((log) => (
              <tr key={`${log.observed_unix}-${log.fingerprint}-${log.pod_id ?? ""}`}>
                <td>{new Date(log.observed_unix * 1000).toLocaleString()}</td>
                <td>
                  <StatusBadge value={log.level} />
                </td>
                <td>{log.service_id || log.pod_id || "runtime"}</td>
                <td>{log.message}</td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : null}
    </Panel>
  );
}
