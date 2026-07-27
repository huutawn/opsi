"use client";

import { ConsoleRouter } from "@/features/console/console-router";
import { ProjectPicker } from "@/components/layout/project-picker";
import { Sidebar } from "@/components/layout/sidebar";
import { Topbar } from "@/components/layout/topbar";
import { useConsoleState } from "@/hooks/use-console-state";
import { LocalClient } from "@/lib/api/local-client";
import { useEffect, useMemo, useRef, useState } from "react";
import type { ConsoleController } from "@/features/console/types";

export function ConsoleShell() {
  const console = useConsoleState();

  if (!console.session && console.state.status === "loading") {
    return <AuthGate checking message="Checking the local credential and Cloud session." />;
  }
  if (console.session && !console.session.authenticated) {
    return <AuthGate message={console.state.message || "Sign in with GitHub to continue."} />;
  }
  if (console.state.status === "permission") {
    return <AuthGate message={console.state.message} />;
  }
  if (!console.session) {
    return <AuthGate message={console.state.message || "The local Opsi backend is unavailable."} />;
  }

  return (
    <div className="app">
      <a className="skipLink" href="#main">
        Skip to content
      </a>
      <Sidebar active={console.active} onSelect={console.setActive} />
      <main className="main" id="main">
        <Topbar
          session={console.session}
          onRefresh={() => void console.actions.load()}
        />
        <ProjectPicker onSelect={console.setProjectID} project={console.state.project} projects={console.state.projects} />
        <ConsoleRouter console={console} />
      </main>
      <MutationDialog console={console} />
    </div>
  );
}

function MutationDialog({ console }: { console: ConsoleController }) {
  const dialog = useRef<HTMLDivElement>(null);
  const [confirmation, setConfirmation] = useState({ key: "", value: "" });
  const review = console.review;

  useEffect(() => {
    if (!review) return;
    const currentReview = review;
    const element = dialog.current;
    const focusable = () => Array.from(element?.querySelectorAll<HTMLElement>("button:not(:disabled), input:not(:disabled)") ?? []);
    focusable()[0]?.focus();
    function keydown(event: KeyboardEvent) {
      if (event.key === "Escape" && currentReview.status !== "submitting") console.closeReview();
      if (event.key !== "Tab") return;
      const items = focusable();
      if (!items.length) return;
      const first = items[0];
      const last = items[items.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    }
    document.addEventListener("keydown", keydown);
    return () => document.removeEventListener("keydown", keydown);
    // The dialog lifecycle is keyed to one reviewed attempt; retry keeps the same key.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [review?.idempotencyKey]);

  if (!review) return null;
  const confirmationValue = confirmation.key === review.idempotencyKey ? confirmation.value : "";
  const confirmed = !review.confirmation || confirmationValue === review.confirmation;
  return (
    <div className="modalBackdrop">
      <div aria-labelledby="mutationTitle" aria-modal="true" className="modal" ref={dialog} role="dialog">
        <p className="eyebrow">Mutation review</p>
        <h2 id="mutationTitle">{review.operation} {review.targetType}</h2>
        <dl className="reviewFacts">
          <div><dt>Project</dt><dd>{review.project}</dd></div>
          <div><dt>Target</dt><dd>{review.targetType} / {review.targetID}</dd></div>
          <div><dt>Idempotency</dt><dd><code>{review.idempotencyKey}</code></dd></div>
        </dl>
        <h3>Proposed change</h3>
        <ul>{review.diff.map((item) => <li key={item}>{item}</li>)}</ul>
        <p className="risk"><b>Risk:</b> {review.risk}</p>
        {review.confirmation ? (
          <label>
            Type <code>{review.confirmation}</code> to confirm
            <input autoComplete="off" className="field" onChange={(event) => setConfirmation({ key: review.idempotencyKey, value: event.target.value })} value={confirmationValue} />
          </label>
        ) : null}
        {review.status === "submitting" ? <p aria-live="polite" role="status">Submitting to the Local backend...</p> : null}
        {review.status === "succeeded" ? <p aria-live="polite" className="success" role="status">{review.evidence}</p> : null}
        {review.status === "failed" ? <div className="errorBox" role="alert"><b>{review.error}</b><span>{review.nextAction}</span></div> : null}
        <div className="modalActions">
          <button disabled={review.status === "submitting"} onClick={console.closeReview} type="button">
            {review.status === "succeeded" ? "Close" : "Cancel"}
          </button>
          {review.status !== "succeeded" ? (
            <button className="primary" disabled={!confirmed || review.status === "submitting"} onClick={() => void console.submitReview()} type="button">
              {review.status === "failed" ? "Retry same attempt" : review.status === "submitting" ? "Submitting..." : "Confirm and submit"}
            </button>
          ) : null}
        </div>
      </div>
    </div>
  );
}

function AuthGate({ message, checking = false }: { message: string; checking?: boolean }) {
  const client = useMemo(() => new LocalClient(), []);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(() => authErrorMessage(callbackErrorCode()));

  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    if (!params.has("auth") && !params.has("auth_error")) return;
    params.delete("auth");
    params.delete("auth_error");
    const query = params.toString();
    window.history.replaceState({}, "", `${window.location.pathname}${query ? `?${query}` : ""}`);
  }, []);

  async function signIn() {
    setBusy(true);
    setError("");
    try {
      const next = await client.startLogin();
      window.location.assign(next.auth_url);
    } catch (cause) {
      setBusy(false);
      setError((cause as Error).message || "Opsi sign-in is unavailable.");
    }
  }

  return (
    <main className="authGate">
      <section className="authGateCard" aria-labelledby="authGateTitle">
        <div className="authMark" aria-hidden="true">O</div>
        <p className="eyebrow">Opsi Console</p>
        <h1 id="authGateTitle">{checking ? "Checking your session" : "Sign in to your workspace"}</h1>
        <p className="authGateText">{checking ? "Opsi is checking the local keychain and Cloud connection." : "Use your GitHub account to access Opsi projects. Project selection happens after your identity is verified."}</p>
        {error ? <div className="authGateError" role="alert">{error}</div> : null}
        {!error && message ? <p className="authGateHint">{message}</p> : null}
        {!checking ? (
          <button className="primary authGateButton" disabled={busy} onClick={() => void signIn()} type="button">
            {busy ? "Opening GitHub..." : "Continue with GitHub"}
          </button>
        ) : null}
        <p className="authGatePrivacy">Your GitHub token is exchanged by Cloud and the resulting PAT stays in the local OS keychain.</p>
      </section>
    </main>
  );
}

function callbackErrorCode() {
  if (typeof window === "undefined") return "";
  return new URLSearchParams(window.location.search).get("auth_error") || "";
}

function authErrorMessage(code: string) {
  switch (code) {
    case "GITHUB_ACCOUNT_UNLINKED":
      return "This GitHub account is not linked to an Opsi user. Sign in with the account invited to this Opsi organization.";
    case "OPSI_MEMBERSHIP_REQUIRED":
      return "Your GitHub-linked Opsi user does not have an active project membership.";
    case "PROJECT_SELECTION_REQUIRED":
      return "This GitHub account belongs to multiple Opsi projects and needs an explicit project selection.";
    case "GITHUB_AUTH_DENIED":
      return "GitHub authorization was cancelled. Start a new sign-in when ready.";
    case "AUTH_SESSION_EXPIRED":
      return "The sign-in request expired. Start a new GitHub sign-in.";
    case "AUTH_UNAVAILABLE":
      return "Opsi sign-in is temporarily unavailable. Try again shortly.";
    case "GITHUB_AUTH_FAILED":
      return "GitHub sign-in failed. Start a new sign-in and try again.";
    default:
      return "";
  }
}
