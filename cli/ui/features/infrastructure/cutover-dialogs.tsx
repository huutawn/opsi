"use client";

import { useEffect, useRef, useState } from "react";
import { LocalAPIError, LocalClient } from "@/lib/api/local-client";
import type {
  ApplicationCutover,
  ApplicationCutoverFinalization,
  ApplicationCutoverReview,
  ApplicationCutoverRollback,
  Resource,
  ResourceBinding,
} from "@/lib/contracts/registry";
import {
  canCutover,
  cutoverWarningExplanation,
  resourceErrorExplanation,
} from "@/lib/presentation/resources/model";

export function CutoverReviewDialog({
  allResources,
  bindings,
  onClose,
  onCutoverApplied,
  projectID,
  sourceResource,
}: {
  allResources: Resource[];
  bindings: ResourceBinding[];
  onClose: () => void;
  onCutoverApplied: (cutover: ApplicationCutover) => Promise<void>;
  projectID: string;
  sourceResource: Resource;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const sourceBindings = bindings.filter((b) => b.target.id === sourceResource.id);
  const targetBindings = bindings.filter((b) => b.target.id !== sourceResource.id);

  const [selectedSourceBindingID, setSelectedSourceBindingID] = useState(sourceBindings[0]?.id || "");
  const [selectedTargetBindingID, setSelectedTargetBindingID] = useState(targetBindings[0]?.id || "");
  const [review, setReview] = useState<ApplicationCutoverReview | null>(null);
  const [reviewing, setReviewing] = useState(false);
  const [showApplyConfirmation, setShowApplyConfirmation] = useState(false);
  const [applying, setApplying] = useState(false);
  const [error, setError] = useState<{ summary: string; action: string; code?: string } | null>(null);

  useEffect(() => {
    const el = dialog.current;
    el?.showModal();
    return () => {
      if (el?.open) el.close();
    };
  }, []);

  const sourceBinding = sourceBindings.find((b) => b.id === selectedSourceBindingID);
  const targetBinding = targetBindings.find((b) => b.id === selectedTargetBindingID);
  const targetResource = allResources.find((r) => r.id === targetBinding?.target.id);
  const applicationID = sourceBinding?.source.id || targetBinding?.source.id || "";

  async function handleRunReview(event: React.FormEvent) {
    event.preventDefault();
    if (!applicationID || !selectedTargetBindingID || reviewing) return;

    setReviewing(true);
    setError(null);
    setReview(null);

    const client = new LocalClient();
    const idempotencyKey = crypto.randomUUID();

    try {
      const result = await client.createCutoverReview(
        projectID,
        applicationID,
        selectedTargetBindingID,
        selectedSourceBindingID,
        idempotencyKey,
      );
      const rev = result.cutover_review ?? result.review;
      if (rev) setReview(rev);
    } catch (cause) {
      const apiErr = cause as LocalAPIError;
      const explanation = resourceErrorExplanation(apiErr.code, apiErr.message);
      setError({ ...explanation, code: apiErr.code });
    } finally {
      setReviewing(false);
    }
  }

  async function handleApplyCutover() {
    if (!review || !applicationID || applying) return;

    setApplying(true);
    setError(null);

    const client = new LocalClient();
    const idempotencyKey = crypto.randomUUID();

    try {
      const result = await client.applyCutover(projectID, applicationID, review.id, idempotencyKey);
      const cut = result.cutover ?? result.application_cutover;
      if (cut) await onCutoverApplied(cut);
      onClose();
    } catch (cause) {
      const apiErr = cause as LocalAPIError;
      const explanation = resourceErrorExplanation(apiErr.code, apiErr.message);
      setError({ ...explanation, code: apiErr.code });
    } finally {
      setApplying(false);
    }
  }

  return (
    <dialog
      aria-describedby="cutover-dialog-desc"
      aria-labelledby="cutover-dialog-title"
      className="connectServerDialog placementDialog resourceDialog"
      onCancel={(e) => {
        e.preventDefault();
        onClose();
      }}
      ref={dialog}
    >
      <div className="dialogHeading">
        <div>
          <p className="eyebrow">PostgreSQL · Safe Database Cutover</p>
          <h2 id="cutover-dialog-title">
            {showApplyConfirmation ? "Confirm Database Cutover" : "Review Database Cutover"}
          </h2>
          <p id="cutover-dialog-desc">
            {showApplyConfirmation
              ? "Verify migration endpoints and rollback preservation before applying."
              : "Preflight check application bindings, database roles, and point-in-time restore status."}
          </p>
        </div>
        <button aria-label="Close dialog" autoFocus className="iconButton" onClick={onClose} type="button">
          <svg aria-hidden="true" viewBox="0 0 20 20">
            <path d="m5 5 10 10M15 5 5 15" />
          </svg>
        </button>
      </div>

      {!review ? (
        <form className="form" onSubmit={handleRunReview}>
          <label className="span2">
            Current Application Source Binding
            <select
              className="select"
              disabled={sourceBindings.length === 0}
              name="sourceBinding"
              onChange={(e) => {
                setSelectedSourceBindingID(e.target.value);
                setReview(null);
              }}
              required
              value={selectedSourceBindingID}
            >
              {sourceBindings.length === 0 ? (
                <option value="">No applications currently bound to {sourceResource.name}</option>
              ) : (
                sourceBindings.map((b) => (
                  <option key={b.id} value={b.id}>
                    App: {b.source.id} → {b.logical_name} ({sourceResource.name})
                  </option>
                ))
              )}
            </select>
          </label>

          <label className="span2">
            Target Binding (Connected to Restored Target Database)
            <select
              className="select"
              disabled={targetBindings.length === 0}
              name="targetBinding"
              onChange={(e) => {
                setSelectedTargetBindingID(e.target.value);
                setReview(null);
              }}
              required
              value={selectedTargetBindingID}
            >
              {targetBindings.length === 0 ? (
                <option value="">No candidate target bindings found</option>
              ) : (
                targetBindings.map((b) => {
                  const trg = allResources.find((r) => r.id === b.target.id);
                  return (
                    <option key={b.id} value={b.id}>
                      App: {b.source.id} → {b.logical_name} on {trg?.name ?? b.target.id}
                    </option>
                  );
                })
              )}
            </select>
            <small className="fieldHint">
              Connect target application binding in Connections tab before executing cutover review.
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
              disabled={reviewing || !selectedSourceBindingID || !selectedTargetBindingID}
              type="submit"
            >
              {reviewing ? "Running Preflight…" : "Run Cutover Review"}
            </button>
          </div>
        </form>
      ) : !showApplyConfirmation ? (
        <div className="cutoverReviewPanel span2">
          <div className="reviewPreflightStatus">
            <div className="reviewHeaderRow">
              <span className="reviewBadge">{review.lifecycle.toUpperCase()}</span>
              <span>Review ID: {review.id}</span>
            </div>
            <div className="reviewGrid">
              <div className="reviewFact">
                <span>Application</span>
                <strong>{review.application_id}</strong>
              </div>
              <div className="reviewFact">
                <span>Source Binding</span>
                <strong>{sourceResource.name} ({review.source_binding_id})</strong>
              </div>
              <div className="reviewFact">
                <span>Target Binding</span>
                <strong>{targetResource?.name ?? review.target_resource_id} ({review.target_binding_id})</strong>
              </div>
              <div className="reviewFact">
                <span>Backup Age</span>
                <strong>
                  {review.backup_age_seconds > 0 ? `${review.backup_age_seconds}s ago` : "0s (Just completed)"}
                </strong>
              </div>
              <div className="reviewFact">
                <span>Source SQL Preflight</span>
                <strong className={review.validation_summary.source_sql_preflight === "pass" ? "statusPass" : "statusFail"}>
                  {review.validation_summary.source_sql_preflight}
                </strong>
              </div>
              <div className="reviewFact">
                <span>Target SQL Preflight</span>
                <strong className={review.validation_summary.target_sql_preflight === "pass" ? "statusPass" : "statusFail"}>
                  {review.validation_summary.target_sql_preflight}
                </strong>
              </div>
              <div className="reviewFact">
                <span>Target Restore Ready</span>
                <strong className={review.validation_summary.target_restore_ready ? "statusPass" : "statusFail"}>
                  {review.validation_summary.target_restore_ready ? "VERIFIED" : "PENDING"}
                </strong>
              </div>
              <div className="reviewFact">
                <span>Role Attributes</span>
                <strong>{review.validation_summary.target_role_attributes}</strong>
              </div>
            </div>
          </div>

          {review.warnings && review.warnings.length > 0 ? (
            <div className="truthCallout warning" role="alert">
              <b>Cutover Preflight Warnings:</b>
              <ul>
                {review.warnings.map((w) => (
                  <li key={w}>{cutoverWarningExplanation(w)}</li>
                ))}
              </ul>
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
            <button onClick={() => setReview(null)} type="button">
              ← Re-select Endpoints
            </button>
            <button
              className="primary"
              disabled={!canCutover(review)}
              onClick={() => setShowApplyConfirmation(true)}
              type="button"
            >
              Proceed to Confirmation →
            </button>
          </div>
        </div>
      ) : (
        <div className="cutoverApplyConfirmation span2">
          <div className="confirmationDiagram">
            <div className="diagramState">
              <span>CURRENT TRAFFIC</span>
              <strong>{review.application_id} → SOURCE ({sourceResource.name})</strong>
            </div>
            <div className="diagramArrow">➔</div>
            <div className="diagramState nextState">
              <span>AFTER CUTOVER</span>
              <strong>{review.application_id} → TARGET ({targetResource?.name ?? review.target_resource_id})</strong>
            </div>
          </div>

          <div className="rollbackPreservationNotice" role="note">
            <p>
              <strong>Rollback Guarantee:</strong> The SOURCE binding remains intact and active. You can immediately{" "}
              <strong>Rollback</strong> at any point until you explicitly choose to <strong>Finalize</strong>.
            </p>
          </div>

          {error ? (
            <div className="truthCallout" role="alert">
              <b>{error.summary}</b>
              <p>{error.action}</p>
              {error.code ? <small className="errorCode">Error code: {error.code}</small> : null}
            </div>
          ) : null}

          <div className="dialogActions">
            <button disabled={applying} onClick={() => setShowApplyConfirmation(false)} type="button">
              ← Back to Review
            </button>
            <button className="primary" disabled={applying} onClick={handleApplyCutover} type="button">
              {applying ? "Applying Cutover…" : "Apply Database Cutover"}
            </button>
          </div>
        </div>
      )}
    </dialog>
  );
}

export function CutoverRollbackDialog({
  cutover,
  onClose,
  onRollbackApplied,
  projectID,
}: {
  cutover: ApplicationCutover;
  onClose: () => void;
  onRollbackApplied: (rollback: ApplicationCutoverRollback) => Promise<void>;
  projectID: string;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<{ summary: string; action: string; code?: string } | null>(null);

  useEffect(() => {
    const el = dialog.current;
    el?.showModal();
    return () => {
      if (el?.open) el.close();
    };
  }, []);

  async function handleRollback() {
    if (submitting) return;

    setSubmitting(true);
    setError(null);

    const client = new LocalClient();
    const idempotencyKey = crypto.randomUUID();

    try {
      const result = await client.applyCutoverRollback(
        projectID,
        cutover.application_id,
        cutover.id,
        idempotencyKey,
      );
      const roll = result.rollback ?? result.cutover_rollback;
      if (roll) await onRollbackApplied(roll);
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
      aria-describedby="rollback-dialog-desc"
      aria-labelledby="rollback-dialog-title"
      className="connectServerDialog placementDialog"
      onCancel={(e) => {
        e.preventDefault();
        onClose();
      }}
      ref={dialog}
    >
      <div className="dialogHeading">
        <div>
          <p className="eyebrow">Cutover Safety · Revert Traffic</p>
          <h2 id="rollback-dialog-title">Rollback Cutover to Source</h2>
          <p id="rollback-dialog-desc">
            Revert application {cutover.application_id} configuration back to the original source database.
          </p>
        </div>
        <button aria-label="Close dialog" autoFocus className="iconButton" onClick={onClose} type="button">
          <svg aria-hidden="true" viewBox="0 0 20 20">
            <path d="m5 5 10 10M15 5 5 15" />
          </svg>
        </button>
      </div>

      <div className="truthCallout warning span2" role="alert">
        <b>Data Divergence Warning (TARGET_WRITES_MAY_NOT_EXIST_ON_SOURCE):</b>
        <p>
          Rollback switches Application traffic back to <strong>SOURCE</strong> ({cutover.source_resource_id}).
          It does <em>not</em> synchronize writes made to TARGET back into SOURCE.
        </p>
      </div>

      {error ? (
        <div className="truthCallout span2" role="alert">
          <b>{error.summary}</b>
          <p>{error.action}</p>
          {error.code ? <small className="errorCode">Error code: {error.code}</small> : null}
        </div>
      ) : null}

      <div className="dialogActions span2">
        <button disabled={submitting} onClick={onClose} type="button">
          Cancel
        </button>
        <button className="primary destructive" disabled={submitting} onClick={handleRollback} type="button">
          {submitting ? "Rolling back…" : "Confirm Rollback to Source"}
        </button>
      </div>
    </dialog>
  );
}

export function CutoverFinalizeDialog({
  cutover,
  onClose,
  onFinalized,
  projectID,
}: {
  cutover: ApplicationCutover;
  onClose: () => void;
  onFinalized: (finalization: ApplicationCutoverFinalization) => Promise<void>;
  projectID: string;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<{ summary: string; action: string; code?: string } | null>(null);

  useEffect(() => {
    const el = dialog.current;
    el?.showModal();
    return () => {
      if (el?.open) el.close();
    };
  }, []);

  async function handleFinalize() {
    if (submitting) return;

    setSubmitting(true);
    setError(null);

    const client = new LocalClient();
    const idempotencyKey = crypto.randomUUID();

    try {
      const result = await client.applyCutoverFinalization(
        projectID,
        cutover.application_id,
        cutover.id,
        idempotencyKey,
      );
      const fin = result.finalization ?? result.application_cutover_finalization;
      if (fin) await onFinalized(fin);
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
      aria-describedby="finalize-dialog-desc"
      aria-labelledby="finalize-dialog-title"
      className="connectServerDialog placementDialog"
      onCancel={(e) => {
        e.preventDefault();
        onClose();
      }}
      ref={dialog}
    >
      <div className="dialogHeading">
        <div>
          <p className="eyebrow">Cutover Lifecycle · Finalization</p>
          <h2 id="finalize-dialog-title">Finalize Database Cutover</h2>
          <p id="finalize-dialog-desc">
            Complete the cutover workflow for application {cutover.application_id}.
          </p>
        </div>
        <button aria-label="Close dialog" autoFocus className="iconButton" onClick={onClose} type="button">
          <svg aria-hidden="true" viewBox="0 0 20 20">
            <path d="m5 5 10 10M15 5 5 15" />
          </svg>
        </button>
      </div>

      <div className="truthCallout span2" role="note">
        <b>Finalize Semantics:</b>
        <p>
          Finalize closes rollback capability by revoking the old SOURCE Application binding.
          It does <strong>NOT</strong> delete SOURCE PostgreSQL data. The source resource remains intact in your
          infrastructure until you choose to decommission it separately.
        </p>
      </div>

      {error ? (
        <div className="truthCallout span2" role="alert">
          <b>{error.summary}</b>
          <p>{error.action}</p>
          {error.code ? <small className="errorCode">Error code: {error.code}</small> : null}
        </div>
      ) : null}

      <div className="dialogActions span2">
        <button disabled={submitting} onClick={onClose} type="button">
          Cancel
        </button>
        <button className="primary" disabled={submitting} onClick={handleFinalize} type="button">
          {submitting ? "Finalizing…" : "Finalize Cutover"}
        </button>
      </div>
    </dialog>
  );
}
