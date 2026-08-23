"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { Button, Icon, StatusBadge } from "@/components/ui/primitives";
import { LocalAPIError, LocalClient } from "@/lib/api/local-client";
import type { ProposalReview, ServiceConfigurationDiff, ServiceConfigurationDraft, ServiceConfigurationPreview, ServiceConfigurationValidation, ServiceRecord } from "@/lib/contracts/registry";
import { asApplicationDependency, isDependencyProposal, isSourcePatchProposal, type DependencyProposalEnvelope, type SourcePatchProposalEnvelope } from "./proposal-review-types";

type Props = {
  application: ServiceRecord;
  canMutate: boolean;
  environmentID?: string;
  projectID: string;
  onApplied: () => Promise<void>;
  onClose: () => void;
};

type Review = {
  proposal: DependencyProposalEnvelope;
  draft: ServiceConfigurationDraft;
  preview: ServiceConfigurationPreview;
  validation: ServiceConfigurationValidation;
  diff: ServiceConfigurationDiff;
  payloadHash: string;
  durable?: ProposalReview;
};

const secretPattern = /(gh[pousr]_[A-Za-z0-9_]{8,}|github_pat_[A-Za-z0-9_]{8,}|(?:postgres|redis|rediss):\/\/[^\s"\\]+|(?:agent[_-]?token|postgres(?:ql)?[_-]?password|valkey[_-]?password|redis[_-]?password|registry[_-]?(?:credential|password|token)|password|token|pat|credential|secret)\s*[:=]\s*["']?[^\s,"'}\\\]]+)/gi;

function safeText(value: string | undefined) {
  return (value ?? "").replace(secretPattern, "[REDACTED]");
}

function redactValue<T>(value: T): T {
  if (typeof value === "string") return safeText(value) as T;
  if (Array.isArray(value)) return value.map(redactValue) as T;
  if (value && typeof value === "object") return Object.fromEntries(Object.entries(value).map(([key, item]) => [key, redactValue(item)])) as T;
  return value;
}

export function ProposalReviewDialog({ application, canMutate, environmentID, projectID, onApplied, onClose }: Props) {
  const dialog = useRef<HTMLDialogElement>(null);
  const confirm = useRef<HTMLDialogElement>(null);
  const client = useMemo(() => new LocalClient(), []);
  const [raw, setRaw] = useState("");
  const [review, setReview] = useState<Review | null>(null);
  const [patch, setPatch] = useState<SourcePatchProposalEnvelope | null>(null);
  const [sourceReview, setSourceReview] = useState<ProposalReview | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [result, setResult] = useState<string | null>(null);

  useEffect(() => {
    const element = dialog.current;
    element?.showModal();
    return () => { if (element?.open) element.close(); };
  }, []);

  function candidateDraft(proposal: DependencyProposalEnvelope): ServiceConfigurationDraft {
    const dependency = asApplicationDependency(proposal);
    const current = application.configuration;
    return {
      schema_version: "opsi.service_configuration/v1",
      environment: current?.environment ?? [],
      public_route: current?.public_route,
      bindings: current?.bindings ?? [],
      resource_bindings: current?.resource_bindings ?? [],
      dependencies: [...(current?.dependencies ?? []).filter((item) => item.logical_name !== dependency.logical_name), dependency],
    };
  }

  async function reviewEnvelope() {
    setError(null);
    setResult(null);
    setReview(null);
    setPatch(null);
    setSourceReview(null);
    let value: unknown;
    try { value = JSON.parse(raw); } catch { setError("Paste one valid proposal JSON object from the read-only MCP result."); return; }
    setRaw("");
    if (!isDependencyProposal(value) && !isSourcePatchProposal(value)) {
      setError("The envelope is not a supported dependency or source-patch proposal.");
      return;
    }
    if (value.project_id !== projectID || value.application_id !== application.id || (environmentID && value.environment_id !== environmentID)) {
      setError("Proposal scope does not match this application and environment.");
      return;
    }
    if (isSourcePatchProposal(value)) {
      const safePatch = redactValue(value);
      setBusy(true);
      try {
        const durable = await client.createProposalReview(projectID, application.id, {
          environment_id: safePatch.environment_id,
          kind: "source_patch",
          analysis_inputs_hash: safePatch.provenance.analysis_inputs_hash,
          source_commit: safePatch.provenance.source_commit,
          application_root: safePatch.provenance.application_root,
          source_patch: safePatch,
        }, `proposal-review-source:${safePatch.provenance.analysis_inputs_hash}`);
        setPatch(safePatch);
        setSourceReview(durable);
      } catch (cause) {
        const apiError = cause as LocalAPIError;
        setError(apiError.message || "Opsi could not create the durable source review.");
      } finally { setBusy(false); }
      return;
    }
    setBusy(true);
    try {
      const draft = candidateDraft(value);
      const [preview, validation, diff] = await Promise.all([
        client.serviceConfigurationPreview(projectID, application.id, draft),
        client.serviceConfigurationValidate(projectID, application.id, draft),
        client.serviceConfigurationDiff(projectID, application.id, draft),
      ]);
      if (!validation.valid) {
        setError(validation.issues?.[0]?.message ?? "Current Opsi authority rejected this proposal.");
        return;
      }
      const canonicalDraft = structuredClone(preview.configuration);
      const durable = await client.createProposalReview(projectID, application.id, {
        environment_id: value.environment_id,
        kind: "dependency",
        analysis_inputs_hash: value.provenance.analysis_inputs_hash,
        source_commit: value.provenance.source_commit,
        application_root: value.provenance.application_root,
        dependency_draft: canonicalDraft,
      }, `proposal-review-create:${preview.draft_state_hash}`);
      setReview({ proposal: value, draft: canonicalDraft, preview, validation, diff, payloadHash: preview.draft_state_hash, durable });
    } catch (cause) {
      const apiError = cause as LocalAPIError;
      setError(apiError.message || "Opsi could not revalidate this proposal.");
    } finally { setBusy(false); }
  }

  async function applyReviewedDependency() {
    if (!review) return;
    setBusy(true);
    setError(null);
    try {
      // Cloud owns the lifecycle: it revalidates, persists approval with the
      // authenticated human actor, and then invokes canonical apply using only
      // its stored normalized draft.
      const created = review.durable ?? await client.createProposalReview(projectID, application.id, {
        environment_id: review.proposal.environment_id,
        kind: "dependency",
        analysis_inputs_hash: review.proposal.provenance.analysis_inputs_hash,
        source_commit: review.proposal.provenance.source_commit,
        application_root: review.proposal.provenance.application_root,
        dependency_draft: review.draft,
      }, `proposal-review-create:${review.payloadHash}`);
      const approved = created.status === "approved" ? created : await client.approveProposalReview(projectID, created.id, `proposal-review-approve:${created.id}`);
      const applied = approved.status === "applied" ? approved : await client.applyProposalReview(projectID, approved.id, `proposal-review-apply:${approved.id}`);
      setReview({ ...review, durable: applied });
      setResult(`Dependency change applied at ServiceConfiguration revision ${applied.resulting_configuration_revision}. Build, deploy, and verification remain separate actions.`);
      await onApplied();
    } catch (cause) {
      const apiError = cause as LocalAPIError;
      setError(apiError.code === "SERVICE_CONFIGURATION_STALE" ? "Proposal is stale. Review the current configuration again before applying." : apiError.message || "Canonical dependency apply failed.");
    } finally {
      setBusy(false);
      if (confirm.current?.open) confirm.current.close();
    }
  }

  async function rejectReviewedDependency() {
    if (!review?.durable) return;
    setBusy(true);
    setError(null);
    try {
      const rejected = await client.rejectProposalReview(projectID, review.durable.id, `proposal-review-reject:${review.durable.id}`);
      setReview({ ...review, durable: rejected });
      setResult("Dependency proposal rejected. No configuration, build, deployment, or verification state was changed.");
    } catch (cause) {
      setError((cause as LocalAPIError).message || "Could not reject the dependency review.");
    } finally {
      setBusy(false);
    }
  }

  function copyPatch() { if (patch) void navigator.clipboard.writeText(JSON.stringify(patch, null, 2)); }
  async function rejectSourcePatch() { if (!sourceReview) return; setBusy(true); try { const rejected = await client.rejectProposalReview(projectID, sourceReview.id, `proposal-review-reject:${sourceReview.id}`); setSourceReview(rejected); setResult("Source patch review rejected. No source repository was changed."); } catch (cause) { setError((cause as LocalAPIError).message || "Could not reject the source review."); } finally { setBusy(false); } }

  return (
    <dialog aria-labelledby="proposal-review-title" className="fixed inset-0 m-auto bg-surface-container-low border border-outline-variant/30 rounded-2xl shadow-2xl p-6 max-w-3xl w-[calc(100%-2rem)] z-[60] text-on-surface max-h-[90vh] overflow-y-auto" onCancel={(event) => { event.preventDefault(); onClose(); }} ref={dialog}>
      <div className="flex items-start justify-between gap-4 border-b border-outline-variant/20 pb-4">
        <div>
          <span className="font-label-sm text-xs text-primary font-bold uppercase tracking-wider">Human review</span>
          <h2 id="proposal-review-title" className="font-headline-md text-xl font-bold">Review AI proposal</h2>
          <p className="text-xs text-on-surface-variant mt-1">Suggestions are untrusted until Opsi’s current authority revalidates them. MCP cannot approve or apply changes.</p>
        </div>
        <button aria-label="Close proposal review" className="p-2 rounded-lg text-on-surface-variant hover:text-on-surface hover:bg-surface-container-highest" onClick={onClose} type="button"><Icon name="close" /></button>
      </div>

      {!review && !patch ? <section className="mt-5 space-y-3">
        <label className="text-xs font-semibold text-on-surface-variant" htmlFor="proposal-envelope">Proposal review envelope</label>
        <textarea aria-describedby="proposal-help" className="w-full min-h-48 bg-surface-container-highest border border-outline-variant/30 rounded-lg p-3 font-code-md text-xs focus:outline-none focus:border-primary/50" id="proposal-envelope" onChange={(event) => setRaw(event.target.value)} placeholder='{ "project_id": "…", "candidate": { … } }' value={raw} />
        <p className="text-[11px] text-on-surface-variant" id="proposal-help">Paste a bounded MCP dependency or source patch proposal. Credentials and credential-bearing URLs are redacted before display.</p>
        <div className="flex justify-end"><Button disabled={!raw.trim() || busy} onClick={() => void reviewEnvelope()} type="button" variant="primary">{busy ? "Revalidating…" : "Review proposal"}</Button></div>
      </section> : null}

      {error ? <div className="mt-5 p-3 rounded-lg border border-status-failed/30 bg-error-container/20 text-xs" role="alert">{error}</div> : null}
      {result ? <div className="mt-5 p-3 rounded-lg border border-status-healthy/30 bg-status-healthy/10 text-xs" role="status">{result}</div> : null}

      {review ? <DependencyReview canMutate={canMutate} review={review} busy={busy} onReject={() => void rejectReviewedDependency()} onApply={() => confirm.current?.showModal()} /> : null}
      {patch ? <SourcePatchReview canMutate={canMutate} patch={patch} review={sourceReview} onCopy={copyPatch} onReject={() => void rejectSourcePatch()} /> : null}

      <dialog aria-labelledby="proposal-confirm-title" className="m-auto bg-surface-container-low border border-outline-variant/30 rounded-xl shadow-2xl p-5 max-w-md w-[calc(100%-2rem)] text-on-surface" ref={confirm}>
        <h3 className="font-headline-md font-bold" id="proposal-confirm-title">Apply dependency change?</h3>
        <p className="text-xs text-on-surface-variant mt-2">This applies exactly the reviewed canonical payload. It does not create a build, deployment, or verification run.</p>
        {review?.proposal.candidate.phase === "build" ? <p className="text-xs text-status-warning mt-3">NEW BUILD RECORD REQUIRED. This build-time dependency change invalidates the current BuildRecord; approval creates neither a BuildJob nor a BuildRecord.</p> : null}
        <div className="flex justify-end gap-3 mt-5"><Button onClick={() => confirm.current?.close()} type="button" variant="secondary">Cancel</Button><Button disabled={busy} onClick={() => void applyReviewedDependency()} type="button" variant="primary">Apply Dependency Change</Button></div>
      </dialog>
    </dialog>
  );
}

function DependencyReview({ canMutate, review, busy, onReject, onApply }: { canMutate: boolean; review: Review; busy: boolean; onReject: () => void; onApply: () => void }) {
  const rejected = review.durable?.status === "rejected";
  return <section className="mt-5 space-y-5 text-xs">
    <div className="flex items-center justify-between"><h3 className="font-headline-md font-semibold">Dependency suggestion</h3><StatusBadge label={review.durable?.status ?? "Revalidated"} value={review.durable?.status === "applied" ? "healthy" : "unknown"} /></div>
    <div className="grid grid-cols-1 sm:grid-cols-2 gap-3"><Fact label="Current revision" value={String(review.preview.current_revision)} /><Fact label="Proposed dependency" value={`${review.proposal.candidate.logical_name} → ${review.proposal.candidate.target_id}`} /><Fact label="Confidence (advisory)" value={review.proposal.confidence ?? "Not supplied"} /><Fact label="Reviewed payload hash" value={review.payloadHash} mono /></div>
    <section className="bg-surface-container border border-outline-variant/20 rounded-xl p-4"><h4 className="font-semibold">Current → proposed</h4><ul className="mt-3 space-y-2 font-code-md">{review.diff.changes.length ? review.diff.changes.map((change, index) => <li className="grid grid-cols-[auto_1fr] gap-2" key={`${change.kind}-${change.name}-${index}`}><span className="text-primary">{change.action}</span><span>{change.kind}{change.name ? ` · ${change.name}` : ""}{change.before || change.after ? `: ${safeText(change.before)} → ${safeText(change.after)}` : ""}</span></li>) : <li>No canonical configuration change is proposed.</li>}</ul></section>
    <section className="bg-surface-container border border-outline-variant/20 rounded-xl p-4"><h4 className="font-semibold">Observed evidence</h4><ul className="mt-3 space-y-2">{(review.proposal.evidence ?? []).map((item, index) => <li key={`${item.file}-${item.line}-${index}`}><span className="font-code-md">{item.file}:{item.line}</span> — {safeText(item.reason)} {item.safe_excerpt ? <span className="text-on-surface-variant">({safeText(item.safe_excerpt)})</span> : null}</li>) || <li>No evidence supplied.</li>}</ul></section>
    {review.proposal.candidate.phase === "build" ? <div className="p-3 rounded-lg bg-status-warning/10 border border-status-warning/30">NEW BUILD RECORD REQUIRED. Build, deploy, and verification are not automatic.</div> : null}
    {!canMutate ? <p className="text-on-surface-variant">Your read-only role can inspect this review but cannot approve, reject, or apply it.</p> : null}
    <div className="flex justify-end gap-3"><Button disabled={busy || rejected || !canMutate} onClick={onReject} type="button" variant="secondary">Reject</Button><Button disabled={busy || rejected || !canMutate} onClick={onApply} type="button" variant="primary">Apply Dependency Change</Button></div>
  </section>;
}

function SourcePatchReview({ canMutate, patch, review, onCopy, onReject }: { canMutate: boolean; patch: SourcePatchProposalEnvelope; review: ProposalReview | null; onCopy: () => void; onReject: () => void }) {
  return <section className="mt-5 space-y-5 text-xs"><div><StatusBadge label={review?.status ?? "Review only"} value="unknown" /><h3 className="font-headline-md font-semibold mt-3">Source patch suggestion</h3><p className="text-on-surface-variant mt-1">Opsi does not currently modify source repositories. Copy Patch is the only source action.</p></div><div className="grid grid-cols-1 sm:grid-cols-2 gap-3"><Fact label="Exact source commit" value={patch.provenance.source_commit} mono /><Fact label="Application root" value={patch.provenance.application_root} mono /></div><section className="space-y-3">{patch.files.map((file) => <article className="bg-surface-container border border-outline-variant/20 rounded-xl overflow-hidden" key={file.path}><h4 className="p-3 font-code-md font-semibold break-all">{file.path}</h4><pre className="p-3 border-t border-outline-variant/20 overflow-x-auto whitespace-pre-wrap font-code-md text-[11px]">{safeText(file.unified_diff)}</pre></article>)}</section>{patch.provenance.dependency_proposal_hash ? <div className="p-3 rounded-lg bg-status-warning/10 border border-status-warning/30">Prerequisite: this patch references an un-applied dependency proposal.</div> : null}<div className="flex justify-end gap-3"><Button disabled={review?.status === "rejected" || !canMutate} onClick={onReject} type="button" variant="secondary">Reject</Button><Button onClick={onCopy} type="button" variant="primary">Copy Patch</Button></div></section>;
}

function Fact({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) { return <div className="bg-surface-container p-3 rounded-lg border border-outline-variant/20"><span className="block text-[10px] uppercase tracking-wider text-on-surface-variant">{label}</span><span className={`block mt-1 break-all ${mono ? "font-code-md" : ""}`}>{safeText(value)}</span></div>; }
