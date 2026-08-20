"use client";

import { useEffect, useMemo, useState } from "react";
import { Button, Empty, Icon, StatusBadge } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";
import { safeLogMessage } from "@/features/observability/data";
import type { ObservabilityModel } from "@/features/observability/observability-view";
import { formatObserved } from "@/features/observability/shared";

export function LogsTab({ console, model }: { console: ConsoleController; model: ObservabilityModel }) {
  const service = console.route.service || "";
  const level = console.route.level || "";
  const query = console.route.query || "";
  const [paused, setPaused] = useState(true);

  useEffect(() => {
    void model.loadLogs({ serviceID: service || undefined });
  }, [console.state.project?.id, service]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (paused) return;
    const timer = window.setInterval(() => {
      if (document.visibilityState === "visible") void model.loadLogs({ serviceID: service || undefined });
    }, 15_000);
    return () => window.clearInterval(timer);
  }, [model, paused, service]);

  const rows = useMemo(
    () =>
      model.data.logs.rows.filter(
        (row) =>
          (!level || row.level === level) &&
          (!query || `${row.service_id ?? ""} ${row.message}`.toLowerCase().includes(query.toLowerCase())),
      ),
    [model.data.logs.rows, level, query],
  );
  const levels = [...new Set(model.data.logs.rows.map((row) => row.level))].sort();

  return (
    <div className="space-y-6" data-testid="observability-logs">
      <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-4">
        <div>
          <p className="font-label-sm text-xs text-primary uppercase tracking-wider">Diagnostic Log Explorer</p>
          <h2 className="font-headline-md text-xl font-bold text-on-surface">System Logs</h2>
          <p className="text-xs text-on-surface-variant mt-0.5">
            Bounded diagnostic logs stream with level filters and search.
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button
            aria-pressed={paused}
            onClick={() => setPaused((value) => !value)}
            size="sm"
            variant="outline"
          >
            <Icon name={paused ? "play_arrow" : "pause"} className="text-[16px]" />
            {paused ? "Resume periodic refresh" : "Pause periodic refresh"}
          </Button>
          <Button
            disabled={model.data.logs.source === "loading"}
            onClick={() => void model.loadLogs({ serviceID: service || undefined })}
            size="sm"
            variant="secondary"
          >
            <Icon name="refresh" className="text-[16px]" />
            Refresh
          </Button>
        </div>
      </div>

      {/* Filters Bar */}
      <div className="grid grid-cols-1 sm:grid-cols-3 gap-4 bg-surface-container-low p-4 rounded-2xl border border-outline-variant/20 shadow-sm text-xs">
        <label className="flex flex-col gap-1.5 font-medium text-on-surface-variant">
          <span>Service</span>
          <select
            className="w-full bg-surface-container border border-outline-variant/30 rounded-xl px-3 py-2 text-xs text-on-surface focus:outline-none focus:border-primary/50"
            name="service"
            onChange={(event) => console.navigate({ service: event.target.value, cursor: "" })}
            value={service}
          >
            <option value="">All loaded services</option>
            {console.state.services.map((item) => (
              <option key={item.id} value={item.id}>
                {item.name}
              </option>
            ))}
          </select>
        </label>

        <label className="flex flex-col gap-1.5 font-medium text-on-surface-variant">
          <span>Level</span>
          <select
            className="w-full bg-surface-container border border-outline-variant/30 rounded-xl px-3 py-2 text-xs text-on-surface focus:outline-none focus:border-primary/50"
            name="level"
            onChange={(event) => console.navigate({ level: event.target.value })}
            value={level}
          >
            <option value="">All levels</option>
            {levels.map((item) => (
              <option key={item} value={item}>
                {item}
              </option>
            ))}
          </select>
        </label>

        <label className="flex flex-col gap-1.5 font-medium text-on-surface-variant">
          <span>Search loaded page</span>
          <input
            aria-label="Search loaded page"
            autoComplete="off"
            className="w-full bg-surface-container border border-outline-variant/30 rounded-xl px-3 py-2 text-xs text-on-surface focus:outline-none focus:border-primary/50"
            name="query"
            onChange={(event) => console.navigate({ query: event.target.value })}
            placeholder="Search message text…"
            type="search"
            value={query}
          />
        </label>
      </div>

      <div className="flex items-center justify-between text-xs text-on-surface-variant font-code-md">
        <span>Source: <b>{model.data.logs.source}</b></span>
        <span>{rows.length} visible / {model.data.logs.rows.length} loaded</span>
      </div>

      {model.data.logs.error ? (
        <div className="p-4 bg-error-container/20 border border-error/30 rounded-xl text-xs text-error" role="alert">
          {model.data.logs.error}. Last factual data is preserved.
        </div>
      ) : null}

      {rows.length ? (
        <div className="bg-surface-container-lowest border border-outline-variant/20 rounded-2xl p-4 font-code-md text-xs text-on-surface max-h-[500px] overflow-y-auto space-y-1.5 shadow-sm">
          {rows.map((row, index) => (
            <div className="flex items-start gap-2.5 leading-relaxed" key={`${row.observed_unix}-${row.fingerprint}-${index}`}>
              <time className="text-on-surface-variant/60 shrink-0 text-[11px]" dateTime={new Date(row.observed_unix * 1000).toISOString()}>
                {formatObserved(row.observed_unix)}
              </time>
              <StatusBadge value={row.level} />
              <strong className="text-primary shrink-0">{row.service_id || "runtime"}</strong>
              <span className="text-on-surface break-all">{safeLogMessage(row.message)}</span>
            </div>
          ))}
        </div>
      ) : model.data.logs.source !== "loading" ? (
        <Empty text="No log rows match the loaded page and filters." />
      ) : (
        <Empty title="Loading logs…" text="Reading one bounded Agent page." />
      )}

      {model.data.logs.nextCursor ? (
        <Button
          onClick={() => void model.loadLogs({ serviceID: service || undefined, cursor: model.data.logs.nextCursor })}
          variant="outline"
        >
          Load next page
        </Button>
      ) : null}
    </div>
  );
}
