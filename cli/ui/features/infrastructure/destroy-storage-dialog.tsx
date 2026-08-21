"use client";

import { useEffect, useRef, useState } from "react";
import { Button, Icon } from "@/components/ui/primitives";
import { LocalAPIError, LocalClient } from "@/lib/api/local-client";
import type { RetainedStorage, RetainedStorageReview } from "@/lib/contracts/registry";
import { formatBytes, resourceErrorExplanation } from "@/lib/presentation/resources/model";

export function DestroyStorageDialog({
  onClose,
  onDestroyed,
  projectID,
  storage,
}: {
  onClose: () => void;
  onDestroyed: (storage: RetainedStorage) => Promise<void>;
  projectID: string;
  storage: RetainedStorage;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [review, setReview] = useState<RetainedStorageReview | null>(null);
  const [reviewing, setReviewing] = useState(false);
  const [confirmName, setConfirmName] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<{ summary: string; action: string; code?: string } | null>(null);

  useEffect(() => {
    const el = dialog.current;
    el?.showModal();
    return () => {
      if (el?.open) el.close();
    };
  }, []);

  async function handleStartReview() {
    setReviewing(true);
    setError(null);

    const client = new LocalClient();
    try {
      const result = await client.reviewRetainedStorageDestroy(projectID, storage.id, crypto.randomUUID());
      setReview(result.review);
    } catch (cause) {
      const apiErr = cause as LocalAPIError;
      const explanation = resourceErrorExplanation(apiErr.code, apiErr.message);
      setError({ ...explanation, code: apiErr.code });
    } finally {
      setReviewing(false);
    }
  }

  async function handleConfirmDestroy() {
    if (!review || confirmName !== storage.resource_name || submitting) return;

    setSubmitting(true);
    setError(null);

    const client = new LocalClient();
    try {
      const result = await client.destroyRetainedStorage(projectID, storage.id, review.review_token, crypto.randomUUID());
      await onDestroyed(result.retained_storage);
      onClose();
    } catch (cause) {
      const apiErr = cause as LocalAPIError;
      const explanation = resourceErrorExplanation(apiErr.code, apiErr.message);
      setError({ ...explanation, code: apiErr.code });
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <dialog
      aria-describedby="destroy-storage-desc"
      aria-labelledby="destroy-storage-title"
      className="fixed inset-0 m-auto bg-surface-container-low border border-error/30 rounded-2xl shadow-2xl p-6 max-w-xl w-full backdrop:bg-background/80 backdrop:backdrop-blur-sm z-50 text-on-surface flex flex-col gap-5 max-h-[90vh] overflow-y-auto"
      onCancel={(e) => {
        e.preventDefault();
        onClose();
      }}
      ref={dialog}
    >
      <div className="flex items-start justify-between border-b border-outline-variant/20 pb-4">
        <div>
          <span className="font-label-sm text-xs text-error font-bold uppercase tracking-wider block mb-1">
            Persistent Storage · Irreversible Deletion
          </span>
          <h2 id="destroy-storage-title" className="font-headline-md text-xl font-bold text-on-surface">
            Destroy Retained Storage
          </h2>
          <p id="destroy-storage-desc" className="font-body-md text-xs text-on-surface-variant mt-1">
            Permanently destroy persistent volume data for <strong className="text-on-surface">{storage.resource_name}</strong>.
          </p>
        </div>
        <button
          aria-label="Close dialog"
          autoFocus
          className="p-1.5 text-on-surface-variant hover:text-on-surface hover:bg-surface-container-highest rounded-lg transition-colors cursor-pointer"
          onClick={onClose}
          type="button"
        >
          <Icon name="close" className="text-[20px]" />
        </button>
      </div>

      <div className="p-4 bg-error-container/20 border border-error/30 rounded-xl space-y-1.5" role="alert">
        <strong className="text-xs text-error uppercase font-bold tracking-wider block">High-Friction Destructive Action</strong>
        <p className="text-xs text-on-surface-variant leading-relaxed">
          Persistent database data will be destroyed immediately (PVC: <code className="text-error font-mono">{storage.pvc_name}</code>). Once destroyed, this data cannot be recovered without a verified backup.
        </p>
      </div>

      {!review ? (
        <div className="space-y-4">
          <div className="grid grid-cols-2 gap-3 bg-surface-container/60 p-4 rounded-xl border border-outline-variant/15 text-xs">
            <div>
              <span className="text-on-surface-variant block mb-0.5">Original Resource</span>
              <strong className="text-on-surface block truncate">{storage.resource_name}</strong>
            </div>
            <div>
              <span className="text-on-surface-variant block mb-0.5">PVC Name</span>
              <strong className="text-on-surface block font-mono text-[11px] truncate">{storage.pvc_name}</strong>
            </div>
            <div>
              <span className="text-on-surface-variant block mb-0.5">Storage Size</span>
              <strong className="text-on-surface block">{storage.actual_size || formatBytes(storage.requested_bytes)}</strong>
            </div>
            <div>
              <span className="text-on-surface-variant block mb-0.5">Storage Class</span>
              <strong className="text-on-surface block font-mono text-[11px]">{storage.storage_class}</strong>
            </div>
          </div>

          {error ? (
            <div className="p-3 bg-error-container/20 text-error border border-error/30 rounded-xl text-xs space-y-1" role="alert">
              <strong className="block font-semibold">{error.summary}</strong>
              <p>{error.action}</p>
              {error.code ? <small className="font-mono text-[10px]">Error code: {error.code}</small> : null}
            </div>
          ) : null}

          <div className="flex items-center justify-end gap-3 pt-3 border-t border-outline-variant/20">
            <Button disabled={reviewing} onClick={onClose} size="sm" type="button" variant="outline">
              Cancel
            </Button>
            <Button disabled={reviewing} onClick={handleStartReview} size="sm" type="button" variant="danger">
              {reviewing ? "Reviewing Storage State…" : "Review Storage for Destruction"}
            </Button>
          </div>
        </div>
      ) : (
        <div className="space-y-4">
          <div className="grid grid-cols-2 gap-3 bg-surface-container/60 p-4 rounded-xl border border-outline-variant/15 text-xs">
            <div>
              <span className="text-on-surface-variant block mb-0.5">Active Resource Check</span>
              <strong className={review.active_resource ? "text-error font-bold" : "text-status-ready font-bold"}>
                {review.active_resource ? "ACTIVE RESOURCE FOUND" : "NO ACTIVE RUNTIME"}
              </strong>
            </div>
            <div>
              <span className="text-on-surface-variant block mb-0.5">Active Binding Check</span>
              <strong className={review.active_binding ? "text-error font-bold" : "text-status-ready font-bold"}>
                {review.active_binding ? "ACTIVE BINDINGS FOUND" : "NO ACTIVE BINDINGS"}
              </strong>
            </div>
          </div>

          {review.warning ? (
            <div className="p-3 bg-status-warning/10 border border-status-warning/30 rounded-xl text-xs text-status-warning" role="alert">
              <strong className="block font-semibold">Review Warning:</strong>
              <p>{review.warning}</p>
            </div>
          ) : null}

          <div className="space-y-1.5">
            <label className="text-xs font-label-sm text-on-surface-variant block">
              Type the resource name <strong className="text-on-surface font-bold">{storage.resource_name}</strong> to confirm permanent deletion:
            </label>
            <input
              autoComplete="off"
              className="field border-error/50 focus:border-error"
              onChange={(e) => setConfirmName(e.target.value)}
              placeholder={storage.resource_name}
              value={confirmName}
            />
          </div>

          {error ? (
            <div className="p-3 bg-error-container/20 text-error border border-error/30 rounded-xl text-xs space-y-1" role="alert">
              <strong className="block font-semibold">{error.summary}</strong>
              <p>{error.action}</p>
              {error.code ? <small className="font-mono text-[10px]">Error code: {error.code}</small> : null}
            </div>
          ) : null}

          <div className="flex items-center justify-end gap-3 pt-3 border-t border-outline-variant/20">
            <Button disabled={submitting} onClick={onClose} size="sm" type="button" variant="outline">
              Cancel
            </Button>
            <Button
              disabled={
                submitting ||
                confirmName !== storage.resource_name ||
                review.active_resource ||
                review.active_binding
              }
              onClick={handleConfirmDestroy}
              size="sm"
              type="button"
              variant="danger"
            >
              {submitting ? "Destroying Storage…" : "Permanently Destroy Retained Storage"}
            </Button>
          </div>
        </div>
      )}
    </dialog>
  );
}
