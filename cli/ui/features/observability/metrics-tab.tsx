"use client";

import { Button, Empty, Icon, StatusBadge } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";
import type { ObservabilityModel } from "@/features/observability/observability-view";
import { PartialCoverageBanner, TimeWindowSelector } from "@/features/observability/shared";
export function MetricsTab({ console, model }: { console: ConsoleController; model: ObservabilityModel }) {
  const telemetry = model.data.telemetry;
  const agentUnavailable = model.data.sources.telemetry === "unavailable";
  return (
    <div className="space-y-6" data-testid="observability-metrics">
      <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-4">
        <div>
          <p className="font-label-sm text-xs text-primary uppercase tracking-wider">Factual Metrics</p>
          <h2 className="font-headline-md text-xl font-bold text-on-surface">Service Metrics</h2>
          <p className="text-xs text-on-surface-variant mt-0.5">
            Resource usage, restart counters, and reported telemetry metrics.
          </p>
        </div>
        <div className="flex items-center gap-3">
          <TimeWindowSelector
            disabled={model.data.sources.telemetry === "loading"}
            onChange={(win) => console.navigate({ window: win })}
            value={console.route.window || "1h"}
          />
          <Button
            disabled={model.data.sources.telemetry === "loading"}
            onClick={() => void model.load()}
            size="sm"
            variant="secondary"
          >
            <Icon name="refresh" className="text-[16px]" />
            Refresh Metrics
          </Button>
        </div>
      </div>
      {agentUnavailable ? (
        <div className="p-4 bg-status-warning/10 border border-status-warning/30 rounded-xl text-status-warning text-xs space-y-1">
          <b>Agent telemetry unavailable</b>
          <p className="text-on-surface-variant text-[11px]">
            Cloud service inventory remains available; metric values are not inferred as zero.
          </p>
        </div>
      ) : null}

      <PartialCoverageBanner
        coverage={model.data.summary?.coverage}
        sourceName="Metrics"
      />

      {telemetry.length ? (
        <div className="space-y-6">
          <Comparison title="Readiness comparison" rows={telemetry.map((item) => ({ label: item.service_id, value: item.ready_pods, suffix: `${item.ready_pods}/${item.pod_count} ready`, status: item.ready_pods < item.pod_count ? "degraded" : "healthy" }))} />
          <Comparison title="Restarts" rows={telemetry.map((item) => ({ label: item.service_id, value: item.restart_count ?? null, suffix: item.restart_count === undefined ? "Unknown" : String(item.restart_count), status: metricStatus(item.restart_count) }))} />
          <Comparison title="Recent errors" rows={telemetry.map((item) => ({ label: item.service_id, value: item.recent_error_count ?? null, suffix: item.recent_error_count === undefined ? "Unknown" : String(item.recent_error_count), status: metricStatus(item.recent_error_count) }))} />
          <Comparison title="CPU by service" rows={telemetry.map((item) => ({ label: item.service_id, value: item.cpu_cores ?? null, suffix: item.cpu_cores === undefined ? "Unknown" : `${item.cpu_cores} cores`, status: "unknown" }))} />
          <Comparison title="Memory by service" rows={telemetry.map((item) => ({ label: item.service_id, value: item.memory_bytes ?? null, suffix: item.memory_bytes === undefined ? "Unknown" : `${item.memory_bytes} bytes`, status: "unknown" }))} />
        </div>
      ) : (
        <Empty title="No metric series" text="The Agent returned no service samples. A blank chart is not rendered." />
      )}

      {console.state.support?.dashboard.panels.map((panel) =>
        panel.series.length ? (
          <Comparison
            key={panel.id}
            note={`${panel.description || panel.query}${
              panel.series.some((series) => (series.points?.length ?? 0) > 0)
                ? " · Recent ordered samples — timestamps not reported. No time axis is drawn."
                : ""
            }`}
            rows={panel.series.map((series) => ({
              label: series.name,
              value: series.value,
              suffix: panel.unit ? `${series.value} ${panel.unit}` : String(series.value),
              status: series.status,
            }))}
            title={panel.title}
          />
        ) : (
          <div className="bg-surface-container-low border border-outline-variant/20 rounded-2xl p-5 shadow-sm space-y-2" key={panel.id}>
            <h3 className="font-headline-md text-sm font-bold text-on-surface">{panel.title}</h3>
            <Empty text="No samples for this panel." />
          </div>
        ),
      )}
    </div>
  );
}

function metricStatus(value?: number) {
  return value === undefined ? "unknown" : value > 0 ? "degraded" : "healthy";
}

function Comparison({
  title,
  rows,
  note,
}: {
  title: string;
  rows: Array<{ label: string; value: number | null; suffix: string; status: string }>;
  note?: string;
}) {
  const known = rows.map((row) => row.value).filter((value): value is number => value !== null && Number.isFinite(value));
  const max = Math.max(1, ...known);

  return (
    <div className="bg-surface-container-low border border-outline-variant/20 rounded-2xl p-5 shadow-sm space-y-4">
      <h3 className="font-headline-md text-sm font-bold text-on-surface flex items-center justify-between">
        <span>{title}</span>
        {note ? <span className="text-xs font-normal text-on-surface-variant font-code-md">{note}</span> : null}
      </h3>

      <div className="space-y-3" role="img" aria-label={`${title}: ${rows.map((row) => `${row.label} ${row.suffix}`).join(", ")}`}>
        {rows.map((row) => (
          <div className="flex items-center gap-4 text-xs" key={row.label}>
            <span className="w-32 font-semibold text-on-surface truncate font-code-md">{row.label}</span>
            <div className="flex-1 bg-surface-container rounded-full h-3 overflow-hidden border border-outline-variant/20">
              <div
                className="bg-primary h-full rounded-full transition-all duration-300"
                style={{ width: row.value === null ? "0%" : `${Math.max(3, (row.value / max) * 100)}%` }}
              />
            </div>
            <div className="flex items-center gap-2 w-32 justify-end font-code-md text-on-surface-variant">
              <StatusBadge value={row.status} />
              <span>{row.suffix}</span>
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}
