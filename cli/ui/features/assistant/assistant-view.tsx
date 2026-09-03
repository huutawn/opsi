"use client";

import { FormEvent, useEffect, useMemo, useState } from "react";
import { Button, Icon, PageHeader, StatusBadge, Textarea } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";
import {
  LocalAPIError,
  LocalClient,
  type AssistantConfigurationProposal,
  type AssistantGrounding,
  type AssistantProvider,
  type AssistantSourcePatchProposal,
  type AssistantTurn,
} from "@/lib/api/local-client";
import type { ProposalReview, ServiceConfigurationChange, ServiceConfigurationDraft } from "@/lib/contracts/registry";
import { useI18n } from "@/lib/i18n";

type ChatMessage = {
  id: string;
  role: "user" | "assistant";
  text: string;
  grounding?: AssistantGrounding;
  configurationProposals?: AssistantConfigurationProposal[];
  sourcePatchProposals?: AssistantSourcePatchProposal[];
};

type ReviewState = {
  review: ProposalReview;
  changes?: ServiceConfigurationChange[];
};

export function AssistantView({ console }: { console: ConsoleController }) {
  const { t } = useI18n();
  const client = useMemo(() => new LocalClient(), []);
  const projectID = console.state.project?.id ?? console.route.projectID;
  const [providers, setProviders] = useState<AssistantProvider[]>([]);
  const [surface, setSurface] = useState("");
  const [providerID, setProviderID] = useState("codex");
  const [conversationID, setConversationID] = useState("");
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [prompt, setPrompt] = useState("");
  const [turn, setTurn] = useState<AssistantTurn | null>(null);
  const [failure, setFailure] = useState("");
  const [reviews, setReviews] = useState<Record<string, ReviewState>>({});
  const [reviewBusy, setReviewBusy] = useState("");
  const [patchBusy, setPatchBusy] = useState("");

  const starters = useMemo(
    () => [
      t("assistant.starter_1", "Review this project for Opsi deployment readiness and list the highest-risk gaps."),
      t("assistant.starter_2", "Review how the frontend should call every backend through one public hostname."),
      t("assistant.starter_3", "Review application configuration variables and propose safe non-secret values."),
    ],
    [t]
  );

  useEffect(() => {
    let active = true;
    client.assistantProviders().then((result) => {
      if (!active) return;
      setProviders(result.providers);
      setSurface(result.mcp_surface);
      const ready = result.providers.find((item) => item.available && item.authenticated);
      if (ready) setProviderID(ready.id);
    }).catch((cause) => active && setFailure(errorMessage(cause, t)));
    return () => { active = false; };
  }, [client, t]);

  useEffect(() => {
    if (!turn || turn.state !== "running" || !projectID) return;
    let active = true;
    const timer = window.setInterval(() => {
      client.assistantTurn(projectID, turn.id).then((next) => {
        if (!active) return;
        setTurn(next);
        if (next.state === "succeeded") {
          setMessages((current) => [
            ...current,
            {
              id: next.id,
              role: "assistant",
              text: next.response || t("assistant.no_response", "No response."),
              grounding: next.grounding,
              configurationProposals: next.configuration_proposals,
              sourcePatchProposals: next.source_patch_proposals,
            },
          ]);
          window.clearInterval(timer);
        } else if (next.state === "failed") {
          const errText = next.error || "The AI agent turn failed.";
          setFailure(next.error_code ? `[${next.error_code}] ${errText}` : errText);
          window.clearInterval(timer);
        }
      }).catch((cause) => {
        if (active) setFailure(errorMessage(cause, t));
        window.clearInterval(timer);
      });
    }, 1000);
    return () => { active = false; window.clearInterval(timer); };
  }, [client, projectID, turn, t]);

  const provider = providers.find((item) => item.id === providerID);
  const providerReady = Boolean(provider?.available && provider.authenticated);
  const cloudPATValid = Boolean(console.session?.authenticated);
  const groundingVerified = messages.some((m) => m.grounding?.status === "verified");
  const overallReady = providerReady && cloudPATValid;
  const canMutate = console.session?.role !== "viewer";

  async function submit(event: FormEvent) {
    event.preventDefault();
    const text = prompt.trim();
    if (!text || !projectID || turn?.state === "running") return;
    setFailure("");
    setPrompt("");
    setMessages((current) => [...current, { id: crypto.randomUUID(), role: "user", text }]);
    try {
      const next = await client.startAssistantTurn(
        projectID,
        { provider_id: providerID, conversation_id: conversationID || undefined, prompt: text },
        crypto.randomUUID()
      );
      setConversationID(next.conversation_id);
      setTurn(next);
    } catch (cause) {
      setFailure(errorMessage(cause, t));
    }
  }

  async function createReview(proposal: AssistantConfigurationProposal) {
    if (!projectID || !canMutate) return;
    setReviewBusy(proposal.application_id);
    setFailure("");
    try {
      const draft = JSON.parse(proposal.draft_json) as ServiceConfigurationDraft;
      const current = await client.serviceConfiguration(projectID, proposal.application_id);
      if (current.revision !== proposal.expected_revision || current.state_hash !== proposal.expected_state_hash) {
        throw new Error("Configuration changed after the agent reviewed it. Ask the agent to review again.");
      }
      const [validation, diff] = await Promise.all([
        client.serviceConfigurationValidate(projectID, proposal.application_id, draft),
        client.serviceConfigurationDiff(projectID, proposal.application_id, draft),
      ]);
      if (!validation.valid) {
        throw new Error(validation.issues?.map((item) => item.message).join(" ") || "Cloud rejected this configuration proposal.");
      }
      const review = await client.createProposalReview(
        projectID,
        proposal.application_id,
        {
          environment_id: proposal.environment_id,
          kind: "service_configuration",
          analysis_inputs_hash: proposal.analysis_inputs_hash,
          configuration_draft: draft,
        },
        crypto.randomUUID()
      );
      setReviews((currentReviews) => ({ ...currentReviews, [proposal.application_id]: { review, changes: diff.changes } }));
    } catch (cause) {
      setFailure(errorMessage(cause, t));
    } finally {
      setReviewBusy("");
    }
  }

  async function reviewAction(applicationID: string, action: "approve" | "reject" | "apply") {
    if (!projectID || !canMutate) return;
    const state = reviews[applicationID];
    if (!state) return;
    setReviewBusy(applicationID);
    setFailure("");
    try {
      const review = action === "approve"
        ? await client.approveProposalReview(projectID, state.review.id, crypto.randomUUID())
        : action === "reject"
          ? await client.rejectProposalReview(projectID, state.review.id, crypto.randomUUID())
          : await client.applyProposalReview(projectID, state.review.id, crypto.randomUUID());
      setReviews((current) => ({ ...current, [applicationID]: { ...state, review } }));
      if (action === "apply") await console.actions.load();
    } catch (cause) {
      setFailure(errorMessage(cause, t));
    } finally {
      setReviewBusy("");
    }
  }

  async function applySourcePatch(turnID: string, proposal: AssistantSourcePatchProposal) {
    if (!projectID || !canMutate) return;
    const files = proposal.proposal.files?.map((file) => file.path).join(", ") || "the reviewed files";
    const confirmMessage = t(
      "assistant.confirm_patch_dialog",
      { commit: proposal.source_commit, files },
      `Apply this verified patch to your local worktree at ${proposal.source_commit}?\n\nFiles: ${files}\n\nOpsi will not test, commit, push, or create a PR.`
    );
    if (!window.confirm(confirmMessage)) return;
    setPatchBusy(proposal.proposal_hash);
    setFailure("");
    try {
      await client.applyAssistantSourcePatch(projectID, turnID, proposal.proposal_hash, proposal.source_commit, crypto.randomUUID());
      setMessages((current) => current.map((message) => message.id === turnID ? { ...message, text: `${message.text}\n\n${t("assistant.patch_applied_notice", "Local source patch applied. Review the diff, then test and commit it yourself.")}` } : message));
    } catch (cause) {
      setFailure(errorMessage(cause, t));
    } finally {
      setPatchBusy("");
    }
  }

  return (
    <main className="mx-auto max-w-7xl space-y-6 p-4 lg:p-margin-desktop">
      <PageHeader
        eyebrow={console.state.project?.name}
        icon="hub"
        title={t("assistant.title", "AI Assistant")}
        description={t("assistant.description", "Review your project with an agent grounded in Opsi's current, read-only MCP facts.")}
      />

      <section aria-labelledby="agent-connection-title" className="border border-outline-variant/30 bg-surface-container p-4 sm:p-5">
        <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
          <div className="flex items-start gap-3">
            <div className="flex h-10 w-10 items-center justify-center rounded-lg bg-primary/10 text-primary">
              <Icon name="hub" />
            </div>
            <div>
              <div className="flex flex-wrap items-center gap-2">
                <h2 className="font-semibold" id="agent-connection-title">{t("assistant.connection_title", "Agent connection")}</h2>
                <StatusBadge
                  label={overallReady ? t("status.connected", "Connected") : t("status.needs_setup", "Needs setup")}
                  status={overallReady ? "ready" : "degraded"}
                />
              </div>

              {/* 3 distinct connection statuses */}
              <div className="mt-2 flex flex-wrap items-center gap-3 text-xs text-on-surface-variant">
                <span className="flex items-center gap-1.5">
                  <span className={`inline-block h-2 w-2 rounded-full ${providerReady ? "bg-status-ready" : "bg-status-degraded"}`} />
                  <span>{providerReady ? t("assistant.provider_authenticated", "Provider authenticated") : t("assistant.provider_needs_setup", "Provider needs setup")}</span>
                </span>
                <span>·</span>
                <span className="flex items-center gap-1.5">
                  <span className={`inline-block h-2 w-2 rounded-full ${cloudPATValid ? "bg-status-ready" : "bg-status-failed"}`} />
                  <span>{cloudPATValid ? t("assistant.cloud_pat_valid", "Opsi Cloud PAT valid") : t("assistant.cloud_unauthenticated", "Cloud unauthenticated")}</span>
                </span>
                <span>·</span>
                <span className="flex items-center gap-1.5">
                  <span className={`inline-block h-2 w-2 rounded-full ${groundingVerified ? "bg-status-ready" : "bg-on-surface-variant/40"}`} />
                  <span>{groundingVerified ? t("assistant.mcp_grounding_verified", "MCP grounding verified") : t("assistant.mcp_grounding_ready", "MCP grounding ready")}</span>
                </span>
              </div>

              <p className="mt-1 text-sm text-on-surface-variant">
                {provider?.name || "Detecting local agents…"}{provider?.version ? ` · ${provider.version}` : ""} · Opsi MCP {surface || "mcp-04"}
              </p>
              {provider?.data_boundary && (
                <p className="mt-1 text-xs text-on-surface-variant">{provider.data_boundary}</p>
              )}
              {provider?.message && (
                <p className="mt-1 text-sm text-status-warning">{provider.message}</p>
              )}
            </div>
          </div>
          {providers.length > 1 && (
            <label className="text-sm text-on-surface-variant">
              {t("assistant.provider_label", "AI agent")}
              <select
                aria-label="AI agent"
                className="ml-2 min-h-10 border border-outline-variant/30 bg-surface-container-low px-3 text-on-surface"
                onChange={(event) => setProviderID(event.target.value)}
                value={providerID}
              >
                {providers.map((item) => (
                  <option key={item.id} value={item.id}>{item.name}</option>
                ))}
              </select>
            </label>
          )}
        </div>
      </section>

      {failure && (
        <div className="border border-error/40 bg-error-container/10 p-4 text-sm text-error" role="alert">
          <strong>Assistant action failed:</strong> {failure}
        </div>
      )}

      <div className="grid min-h-[560px] gap-6 lg:grid-cols-[minmax(0,1fr)_320px]">
        <section aria-labelledby="assistant-chat-title" className="flex min-h-[560px] flex-col border border-outline-variant/30 bg-surface-container-low">
          <div className="border-b border-outline-variant/30 p-4">
            <h2 className="font-semibold" id="assistant-chat-title">Project chat</h2>
            <p className="mt-1 text-xs text-on-surface-variant">
              Conversation history is owned by {provider?.name || "the selected agent"}; Opsi keeps only the active local projection.
            </p>
          </div>
          <div aria-live="polite" className="flex-1 space-y-5 overflow-y-auto p-4 sm:p-6">
            {messages.length === 0 && (
              <div className="mx-auto max-w-xl py-12 text-center">
                <Icon className="mx-auto h-10 w-10 text-primary" name="hub" />
                <h3 className="mt-4 text-lg font-semibold">{t("assistant.starters_title", "Ask from current Opsi facts")}</h3>
                <p className="mt-2 text-sm text-on-surface-variant">
                  Review architecture, deployment readiness, FE/BE routing, dependencies, and safe configuration variables.
                </p>
                <div className="mt-6 grid gap-2 text-left">
                  {starters.map((item) => (
                    <button
                      className="min-h-11 border border-outline-variant/30 bg-surface-container p-3 text-left text-sm hover:border-primary/50"
                      key={item}
                      onClick={() => setPrompt(item)}
                      type="button"
                    >
                      {item}
                    </button>
                  ))}
                </div>
              </div>
            )}
            {messages.map((message) => (
              <article
                className={message.role === "user" ? "ml-auto max-w-2xl bg-primary/10 p-4" : "mr-auto max-w-3xl border-l-2 border-primary bg-surface-container p-4"}
                key={message.id}
              >
                <div className="flex items-center justify-between gap-2">
                  <p className="text-xs font-medium uppercase tracking-wider text-on-surface-variant">
                    {message.role === "user" ? "You" : provider?.name || "Agent"}
                  </p>
                  {message.role === "assistant" && message.grounding && message.grounding.status === "verified" && (
                    <span className="inline-flex items-center gap-1 rounded bg-primary/10 px-2 py-0.5 text-xs font-medium text-primary">
                      <Icon className="h-3 w-3" name="check" />
                      Grounded by {message.grounding.successful_tool_calls} Opsi MCP {message.grounding.successful_tool_calls === 1 ? "call" : "calls"}
                    </span>
                  )}
                </div>
                <p className="mt-2 whitespace-pre-wrap text-sm leading-6">{message.text}</p>
                {message.configurationProposals?.map((proposal) => (
                  <ProposalCard
                    canMutate={canMutate}
                    key={`${message.id}-${proposal.application_id}`}
                    onAction={reviewAction}
                    onCreate={createReview}
                    proposal={proposal}
                    review={reviews[proposal.application_id]}
                    working={reviewBusy === proposal.application_id}
                  />
                ))}
                {message.sourcePatchProposals?.map((proposal) => (
                  <SourcePatchCard canMutate={canMutate} key={`${message.id}-${proposal.proposal_hash}`} onApply={() => applySourcePatch(message.id, proposal)} proposal={proposal} working={patchBusy === proposal.proposal_hash} />
                ))}
              </article>
            ))}
            {turn?.state === "running" && (
              <div className="mr-auto flex items-center gap-2 border-l-2 border-status-progress bg-surface-container p-4 text-sm text-on-surface-variant" role="status">
                <Icon className="animate-spin" name="sync" />
                {t("assistant.thinking", "Agent is thinking…")}
              </div>
            )}
          </div>
          <form className="border-t border-outline-variant/30 bg-surface-container p-4" onSubmit={submit}>
            <label className="sr-only" htmlFor="assistant-prompt">Message AI Assistant</label>
            <Textarea
              disabled={!overallReady || turn?.state === "running"}
              id="assistant-prompt"
              maxLength={16 * 1024}
              onChange={(event) => setPrompt(event.target.value)}
              placeholder={overallReady ? t("assistant.prompt_placeholder", "Ask for a project review or configuration recommendation…") : "Connect and authenticate a local AI agent and Opsi Cloud session first."}
              rows={3}
              value={prompt}
            />
            <div className="mt-3 flex items-center justify-between gap-3">
              <p className="text-xs text-on-surface-variant">Read-only MCP · no deploy, shell, or automatic Apply</p>
              <Button disabled={!overallReady || !prompt.trim() || turn?.state === "running"} type="submit">
                <Icon name="arrow_forward" />
                {t("assistant.send", "Send")}
              </Button>
            </div>
          </form>
        </section>

        <aside className="space-y-4">
          <section className="border border-outline-variant/30 bg-surface-container p-5">
            <p className="text-xs font-medium uppercase tracking-wider text-secondary">Trust boundary</p>
            <h2 className="mt-2 font-semibold">Opsi remains authority</h2>
            <ul className="mt-3 space-y-3 text-sm text-on-surface-variant">
              <li>Agent reads project and source facts only through Opsi MCP.</li>
              <li>MCP proposal validation never persists or applies changes.</li>
              <li>Configuration requires separate Review, Approve, and Apply actions.</li>
              <li>Secrets are references; literal secret values are rejected by Cloud.</li>
            </ul>
          </section>
          <section className="border border-outline-variant/30 bg-surface-container p-5">
            <p className="text-xs font-medium uppercase tracking-wider text-secondary">Routing model</p>
            <h2 className="mt-2 font-semibold">One origin, many services</h2>
            <p className="mt-3 text-sm leading-6 text-on-surface-variant">
              The frontend stays at <code>/</code>. Browser backends use paths such as <code>/api</code> on the same hostname, while Traefik routes longest-prefix matches to each Kubernetes Service.
            </p>
          </section>
        </aside>
      </div>
    </main>
  );
}

function ProposalCard({
  canMutate,
  onAction,
  onCreate,
  proposal,
  review,
  working,
}: {
  canMutate: boolean;
  onAction: (applicationID: string, action: "approve" | "reject" | "apply") => void;
  onCreate: (proposal: AssistantConfigurationProposal) => void;
  proposal: AssistantConfigurationProposal;
  review?: ReviewState;
  working: boolean;
}) {
  const { t } = useI18n();
  return (
    <section aria-label={`Configuration proposal for ${proposal.application_name}`} className="mt-4 border border-secondary/40 bg-secondary/5 p-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <p className="text-xs font-medium uppercase tracking-wider text-secondary">{t("assistant.configuration_proposals", "Configuration proposal")}</p>
          <h3 className="mt-1 font-semibold">{proposal.application_name}</h3>
        </div>
        <StatusBadge
          className="!text-on-surface"
          label={review ? review.review.status.replaceAll("_", " ") : t("assistant.agent_draft_validated", "Agent draft (validated)")}
          status={review?.review.status === "applied" ? "ready" : review?.review.status === "stale" || review?.review.status === "rejected" ? "failed" : "pending"}
        />
      </div>
      <p className="mt-3 text-sm text-on-surface-variant">{proposal.rationale}</p>
      {review && (
        <ul className="mt-3 space-y-1 text-xs text-on-surface-variant">
          {review.changes?.map((change, index) => (
            <li key={`${change.name}-${index}`}>
              <code>{change.action} {change.kind} {change.name}</code>
            </li>
          ))}
        </ul>
      )}
      <div className="mt-4 flex flex-wrap items-center gap-2">
        {!review && canMutate && (
          <Button disabled={working} onClick={() => onCreate(proposal)} variant="secondary">
            <Icon name="rate_review" />
            {t("assistant.create_review", "Create review")}
          </Button>
        )}
        {!review && !canMutate && (
          <p className="text-xs text-on-surface-variant">{t("assistant.read_only_access", "View-only role: ask a developer or owner to create review.")}</p>
        )}
        {review && review.review.status === "review_required" && canMutate && (
          <>
            <Button disabled={working} onClick={() => onAction(proposal.application_id, "approve")} variant="primary">
              <Icon name="check" />
              {t("assistant.approve", "Approve")}
            </Button>
            <Button disabled={working} onClick={() => onAction(proposal.application_id, "reject")} variant="secondary">
              <Icon name="close" />
              {t("assistant.reject", "Reject")}
            </Button>
          </>
        )}
        {review && review.review.status === "approved" && canMutate && (
          <Button disabled={working} onClick={() => onAction(proposal.application_id, "apply")} variant="primary">
            <Icon name="done_all" />
            {t("assistant.apply", "Apply configuration")}
          </Button>
        )}
      </div>
    </section>
  );
}

function SourcePatchCard({ canMutate, onApply, proposal, working }: { canMutate: boolean; onApply: () => void; proposal: AssistantSourcePatchProposal; working: boolean }) {
  const { t } = useI18n();
  const files = proposal.proposal.files || [];
  return (
    <section aria-label="Validated local source patch" className="mt-4 border border-status-warning/40 bg-status-warning/5 p-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <p className="text-xs font-medium uppercase tracking-wider text-status-warning">{t("assistant.source_patch_proposals", "Validated source patch")}</p>
          <h3 className="mt-1 font-semibold">Local worktree only</h3>
        </div>
        <StatusBadge className="!text-on-surface" label={proposal.validation_status.replaceAll("_", " ")} status={proposal.validation_status === "VALID" ? "ready" : "pending"} />
      </div>
      <p className="mt-3 text-xs text-on-surface-variant">Commit <code>{proposal.source_commit}</code> · {proposal.application_root || "."}</p>
      {proposal.proposal.rationale?.inference && <p className="mt-2 text-sm text-on-surface-variant">{proposal.proposal.rationale.inference}</p>}
      <div className="mt-3 space-y-3">
        {files.map((file) => <details className="border border-outline-variant/20 p-2" key={file.path}><summary className="cursor-pointer text-sm font-medium">{file.path}</summary><pre className="mt-2 max-h-56 overflow-auto whitespace-pre-wrap text-xs text-on-surface-variant">{file.unified_diff}</pre></details>)}
      </div>
      <p className="mt-3 text-xs text-status-warning">Not built or tested. Applying does not stage, commit, push, or create a pull request.</p>
      <div className="mt-4">
        {canMutate ? <Button disabled={working} onClick={onApply} variant="secondary"><Icon name="edit_note" />{t("assistant.apply_patch", "Apply to local worktree")}</Button> : <p className="text-xs text-on-surface-variant">{t("assistant.read_only_access", "View-only role: ask a developer or owner to apply this local patch.")}</p>}
      </div>
    </section>
  );
}

function errorMessage(cause: unknown, t?: (key: string, fb?: string) => string) {
  if (cause instanceof LocalAPIError || cause instanceof Error) {
    return cause.message;
  }
  return t ? t("common.error", "Unexpected local assistant error.") : "Unexpected local assistant error.";
}
