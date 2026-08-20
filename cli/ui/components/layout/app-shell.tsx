"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { ConsoleRouter } from "@/features/console/console-router";
import type { ConsoleController } from "@/features/console/types";
import { ContextHeader } from "@/components/layout/context-header";
import { Sidebar } from "@/components/navigation/sidebar";
import { useConsoleState } from "@/hooks/use-console-state";
import { LocalClient, type SelectableProject } from "@/lib/api/local-client";
import { currentEnvironment } from "@/lib/presentation/infrastructure/model";
import { Button, Icon } from "@/components/ui/primitives";

export function AppShell() {
  const console = useConsoleState();
  const [navigationOpen, setNavigationOpen] = useState(false);
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
    if (background) background.inert = true;
    const drawer = navigation.current;
    const focusable = () => focusableElements(drawer);
    window.requestAnimationFrame(() => drawer?.querySelector<HTMLElement>('[aria-label="Close navigation"]')?.focus());
    function keydown(event: KeyboardEvent) {
      if (event.key === "Escape") {
        event.preventDefault();
        closeNavigation();
        return;
      }
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
    return () => {
      document.removeEventListener("keydown", keydown);
      if (background) background.inert = false;
    };
  }, [navigationOpen]);

  if (!console.session && console.state.status === "loading") {
    return <AuthGate checking message="Checking local credentials and Cloud session…" />;
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

  const environments = console.state.foundation.placement?.environments ?? [];
  const environment = currentEnvironment(console.state.foundation.placement, console.route.environment ?? "");
  const environmentName = environment?.name ?? (environments.length > 1 ? "Choose environment" : "Production");

  return (
    <div className="min-h-screen bg-background text-on-background font-body-md">
      <a
        className="sr-only focus:not-sr-only focus:fixed focus:top-4 focus:left-4 focus:z-50 focus:bg-primary focus:text-on-primary focus:px-4 focus:py-2 focus:rounded-lg shadow-lg"
        href="#main-content"
      >
        Skip to content
      </a>

      <Sidebar
        agentConnected={console.session.agent_connected}
        cloudConnected={console.session.cloud_connected}
        drawerRef={navigation}
        environment={environmentName}
        environmentID={environment?.id ?? ""}
        environments={environments}
        onBrowse={() => console.navigate({ projectID: "", view: "projects", tab: "" })}
        onClose={closeNavigation}
        onEnvironment={(id) => console.navigate({ environment: id })}
        onNavigate={console.navigate}
        onSelectProject={console.setProjectID}
        open={navigationOpen}
        orgID={console.session.org_id ?? ""}
        project={console.state.project}
        projects={console.state.projects}
        route={console.route}
      />

      <div className="pl-0 lg:pl-72 flex flex-col min-h-screen max-w-full overflow-x-hidden">
        <ContextHeader
          environment={environmentName}
          lastUpdated={latestUpdate(console)}
          menuButtonRef={menuButton}
          onMenu={() => setNavigationOpen(true)}
          onRefresh={() => void console.actions.load()}
          project={console.state.project}
          route={console.route}
          serviceScope={console.state.services.find((item) => item.id === console.route.service)?.name}
          session={console.session}
        />

        <main className="relative pt-16 min-h-screen bg-surface flex-1 max-w-full overflow-x-hidden" id="main-content" ref={main} tabIndex={-1}>
          <ConsoleRouter console={console} />
        </main>
      </div>

      <MutationDialog console={console} />
    </div>
  );
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
    returnFocus.current =
      document.querySelector<HTMLElement>(`[data-review-trigger="${review.operation}"]`) ??
      (document.activeElement as HTMLElement | null);
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
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [review?.idempotencyKey]);

  if (!review) return null;
  const confirmationValue = confirmation.key === review.idempotencyKey ? confirmation.value : "";
  const confirmed =
    (!review.confirmation || confirmationValue === review.confirmation) && (!review.credential || hasCredential);

  function submit() {
    const value = review?.credential ? credential.current?.value ?? "" : undefined;
    if (credential.current) credential.current.value = "";
    setHasCredential(false);
    void console.submitReview(value);
  }

  const credentialField =
    review.credential && review.status !== "submitting" && review.status !== "succeeded" ? (
      <div className="flex flex-col gap-2">
        <label className="font-label-sm text-xs text-on-surface-variant uppercase tracking-wider">
          {review.credential.label}
        </label>
        {review.credential.label.includes("private key") ? (
          <textarea
            aria-label={review.credential.inputLabel}
            autoComplete="off"
            className="w-full bg-surface-container-highest border border-outline-variant/30 rounded-lg p-3 font-code-md text-xs text-on-surface focus:outline-none focus:border-primary/50 min-h-[100px]"
            onInput={(event) => setHasCredential(Boolean(event.currentTarget.value))}
            ref={credential as React.RefObject<HTMLTextAreaElement>}
          />
        ) : (
          <input
            aria-label={review.credential.inputLabel}
            autoComplete="new-password"
            className="w-full bg-surface-container-highest border border-outline-variant/30 rounded-lg p-3 font-body-md text-sm text-on-surface focus:outline-none focus:border-primary/50"
            onInput={(event) => setHasCredential(Boolean(event.currentTarget.value))}
            ref={credential as React.RefObject<HTMLInputElement>}
            type="password"
          />
        )}
      </div>
    ) : null;

  return (
    <dialog
      aria-describedby="mutationRisk"
      aria-label={`${review.operation} ${review.targetType}`}
      aria-labelledby="mutationTitle"
      className="fixed inset-0 m-auto bg-surface-container-low border border-outline-variant/30 rounded-2xl shadow-2xl p-6 max-w-lg w-full backdrop:bg-background/80 backdrop:backdrop-blur-sm z-50 text-on-surface flex flex-col gap-5"
      onCancel={(event) => {
        event.preventDefault();
        if (review.status !== "submitting") console.closeReview();
      }}
      ref={dialog}
    >
      <div className="flex items-start justify-between border-b border-outline-variant/20 pb-4">
        <div>
          <span className="font-label-sm text-xs text-on-surface-variant uppercase tracking-wider block mb-1">
            Mutation Review
          </span>
          <h2 id="mutationTitle" className="font-headline-md text-xl text-on-surface">
            {review.operation} {review.targetType}
          </h2>
        </div>
        <button
          className="p-1 text-on-surface-variant hover:text-on-surface rounded-lg"
          disabled={review.status === "submitting"}
          onClick={console.closeReview}
          type="button"
        >
          <Icon name="close" className="text-[20px]" />
        </button>
      </div>

      <div className="bg-surface-container rounded-xl p-4 border border-outline-variant/20 grid grid-cols-2 gap-3 text-xs">
        <div>
          <span className="font-label-sm text-on-surface-variant uppercase block">Project</span>
          <span className="font-body-md text-on-surface font-medium">{review.project}</span>
        </div>
        <div>
          <span className="font-label-sm text-on-surface-variant uppercase block">Target</span>
          <span className="font-code-md text-on-surface font-medium truncate block">{review.targetType} / {review.targetID}</span>
        </div>
      </div>

      <div className="flex flex-col gap-2">
        <span className="font-label-sm text-xs text-on-surface-variant uppercase tracking-wider">
          Proposed Changes
        </span>
        <div className="bg-surface-container-lowest p-3 rounded-lg border border-outline-variant/20 space-y-1 font-code-md text-xs">
          {review.diff.map((item, idx) => (
            <div key={idx} className="flex items-center gap-2 text-on-surface">
              <span className="text-secondary">•</span>
              <span>{item}</span>
            </div>
          ))}
        </div>
      </div>

      <div className="bg-status-warning/10 border border-status-warning/30 rounded-xl p-4" id="mutationRisk">
        <div className="flex items-start gap-2 text-status-warning text-xs font-body-md">
          <Icon name="warning" className="text-[18px] shrink-0 mt-0.5" />
          <div>
            <strong className="font-semibold block mb-0.5">Operational Risk Notice:</strong>
            <span className="text-on-surface-variant">{review.risk}</span>
          </div>
        </div>
      </div>

      {review.confirmation ? (
        <div className="flex flex-col gap-2">
          <label className="font-label-sm text-xs text-on-surface-variant uppercase tracking-wider block">
            Type <code className="text-primary font-bold">{review.confirmation}</code> to confirm:
            <input
              aria-label={`Type ${review.confirmation} to confirm`}
              autoComplete="off"
              className="w-full mt-2 bg-surface-container-highest border border-outline-variant/30 rounded-lg p-3 font-code-md text-sm text-on-surface focus:outline-none focus:border-primary/50"
              onChange={(event) => setConfirmation({ key: review.idempotencyKey, value: event.target.value })}
              value={confirmationValue}
            />
          </label>
        </div>
      ) : null}

      {credentialField}

      {review.status === "submitting" ? (
        <div className="flex items-center gap-2 text-sm text-status-progress" role="status">
          <Icon name="sync" className="animate-spin text-[18px]" />
          <span>Submitting reviewed mutation to Local Edge…</span>
        </div>
      ) : null}

      {review.status === "succeeded" ? (
        <div className="bg-status-ready/10 border border-status-ready/30 rounded-xl p-3 text-status-ready text-sm flex items-center gap-2" role="status">
          <Icon name="check_circle" className="text-[18px]" />
          <span>{review.evidence}</span>
        </div>
      ) : null}

      {review.status === "failed" ? (
        <div className="errorBox bg-error-container/20 border border-error/30 rounded-xl p-3 text-error text-xs flex flex-col gap-1" role="alert">
          <div className="flex items-center gap-2 font-bold">
            <Icon name="error" className="text-[18px]" />
            <span>{review.error}</span>
          </div>
          {review.nextAction ? <span className="text-on-surface-variant pl-6">{review.nextAction}</span> : null}
        </div>
      ) : null}

      <details className="text-xs text-on-surface-variant">
        <summary className="cursor-pointer font-medium hover:text-on-surface select-none">Technical details</summary>
        <div className="pt-2 space-y-1 font-code-md">
          <p>
            <span>Idempotency key</span>: <code>{review.idempotencyKey}</code>
          </p>
        </div>
      </details>

      <div className="flex items-center justify-end gap-3 pt-4 border-t border-outline-variant/20">
        <Button
          disabled={review.status === "submitting"}
          onClick={console.closeReview}
          type="button"
          variant="secondary"
        >
          {review.status === "succeeded" ? "Close" : "Cancel"}
        </Button>
        {review.status !== "succeeded" ? (
          <Button
            disabled={!confirmed || review.status === "submitting"}
            onClick={submit}
            type="button"
            variant={review.operation.includes("destroy") || review.operation.includes("remove") ? "danger" : "primary"}
          >
            {review.status === "failed"
              ? "Retry same attempt"
              : review.status === "submitting"
                ? "Submitting…"
                : "Confirm and submit"}
          </Button>
        ) : null}
      </div>
    </dialog>
  );
}

function AuthGate({ message, checking = false }: { message: string; checking?: boolean }) {
  const client = useMemo(() => new LocalClient(), []);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(() => authErrorMessage(callbackErrorCode()));
  const [selectionID, setSelectionID] = useState(() => callbackSelectionID());
  const [projects, setProjects] = useState<SelectableProject[] | null>(null);
  const [selectedProject, setSelectedProject] = useState<string>("");
  const [loadingProjects, setLoadingProjects] = useState(() => Boolean(callbackSelectionID()));

  useEffect(() => {
    if (!selectionID) {
      const params = new URLSearchParams(window.location.search);
      if (!params.has("auth") && !params.has("auth_error")) return;
      params.delete("auth");
      params.delete("auth_error");
      const query = params.toString();
      window.history.replaceState({}, "", `${window.location.pathname}${query ? `?${query}` : ""}`);
      return;
    }
    let ignore = false;
    client
      .getSelectableProjects(selectionID)
      .then((res) => {
        if (ignore) return;
        setProjects(res.projects);
        if (res.projects && res.projects.length > 0) {
          setSelectedProject(res.projects[0].id);
        }
      })
      .catch((cause) => {
        if (ignore) return;
        setError((cause as Error).message || "The project selection session has expired. Start a new sign-in.");
        setSelectionID("");
      })
      .finally(() => {
        if (!ignore) {
          setLoadingProjects(false);
        }
      });
    return () => {
      ignore = true;
    };
  }, [client, selectionID]);

  async function signIn() {
    setBusy(true);
    setError("");
    try {
      const projectID = new URLSearchParams(window.location.search).get("project") || undefined;
      const next = await client.startLogin(projectID);
      window.location.assign(next.auth_url);
    } catch (cause) {
      setBusy(false);
      setError((cause as Error).message || "Opsi sign-in is unavailable.");
    }
  }

  async function completeSelection() {
    if (!selectionID || !selectedProject) return;
    setBusy(true);
    setError("");
    try {
      await client.selectProject(selectionID, selectedProject);
      window.location.assign(`/?auth=ok&project=${encodeURIComponent(selectedProject)}`);
    } catch (cause) {
      setBusy(false);
      setError((cause as Error).message || "Failed to select project. Please try again.");
    }
  }

  function cancelSelection() {
    setSelectionID("");
    setProjects(null);
    setSelectedProject("");
    const params = new URLSearchParams(window.location.search);
    params.delete("auth");
    params.delete("selection_id");
    const query = params.toString();
    window.history.replaceState({}, "", `${window.location.pathname}${query ? `?${query}` : ""}`);
  }

  return (
    <main className="min-h-screen flex flex-col items-center justify-center p-4 lg:p-margin-desktop bg-background text-on-background relative overflow-hidden">
      <div className="absolute inset-0 bg-[radial-gradient(ellipse_at_top_right,_var(--tw-gradient-stops))] from-primary/10 via-background to-background pointer-events-none" />

      <div className="relative z-10 flex flex-col items-center w-full max-w-[440px]">
        <div className="w-full bg-surface-container-low shadow-2xl rounded-2xl overflow-hidden backdrop-blur-sm border border-outline-variant/20">
          <div className="p-8 sm:p-10 flex flex-col items-center text-center">
            {/* Logo */}
            <div className="w-16 h-16 rounded-xl bg-white flex flex-col items-center justify-center shadow-md mb-6 p-2">
              <svg className="w-8 h-8 text-[#00a6e0]" viewBox="0 0 48 48" fill="none" xmlns="http://www.w3.org/2000/svg">
                <path d="M24 4L40 13.24V31.76L24 41L8 31.76V13.24L24 4Z" stroke="#00354a" strokeWidth="3" fill="#c4e7ff" />
                <path d="M24 4V22.5M24 22.5L40 13.24M24 22.5L8 13.24" stroke="#00354a" strokeWidth="3" />
                <path d="M24 22.5V41" stroke="#00354a" strokeWidth="3" />
                <circle cx="24" cy="22.5" r="3.5" fill="#00a6e0" />
              </svg>
              <span className="text-[9px] font-bold text-[#00354a] tracking-tight -mt-0.5 font-headline-md">Opsi</span>
            </div>

            {selectionID && (loadingProjects || projects) ? (
              <>
                <h1 className="font-headline-md text-2xl text-on-surface mb-2 tracking-tight">Choose a Project</h1>
                <p className="font-body-md text-sm text-on-surface-variant mb-6 leading-relaxed max-w-[320px]">
                  Select a project authorized for your GitHub account.
                </p>
                {error ? (
                  <div className="w-full bg-error-container/20 border border-error/30 text-error p-3 rounded-lg text-xs mb-4 text-left" role="alert">
                    {error}
                  </div>
                ) : null}
                {loadingProjects ? (
                  <div className="flex items-center gap-2 text-sm text-on-surface-variant py-4">
                    <Icon name="sync" className="animate-spin text-[18px]" />
                    <span>Loading accessible projects…</span>
                  </div>
                ) : projects && projects.length > 0 ? (
                  <form className="w-full flex flex-col gap-4 text-left" onSubmit={(e) => { e.preventDefault(); void completeSelection(); }}>
                    <div className="flex flex-col gap-2 max-h-60 overflow-y-auto">
                      {projects.map((p) => (
                        <label
                          key={p.id}
                          className={`flex items-center gap-3 p-3 rounded-xl border cursor-pointer transition-colors ${
                            selectedProject === p.id
                              ? "bg-primary/10 border-primary"
                              : "bg-surface-container border-outline-variant/30 hover:bg-surface-container-high"
                          }`}
                        >
                          <input
                            checked={selectedProject === p.id}
                            className="accent-primary"
                            name="project"
                            onChange={() => setSelectedProject(p.id)}
                            type="radio"
                            value={p.id}
                          />
                          <div className="flex flex-col min-w-0">
                            <span className="font-body-md text-sm font-semibold text-on-surface truncate">
                              {p.name || p.id}
                            </span>
                            <span className="font-code-md text-xs text-on-surface-variant truncate">
                              {p.id} {p.role ? `• ${p.role}` : ""}
                            </span>
                          </div>
                        </label>
                      ))}
                    </div>
                    <Button disabled={busy || !selectedProject} type="submit" variant="primary">
                      {busy ? "Signing in…" : "Continue with Selected Project"}
                    </Button>
                    <button
                      className="text-xs text-on-surface-variant hover:text-on-surface underline text-center cursor-pointer"
                      disabled={busy}
                      onClick={cancelSelection}
                      type="button"
                    >
                      Cancel
                    </button>
                  </form>
                ) : (
                  <div className="space-y-4">
                    <p className="text-sm text-on-surface-variant">No accessible projects found.</p>
                    <Button onClick={cancelSelection} type="button" variant="secondary">
                      Back to sign-in
                    </Button>
                  </div>
                )}
              </>
            ) : (
              <>
                <h1 className="font-headline-md text-2xl text-on-surface mb-2 tracking-tight">
                  {checking ? "Checking Session" : "Sign in to Opsi"}
                </h1>
                <p className="font-body-md text-sm text-on-surface-variant mb-8 leading-relaxed max-w-[320px]">
                  {checking
                    ? "Verifying local credentials and Cloud session."
                    : "Deploy and manage your infrastructure with factual state management."}
                </p>

                {error ? (
                  <div className="w-full bg-error-container/20 border border-error/30 text-error p-3 rounded-lg text-xs mb-4 text-left" role="alert">
                    <strong>Sign-in failed: </strong>{error}
                  </div>
                ) : null}

                {!error && message && !checking ? (
                  <div className="w-full bg-surface-container p-3 rounded-lg text-xs text-on-surface-variant mb-4 text-left border border-outline-variant/20">
                    {message}
                  </div>
                ) : null}

                {!checking ? (
                  <button
                    className="w-full group relative flex items-center justify-center gap-3 bg-[#24292e] hover:bg-[#2f363d] text-white py-3 px-6 rounded-lg font-body-md text-[15px] font-semibold transition-all duration-200 shadow-sm cursor-pointer disabled:opacity-50"
                    disabled={busy}
                    onClick={() => void signIn()}
                    type="button"
                  >
                    <svg aria-hidden="true" className="w-5 h-5 fill-current" viewBox="0 0 24 24">
                      <path
                        clipRule="evenodd"
                        d="M12 2C6.477 2 2 6.477 2 12c0 4.42 2.865 8.166 6.839 9.489.5.092.682-.217.682-.482 0-.237-.008-.866-.013-1.7-2.782.603-3.369-1.34-3.369-1.34-.454-1.156-1.11-1.463-1.11-1.463-.908-.62.069-.608.069-.608 1.003.07 1.531 1.03 1.531 1.03.892 1.529 2.341 1.087 2.91.831.092-.646.35-1.086.636-1.336-2.22-.253-4.555-1.11-4.555-4.943 0-1.091.39-1.984 1.03-2.682-.103-.253-.447-1.27.098-2.646 0 0 .84-.269 2.75 1.025A9.564 9.564 0 0112 6.844c.85.004 1.705.114 2.504.336 1.909-1.294 2.747-1.025 2.747-1.025.546 1.376.202 2.394.1 2.646.64.699 1.026 1.591 1.026 2.682 0 3.841-2.337 4.687-4.565 4.935.359.309.678.919.678 1.852 0 1.336-.012 2.415-.012 2.743 0 .267.18.578.688.48C19.138 20.161 22 16.416 22 12c0-5.523-4.477-10-10-10z"
                        fillRule="evenodd"
                      />
                    </svg>
                    <span>{busy ? "Opening GitHub…" : "Continue with GitHub"}</span>
                  </button>
                ) : null}

                <div className="mt-6 flex items-center gap-2 text-on-surface-variant/70 font-body-md text-[13px]">
                  <Icon name="info" className="text-[16px]" />
                  <span>Opsi uses GitHub for identity and repository access.</span>
                </div>
              </>
            )}
          </div>

          <div className="bg-surface-container-highest/30 px-8 py-4 flex flex-col items-center border-t border-outline-variant/10">
            <div className="flex gap-4 font-body-md text-[13px] text-on-surface-variant">
              <span className="hover:text-primary transition-colors">Documentation</span>
              <span className="text-outline-variant">•</span>
              <span className="hover:text-primary transition-colors">Support</span>
              <span className="text-outline-variant">•</span>
              <span className="flex items-center gap-1.5 text-on-surface-variant">
                <span className="w-2 h-2 rounded-full bg-status-ready inline-block" />
                Status
              </span>
            </div>
          </div>
        </div>

        <div className="mt-8 font-label-sm text-xs text-on-surface-variant/40 tracking-wider">
          OPSI CLI v2.4.1
        </div>
      </div>
    </main>
  );
}

function callbackErrorCode() {
  return typeof window === "undefined" ? "" : new URLSearchParams(window.location.search).get("auth_error") || "";
}

function callbackSelectionID() {
  if (typeof window === "undefined") return "";
  const params = new URLSearchParams(window.location.search);
  return params.get("auth") === "select_project" ? params.get("selection_id") || "" : "";
}

function latestUpdate(console: ConsoleController) {
  const deployments = Array.isArray(console.state.deployments) ? console.state.deployments : [];
  const nodes = Array.isArray(console.state.nodes) ? console.state.nodes : [];
  return (
    deployments
      .map((item) => item.updated_at || item.created_at)
      .filter(Boolean)
      .sort()
      .at(-1) ??
    nodes
      .map((item) => item.last_seen_at)
      .filter((value): value is string => Boolean(value))
      .sort()
      .at(-1)
  );
}

function authErrorMessage(code: string) {
  return (
    ({
      GITHUB_ACCOUNT_UNLINKED: "This GitHub account is not linked to an Opsi user.",
      OPSI_MEMBERSHIP_REQUIRED: "No Opsi projects are available for this account.",
      PROJECT_SELECTION_REQUIRED: "This account needs an explicit project selection.",
      GITHUB_AUTH_DENIED: "GitHub authorization was cancelled.",
      AUTH_SESSION_EXPIRED: "The sign-in request expired.",
      AUTH_UNAVAILABLE: "Opsi sign-in is temporarily unavailable.",
      GITHUB_AUTH_FAILED: "GitHub sign-in failed.",
    } as Record<string, string>)[code] ?? ""
  );
}

function focusableElements(root: HTMLElement | null | undefined) {
  return Array.from(
    root?.querySelectorAll<HTMLElement>(
      'a[href], button:not(:disabled), summary, input:not(:disabled), select:not(:disabled), textarea:not(:disabled), [tabindex]:not([tabindex="-1"])'
    ) ?? []
  ).filter((element) => element.getClientRects().length > 0);
}

function trapTabKey(event: KeyboardEvent, root: HTMLElement | null) {
  if (event.key !== "Tab") return;
  const items = focusableElements(root);
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
