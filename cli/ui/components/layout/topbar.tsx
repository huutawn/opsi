import { useEffect, useMemo, useState } from "react";
import { LocalClient, type LocalSessionStatus } from "@/lib/api/local-client";

export function Topbar({
  orgID,
  onOrgID,
  onRefresh,
}: {
  orgID: string;
  onOrgID: (value: string) => void;
  onRefresh: () => void;
}) {
  const client = useMemo(() => new LocalClient(), []);
  const [session, setSession] = useState<LocalSessionStatus | null>(null);
  const [authError, setAuthError] = useState(() => {
    if (typeof window === "undefined") return "";
    const code = new URLSearchParams(window.location.search).get("auth_error");
    return code ? authErrorMessage(code) : "";
  });

  async function refreshSession() {
    try {
      setSession(await client.session());
      setAuthError("");
    } catch (error) {
      setAuthError((error as Error).message);
    }
  }

  useEffect(() => {
    let cancelled = false;
    const params = new URLSearchParams(window.location.search);
    if (params.has("auth") || params.has("auth_error")) {
      params.delete("auth");
      params.delete("auth_error");
      const query = params.toString();
      window.history.replaceState({}, "", `${window.location.pathname}${query ? `?${query}` : ""}`);
    }
    client
      .session()
      .then((next) => {
        if (!cancelled) setSession(next);
      })
      .catch((error: Error) => {
        if (!cancelled) setAuthError(error.message);
      });
    return () => {
      cancelled = true;
    };
  }, [client]);

  async function login() {
    try {
      const next = await client.startLogin();
      window.location.assign(next.auth_url);
    } catch (error) {
      setAuthError((error as Error).message);
    }
  }

  async function logout() {
    try {
      await client.logout();
      await refreshSession();
      onRefresh();
    } catch (error) {
      setAuthError((error as Error).message);
    }
  }

  return (
    <div className="topbar">
      {session?.authenticated ? (
        <>
          <label>
            <span className="srOnly">Org ID</span>
            <input className="field" onChange={(event) => onOrgID(event.target.value)} value={orgID} />
          </label>
          <button onClick={onRefresh} type="button">
            Refresh
          </button>
        </>
      ) : null}
      <span className="statusPill">
        {session === null
          ? "Checking session"
          : session.authenticated
          ? "Signed in"
          : session.token_status === "invalid"
            ? "Credential invalid"
            : session.token_status === "unverified"
              ? "Credential unverified"
              : "Signed out"}
      </span>
      <span className="statusPill">Cloud {session?.cloud_connected ?? "unknown"}</span>
      <span className="statusPill">Agent {session?.agent_connected ?? "unknown"}</span>
      {session?.authenticated ? (
        <button onClick={() => void logout()} type="button">
          Logout
        </button>
      ) : (
        <button onClick={() => void login()} type="button">
          {session?.token_status === "invalid" ? "Continue with GitHub" : "Sign in with GitHub"}
        </button>
      )}
      {authError ? <div className="authNotice" role="alert">{authError}</div> : null}
    </div>
  );
}

function authErrorMessage(code: string) {
  switch (code) {
    case "GITHUB_ACCOUNT_UNLINKED":
      return "This GitHub account is not linked to an Opsi user. Sign in with the GitHub account invited to this Opsi organization.";
    case "OPSI_MEMBERSHIP_REQUIRED":
      return "Your GitHub-linked Opsi user does not have an active project membership.";
    case "PROJECT_SELECTION_REQUIRED":
      return "This GitHub account belongs to multiple Opsi projects. Select a project before continuing.";
    case "GITHUB_AUTH_DENIED":
      return "GitHub authorization was cancelled. You can try again when ready.";
    case "AUTH_SESSION_EXPIRED":
      return "The sign-in request expired. Start a new GitHub sign-in.";
    case "AUTH_UNAVAILABLE":
      return "Opsi sign-in is temporarily unavailable. Try again shortly.";
    default:
      return "GitHub sign-in failed. Start a new sign-in and try again.";
  }
}
