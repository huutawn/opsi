"use client";

import { FormEvent, useEffect, useRef, useState } from "react";
import { Button, Icon, StatusBadge, Textarea } from "@/components/ui/primitives";
import type {
  AssistantConfigurationProposal,
  AssistantConversationSummary,
  AssistantGrounding,
  AssistantProgressEvent,
  AssistantProvider,
  AssistantSourcePatchProposal,
  AssistantTurn,
} from "@/lib/api/local-client";
import type { ProposalReview, ServiceConfigurationChange } from "@/lib/contracts/registry";
import { useI18n } from "@/lib/i18n";

export type ChatMessage = {
  id: string;
  turnId?: string;
  role: "user" | "assistant";
  text: string;
  redacted?: boolean;
  state?: "running" | "succeeded" | "failed";
  errorCode?: string;
  diagnosticCode?: string;
  error?: string;
  nextAction?: string;
  progress?: AssistantProgressEvent[];
  grounding?: AssistantGrounding;
  configurationProposals?: AssistantConfigurationProposal[];
  sourcePatchProposals?: AssistantSourcePatchProposal[];
};

export type ReviewState = {
  review: ProposalReview;
  changes?: ServiceConfigurationChange[];
};

export interface AssistantConversationPanelProps {
  activeConv?: AssistantConversationSummary;
  conversations: AssistantConversationSummary[];
  showHistory: boolean;
  onToggleHistory: () => void;
  conversationID: string;
  onSelectConversation: (id: string) => void;
  onDeleteConversation: (id: string) => void;
  onNewChat: () => void;
  turn: AssistantTurn | null;
  messages: ChatMessage[];
  provider?: AssistantProvider;
  starters: string[];
  onSelectStarter: (starter: string) => void;
  onRetryTurn: (turnID: string) => void;
  canMutate: boolean;
  reviews: Record<string, ReviewState>;
  reviewBusy: string;
  onCreateReview: (proposal: AssistantConfigurationProposal) => void;
  onReviewAction: (applicationID: string, action: "approve" | "reject" | "apply") => void;
  onApplySourcePatch: (messageID: string, proposal: AssistantSourcePatchProposal) => void;
  patchBusy: string;
  prompt: string;
  onPromptChange: (value: string) => void;
  onSubmit: (event: FormEvent) => void;
  overallReady: boolean;
  historyAvailable: boolean;
}

export function AssistantConversationPanel({
  activeConv,
  conversations,
  showHistory,
  onToggleHistory,
  conversationID,
  onSelectConversation,
  onDeleteConversation,
  onNewChat,
  turn,
  messages,
  provider,
  starters,
  onSelectStarter,
  onRetryTurn,
  canMutate,
  reviews,
  reviewBusy,
  onCreateReview,
  onReviewAction,
  onApplySourcePatch,
  patchBusy,
  prompt,
  onPromptChange,
  onSubmit,
  overallReady,
  historyAvailable,
}: AssistantConversationPanelProps) {
  const { t } = useI18n();
  const feedRef = useRef<HTMLDivElement>(null);
  const isNearBottomRef = useRef(true);
  const [showJumpToLatest, setShowJumpToLatest] = useState(false);
  const prevConvIdRef = useRef(conversationID);
  const prevMessagesLengthRef = useRef(messages.length);
  const prevProgressLengthRef = useRef(turn?.progress?.length ?? 0);
  const prevTurnStateRef = useRef(turn?.state);
  const prevTurnIdRef = useRef(turn?.id);
  const checkIfNearBottom = () => {
    const el = feedRef.current;
    if (!el) return true;
    if (el.scrollHeight <= el.clientHeight) return true;
    const threshold = 64;
    return el.scrollHeight - el.scrollTop - el.clientHeight <= threshold;
  };

  const scrollToBottom = (smooth = false) => {
    const el = feedRef.current;
    if (!el) return;
    if (smooth) {
      try {
        el.scrollTo({ top: el.scrollHeight, behavior: "smooth" });
      } catch {
        el.scrollTop = el.scrollHeight;
      }
    } else {
      el.scrollTop = el.scrollHeight;
    }
    isNearBottomRef.current = true;
    setShowJumpToLatest(false);
  };
  const handleScroll = () => {
    const near = checkIfNearBottom();
    isNearBottomRef.current = near;
    if (near) {
      setShowJumpToLatest(false);
    }
  };

  const handleJumpToLatest = () => {
    const el = feedRef.current;
    if (el) {
      el.scrollTop = el.scrollHeight;
    }
    isNearBottomRef.current = true;
    setShowJumpToLatest(false);
  };

  // Native scroll listener on feedRef
  useEffect(() => {
    const el = feedRef.current;
    if (!el) return;
    const onScroll = () => {
      const near = checkIfNearBottom();
      isNearBottomRef.current = near;
      if (near) {
        setShowJumpToLatest(false);
      }
    };
    el.addEventListener("scroll", onScroll, { passive: true });
    return () => el.removeEventListener("scroll", onScroll);
  }, []);

  // 1. Initial mount: scroll to bottom
  useEffect(() => {
    scrollToBottom(false);
  }, []);

  // 2. Switching conversation: scroll to bottom immediately
  useEffect(() => {
    if (prevConvIdRef.current !== conversationID) {
      prevConvIdRef.current = conversationID;
      scrollToBottom(false);
    }
  }, [conversationID]);

  // 3. Messages or live progress update
  useEffect(() => {
    const currentLen = messages.length;
    const prevLen = prevMessagesLengthRef.current;
    prevMessagesLengthRef.current = currentLen;

    const currentProgLen = turn?.progress?.length ?? 0;
    const prevProgLen = prevProgressLengthRef.current;
    prevProgressLengthRef.current = currentProgLen;

    const currentState = turn?.state;
    const prevState = prevTurnStateRef.current;
    prevTurnStateRef.current = currentState;

    const currentTurnId = turn?.id;
    const prevTurnId = prevTurnIdRef.current;
    prevTurnIdRef.current = currentTurnId;

    const hasNewMessage = currentLen > prevLen;
    const hasNewProgress = currentProgLen > prevProgLen;
    const hasTurnChange = currentState !== prevState || currentTurnId !== prevTurnId;

    if (!hasNewMessage && !hasNewProgress && !hasTurnChange) {
      return;
    }

    // If user just posted a message, always auto-scroll to bottom
    const lastMsg = messages[messages.length - 1];
    const userJustSent = hasNewMessage && lastMsg?.role === "user";

    if (userJustSent) {
      scrollToBottom(false);
      return;
    }
    const isCurrentlyNear = checkIfNearBottom();
    isNearBottomRef.current = isCurrentlyNear;
    if (isCurrentlyNear) {
      scrollToBottom(false);
    } else {
      setShowJumpToLatest(true);
    }
  }, [messages, turn?.progress, turn?.state, turn?.id]);

  return (
    <section
      aria-labelledby="assistant-chat-title"
      className="relative flex h-[72dvh] min-h-[32rem] max-h-[48rem] flex-col overflow-hidden border border-outline-variant/30 bg-surface-container-low"
      style={{ height: "72dvh", minHeight: "32rem", maxHeight: "48rem" }}
    >
      {/* Chat header */}
      <div className="flex shrink-0 flex-wrap items-center justify-between gap-3 border-b border-outline-variant/30 p-4">
        <div>
          <h2 className="font-semibold" id="assistant-chat-title">
            {activeConv?.title ? activeConv.title : t("assistant.project_chat", "Project chat")}
          </h2>
          <p className="mt-1 text-xs text-on-surface-variant">
            {t("assistant.chat_subtitle", "Local persistent history · Opsi read-only MCP")}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button
            aria-label={t("assistant.new_chat", "New chat")}
            disabled={turn?.state === "running"}
            onClick={onNewChat}
            size="sm"
            variant="secondary"
          >
            <Icon name="add" />
            {t("assistant.new_chat", "New chat")}
          </Button>
          {conversations.length > 0 && (
            <button
              aria-expanded={showHistory}
              aria-label={t("assistant.toggle_history", "Recent chats")}
              className="flex items-center gap-1.5 rounded border border-outline-variant/30 bg-surface-container px-2.5 py-1.5 text-xs font-medium text-on-surface hover:border-primary/50 focus:outline-none focus:ring-2 focus:ring-primary"
              disabled={turn?.state === "running"}
              onClick={onToggleHistory}
              type="button"
            >
              <Icon className="h-4 w-4" name="history" />
              <span>
                {t("assistant.recent_chats", "Recent chats")} ({conversations.length})
              </span>
              <Icon className="h-3.5 w-3.5" name={showHistory ? "expand_less" : "expand_more"} />
            </button>
          )}
        </div>
      </div>

      {/* Collapsible Recent Chats panel */}
      {showHistory && conversations.length > 0 && (
        <div className="shrink-0 border-b border-outline-variant/30 bg-surface-container/50 p-3">
          <p className="mb-2 text-xs font-semibold uppercase tracking-wider text-on-surface-variant">
            {t("assistant.local_conversations", "Recent conversations on this device")}
          </p>
          <div className="max-h-48 space-y-1.5 overflow-y-auto">
            {conversations.map((conv) => (
              <div
                className={`flex items-center justify-between rounded p-2 text-xs transition-colors ${
                  conv.id === conversationID
                    ? "border border-primary/40 bg-primary/15 font-medium"
                    : "bg-surface-container hover:bg-surface-container-high"
                }`}
                key={conv.id}
              >
                <button
                  className="flex-1 truncate text-left focus:outline-none focus:underline"
                  disabled={turn?.state === "running"}
                  onClick={() => onSelectConversation(conv.id)}
                  type="button"
                >
                  <span className="truncate">{conv.title || conv.id}</span>
                  <span className="ml-2 text-on-surface-variant/70">
                    ({conv.message_count} {t("assistant.messages_short", "messages")})
                  </span>
                </button>
                <button
                  aria-label={`${t("assistant.delete_chat", "Delete chat")}: ${conv.title || conv.id}`}
                  className="ml-2 inline-flex min-h-10 min-w-10 items-center justify-center text-on-surface-variant hover:text-error focus:outline-none focus:ring-2 focus:ring-error"
                  disabled={turn?.state === "running" || conv.last_turn_state === "running"}
                  onClick={(e) => {
                    e.stopPropagation();
                    onDeleteConversation(conv.id);
                  }}
                  title={t("common.delete", "Delete")}
                  type="button"
                >
                  <Icon className="h-3.5 w-3.5" name="delete" />
                </button>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Message Feed area with relative positioning for Jump to Latest */}
      <div className="relative flex min-h-0 flex-1 flex-col">
        <div
          aria-label={t("assistant.conversation_log", "Conversation history")}
          className="flex-1 space-y-5 overflow-y-auto overscroll-contain p-4 sm:p-6 focus:outline-none focus-visible:ring-1 focus-visible:ring-primary/50"
          onScroll={handleScroll}
          ref={feedRef}
          role="log"
          tabIndex={0}
        >
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
                    onClick={() => onSelectStarter(item)}
                    type="button"
                  >
                    {item}
                  </button>
                ))}
              </div>
            </div>
          )}

          {messages.map((message) => {
            if (message.state === "failed") {
              return (
                <article
                  className="mr-auto max-w-3xl border-l-2 border-error bg-error-container/10 p-4"
                  key={message.id}
                >
                  <div className="flex items-center justify-between gap-2">
                    <p className="text-xs font-medium uppercase tracking-wider text-error">
                      {provider?.name || "Agent"} · {t("status.failed", "Failed")}
                    </p>
                    {(message.diagnosticCode || message.errorCode) && (
                      <span className="rounded bg-error/15 px-2 py-0.5 font-mono text-xs font-semibold text-error">
                        {message.diagnosticCode || message.errorCode}
                      </span>
                    )}
                  </div>
                  <p className="mt-2 text-sm leading-6 text-on-surface">{message.text}</p>
                  {message.nextAction && (
                    <div className="mt-3 flex items-start gap-2 rounded bg-surface-container p-2.5 text-xs text-on-surface-variant">
                      <Icon className="mt-0.5 h-4 w-4 shrink-0 text-status-warning" name="lightbulb" />
                      <div>
                        <strong className="font-semibold text-on-surface">{t("assistant.next_action", "Next action")}:</strong>{" "}
                        {message.nextAction}
                      </div>
                    </div>
                  )}
                  <div className="mt-4 flex items-center gap-3">
                    <Button
                      aria-label={t("assistant.retry", "Retry")}
                      disabled={turn?.state === "running" || message.redacted}
                      onClick={() => onRetryTurn(message.turnId || message.id)}
                      size="sm"
                      variant="primary"
                    >
                      <Icon name="refresh" />
                      {t("assistant.retry", "Retry")}
                    </Button>
                    {message.redacted && (
                      <span className="text-xs text-on-surface-variant">
                        {t("assistant.prompt_redacted_no_retry", "Prompt contained redacted credentials and cannot be retried automatically.")}
                      </span>
                    )}
                  </div>
                  {message.progress && message.progress.length > 0 && (
                    <TechnicalDetails progress={message.progress} t={t} />
                  )}
                </article>
              );
            }

            return (
              <article
                className={
                  message.role === "user"
                    ? "ml-auto max-w-2xl bg-primary/10 p-4"
                    : "mr-auto max-w-3xl border-l-2 border-primary bg-surface-container p-4"
                }
                key={message.id}
              >
                <div className="flex items-center justify-between gap-2">
                  <p className="text-xs font-medium uppercase tracking-wider text-on-surface-variant">
                    {message.role === "user" ? t("assistant.you", "You") : provider?.name || t("assistant.agent", "Agent")}
                  </p>
                  {message.role === "assistant" && message.grounding && message.grounding.status === "verified" && (
                    <span className="inline-flex items-center gap-1 rounded bg-primary/10 px-2 py-0.5 text-xs font-medium text-primary">
                      <Icon className="h-3 w-3" name="check" />
                      {t("assistant.grounded_by", "Grounded by")} {message.grounding.successful_tool_calls} Opsi MCP {t("assistant.calls", "calls")}
                    </span>
                  )}
                </div>
                <p className="mt-2 whitespace-pre-wrap text-sm leading-6">{message.text}</p>
                {message.configurationProposals?.map((proposal) => (
                  <ProposalCard
                    canMutate={canMutate}
                    key={`${message.id}-${proposal.application_id}`}
                    onAction={onReviewAction}
                    onCreate={onCreateReview}
                    proposal={proposal}
                    review={reviews[proposal.application_id]}
                    working={reviewBusy === proposal.application_id}
                  />
                ))}
                {message.sourcePatchProposals?.map((proposal) => (
                  <SourcePatchCard
                    canMutate={canMutate}
                    key={`${message.id}-${proposal.proposal_hash}`}
                    onApply={() => onApplySourcePatch(message.id, proposal)}
                    proposal={proposal}
                    working={patchBusy === proposal.proposal_hash}
                  />
                ))}
                {message.progress && message.progress.length > 0 && (
                  <TechnicalDetails progress={message.progress} t={t} />
                )}
              </article>
            );
          })}

          {/* Live Progress Bubble */}
          {turn?.state === "running" && (
            <article
              aria-atomic="true"
              aria-label={t("assistant.thinking", "Agent is thinking…")}
              aria-live="polite"
              className="mr-auto max-w-3xl border-l-2 border-primary bg-surface-container p-4"
              role="status"
            >
              <div className="flex items-center justify-between gap-2">
                <p className="text-xs font-medium uppercase tracking-wider text-on-surface-variant">
                  {provider?.name || "Agent"}
                </p>
                <span className="inline-flex items-center gap-1.5 text-xs font-medium text-primary">
                  <Icon className="h-3.5 w-3.5 animate-spin" name="sync" />
                  <span>{t("status.running", "Running")}</span>
                </span>
              </div>
              <div className="mt-3 flex items-center gap-2 text-sm font-medium text-on-surface">
                <span className="inline-block h-2 w-2 animate-ping rounded-full bg-primary" />
                <span>{currentProgressSummary(turn, t)}</span>
              </div>
              {turn.progress && turn.progress.length > 0 && (
                <TechnicalDetails progress={turn.progress} t={t} />
              )}
            </article>
          )}
        </div>

        {/* Floating Jump to Latest Button */}
        {showJumpToLatest && (
          <div className="pointer-events-auto absolute bottom-3 left-1/2 z-10 -translate-x-1/2">
            <Button
              aria-label={t("assistant.jump_to_latest", "Jump to latest")}
              className="shadow-md focus-visible:ring-2 focus-visible:ring-primary focus-visible:ring-offset-2"
              onClick={handleJumpToLatest}
              size="sm"
              variant="primary"
            >
              <Icon className="h-4 w-4" name="arrow_downward" />
              <span>{t("assistant.jump_to_latest", "Jump to latest")}</span>
            </Button>
          </div>
        )}
      </div>

      {/* Composer fixed at bottom */}
      <form className="shrink-0 border-t border-outline-variant/30 bg-surface-container p-4" onSubmit={onSubmit}>
        <label className="sr-only" htmlFor="assistant-prompt">
          Message AI Assistant
        </label>
        <Textarea
          disabled={!overallReady || !historyAvailable || turn?.state === "running"}
          id="assistant-prompt"
          maxLength={16 * 1024}
          onChange={(event) => onPromptChange(event.target.value)}
          placeholder={
            overallReady
              ? t("assistant.prompt_placeholder", "Ask for a project review or configuration recommendation…")
              : t("assistant.connect_first", "Connect and authenticate a local AI agent and Opsi Cloud session first.")
          }
          rows={3}
          value={prompt}
        />
        <div className="mt-3 flex items-center justify-between gap-3">
          <p className="text-xs text-on-surface-variant">
            {t("assistant.composer_boundary", "Read-only MCP · no deploy, shell, or automatic Apply")}
          </p>
          <Button
            disabled={!overallReady || !historyAvailable || !prompt.trim() || turn?.state === "running"}
            type="submit"
          >
            <Icon name="arrow_forward" />
            {t("assistant.send", "Send")}
          </Button>
        </div>
      </form>
    </section>
  );
}

function currentProgressSummary(turn: AssistantTurn, t: (key: string, fb?: string) => string): string {
  if (turn.progress && turn.progress.length > 0) {
    const latest = turn.progress[turn.progress.length - 1];
    if (latest.phase === "queued") return t("assistant.progress_queued", "Queued");
    if (latest.phase === "starting_provider") return t("assistant.progress_starting_provider", "Starting AI provider");
    if (latest.phase === "starting_mcp") return t("assistant.progress_starting_mcp", "Starting Opsi MCP");
    if (latest.phase === "generating_response") return t("assistant.progress_generating", "Generating response");
    if (latest.phase === "tool_running") return t("assistant.progress_tool_running", "Reading Opsi project facts");
    if (latest.phase === "tool_succeeded") return t("assistant.progress_tool_succeeded", "Opsi project facts received");
    if (latest.summary) return latest.summary;
  }
  return t("assistant.thinking", "Agent is thinking…");
}

function TechnicalDetails({
  progress,
  t,
}: {
  progress: AssistantProgressEvent[];
  t: (key: string, fb?: string) => string;
}) {
  return (
    <details className="mt-3 rounded border border-outline-variant/30 bg-surface-container-low p-2.5 text-xs text-on-surface-variant">
      <summary className="cursor-pointer font-medium text-on-surface hover:text-primary focus:outline-none focus:ring-1 focus:ring-primary">
        {t("assistant.technical_details", "Technical details")} ({progress.length} {progress.length === 1 ? "step" : "steps"})
      </summary>
      <div className="mt-2 space-y-1.5 border-t border-outline-variant/20 pt-2 font-mono">
        {progress.map((step) => (
          <div className="flex items-center justify-between gap-2" key={`${step.sequence}-${step.phase}`}>
            <div className="flex items-center gap-1.5 truncate">
              <span
                className={`inline-block h-1.5 w-1.5 rounded-full ${
                  step.phase.includes("succeeded")
                    ? "bg-status-ready"
                    : step.phase.includes("failed")
                    ? "bg-status-failed"
                    : "bg-primary"
                }`}
              />
              <span className="font-semibold text-on-surface">{step.tool || step.phase}</span>
              {step.code && <span className="font-bold text-error">[{step.code}]</span>}
              <span className="truncate text-on-surface-variant">— {step.summary}</span>
            </div>
            <span className="shrink-0 font-mono text-on-surface-variant">{formatStepTime(step.timestamp)}</span>
          </div>
        ))}
      </div>
    </details>
  );
}

function formatStepTime(isoString?: string): string {
  if (!isoString) return "";
  try {
    const d = new Date(isoString);
    return d.toLocaleTimeString([], { hour12: false, hour: "2-digit", minute: "2-digit", second: "2-digit" });
  } catch {
    return "";
  }
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
