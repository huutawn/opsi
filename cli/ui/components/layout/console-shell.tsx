"use client";

import { ConsoleRouter } from "@/features/console/console-router";
import { ProjectPicker } from "@/components/layout/project-picker";
import { Sidebar } from "@/components/layout/sidebar";
import { Topbar } from "@/components/layout/topbar";
import { useConsoleState } from "@/hooks/use-console-state";
import { LocalClient } from "@/lib/api/local-client";
import { useEffect, useMemo, useState } from "react";

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
          orgID={console.orgID}
          onOrgID={console.setOrgID}
          onRefresh={() => void console.actions.load()}
        />
        <ProjectPicker onSelect={console.setProjectID} project={console.state.project} projects={console.state.projects} />
        <ConsoleRouter console={console} />
      </main>
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
