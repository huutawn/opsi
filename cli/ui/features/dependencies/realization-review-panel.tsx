"use client";

import { useEffect, useMemo, useState } from "react";
import { Button, Icon, StatusBadge } from "@/components/ui/primitives";
import { LocalAPIError, LocalClient } from "@/lib/api/local-client";
import type { DependencyReviewResult, ServiceRecord } from "@/lib/contracts/registry";
import { formatSymbolicSource } from "./types";

type Props = {
  consumer: ServiceRecord;
  onApplied: () => Promise<void>;
  onClose: () => void;
  projectID: string;
};

export function RealizationReviewDialog({
  consumer,
  onApplied,
  onClose,
  projectID,
}: Props) {
  const client = useMemo(() => new LocalClient(), []);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [plan, setPlan] = useState<DependencyReviewResult | null>(null);
  const [error, setError] = useState<{ code?: string; message: string; nextAction?: string } | null>(null);
  const [appliedMessage, setAppliedMessage] = useState("");

  useEffect(() => {
    let active = true;
    async function loadReview() {
      setLoading(true);
      setError(null);
      try {
        const res = await client.dependenciesReview(projectID, consumer.id);
        if (active) setPlan(res);
      } catch (cause) {
        if (!active) return;
        const apiErr = cause as LocalAPIError;
        setError({
          code: apiErr.code || "REALIZATION_REVIEW_FAILED",
          message: apiErr.message || "Failed to review dependency realization plan.",
          nextAction: apiErr.nextAction,
        });
      } finally {
        if (active) setLoading(false);
      }
    }
    void loadReview();
    return () => {
      active = false;
    };
  }, [client, consumer.id, projectID]);

  async function handleApply() {
    setBusy(true);
    setError(null);
    const idempotencyKey = crypto.randomUUID();
    try {
      const res = await client.dependenciesApply(projectID, consumer.id, idempotencyKey);
      setAppliedMessage(`Dependency connection realized successfully (${res.realized} binding${res.realized === 1 ? "" : "s"}).`);
      await onApplied();
      setTimeout(() => {
        onClose();
      }, 1200);
    } catch (cause) {
      const apiErr = cause as LocalAPIError;
      setError({
        code: apiErr.code || "REALIZATION_APPLY_FAILED",
        message: apiErr.message || "Failed to apply dependency realization.",
        nextAction: apiErr.nextAction,
      });
    } finally {
      setBusy(false);
    }
  }

  const hasConflicts = Boolean(
    plan?.dependencies.some((d) => d.projections.some((p) => p.conflict))
  );
  const hasMigration = Boolean(
    plan?.dependencies.some((d) => d.binding_action === "migration_required")
  );

  return (
    <dialog
      aria-labelledby="realize-dialog-title"
      className="fixed inset-0 m-auto bg-surface-container-low border border-outline-variant/30 rounded-2xl shadow-2xl p-6 max-w-xl w-full backdrop:bg-background/80 backdrop:backdrop-blur-sm z-50 text-on-surface flex flex-col gap-5 max-h-[90vh] overflow-y-auto"
      open
    >
      <div className="flex items-start justify-between border-b border-outline-variant/20 pb-4">
        <div>
          <span className="font-label-sm text-xs text-primary font-bold uppercase tracking-wider block mb-1">
            Zero-Mutation Realization Review
          </span>
          <h2 id="realize-dialog-title" className="font-headline-md text-xl font-bold text-on-surface">
            Review Dependency Realization
          </h2>
          <p className="font-body-md text-xs text-on-surface-variant mt-0.5">
            Consumer: <strong className="text-on-surface">{consumer.name}</strong> ({consumer.id})
          </p>
        </div>
        <button
          aria-label="Close dialog"
          className="p-1.5 text-on-surface-variant hover:text-on-surface hover:bg-surface-container-highest rounded-lg transition-colors cursor-pointer"
          onClick={onClose}
          type="button"
        >
          <Icon name="close" className="text-[20px]" />
        </button>
      </div>

      {appliedMessage ? (
        <div className="p-4 bg-status-ready/10 border border-status-ready/30 rounded-xl text-xs text-status-ready flex items-center gap-2" role="status">
          <Icon name="check_circle" className="text-[18px]" />
          <span>{appliedMessage}</span>
        </div>
      ) : null}

      {error ? (
        <div className="p-4 bg-error-container/20 border border-status-failed/30 rounded-xl text-xs space-y-1 text-on-surface" role="alert">
          <div className="flex items-center gap-2 text-status-failed font-bold">
            <Icon name="error" className="text-[16px]" />
            <span>{error.code || "Realization Error"}</span>
          </div>
          <p>{error.message}</p>
          {error.nextAction ? <p className="text-on-surface-variant text-[11px] mt-1">{error.nextAction}</p> : null}
        </div>
      ) : null}

      {loading ? (
        <div className="py-8 text-center text-xs text-on-surface-variant flex items-center justify-center gap-2">
          <Icon name="sync" className="animate-spin text-[18px]" />
          <span>Evaluating dependency realization plan…</span>
        </div>
      ) : plan ? (
        <div className="space-y-4 text-xs">
          <p className="text-on-surface-variant">
            Cloud evaluated the declared dependency contract against active managed resource bindings. No runtime resources have been mutated yet.
          </p>

          <div className="space-y-3">
            {plan.dependencies.map((item, idx) => (
              <div
                key={idx}
                className="bg-surface-container p-4 rounded-xl border border-outline-variant/20 space-y-2.5"
              >
                <div className="flex items-center justify-between">
                  <div>
                    <strong className="font-headline-md text-sm text-on-surface block">
                      {item.logical_name}
                    </strong>
                    <span className="text-[11px] text-on-surface-variant font-code-md">
                      Target: {item.target_display_name || item.target_identity} ({item.protocol})
                    </span>
                  </div>
                  <StatusBadge
                    label={
                      item.binding_action === "create"
                        ? "Create Binding"
                        : item.binding_action === "reuse"
                        ? "Reuse Binding"
                        : item.binding_action === "migration_required"
                        ? "Migration Required"
                        : "Ready"
                    }
                    value={
                      item.binding_action === "migration_required"
                        ? "failed"
                        : item.binding_action === "create"
                        ? "in_progress"
                        : "healthy"
                    }
                  />
                </div>

                {item.binding_action === "reuse" ? (
                  <p className="text-[11px] text-primary bg-primary-container/40 p-2 rounded-lg border border-primary/20">
                    Existing ResourceBinding <code>{item.existing_binding_id}</code> will be reused. No new credentials will be provisioned.
                  </p>
                ) : null}

                {item.binding_action === "migration_required" ? (
                  <div className="p-3 bg-error-container/20 rounded-lg border border-error/30 text-error space-y-1">
                    <strong>EXPLICIT_MIGRATION_REQUIRED</strong>
                    <p className="text-[11px]">
                      Target managed database changed. Live data migration and application cutover must be executed explicitly.
                    </p>
                  </div>
                ) : null}

                {/* Safe Environment Projections */}
                <div className="space-y-1.5 pt-1">
                  <span className="font-label-sm text-[11px] text-on-surface-variant uppercase font-semibold block">
                    Projected Injections (Safe Symbolic Blueprint)
                  </span>
                  <ul className="space-y-1 bg-surface-container-highest p-2.5 rounded-lg font-code-md text-[11px]">
                    {item.projections.map((proj, pIdx) => (
                      <li key={pIdx} className="flex items-center justify-between">
                        <span className="text-on-surface font-bold">{proj.env_name}</span>
                        <span className="text-on-surface-variant">← {formatSymbolicSource(proj.symbolic_source, item.protocol)}</span>
                      </li>
                    ))}
                  </ul>
                </div>
              </div>
            ))}
          </div>

          <div className="flex items-center justify-between pt-3 border-t border-outline-variant/20">
            <Button onClick={onClose} variant="secondary" type="button">
              Cancel
            </Button>
            <Button
              disabled={busy || Boolean(appliedMessage) || hasConflicts || hasMigration}
              onClick={handleApply}
              variant="primary"
              type="button"
            >
              {busy ? "Applying…" : "Apply Realization & Bind"}
            </Button>
          </div>
        </div>
      ) : null}
    </dialog>
  );
}
