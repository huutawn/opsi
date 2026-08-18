"use client";

import { Button, Empty, Icon, StatusBadge } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";
import { overallHealth, type SourceState } from "@/features/observability/data";
import type { ObservabilityModel } from "@/features/observability/observability-view";
import { SourceBadge, formatObserved } from "@/features/observability/shared";

export function HealthTab({ console, model }: { console: ConsoleController; model: ObservabilityModel }) {
  const agentAvailable = console.session?.agent_connected === "ok";
  const health = overallHealth(model.data, console.state.services.length, agentAvailable);
  const support = console.state.support;
  const serviceIDs = new Set(console.state.services.map((service) => service.id));
  const unresolvedTelemetry = model.data.telemetry.filter((item) => !serviceIDs.has(item.service_id));

  return (
    <div className="space-y-6" data-testid="observability-health">
      <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-4">
        <div>
          <p className="font-label-sm text-xs text-primary uppercase tracking-wider">Telemetry & Coverage</p>
          <h2 className="font-headline-md text-xl font-bold text-on-surface">System Health</h2>
          <p className="text-xs text-on-surface-variant mt-0.5">
            Service telemetry, coverage, and incident alerts.
          </p>
        </div>
        <div className="flex items-center gap-3">
          <StatusBadge value={health} />
          <Button
            disabled={model.data.sources.telemetry === "loading"}
            onClick={() => void model.load()}
            size="sm"
            variant="secondary"
          >
            <Icon name="refresh" className="text-[16px]" />
            Refresh Health
          </Button>
        </div>
      </div>

      {model.data.error ? (
        <div className="p-4 bg-error-container/20 border border-error/30 rounded-xl text-xs text-error flex items-center gap-2" role="alert">
          <Icon name="error" className="text-[18px] shrink-0" />
          <span>{model.data.error}</span>
        </div>
      ) : null}

      <div className="grid grid-cols-2 sm:grid-cols-5 gap-3 bg-surface-container-low p-4 rounded-2xl border border-outline-variant/20 shadow-sm text-xs" aria-label="Source coverage">
        <SourceBadge label="Cloud registry" state={model.data.sources.registry} />
        <SourceBadge label="Agent telemetry" state={model.data.sources.telemetry} />
        <SourceBadge label="Incident store" state={model.data.sources.incidents} />
        <SourceBadge label="Support projection" state={support ? "ready" : "unavailable"} />
        <div className="flex flex-col gap-1 p-2 bg-surface-container rounded-xl border border-outline-variant/15">
          <span className="text-[10px] text-on-surface-variant uppercase font-semibold">Last Factual Update</span>
          <b className="text-on-surface font-code-md text-xs">{formatObserved(model.data.summary?.end_unix)}</b>
        </div>
      </div>

      <div className="bg-surface-container-low border border-outline-variant/20 rounded-2xl overflow-hidden shadow-sm">
        <div className="p-4 border-b border-outline-variant/20">
          <h3 className="font-headline-md text-sm font-bold text-on-surface">Service Health Matrix</h3>
        </div>
        <div className="overflow-x-auto">
          <table className="w-full text-left border-collapse text-xs">
            <thead>
              <tr className="bg-surface-container/60 text-[11px] font-label-sm uppercase tracking-wider text-on-surface-variant border-b border-outline-variant/20">
                <th className="py-3 px-4 font-semibold">Service</th>
                <th className="py-3 px-4 font-semibold">Ready / Desired</th>
                <th className="py-3 px-4 font-semibold">Restarts</th>
                <th className="py-3 px-4 font-semibold">Errors</th>
                <th className="py-3 px-4 font-semibold">Last Seen</th>
                <th className="py-3 px-4 font-semibold">Health</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-outline-variant/15 text-on-surface">
              {console.state.services.length ? (
                console.state.services.map((service) => {
                  const telemetry = model.data.telemetry.find((item) => item.service_id === service.id);
                  const desired = telemetry?.pod_count ?? service.replicas;
                  const ready = telemetry?.ready_pods;
                  const unavailable = !agentAvailable || model.data.sources.telemetry === "unavailable";
                  const serviceHealth = unavailable
                    ? "unavailable"
                    : telemetry
                    ? ready !== undefined && desired !== undefined && ready < desired
                      ? "degraded"
                      : telemetry.health
                    : "unknown";

                  return (
                    <tr
                      className="hover:bg-surface-container/60 transition-colors cursor-pointer"
                      key={service.id}
                      onClick={() => console.navigate({ service: service.id, tab: "metrics" })}
                    >
                      <td className="py-3.5 px-4 font-semibold text-on-surface">{service.name}</td>
                      <td className="py-3.5 px-4 font-code-md">{unavailable ? "Unavailable" : `${ready ?? "Unknown"}/${desired ?? "Unknown"}`}</td>
                      <td className="py-3.5 px-4 font-code-md">{unavailable ? "Unavailable" : telemetry?.restart_count ?? "Unknown"}</td>
                      <td className="py-3.5 px-4 font-code-md">{unavailable ? "Unavailable" : telemetry?.recent_error_count ?? "Unknown"}</td>
                      <td className="py-3.5 px-4 font-code-md text-on-surface-variant text-[11px]">{unavailable ? "Unavailable" : formatObserved(telemetry?.last_seen_unix)}</td>
                      <td className="py-3.5 px-4"><StatusBadge value={serviceHealth} /></td>
                    </tr>
                  );
                })
              ) : (
                <tr>
                  <td colSpan={6} className="py-8 text-center text-on-surface-variant">No services in this project.</td>
                </tr>
              )}
              {unresolvedTelemetry.map((telemetry) => (
                <tr className="hover:bg-surface-container/60 transition-colors" key={`unresolved-${telemetry.service_id}`}>
                  <td className="py-3.5 px-4 font-semibold text-on-surface-variant italic">Unresolved identity</td>
                  <td className="py-3.5 px-4 font-code-md">{telemetry.ready_pods}/{telemetry.pod_count}</td>
                  <td className="py-3.5 px-4 font-code-md">{telemetry.restart_count ?? "Unknown"}</td>
                  <td className="py-3.5 px-4 font-code-md">{telemetry.recent_error_count ?? "Unknown"}</td>
                  <td className="py-3.5 px-4 font-code-md text-on-surface-variant text-[11px]">{formatObserved(telemetry.last_seen_unix)}</td>
                  <td className="py-3.5 px-4"><StatusBadge value="unknown" /></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>

      <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
        <div className="bg-surface-container-low border border-outline-variant/20 rounded-2xl p-5 shadow-sm space-y-3">
          <h3 className="font-headline-md text-sm font-bold text-on-surface">Active Alerts</h3>
          {support?.active_alerts?.length ? (
            <ul className="divide-y divide-outline-variant/15 text-xs">
              {support.active_alerts.map((alert) => (
                <li className="py-2.5 flex items-center justify-between gap-3" key={alert.id}>
                  <div className="flex items-center gap-2">
                    <StatusBadge value={alert.severity} />
                    <b className="text-on-surface">{alert.title}</b>
                  </div>
                  <small className="text-on-surface-variant font-code-md">{alert.resource_id || alert.runbook_id}</small>
                </li>
              ))}
            </ul>
          ) : (
            <Empty text={support ? "No active alerts reported." : "Alert source unavailable."} />
          )}
        </div>

        <div className="bg-surface-container-low border border-outline-variant/20 rounded-2xl p-5 shadow-sm space-y-3">
          <h3 className="font-headline-md text-sm font-bold text-on-surface">Recent Incidents</h3>
          {model.data.incidents.filter((item) => item.status !== "resolved").length ? (
            <ul className="divide-y divide-outline-variant/15 text-xs">
              {model.data.incidents.filter((item) => item.status !== "resolved").slice(0, 5).map((incident) => (
                <li className="py-2.5 flex items-center justify-between gap-3" key={incident.incident_id}>
                  <button
                    className="flex items-center justify-between w-full text-left hover:text-primary transition-colors cursor-pointer"
                    onClick={() => console.navigate({ tab: "incidents", incident: incident.incident_id })}
                    type="button"
                  >
                    <div className="flex items-center gap-2">
                      <StatusBadge value={incident.severity || "warning"} />
                      <b className="text-on-surface font-code-md">{incident.incident_id}</b>
                    </div>
                    <small className="text-on-surface-variant">{incident.service_id || incident.anomaly_type || "Impact not reported"}</small>
                  </button>
                </li>
              ))}
            </ul>
          ) : (
            <Empty text={model.data.sources.incidents === "unavailable" ? "Incident source unavailable." : "No open incidents."} />
          )}
        </div>
      </div>
    </div>
  );
}
