import { useMemo } from "react";
import { LocalClient, type LocalSessionStatus } from "@/lib/api/local-client";

export function Topbar({
  session,
  onRefresh,
}: {
  session: LocalSessionStatus;
  onRefresh: () => void;
}) {
  const client = useMemo(() => new LocalClient(), []);

  async function logout() {
    try {
      await client.logout();
      onRefresh();
    } catch {
      onRefresh();
    }
  }

  return (
    <div className="topbar">
      {session?.authenticated ? (
        <>
          <span className="statusPill">Org {session.org_id || "unavailable"}</span>
          <button onClick={onRefresh} type="button">
            Refresh
          </button>
        </>
      ) : null}
      <span className="statusPill">
        {session.authenticated ? "Signed in" : "Session unavailable"}
      </span>
      <span className="statusPill">Cloud {session.cloud_connected}</span>
      <span className="statusPill">Agent {session.agent_connected}</span>
      <button onClick={() => void logout()} type="button">
        Logout
      </button>
    </div>
  );
}
