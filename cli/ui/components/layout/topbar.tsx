import { useMemo } from "react";
import { LocalClient, type LocalSessionStatus } from "@/lib/api/local-client";

export function Topbar({
  session,
  orgID,
  onOrgID,
  onRefresh,
}: {
  session: LocalSessionStatus;
  orgID: string;
  onOrgID: (value: string) => void;
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
