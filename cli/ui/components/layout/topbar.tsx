import { useEffect, useMemo, useState } from "react";
import { LocalClient, type LocalSessionStatus } from "@/lib/api/local-client";

export function Topbar({
  orgID,
  projectID,
  onOrgID,
  onRefresh,
}: {
  orgID: string;
  projectID?: string;
  onOrgID: (value: string) => void;
  onRefresh: () => void;
}) {
  const client = useMemo(() => new LocalClient(), []);
  const [session, setSession] = useState<LocalSessionStatus | null>(null);
  const [authError, setAuthError] = useState("");

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
      const next = await client.startLogin(projectID);
      window.location.assign(next.auth_url);
    } catch (error) {
      setAuthError((error as Error).message);
    }
  }

  async function logout() {
    try {
      await client.logout(projectID);
      await refreshSession();
      onRefresh();
    } catch (error) {
      setAuthError((error as Error).message);
    }
  }

  return (
    <div className="topbar">
      <label>
        <span className="srOnly">Org ID</span>
        <input className="field" onChange={(event) => onOrgID(event.target.value)} value={orgID} />
      </label>
      <button onClick={onRefresh} type="button">
        Refresh
      </button>
      <span className="statusPill">{session?.authenticated ? "Signed in" : "Signed out"}</span>
      <span className="statusPill">Cloud {session?.cloud_connected ?? "unknown"}</span>
      <span className="statusPill">Agent {session?.agent_connected ?? "unknown"}</span>
      {session?.authenticated ? (
        <button onClick={() => void logout()} type="button">
          Logout
        </button>
      ) : (
        <button onClick={() => void login()} type="button">
          Login
        </button>
      )}
      {authError ? <span className="statusPill">{authError}</span> : null}
    </div>
  );
}
