"use client";

import { useEffect, useRef, useState } from "react";
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
    const idempotencyKey = crypto.randomUUID();

    try {
      const result = await client.reviewRetainedStorageDestroy(projectID, storage.id, idempotencyKey);
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
    if (!review || submitting) return;

    setSubmitting(true);
    setError(null);

    const client = new LocalClient();
    const idempotencyKey = crypto.randomUUID();

    try {
      const result = await client.destroyRetainedStorage(projectID, storage.id, review.review_token, idempotencyKey);
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
      className="connectServerDialog placementDialog resourceDialog"
      onCancel={(e) => {
        e.preventDefault();
        onClose();
      }}
      ref={dialog}
    >
      <div className="dialogHeading">
        <div>
          <p className="eyebrow">Persistent Storage · Irreversible Deletion</p>
          <h2 id="destroy-storage-title">Destroy Retained Storage</h2>
          <p id="destroy-storage-desc">
            Permanently destroy persistent volume data for <strong>{storage.resource_name}</strong>.
          </p>
        </div>
        <button aria-label="Close dialog" autoFocus className="iconButton" onClick={onClose} type="button">
          <svg aria-hidden="true" viewBox="0 0 20 20">
            <path d="m5 5 10 10M15 5 5 15" />
          </svg>
        </button>
      </div>

      <div className="truthCallout warning span2" role="alert">
        <b>HIGH-FRICTION DESTRUCTIVE ACTION:</b>
        <p>
          Persistent database data will be destroyed. This operation immediately de-provisions the underlying storage
          volume (PVC: <code>{storage.pvc_name}</code>). Once destroyed, this data cannot be recovered unless you have
          a verified logical backup.
        </p>
      </div>

      {!review ? (
        <div className="destroyReviewPrompt span2">
          <div className="reviewGrid">
            <div className="reviewFact">
              <span>Original Resource</span>
              <strong>{storage.resource_name} ({storage.original_resource_id})</strong>
            </div>
            <div className="reviewFact">
              <span>PVC Name</span>
              <strong>{storage.pvc_name}</strong>
            </div>
            <div className="reviewFact">
              <span>Storage Size</span>
              <strong>{storage.actual_size || formatBytes(storage.requested_bytes)}</strong>
            </div>
            <div className="reviewFact">
              <span>Storage Class</span>
              <strong>{storage.storage_class}</strong>
            </div>
          </div>

          {error ? (
            <div className="truthCallout" role="alert">
              <b>{error.summary}</b>
              <p>{error.action}</p>
              {error.code ? <small className="errorCode">Error code: {error.code}</small> : null}
            </div>
          ) : null}

          <div className="dialogActions">
            <button disabled={reviewing} onClick={onClose} type="button">
              Cancel
            </button>
            <button className="primary destructive" disabled={reviewing} onClick={handleStartReview} type="button">
              {reviewing ? "Reviewing Storage State…" : "Review Storage for Destruction"}
            </button>
          </div>
        </div>
      ) : (
        <div className="destroyConfirmForm span2">
          <div className="reviewGrid">
            <div className="reviewFact">
              <span>Active Resource Check</span>
              <strong className={review.active_resource ? "statusFail" : "statusPass"}>
                {review.active_resource ? "ACTIVE RESOURCE FOUND" : "NO ACTIVE RUNTIME"}
              </strong>
            </div>
            <div className="reviewFact">
              <span>Active Binding Check</span>
              <strong className={review.active_binding ? "statusFail" : "statusPass"}>
                {review.active_binding ? "ACTIVE BINDINGS FOUND" : "NO ACTIVE BINDINGS"}
              </strong>
            </div>
          </div>

          {review.warning ? (
            <div className="truthCallout warning" role="alert">
              <b>Review Warning:</b>
              <p>{review.warning}</p>
            </div>
          ) : null}

          <label className="destroyConfirmationField">
            Type the resource name <strong>{storage.resource_name}</strong> to confirm permanent data destruction:
            <input
              autoComplete="off"
              className="field"
              onChange={(e) => setConfirmName(e.target.value)}
              placeholder={storage.resource_name}
              value={confirmName}
            />
          </label>

          {error ? (
            <div className="truthCallout" role="alert">
              <b>{error.summary}</b>
              <p>{error.action}</p>
              {error.code ? <small className="errorCode">Error code: {error.code}</small> : null}
            </div>
          ) : null}

          <div className="dialogActions">
            <button disabled={submitting} onClick={onClose} type="button">
              Cancel
            </button>
            <button
              className="primary destructive"
              disabled={
                submitting ||
                confirmName !== storage.resource_name ||
                review.active_resource ||
                review.active_binding
              }
              onClick={handleConfirmDestroy}
              type="button"
            >
              {submitting ? "Destroying Storage…" : "Permanently Destroy Retained Storage"}
            </button>
          </div>
        </div>
      )}
    </dialog>
  );
}
