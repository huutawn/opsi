"use client";

import { useEffect } from "react";
import { Button, Empty, Icon } from "@/components/ui/primitives";
import { DeploymentDetail } from "@/features/delivery/deployment-detail";
import { DeliveryStatus, ServiceFilter, displayTime, short, type DeliveryViewProps } from "@/features/delivery/shared";

export function DeploymentsView({ console, data, selectedService }: DeliveryViewProps) {
  const filtered = data.deployments.filter(
    (job) =>
      (!selectedService || job.service_id === selectedService.id) &&
      (!console.route.status || (job.rollout_state || job.status) === console.route.status) &&
      (!console.route.kind || (console.route.kind === "preview") === Boolean(job.snapshot?.preview))
  );
  const selected = filtered.find((job) => job.id === console.route.deployment) ?? filtered[0];

  useEffect(() => {
    if (!console.route.deployment && selected) console.navigate({ deployment: selected.id });
  }, [console, selected]);

  return (
    <div className="space-y-6">
      {/* Toolbar */}
      <div className="flex flex-col md:flex-row items-center justify-between gap-4 bg-surface-container-low p-4 rounded-xl border border-outline-variant/20 shadow-sm">
        <div className="flex flex-wrap items-center gap-4 w-full md:w-auto">
          <ServiceFilter console={console} selected={selectedService} services={data.services} />
          <div className="relative">
            <select
              className="bg-surface-container-highest border border-outline-variant/30 text-on-surface text-xs rounded-lg py-2 pl-3 pr-8 appearance-none focus:outline-none focus:border-primary/50 cursor-pointer"
              onChange={(event) => console.navigate({ status: event.target.value, deployment: "" })}
              value={console.route.status ?? ""}
            >
              <option value="">All Rollout States</option>
              {["queued", "leased", "pulling", "applying", "waiting_ready", "succeeded", "failed", "cancelled"].map((state) => (
                <option key={state} value={state}>
                  {state.replaceAll("_", " ")}
                </option>
              ))}
            </select>
            <Icon name="expand_more" className="absolute right-2 top-1/2 -translate-y-1/2 text-on-surface-variant pointer-events-none text-[18px]" />
          </div>

          <div className="relative">
            <select
              className="bg-surface-container-highest border border-outline-variant/30 text-on-surface text-xs rounded-lg py-2 pl-3 pr-8 appearance-none focus:outline-none focus:border-primary/50 cursor-pointer"
              onChange={(event) => console.navigate({ kind: event.target.value, deployment: "" })}
              value={console.route.kind ?? ""}
            >
              <option value="">Production & Preview</option>
              <option value="production">Production</option>
              <option value="preview">Preview</option>
            </select>
            <Icon name="expand_more" className="absolute right-2 top-1/2 -translate-y-1/2 text-on-surface-variant pointer-events-none text-[18px]" />
          </div>
        </div>

        <Button
          onClick={() => console.navigate({ view: "infrastructure", tab: "topology" })}
          size="sm"
          variant="secondary"
        >
          <Icon name="account_tree" className="text-[16px]" />
          Review in Topology
        </Button>
      </div>

      {/* Deployments Master-Detail Layout */}
      <div className="grid grid-cols-1 lg:grid-cols-12 gap-6 items-start">
        {/* Left Column: Rollout List */}
        <div className="lg:col-span-5 bg-surface-container-low border border-outline-variant/20 rounded-xl p-4 flex flex-col gap-3 shadow-sm">
          <div className="flex items-center justify-between border-b border-outline-variant/20 pb-3">
            <h2 className="font-headline-md text-sm font-semibold text-on-surface">Deployments</h2>
            <span className="text-xs text-on-surface-variant">Rollout Record History</span>
          </div>

          {filtered.length ? (
            <div className="flex flex-col gap-2 max-h-[640px] overflow-y-auto">
              {filtered.map((job) => {
                const isSelected = selected?.id === job.id;
                const serviceName = data.services.find((s) => s.id === job.service_id)?.name ?? job.service_id;
                const digest = job.desired_digest || job.snapshot?.image.digest;
                const isFailed = job.status === "failed" || job.rollout_state === "failed";
                const isRunning = job.status === "succeeded" || job.rollout_state === "running" || job.rollout_state === "succeeded";

                return (
                  <div
                    key={job.id}
                    onClick={() => console.navigate({ deployment: job.id, service: job.service_id })}
                    className={`p-3.5 rounded-xl border cursor-pointer transition-all flex flex-col gap-2 ${
                      isSelected
                        ? "bg-primary-container/80 border-primary text-on-surface shadow-sm"
                        : "bg-surface-container border-outline-variant/20 hover:bg-surface-container-high"
                    }`}
                  >
                    <div className="flex items-center justify-between">
                      <div className="flex items-center gap-2 min-w-0">
                        <span
                          className={`w-2 h-2 rounded-full shrink-0 ${
                            isRunning ? "bg-status-ready" : isFailed ? "bg-status-failed" : "bg-status-progress animate-pulse"
                          }`}
                        />
                        <strong className="font-body-md text-sm text-on-surface truncate">{serviceName}</strong>
                      </div>
                      <DeliveryStatus status={job.rollout_state || job.status} />
                    </div>

                    <div className="flex items-center justify-between text-[11px] font-code-md text-on-surface-variant">
                      <span>{job.environment_id || "Production"} • {job.snapshot?.preview ? "Preview" : "Release"}</span>
                      <span>{displayTime(job.finished_at || job.created_at)}</span>
                    </div>

                    <div className="text-[10px] font-code-md text-on-surface-variant/80 truncate">
                      Digest: {short(digest, 20)}
                    </div>
                  </div>
                );
              })}
            </div>
          ) : (
            <Empty
              text="No factual DeploymentJob matches the active filter criteria."
              title="No Deployments Found"
            />
          )}
        </div>

        {/* Right Column: Deployment Detail Inspector */}
        <div className="lg:col-span-7">
          <DeploymentDetail console={console} data={data} selected={selected} />
        </div>
      </div>
    </div>
  );
}
