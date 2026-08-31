"use client";

import { useEffect, useRef } from "react";
import { Badge, Button, Icon } from "@/components/ui/primitives";
import type { ResourceRecommendation } from "@/lib/contracts/registry";

type Props = {
  recommendation: ResourceRecommendation | null;
  loading?: boolean;
  error?: string | null;
  applying?: boolean;
  onApply: (rec: ResourceRecommendation) => void | Promise<void>;
  onClose: () => void;
};

export function ResourceProposalDialog({
  recommendation,
  loading = false,
  error = null,
  applying = false,
  onApply,
  onClose,
}: Props) {
  const dialogRef = useRef<HTMLDialogElement>(null);

  useEffect(() => {
    const dialog = dialogRef.current;
    if (dialog && !dialog.open) {
      dialog.showModal();
    }
    return () => {
      if (dialog?.open) dialog.close();
    };
  }, []);
  const eligible = Boolean(recommendation?.eligible && recommendation.applications.length > 0);
  const projection = recommendation?.budget_projection || {
    real_capacity: { cpu_millicores: recommendation?.target_capacity?.cpu_millicores || 0, memory_bytes: recommendation?.target_capacity?.memory_bytes || 0 },
    system_reserve: { cpu_millicores: 250, memory_bytes: 256 << 20 },
    existing_workloads: { cpu_millicores: 0, memory_bytes: 0 },
    planned_managed: { cpu_millicores: 0, memory_bytes: 0 },
    available_for_run: { cpu_millicores: recommendation?.target_capacity?.cpu_millicores || 0, memory_bytes: recommendation?.target_capacity?.memory_bytes || 0 },
    remaining_after_proposal: { cpu_millicores: 0, memory_bytes: 0 },
  };

  return (
    <dialog
      aria-label="Resource allocation proposal"
      aria-labelledby="resource-proposal-title"
      className="fixed inset-0 z-50 m-auto max-h-[90vh] w-[calc(100vw-2rem)] max-w-3xl overflow-y-auto rounded-2xl border border-outline-variant/30 bg-surface p-6 shadow-2xl backdrop:bg-scrim/60"
      onCancel={(event) => {
        event.preventDefault();
        onClose();
      }}
      onClose={onClose}
      ref={dialogRef}
    >
      <div className="flex flex-col gap-5">
        <div className="flex items-start justify-between gap-4">
          <div>
            <div className="flex items-center gap-2">
              <span className="flex h-8 w-8 items-center justify-center rounded-lg bg-primary/10 text-primary">
                <Icon className="text-xl" name="tune" />
              </span>
              <h2 className="text-lg font-semibold text-on-surface" id="resource-proposal-title">
                Resource allocation proposal
              </h2>
            </div>
            <p className="mt-1.5 text-xs text-on-surface-variant leading-relaxed">
              Opsi inspected the connected server&apos;s verified capacity and calculated safe CPU and RAM requests with bounded burst limits.
            </p>
          </div>
          <button
            aria-label="Close dialog"
            className="rounded-lg p-1.5 text-on-surface-variant hover:bg-surface-container-high focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary"
            onClick={onClose}
            type="button"
          >
            <Icon name="close" />
          </button>
        </div>

        {loading && (
          <div className="flex flex-col items-center justify-center gap-3 py-12 text-on-surface-variant" role="status">
            <Icon className="animate-spin text-2xl text-primary" name="sync" />
            <p className="text-sm">Calculating resource allocation proposal from real capacity…</p>
          </div>
        )}

        {error && !loading && (
          <div className="rounded-xl border border-status-failed/40 bg-error-container/15 p-4 text-xs text-error" role="alert">
            <div className="flex items-center gap-2 font-medium">
              <Icon name="error" />
              <span>Failed to generate resource recommendation</span>
            </div>
            <p className="mt-1">{error}</p>
          </div>
        )}

        {recommendation && !loading && (
          <div className="space-y-5">
            {/* Target Capacity Overview */}
            <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
              <div className="rounded-xl border border-outline-variant/20 bg-surface-container-low p-3">
                <p className="text-[11px] font-medium uppercase tracking-wider text-on-surface-variant">Real capacity</p>
                <p className="mt-1 font-mono text-sm font-semibold text-on-surface">
                  {projection.real_capacity.cpu_millicores}m CPU
                </p>
                <p className="font-mono text-xs text-on-surface-variant">
                  {formatBytes(projection.real_capacity.memory_bytes)} RAM
                </p>
              </div>

              <div className="rounded-xl border border-outline-variant/20 bg-surface-container-low p-3">
                <p className="text-[11px] font-medium uppercase tracking-wider text-on-surface-variant">System reserve</p>
                <p className="mt-1 font-mono text-sm font-semibold text-on-surface">
                  {projection.system_reserve.cpu_millicores}m CPU
                </p>
                <p className="font-mono text-xs text-on-surface-variant">
                  {formatBytes(projection.system_reserve.memory_bytes)} RAM
                </p>
              </div>

              <div className="rounded-xl border border-outline-variant/20 bg-surface-container-low p-3">
                <p className="text-[11px] font-medium uppercase tracking-wider text-on-surface-variant">
                  {projection.existing_workloads.cpu_millicores > 0 ? "Existing & managed" : "Managed services"}
                </p>
                <p className="mt-1 font-mono text-sm font-semibold text-on-surface">
                  {projection.existing_workloads.cpu_millicores + projection.planned_managed.cpu_millicores}m CPU
                </p>
                <p className="font-mono text-xs text-on-surface-variant">
                  {formatBytes(projection.existing_workloads.memory_bytes + projection.planned_managed.memory_bytes)} RAM
                </p>
              </div>

              <div className="rounded-xl border border-outline-variant/20 bg-surface-container-low p-3">
                <p className="text-[11px] font-medium uppercase tracking-wider text-on-surface-variant">Available for apps</p>
                <p className="mt-1 font-mono text-sm font-semibold text-primary">
                  {projection.available_for_run.cpu_millicores}m CPU
                </p>
                <p className="font-mono text-xs text-on-surface-variant">
                  {formatBytes(projection.available_for_run.memory_bytes)} RAM
                </p>
              </div>
            </div>
            {!recommendation.eligible && (
              <div className="rounded-xl border border-status-warning/40 bg-status-warning/10 p-4 text-xs text-on-surface" role="status">
                <div className="flex items-center gap-2 font-medium text-status-warning">
                  <Icon name="warning" />
                  <span>Recommendation unavailable</span>
                </div>
                <p className="mt-1 text-on-surface-variant">{recommendation.reason || "The connected server does not meet capacity prerequisites."}</p>
              </div>
            )}

            {recommendation.warnings && recommendation.warnings.length > 0 && (
              <div className="space-y-1.5" role="status">
                {recommendation.warnings.map((warning, index) => (
                  <div className="flex items-center gap-2 rounded-lg border border-status-warning/30 bg-status-warning/5 px-3 py-2 text-xs text-on-surface-variant" key={index}>
                    <Icon className="text-status-warning" name="info" />
                    <span>{warning}</span>
                  </div>
                ))}
              </div>
            )}

            {/* Applications List with Before & After */}
            {recommendation.applications.length > 0 && (
              <div className="space-y-3">
                <div className="flex items-center justify-between">
                  <h3 className="text-xs font-bold uppercase tracking-wider text-on-surface-variant">
                    Proposed application allocations
                  </h3>
                  <Badge>
                    {recommendation.applications.length} workload{recommendation.applications.length === 1 ? "" : "s"}
                  </Badge>
                </div>

                <div className="divide-y divide-outline-variant/20 rounded-xl border border-outline-variant/20 bg-surface-container-lowest">
                  {recommendation.applications.map((app) => (
                    <article className="p-4" key={app.key}>
                      <div className="flex flex-wrap items-center justify-between gap-2">
                        <div className="flex items-center gap-2">
                          <span className="font-semibold text-sm text-on-surface">{app.name}</span>
                          <span className="font-mono text-xs text-on-surface-variant"><code>{app.key}</code></span>
                        </div>
                        <span className="rounded-md bg-surface-container-high px-2 py-0.5 font-mono text-[11px] text-on-surface-variant">
                          {app.replicas} replica{app.replicas === 1 ? "" : "s"}
                        </span>
                      </div>

                      <div className="mt-3 grid gap-3 sm:grid-cols-2">
                        {/* CPU */}
                        <div className="rounded-lg border border-outline-variant/15 bg-surface-container-low p-3">
                          <div className="flex items-center justify-between text-xs font-medium text-on-surface-variant">
                            <span>CPU request & limit</span>
                            <span className="text-[11px] text-primary">Burst enabled</span>
                          </div>
                          <div className="mt-2 flex items-center justify-between font-mono text-xs">
                            <div className="text-on-surface-variant">
                              <span className="text-[10px] block uppercase text-on-surface-variant/70">Current</span>
                              {app.current.cpu_request_milli}m / {app.current.cpu_limit_milli}m
                            </div>
                            <Icon className="text-primary text-sm" name="arrow_forward" />
                            <div className="font-semibold text-on-surface text-right">
                              <span className="text-[10px] block uppercase text-primary">Proposed</span>
                              {app.proposed.cpu_request_milli}m / {app.proposed.cpu_limit_milli}m
                            </div>
                          </div>
                        </div>

                        {/* Memory */}
                        <div className="rounded-lg border border-outline-variant/15 bg-surface-container-low p-3">
                          <div className="flex items-center justify-between text-xs font-medium text-on-surface-variant">
                            <span>RAM request & limit</span>
                            <span className="text-[11px] text-primary">Bounded limit</span>
                          </div>
                          <div className="mt-2 flex items-center justify-between font-mono text-xs">
                            <div className="text-on-surface-variant">
                              <span className="text-[10px] block uppercase text-on-surface-variant/70">Current</span>
                              {formatBytes(app.current.memory_request_bytes)} / {formatBytes(app.current.memory_limit_bytes)}
                            </div>
                            <Icon className="text-primary text-sm" name="arrow_forward" />
                            <div className="font-semibold text-on-surface text-right">
                              <span className="text-[10px] block uppercase text-primary">Proposed</span>
                              {formatBytes(app.proposed.memory_request_bytes)} / {formatBytes(app.proposed.memory_limit_bytes)}
                            </div>
                          </div>
                        </div>
                      </div>
                    </article>
                  ))}
                </div>
              </div>
            )}
            {/* Remaining Headroom */}
            <div className="flex items-center justify-between rounded-xl border border-outline-variant/20 bg-surface-container-low px-4 py-3 text-xs text-on-surface-variant">
              <span>Remaining capacity after proposed allocation:</span>
              <span className="font-mono font-medium text-on-surface">
                {projection.remaining_after_proposal.cpu_millicores}m CPU · {formatBytes(projection.remaining_after_proposal.memory_bytes)} RAM
              </span>
            </div>
          </div>
        )}

        <div className="flex items-center justify-end gap-3 border-t border-outline-variant/20 pt-4">
          <Button onClick={onClose} type="button" variant="outline">
            Close
          </Button>
          <Button
            disabled={!eligible || applying || loading}
            onClick={() => {
              if (recommendation) {
                void onApply(recommendation);
              }
            }}
            type="button"
            variant="primary"
          >
            {applying ? "Applying to draft…" : "Apply to draft"}
          </Button>
        </div>
      </div>
    </dialog>
  );
}

function formatBytes(bytes: number) {
  if (!bytes) return "0 MiB";
  const mib = Math.round(bytes / (1024 * 1024));
  if (mib >= 1024 && mib % 1024 === 0) {
    return `${mib / 1024} GiB`;
  }
  return `${mib} MiB`;
}
