"use client";

import { useEffect, useState } from "react";
import { Empty, Panel, StatusBadge } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";
import { LocalClient } from "@/lib/api/local-client";
import type { PlacementFacts } from "@/lib/contracts/registry";

const client = new LocalClient();

export function RuntimeView({ console }: { console: ConsoleController }) {
  const projectID = console.state.project?.id;
  const [facts, setFacts] = useState<PlacementFacts | null>(null);
  const [status, setStatus] = useState<"idle" | "loading" | "error">("idle");
  const [error, setError] = useState("");

  function load() {
    if (!projectID) return;
    setStatus("loading");
    setError("");
    client
      .placementFacts(projectID)
      .then((result) => {
        setFacts(result);
        setStatus("idle");
      })
      .catch((cause: Error) => {
        setError(cause.message);
        setStatus("error");
      });
  }

  useEffect(() => {
    queueMicrotask(() => load());
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [projectID]);

  if (!projectID) return <Empty text="Select a project first." />;
  return (
    <section className="grid">
      {console.session?.agent_connected !== "ok" ? (
        <div className="errorBox" role="status"><b>AGENT_UNAVAILABLE</b><span>Cloud runtime inventory remains visible. Start or reconnect the configured Agent before Agent-dependent actions.</span></div>
      ) : null}
      <Panel title="Runtime inventory">
        <button disabled={status === "loading"} onClick={load} type="button">{status === "loading" ? "Loading..." : "Refresh runtime"}</button>
        {status === "loading" ? <p aria-live="polite" role="status">Loading factual topology facts...</p> : null}
        {status === "error" ? <div className="errorBox" role="alert"><b>{error}</b><button onClick={load} type="button">Retry</button></div> : null}
        {facts?.runtimes.length ? (
          <div className="tableWrap">
            <table>
              <thead><tr><th>Runtime</th><th>Environment</th><th>Type</th><th>Status</th><th>Nodes</th><th>Agents</th></tr></thead>
              <tbody>
                {facts.runtimes.map((runtime) => (
                  <tr key={runtime.id}>
                    <td>{runtime.name}<br /><code>{runtime.id}</code></td>
                    <td>{facts.environments.find((item) => item.id === runtime.environment_id)?.name || runtime.environment_id}</td>
                    <td>{runtime.type}</td>
                    <td><StatusBadge value={runtime.status} /></td>
                    <td>{facts.nodes.filter((item) => item.runtime_id === runtime.id).length}</td>
                    <td>{facts.agents.filter((item) => item.runtime_id === runtime.id).length}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : status === "idle" ? <Empty text="No runtime inventory exists for this project." /> : null}
      </Panel>
    </section>
  );
}
