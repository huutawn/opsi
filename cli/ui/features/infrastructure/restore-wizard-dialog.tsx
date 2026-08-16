"use client";

import { useEffect, useRef, useState } from "react";
import { LocalAPIError, LocalClient } from "@/lib/api/local-client";
import type { Backup, Resource, Restore, RestoreReview } from "@/lib/contracts/registry";
import {
  formatBytes,
  resourceErrorExplanation,
} from "@/lib/presentation/resources/model";

export function RestoreWizardDialog({
  allResources,
  backups,
  initialBackupID,
  onClose,
  onRestoreCreated,
  projectID,
  sourceResource,
}: {
  allResources: Resource[];
  backups: Backup[];
  initialBackupID?: string;
  onClose: () => void;
  onRestoreCreated: (restore: Restore) => Promise<void>;
  projectID: string;
  sourceResource: Resource;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const succeededBackups = backups.filter(
    (b) => b.source_resource_id === sourceResource.id && b.lifecycle === "succeeded",
  );
  const candidateTargets = allResources.filter(
    (r) => r.type === "postgres" && r.id !== sourceResource.id && r.lifecycle === "ready",
  );

  const [selectedBackupID, setSelectedBackupID] = useState(initialBackupID || succeededBackups[0]?.id || "");
  const [selectedTargetID, setSelectedTargetID] = useState(candidateTargets[0]?.id || "");
  const [review, setReview] = useState<RestoreReview | null>(null);
  const [reviewing, setReviewing] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<{ summary: string; action: string; code?: string } | null>(null);

  useEffect(() => {
    const el = dialog.current;
    el?.showModal();
    return () => {
      if (el?.open) el.close();
    };
  }, []);

  const selectedBackup = succeededBackups.find((b) => b.id === selectedBackupID);
  const selectedTarget = candidateTargets.find((r) => r.id === selectedTargetID);

  async function handleReview(event: React.FormEvent) {
    event.preventDefault();
    if (!selectedBackupID || !selectedTargetID || reviewing) return;

    setReviewing(true);
    setError(null);
    setReview(null);

    const client = new LocalClient();
    const idempotencyKey = crypto.randomUUID();

    try {
      const result = await client.reviewRestore(projectID, selectedBackupID, selectedTargetID, idempotencyKey);
      setReview(result.review);
    } catch (cause) {
      const apiErr = cause as LocalAPIError;
      const explanation = resourceErrorExplanation(apiErr.code, apiErr.message);
      setError({ ...explanation, code: apiErr.code });
    } finally {
      setReviewing(false);
    }
  }

  async function handleApplyRestore() {
    if (!review || !selectedBackupID || !selectedTargetID || submitting) return;

    setSubmitting(true);
    setError(null);

    const client = new LocalClient();
    const idempotencyKey = crypto.randomUUID();

    try {
      const result = await client.createRestore(projectID, selectedBackupID, selectedTargetID, review.id, idempotencyKey);
      await onRestoreCreated(result.restore);
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
      aria-describedby="restore-wizard-desc"
      aria-labelledby="restore-wizard-title"
      className="connectServerDialog placementDialog resourceDialog"
      onCancel={(e) => {
        e.preventDefault();
        onClose();
      }}
      ref={dialog}
    >
      <div className="dialogHeading">
        <div>
          <p className="eyebrow">PostgreSQL · Point-in-Time Recovery</p>
          <h2 id="restore-wizard-title">Restore PostgreSQL Database</h2>
          <p id="restore-wizard-desc">
            Restore a verified logical backup into a separate target PostgreSQL instance.
          </p>
        </div>
        <button aria-label="Close dialog" autoFocus className="iconButton" onClick={onClose} type="button">
          <svg aria-hidden="true" viewBox="0 0 20 20">
            <path d="m5 5 10 10M15 5 5 15" />
          </svg>
        </button>
      </div>

      <div className="restoreSafetyNotice span2" role="note">
        <strong>Important Restore Semantics:</strong>
        <p>
          Restore creates/restores data into a <strong>DIFFERENT</strong> PostgreSQL Resource. It does <em>not</em>{" "}
          overwrite the source database (<strong>{sourceResource.name}</strong>). It restores a static point-in-time
          snapshot and does not continuously synchronize writes.
        </p>
      </div>

      {!review ? (
        <form className="form" onSubmit={handleReview}>
          <label className="span2">
            Select Verified Backup
            <select
              className="select"
              disabled={succeededBackups.length === 0}
              name="backup"
              onChange={(e) => {
                setSelectedBackupID(e.target.value);
                setReview(null);
              }}
              required
              value={selectedBackupID}
            >
              {succeededBackups.length === 0 ? (
                <option value="">No succeeded backups available for this resource</option>
              ) : (
                succeededBackups.map((b) => (
                  <option key={b.id} value={b.id}>
                    {b.id} · {formatBytes(b.artifact_size)} · {b.completed_at || b.created_at} ({b.source_database})
                  </option>
                ))
              )}
            </select>
          </label>

          <label className="span2">
            Target PostgreSQL Resource (must be pristine empty)
            <select
              className="select"
              disabled={candidateTargets.length === 0}
              name="target"
              onChange={(e) => {
                setSelectedTargetID(e.target.value);
                setReview(null);
              }}
              required
              value={selectedTargetID}
            >
              {candidateTargets.length === 0 ? (
                <option value="">No other Ready PostgreSQL resources available</option>
              ) : (
                candidateTargets.map((r) => (
                  <option key={r.id} value={r.id}>
                    {r.name} ({r.id}) · {r.lifecycle}
                  </option>
                ))
              )}
            </select>
            <small className="fieldHint">
              Target must be in Ready state and contain no existing application data or bindings.
            </small>
          </label>

          {error ? (
            <div className="truthCallout span2" role="alert">
              <b>{error.summary}</b>
              <p>{error.action}</p>
              {error.code ? <small className="errorCode">Error code: {error.code}</small> : null}
            </div>
          ) : null}

          <div className="dialogActions span2">
            <button disabled={reviewing} onClick={onClose} type="button">
              Cancel
            </button>
            <button
              className="primary"
              disabled={reviewing || !selectedBackupID || !selectedTargetID}
              type="submit"
            >
              {reviewing ? "Validating Target…" : "Review Restore Preflight"}
            </button>
          </div>
        </form>
      ) : (
        <div className="restoreReviewSummary span2">
          <div className="reviewGrid">
            <div className="reviewFact">
              <span>Source Resource</span>
              <strong>{sourceResource.name} ({sourceResource.id})</strong>
            </div>
            <div className="reviewFact">
              <span>Backup Point</span>
              <strong>{selectedBackup?.id}</strong>
              <small>{selectedBackup?.completed_at}</small>
            </div>
            <div className="reviewFact">
              <span>Target Resource</span>
              <strong>{selectedTarget?.name} ({selectedTarget?.id})</strong>
            </div>
            <div className="reviewFact">
              <span>Pristine Target Check</span>
              <strong className={review.pristine ? "statusPass" : "statusFail"}>
                {review.pristine ? "PASSED (Clean database)" : "FAILED (Contains user objects)"}
              </strong>
            </div>
            <div className="reviewFact">
              <span>Artifact Size</span>
              <strong>{formatBytes(review.artifact_size)}</strong>
            </div>
            <div className="reviewFact">
              <span>Objects in Archive</span>
              <strong>
                {review.objects.tables} tables, {review.objects.sequences} seqs, {review.objects.functions} funcs
              </strong>
            </div>
          </div>

          {review.warning ? (
            <div className="truthCallout warning" role="alert">
              <b>Preflight Warning</b>
              <p>{review.warning}</p>
            </div>
          ) : null}

          {error ? (
            <div className="truthCallout" role="alert">
              <b>{error.summary}</b>
              <p>{error.action}</p>
              {error.code ? <small className="errorCode">Error code: {error.code}</small> : null}
            </div>
          ) : null}

          <div className="dialogActions">
            <button disabled={submitting} onClick={() => setReview(null)} type="button">
              ← Change Selections
            </button>
            <button
              className="primary"
              disabled={submitting || !review.pristine}
              onClick={handleApplyRestore}
              type="button"
            >
              {submitting ? "Starting Restore…" : "Apply Restore into Target"}
            </button>
          </div>
        </div>
      )}
    </dialog>
  );
}
