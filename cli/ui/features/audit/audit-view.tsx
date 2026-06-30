"use client";

import { useMemo, useState } from "react";
import { Empty, Panel, StatusBadge } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";
import { formatTime } from "@/lib/formatting/time";

export function AuditView({ console }: { console: ConsoleController }) {
  const [query, setQuery] = useState("");
  const [result, setResult] = useState("");
  const rows = useMemo(() => {
    const needle = query.trim().toLowerCase();
    return console.state.audit.filter((item) => {
      const haystack = [item.actor_user_id, item.actor_type, item.action, item.resource_type, item.resource_id, JSON.stringify(item.metadata_redacted ?? {})]
        .join(" ")
        .toLowerCase();
      return (!needle || haystack.includes(needle)) && (!result || item.result === result);
    });
  }, [console.state.audit, query, result]);

  return (
    <Panel title="Audit">
      <div className="filterBar">
        <label>
          Search audit
          <input className="field" onChange={(event) => setQuery(event.target.value)} placeholder="actor, action, resource, request id" value={query} />
        </label>
        <label>
          Result
          <select className="select" onChange={(event) => setResult(event.target.value)} value={result}>
            <option value="">All</option>
            <option value="success">Success</option>
            <option value="denied">Denied</option>
            <option value="failed">Failed</option>
          </select>
        </label>
      </div>
      {rows.length ? (
        <div className="tableWrap">
          <table>
            <thead>
              <tr>
                <th>Actor</th>
                <th>Action</th>
                <th>Resource</th>
                <th>Result</th>
                <th>Redacted metadata</th>
                <th>Time</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((item) => (
                <tr key={item.id}>
                  <td>{item.actor_user_id || item.actor_type}</td>
                  <td>{item.action}</td>
                  <td>
                    {item.resource_type}/{item.resource_id}
                  </td>
                  <td>
                    <StatusBadge value={item.result} />
                  </td>
                  <td className="muted">{formatMetadata(item.metadata_redacted)}</td>
                  <td>{formatTime(item.created_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <Empty text={console.state.audit.length ? "No audit events match these filters." : "No audit events for this project."} />
      )}
    </Panel>
  );
}

function formatMetadata(value?: Record<string, unknown>) {
  if (!value || !Object.keys(value).length) return "-";
  return JSON.stringify(value);
}
