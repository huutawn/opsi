"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { ConsoleRouter } from "@/features/console/console-router";
import type { ConsoleController } from "@/features/console/types";
import { ContextHeader } from "@/components/layout/context-header";
import { Sidebar } from "@/components/navigation/sidebar";
import { useConsoleState } from "@/hooks/use-console-state";
import { LocalClient } from "@/lib/api/local-client";
import { currentEnvironment } from "@/lib/presentation/infrastructure/model";

export function AppShell() {
  const console = useConsoleState();
  const [navigationOpen, setNavigationOpen] = useState(false);
  const [collapsed, setCollapsed] = useState(false);
  const menuButton = useRef<HTMLButtonElement>(null);
  const navigation = useRef<HTMLElement>(null);
  const main = useRef<HTMLElement>(null);
  function closeNavigation() {
    setNavigationOpen(false);
    window.requestAnimationFrame(() => menuButton.current?.focus());
  }
  useEffect(() => {
    if (!navigationOpen) return;
    const background = main.current;
    const skipLink = document.querySelector<HTMLElement>(".skipLink");
    if (background) background.inert = true;
    if (skipLink) skipLink.inert = true;
    const drawer = navigation.current;
    const focusable = () => focusableElements(drawer);
    window.requestAnimationFrame(() => drawer?.querySelector<HTMLElement>('[aria-label="Close navigation"]')?.focus());
    function keydown(event: KeyboardEvent) {
      if (event.key === "Escape") { event.preventDefault(); closeNavigation(); return; }
      if (event.key !== "Tab") return;
      const items = focusable();
      if (!items.length) return;
      const first = items[0];
      const last = items[items.length - 1];
      if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
      else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
    }
    document.addEventListener("keydown", keydown);
    return () => {
      document.removeEventListener("keydown", keydown);
      if (background) background.inert = false;
      if (skipLink) skipLink.inert = false;
    };
  }, [navigationOpen]);

  if (!console.session && console.state.status === "loading") return <AuthGate checking message="Checking the local credential and Cloud session." />;
  if (console.session && !console.session.authenticated) return <AuthGate message={console.state.message || "Sign in with GitHub to continue."} />;
  if (console.state.status === "permission") return <AuthGate message={console.state.message} />;
  if (!console.session) return <AuthGate message={console.state.message || "The local Opsi backend is unavailable."} />;

  const environments = console.state.foundation.placement?.environments ?? [];
  const environment = currentEnvironment(console.state.foundation.placement, console.route.environment ?? "");
  const environmentName = environment?.name ?? (environments.length > 1 ? "Choose environment" : "Environment not reported");
  return <div className={`app ${collapsed ? "sidebarCollapsed" : ""}`}>
    <a className="skipLink" href="#main">Skip to content</a>
    <Sidebar collapsed={collapsed} drawerRef={navigation} environment={environmentName} onBrowse={() => console.navigate({ projectID: "", view: "projects", tab: "" })} onClose={closeNavigation} onCollapse={() => setCollapsed((value) => !value)} onNavigate={console.navigate} onSelectProject={console.setProjectID} open={navigationOpen} orgID={console.session.org_id ?? ""} project={console.state.project} projects={console.state.projects} route={console.route} />
    <main className="main" id="main" ref={main} tabIndex={-1}>
      <ContextHeader environment={environmentName} environmentID={environment?.id ?? ""} environments={environments} lastUpdated={latestUpdate(console)} menuButtonRef={menuButton} onEnvironment={(id) => console.navigate({ environment: id })} onMenu={() => setNavigationOpen(true)} onRefresh={() => void console.actions.load()} project={console.state.project} route={console.route} serviceScope={console.state.services.find((item) => item.id === console.route.service)?.name} session={console.session} />
      <ConsoleRouter console={console} />
    </main>
    <MutationDialog console={console} />
  </div>;
}

function MutationDialog({ console }: { console: ConsoleController }) {
  const dialog = useRef<HTMLDialogElement>(null);
  const credential = useRef<HTMLInputElement | HTMLTextAreaElement>(null);
  const returnFocus = useRef<HTMLElement | null>(null);
  const [confirmation, setConfirmation] = useState({ key: "", value: "" });
  const [hasCredential, setHasCredential] = useState(false);
  const review = console.review;
  useEffect(() => {
    if (!review) return;
    returnFocus.current = document.querySelector<HTMLElement>(`[data-review-trigger="${review.operation}"]`)
      ?? document.activeElement as HTMLElement | null;
    const element = dialog.current;
    element?.showModal();
    const keydown = (event: KeyboardEvent) => trapTabKey(event, element);
    element?.addEventListener("keydown", keydown);
    window.requestAnimationFrame(() => (credential.current ?? focusableElements(element)[0])?.focus());
    return () => {
      element?.removeEventListener("keydown", keydown);
      if (element?.open) element.close();
      returnFocus.current?.focus();
    };
    // One reviewed attempt keeps one idempotency key across retries.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [review?.idempotencyKey]);
  if (!review) return null;
  const confirmationValue = confirmation.key === review.idempotencyKey ? confirmation.value : "";
  const confirmed = (!review.confirmation || confirmationValue === review.confirmation) && (!review.credential || hasCredential);
  function submit() {
    const value = review?.credential ? credential.current?.value ?? "" : undefined;
    if (credential.current) credential.current.value = "";
    setHasCredential(false);
    void console.submitReview(value);
  }
  const credentialField = review.credential && review.status !== "submitting" && review.status !== "succeeded" ? <label>{review.credential.label}{review.credential.label.includes("private key")
    ? <textarea autoComplete="off" className="textarea" onInput={(event) => setHasCredential(Boolean(event.currentTarget.value))} ref={credential as React.RefObject<HTMLTextAreaElement>} aria-label={review.credential.inputLabel} />
    : <input autoComplete="new-password" className="field" onInput={(event) => setHasCredential(Boolean(event.currentTarget.value))} ref={credential as React.RefObject<HTMLInputElement>} type="password" aria-label={review.credential.inputLabel} />}</label> : null;
  return <dialog aria-describedby="mutationRisk" aria-labelledby="mutationTitle" className="modal" onCancel={(event) => { event.preventDefault(); if (review.status !== "submitting") console.closeReview(); }} ref={dialog}>
    <p className="eyebrow">Review and confirm</p><h2 id="mutationTitle">{review.operation} {review.targetType}</h2>
    <dl className="reviewFacts"><div><dt>Project</dt><dd>{review.project}</dd></div><div><dt>Target</dt><dd>{review.targetType} / {review.targetID}</dd></div><div><dt>Required permission</dt><dd>Existing Local API authorization</dd></div></dl>
    <h3>Before / after</h3><ul>{review.diff.map((item) => <li key={item}>{item}</li>)}</ul>
    <p className="risk" id="mutationRisk"><b>Risk:</b> {review.risk}</p>
    <details className="technicalDetails"><summary>Technical details</summary><span>Idempotency key</span><code>{review.idempotencyKey}</code></details>
    {review.confirmation ? <label>Type <code>{review.confirmation}</code> to confirm<input autoComplete="off" className="field" onChange={(event) => setConfirmation({ key: review.idempotencyKey, value: event.target.value })} value={confirmationValue} /></label> : null}
    {credentialField}
    {review.status === "submitting" ? <p aria-live="polite" role="status">Submitting to the Local backend…</p> : null}
    {review.status === "succeeded" ? <p aria-live="polite" className="success" role="status">{review.evidence}</p> : null}
    {review.status === "failed" ? <div className="errorBox" role="alert"><b>{review.error}</b><span>{review.nextAction}</span></div> : null}
    <div className="modalActions"><button disabled={review.status === "submitting"} onClick={console.closeReview} type="button">{review.status === "succeeded" ? "Close" : "Cancel"}</button>{review.status !== "succeeded" ? <button className="primary" disabled={!confirmed || review.status === "submitting"} onClick={submit} type="button">{review.status === "failed" ? "Retry same attempt" : review.status === "submitting" ? "Submitting…" : "Confirm and submit"}</button> : null}</div>
  </dialog>;
}

function AuthGate({ message, checking = false }: { message: string; checking?: boolean }) {
  const client = useMemo(() => new LocalClient(), []);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(() => authErrorMessage(callbackErrorCode()));
  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    if (!params.has("auth") && !params.has("auth_error")) return;
    params.delete("auth"); params.delete("auth_error");
    const query = params.toString();
    window.history.replaceState({}, "", `${window.location.pathname}${query ? `?${query}` : ""}`);
  }, []);
  async function signIn() { setBusy(true); setError(""); try { const projectID = new URLSearchParams(window.location.search).get("project") || undefined; const next = await client.startLogin(projectID); window.location.assign(next.auth_url); } catch (cause) { setBusy(false); setError((cause as Error).message || "Opsi sign-in is unavailable."); } }
  return <main className="authGate"><section className="authGateCard" aria-labelledby="authGateTitle"><div className="authMark" aria-hidden="true">O</div><p className="eyebrow">Opsi</p><h1 id="authGateTitle">{checking ? "Checking your session" : "Sign in to Opsi"}</h1><p className="authGateText">{checking ? "Opsi is checking the local keychain and Cloud connection." : "Continue with the GitHub account linked to your Opsi workspace."}</p>{error ? <div className="authGateError" role="alert"><b>Sign-in failed.</b> {error} Start a new sign-in when ready.</div> : null}{!error && message ? <p className="authGateHint">{message}</p> : null}{!checking ? <button className="primary authGateButton" disabled={busy} onClick={() => void signIn()} type="button">{busy ? "Opening GitHub…" : "Continue with GitHub"}</button> : null}<p className="authGatePrivacy">The resulting PAT stays in your OS keychain.</p></section></main>;
}

function callbackErrorCode() { return typeof window === "undefined" ? "" : new URLSearchParams(window.location.search).get("auth_error") || ""; }
function latestUpdate(console: ConsoleController) {
  const deployments = Array.isArray(console.state.deployments) ? console.state.deployments : [];
  const nodes = Array.isArray(console.state.nodes) ? console.state.nodes : [];
  return deployments.map((item) => item.updated_at || item.created_at).filter(Boolean).sort().at(-1)
    ?? nodes.map((item) => item.last_seen_at).filter((value): value is string => Boolean(value)).sort().at(-1);
}
function authErrorMessage(code: string) {
  return ({ GITHUB_ACCOUNT_UNLINKED: "This GitHub account is not linked to an Opsi user.", OPSI_MEMBERSHIP_REQUIRED: "This account has no active Opsi project membership.", PROJECT_SELECTION_REQUIRED: "This account needs an explicit project selection.", GITHUB_AUTH_DENIED: "GitHub authorization was cancelled.", AUTH_SESSION_EXPIRED: "The sign-in request expired.", AUTH_UNAVAILABLE: "Opsi sign-in is temporarily unavailable.", GITHUB_AUTH_FAILED: "GitHub sign-in failed." } as Record<string, string>)[code] ?? "";
}

function focusableElements(root: HTMLElement | null | undefined) {
  return Array.from(root?.querySelectorAll<HTMLElement>('a[href], button:not(:disabled), summary, input:not(:disabled), select:not(:disabled), textarea:not(:disabled), [tabindex]:not([tabindex="-1"])') ?? [])
    .filter((element) => element.getClientRects().length > 0);
}

function trapTabKey(event: KeyboardEvent, root: HTMLElement | null) {
  if (event.key !== "Tab") return;
  const items = focusableElements(root);
  if (!items.length) return;
  const first = items[0];
  const last = items[items.length - 1];
  if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
  else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
}
