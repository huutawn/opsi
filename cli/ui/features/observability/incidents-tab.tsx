"use client";

import { Button, Empty, Icon, StatusBadge } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";
import { safeLogMessage } from "@/features/observability/data";
import type { ObservabilityModel } from "@/features/observability/observability-view";
import { Fact, formatObserved } from "@/features/observability/shared";

export function IncidentsTab({ console, model }: { console: ConsoleController; model: ObservabilityModel }) {
  const selectedID = console.route.incident || "";
  const selected = model.data.selectedIncident;
  const evidence = model.data.evidence;

  return (
    <div className="grid grid-cols-1 lg:grid-cols-12 gap-6 items-start" data-testid="observability-incidents">
      {/* Master List */}
      <section className="lg:col-span-4 bg-surface-container-low border border-outline-variant/20 rounded-2xl p-4 shadow-sm space-y-4">
        <div className="flex items-center justify-between pb-3 border-b border-outline-variant/20">
          <div>
            <p className="font-label-sm text-xs text-primary uppercase tracking-wider">Factual Inventory</p>
            <h2 className="font-headline-md text-base font-bold text-on-surface">Incidents</h2>
          </div>
          <span className="bg-surface-container px-2.5 py-0.5 rounded-full font-code-md text-xs text-on-surface-variant font-bold">
            {model.data.incidents.length}
          </span>
        </div>

        {model.data.sources.incidents === "unavailable" ? (
          <div className="p-3 bg-status-warning/10 border border-status-warning/30 rounded-xl text-status-warning text-xs">
            Incident source unavailable. No empty count is inferred.
          </div>
        ) : null}

        {model.data.incidents.length ? (
          <ul className="space-y-2 max-h-[600px] overflow-y-auto">
            {model.data.incidents.map((incident) => {
              const isSelected = selectedID === incident.incident_id;
              return (
                <li key={incident.incident_id}>
                  <button
                    aria-pressed={isSelected}
                    className={`w-full p-3 rounded-xl border text-left transition-all cursor-pointer flex items-start justify-between gap-2 ${
                      isSelected
                        ? "bg-primary-container/80 border-primary shadow-sm"
                        : "bg-surface-container border-outline-variant/20 hover:bg-surface-container-high"
                    }`}
                    onClick={() => console.navigate({ incident: incident.incident_id })}
                    type="button"
                  >
                    <div className="space-y-1 min-w-0">
                      <strong className="text-xs font-bold text-on-surface font-code-md block truncate">
                        {incident.incident_id}
                      </strong>
                      <small className="text-[11px] text-on-surface-variant block truncate">
                        {incident.service_id || "Service not reported"} · {incident.node_id || "Node not reported"}
                      </small>
                      <small className="text-[10px] text-on-surface-variant/70 font-code-md block">
                        {formatObserved(incident.created_at_unix)}
                      </small>
                    </div>
                    <StatusBadge value={incident.severity || incident.status} />
                  </button>
                </li>
              );
            })}
          </ul>
        ) : (
          <Empty text="No incident records were returned." />
        )}
      </section>

      {/* Detail Panel */}
      <section className="lg:col-span-8 bg-surface-container-low border border-outline-variant/20 rounded-2xl p-6 shadow-sm space-y-6">
        {selected ? (
          <IncidentDetail console={console} model={model} incident={selected} evidence={evidence} />
        ) : (
          <Empty title="Select an incident" text="Choose an inventory record to inspect evidence and next action." />
        )}
      </section>
    </div>
  );
}

function IncidentDetail({
  console,
  model,
  incident,
  evidence,
}: {
  console: ConsoleController;
  model: ObservabilityModel;
  incident: NonNullable<ObservabilityModel["data"]["selectedIncident"]>;
  evidence: ObservabilityModel["data"]["evidence"];
}) {
  const identity = `${incident.service_id ? ` --service-id ${incident.service_id}` : ""}${incident.node_id ? ` --node-id ${incident.node_id}` : ""}`;

  return (
    <div className="space-y-6">
      <div className="flex items-start justify-between gap-4 pb-4 border-b border-outline-variant/20">
        <div>
          <span className="font-label-sm text-xs text-primary uppercase tracking-wider block mb-1">Incident Detail</span>
          <h2 className="font-headline-md text-xl font-bold text-on-surface font-code-md">{incident.incident_id}</h2>
          <p className="text-xs text-on-surface-variant mt-1 font-code-md">
            {incident.service_id || "Service not reported"} · {incident.node_id || "Node not reported"} · {incident.pod_id || "Pod not reported"}
          </p>
        </div>
        <StatusBadge value={incident.severity || incident.status} />
      </div>

      <div className="grid grid-cols-2 sm:grid-cols-4 gap-3 bg-surface-container p-4 rounded-2xl border border-outline-variant/15 text-xs">
        <Fact label="Status" value={incident.status} />
        <Fact label="Started" value={formatObserved(incident.created_at_unix)} />
        <Fact label="Anomaly" value={incident.anomaly_type || "Not reported"} />
        <Fact label="MTTR" value={incident.mttr_seconds === undefined ? "Not reported" : `${incident.mttr_seconds}s`} />
      </div>

      {model.data.evidenceState === "loading" ? (
        <Empty title="Loading evidence…" text="Reading bounded Agent evidence." />
      ) : model.data.evidenceState === "unavailable" ? (
        <div className="p-4 bg-status-warning/10 border border-status-warning/30 rounded-xl text-status-warning text-xs space-y-1">
          <b>Evidence unavailable</b>
          <p className="text-on-surface-variant text-[11px]">The UI fails closed and does not infer completeness. {model.data.evidenceError}</p>
        </div>
      ) : evidence ? (
        <EvidencePanel evidence={evidence} />
      ) : (
        <Empty text="No evidence returned." />
      )}

      {/* Next Actions */}
      <section className="bg-surface-container p-5 rounded-2xl border border-outline-variant/15 space-y-4">
        <div>
          <h3 className="font-headline-md text-sm font-bold text-on-surface">Next Action</h3>
          <p className="text-xs text-on-surface-variant mt-0.5">
            Investigation can proceed with logs or deployments. Approval and execution use CLI.
          </p>
        </div>

        <div className="flex flex-wrap gap-2">
          <Button
            onClick={() => console.navigate({ tab: "logs", service: incident.service_id || "" })}
            size="sm"
            variant="secondary"
          >
            View Logs
          </Button>
          <Button
            onClick={() => console.navigate({ view: "delivery", tab: "deployments", service: incident.service_id || "" })}
            size="sm"
            variant="secondary"
          >
            View Deployments
          </Button>
        </div>

        <div className="bg-surface-container-lowest p-3.5 rounded-xl border border-outline-variant/20 font-code-md text-xs text-on-surface space-y-1.5">
          <span className="font-bold text-primary block text-[11px] uppercase">Continue in CLI</span>
          <code className="block text-on-surface bg-surface-container/60 p-2 rounded border border-outline-variant/10 select-all">
            opsi action preflight --project-id {incident.project_id} --kind incident_resolve --incident-id {incident.incident_id}{identity}
          </code>
          <code className="block text-on-surface bg-surface-container/60 p-2 rounded border border-outline-variant/10 select-all">
            opsi action approve &lt;challenge-id&gt; --project-id {incident.project_id} --device-id &lt;device-id&gt;
          </code>
          <code className="block text-on-surface bg-surface-container/60 p-2 rounded border border-outline-variant/10 select-all">
            opsi action execute &lt;challenge-id&gt; --project-id {incident.project_id}
          </code>
        </div>
      </section>
    </div>
  );
}

function EvidencePanel({ evidence }: { evidence: NonNullable<ObservabilityModel["data"]["evidence"]> }) {
  const partial = evidence.coverage.some((item) => item.status !== "available" || item.truncated) || Boolean(evidence.truncations?.length);

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between p-3 bg-surface-container rounded-xl border border-outline-variant/15 text-xs">
        <div className="flex items-center gap-2">
          <StatusBadge value={partial ? "degraded" : "healthy"} />
          <span className="font-semibold">{partial ? "Partial evidence" : "Evidence complete"}</span>
        </div>
        <code className="font-code-md text-[11px] text-on-surface-variant truncate max-w-[200px]">{evidence.content_sha256}</code>
      </div>

      <div className="grid grid-cols-2 sm:grid-cols-3 gap-3 bg-surface-container/60 p-4 rounded-2xl border border-outline-variant/15 text-xs">
        <Fact label="Window start" value={formatObserved(evidence.observation_window.start_unix)} />
        <Fact label="Window end" value={formatObserved(evidence.observation_window.end_unix)} />
        <Fact label="Desired digest" value={evidence.deployment.desired_digest || "Not reported"} />
        <Fact label="Observed digest" value={evidence.deployment.observed_digest || "Not reported"} />
        <Fact label="Rollout" value={evidence.rollout.state || "Not reported"} />
        <Fact label="Readiness hash" value={evidence.rollout.readiness_hash || "Not reported"} />
      </div>
    </div>
  );
}
