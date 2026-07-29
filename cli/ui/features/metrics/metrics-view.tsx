"use client";

import { useEffect, useState } from "react";
import { Empty, Metric, Panel } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";
import { LocalClient } from "@/lib/api/local-client";
import type { TelemetrySummary } from "@/lib/contracts/registry";

const client = new LocalClient();

export function MetricsView({ console }: { console: ConsoleController }) {
  const projectID = console.state.project?.id;
  const [summary, setSummary] = useState<TelemetrySummary | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");

  function load() {
    if (!projectID || console.session?.agent_connected !== "ok") return;
    setLoading(true);
    setError("");
    client.telemetrySummary(projectID).then(setSummary).catch((cause: Error) => setError(cause.message)).finally(() => setLoading(false));
  }

  useEffect(() => {
    queueMicrotask(() => load());
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [projectID, console.session?.agent_connected]);

  if (!projectID) return <Empty text="Select a project first." />;
  if (console.session?.agent_connected !== "ok") {
    return <div className="errorBox" role="status"><b>AGENT_UNAVAILABLE</b><span>Metrics require the configured TLS Agent. Cloud project data remains available in other views.</span></div>;
  }
  return (
    <Panel title="Agent telemetry">
      <button disabled={loading} onClick={load} type="button">{loading ? "Loading..." : "Refresh metrics"}</button>
      {loading ? <p aria-live="polite" role="status">Loading redacted telemetry summary...</p> : null}
      {error ? <div className="errorBox" role="alert"><b>{error}</b><button onClick={load} type="button">Retry</button></div> : null}
      {summary ? (
        <div className="metrics">
          <Metric label="Health" value={summary.health || "unknown"} />
          <Metric label="Metrics" value={summary.metric_count ?? 0} />
          <Metric label="Logs" value={summary.log_count ?? 0} />
          <Metric label="Errors" value={summary.error_count ?? 0} />
          <Metric label="Services" value={summary.service_count ?? 0} />
        </div>
      ) : !loading && !error ? <Empty text="No Agent telemetry summary is available." /> : null}
    </Panel>
  );
}
