"use client";

import { type FormEvent, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Icon, PageHeader, StatusBadge } from "@/components/ui/primitives";
import { AssistantConversationPanel, type ChatMessage, type ReviewState } from "./assistant-conversation-panel";
import type { ConsoleController } from "@/features/console/types";
import {
  LocalAPIError,
  LocalClient,
  type AssistantConfigurationProposal,
  type AssistantConversationSummary,
  type AssistantProvider,
  type AssistantSourcePatchProposal,
  type AssistantTurn,
} from "@/lib/api/local-client";
import type { ServiceConfigurationDraft } from "@/lib/contracts/registry";
import { useI18n } from "@/lib/i18n";

export function AssistantView({ console }: { console: ConsoleController }) {
  const { t } = useI18n();
  const client = useMemo(() => new LocalClient(), []);
  const projectID = console.state.project?.id ?? console.route.projectID;
  const [providers, setProviders] = useState<AssistantProvider[]>([]);
  const [surface, setSurface] = useState("");
  const [historyAvailable, setHistoryAvailable] = useState(true);
  const [providerID, setProviderID] = useState("codex");
  const [conversationID, setConversationID] = useState("");
  const [conversations, setConversations] = useState<AssistantConversationSummary[]>([]);
  const [showHistory, setShowHistory] = useState(false);
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [prompt, setPrompt] = useState("");
  const [turn, setTurn] = useState<AssistantTurn | null>(null);
  const [failure, setFailure] = useState("");
  const [reviews, setReviews] = useState<Record<string, ReviewState>>({});
  const [reviewBusy, setReviewBusy] = useState("");
  const [patchBusy, setPatchBusy] = useState("");
  const interactionEpoch = useRef(0);

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
      setHistoryAvailable(result.history_available !== false);
      const ready = result.providers.find((item) => item.available && item.authenticated);
      if (ready) setProviderID(ready.id);
    }).catch((cause) => active && setFailure(errorMessage(cause, t)));
    return () => { active = false; };
  }, [client, t]);

  const refreshConversations = useCallback(() => {
    if (!projectID) return;
    client.assistantConversations(projectID, providerID)
      .then((res) => setConversations(res?.conversations ?? []))
      .catch((cause) => setFailure(errorMessage(cause, t)));
  }, [client, projectID, providerID, t]);

  const loadConversation = useCallback(async (id: string, liveTurn?: AssistantTurn) => {
    if (!projectID) return;
    try {
      const detail = await client.assistantConversation(projectID, id);
      setConversationID(detail.id);
      const runningMessage = [...(detail.messages ?? [])].reverse().find((message) => message.role === "assistant" && message.state === "running");
      const loadedMessages: ChatMessage[] = (detail.messages ?? []).filter((message) => message !== runningMessage).map((m) => ({
        id: m.id,
        turnId: m.turn_id,
        role: m.role,
        text: m.text,
        redacted: m.redacted,
        grounding: m.grounding,
        configurationProposals: liveTurn?.id === m.turn_id ? liveTurn.configuration_proposals : undefined,
        sourcePatchProposals: liveTurn?.id === m.turn_id ? liveTurn.source_patch_proposals : undefined,
        progress: m.progress,
        errorCode: m.error_code,
        diagnosticCode: m.diagnostic_code,
        error: m.error,
        nextAction: m.next_action,
        state: (m.state as "running" | "succeeded" | "failed") || (m.error_code ? "failed" : "succeeded"),
      }));
      if (liveTurn && !loadedMessages.some((message) => message.turnId === liveTurn.id && message.role === "assistant")) {
        const liveMessage: ChatMessage = {
          id: `msg-${liveTurn.id}-assistant`,
          turnId: liveTurn.id,
          role: "assistant",
          text: liveTurn.response || liveTurn.error || t("assistant.no_response", "No response."),
          state: liveTurn.state,
          errorCode: liveTurn.error_code,
          diagnosticCode: liveTurn.diagnostic_code,
          error: liveTurn.error,
          nextAction: liveTurn.next_action,
          progress: liveTurn.progress,
          grounding: liveTurn.grounding,
          configurationProposals: liveTurn.configuration_proposals,
          sourcePatchProposals: liveTurn.source_patch_proposals,
        };
        setMessages((current) => loadedMessages.length > 0 ? [...loadedMessages, liveMessage] : [...current.filter((message) => message.turnId !== liveTurn.id), liveMessage]);
      } else {
        setMessages(loadedMessages);
      }
      setTurn(runningMessage ? {
        id: runningMessage.turn_id,
        conversation_id: detail.id,
        provider_id: detail.provider_id,
        project_id: detail.project_id,
        state: "running",
        progress: runningMessage.progress,
        started_at: runningMessage.created_at,
      } : null);
    } catch (cause) {
      setFailure(errorMessage(cause, t));
    }
  }, [client, projectID, t]);

  // Restore the most recent local conversation whenever project/provider changes.
  useEffect(() => {
    if (!projectID) return;
    let active = true;
    const restoreEpoch = interactionEpoch.current;
    client.assistantConversations(projectID, providerID)
      .then((res) => {
        if (!active || restoreEpoch !== interactionEpoch.current) return;
        const convList = res?.conversations ?? [];
        setConversations(convList);
        if (convList.length > 0) void loadConversation(convList[0].id);
        else {
          setConversationID("");
          setMessages([]);
          setTurn(null);
        }
      })
      .catch((cause) => active && setFailure(errorMessage(cause, t)));
    return () => { active = false; };
  }, [client, loadConversation, projectID, providerID, t]);

  function startNewChat() {
    interactionEpoch.current += 1;
    setConversationID("");
    setMessages([]);
    setTurn(null);
    setFailure("");
    setShowHistory(false);
  }

  async function deleteChat(id: string) {
    if (!projectID) return;
    if (!window.confirm(t("assistant.confirm_delete_chat", "Delete this local chat history? This cannot be undone."))) return;
    try {
      await client.deleteAssistantConversation(projectID, id);
      setConversations((prev) => prev.filter((c) => c.id !== id));
      if (conversationID === id) {
        startNewChat();
      }
    } catch (cause) {
      setFailure(errorMessage(cause, t));
    }
  }

  useEffect(() => {
    const activeTurnID = turn?.state === "running" ? turn.id : "";
    if (!activeTurnID || !projectID) return;
    let active = true;
    const timer = window.setInterval(() => {
      client.assistantTurn(projectID, activeTurnID).then((next) => {
        if (!active) return;
        setTurn(next);
        if (next.state === "succeeded") {
          window.clearInterval(timer);
          refreshConversations();
          void loadConversation(next.conversation_id, next);
        } else if (next.state === "failed") {
          window.clearInterval(timer);
          refreshConversations();
          void loadConversation(next.conversation_id, next);
        }
      }).catch((cause) => {
        if (active) setFailure(errorMessage(cause, t));
        window.clearInterval(timer);
      });
    }, 1000);
    return () => { active = false; window.clearInterval(timer); };
  }, [client, loadConversation, projectID, refreshConversations, turn?.id, turn?.state, t]);

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
    interactionEpoch.current += 1;
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
      refreshConversations();
    } catch (cause) {
      setFailure(errorMessage(cause, t));
    }
  }

  async function retryTurn(turnID: string) {
    if (!projectID || turn?.state === "running") return;
    interactionEpoch.current += 1;
    setFailure("");
    try {
      const next = await client.retryAssistantTurn(projectID, turnID, crypto.randomUUID());
      setConversationID(next.conversation_id);
      setTurn(next);
      refreshConversations();
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

  const activeConv = conversations.find((c) => c.id === conversationID);

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
          <strong>{t("assistant.action_failed", "Assistant action failed")}:</strong> {failure}
        </div>
      )}
      {!historyAvailable && (
        <div className="border border-status-warning/40 bg-status-warning/10 p-4 text-sm text-on-surface" role="alert">
          {t("assistant.history_unavailable", "Local chat history is unavailable. Repair the history file and restart Opsi before chatting.")}
        </div>
      )}

      <div className="grid items-start gap-6 lg:grid-cols-[minmax(0,1fr)_320px]">
        <AssistantConversationPanel
          activeConv={activeConv}
          canMutate={canMutate}
          conversationID={conversationID}
          conversations={conversations}
          historyAvailable={historyAvailable}
          messages={messages}
          onApplySourcePatch={applySourcePatch}
          onCreateReview={createReview}
          onDeleteConversation={deleteChat}
          onNewChat={startNewChat}
          onPromptChange={setPrompt}
          onRetryTurn={retryTurn}
          onReviewAction={reviewAction}
          onSelectConversation={(id) => {
            loadConversation(id);
            setShowHistory(false);
          }}
          onSelectStarter={setPrompt}
          onSubmit={submit}
          onToggleHistory={() => setShowHistory((prev) => !prev)}
          overallReady={overallReady}
          patchBusy={patchBusy}
          prompt={prompt}
          provider={provider}
          reviewBusy={reviewBusy}
          reviews={reviews}
          showHistory={showHistory}
          starters={starters}
          turn={turn}
        />

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


function errorMessage(cause: unknown, t?: (key: string, fb?: string) => string) {
  if (cause instanceof LocalAPIError || cause instanceof Error) {
    return cause.message;
  }
  return t ? t("common.error", "Unexpected local assistant error.") : "Unexpected local assistant error.";
}
