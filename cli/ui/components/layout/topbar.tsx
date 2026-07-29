import { useMemo } from "react";
import { LocalClient, type LocalSessionStatus } from "@/lib/api/local-client";
import type { Project } from "@/lib/contracts/registry";
import type { ConsoleRoute } from "@/features/console/navigation";

export function Topbar({
  environment,
  onMenu,
  onRefresh,
  project,
  route,
  session,
}: {
  environment: string;
  onMenu: () => void;
  onRefresh: () => void;
  project: Project | null;
  route: ConsoleRoute;
  session: LocalSessionStatus;
}) {
  const client = useMemo(() => new LocalClient(), []);

  async function logout() {
    try {
      await client.logout();
    } finally {
      onRefresh();
    }
  }

  return (
    <header className="topbar">
      <button aria-label="Open navigation" className="iconButton mobileOnly" onClick={onMenu} type="button">
        <svg aria-hidden="true" viewBox="0 0 20 20"><path d="M3 5h14M3 10h14M3 15h14" /></svg>
      </button>
      <div className="breadcrumb" aria-label="Current context">
        <span className="workspaceCrumb">{session.org_id || "Workspace unavailable"}</span>
        {project ? <><i aria-hidden="true" className="crumbDivider">/</i><strong className="projectCrumb" title={project.name}>{project.name}</strong><i aria-hidden="true" className="crumbDivider">/</i><span className="environmentCrumb" title={environment}>{environment}</span></> : null}
        {route.view !== "projects" ? <><i aria-hidden="true" className="crumbDivider">/</i><span className="viewCrumb">{route.view}</span></> : null}
      </div>
      <div className="topbarActions">
        {session.cloud_connected !== "ok" ? <span className="sourceWarning">Cloud unavailable</span> : null}
        {project && session.agent_connected !== "ok" ? <span className="sourceWarning">Agent unavailable</span> : null}
        <button aria-label="Refresh current data" className="iconButton" onClick={onRefresh} type="button">
          <svg aria-hidden="true" viewBox="0 0 20 20"><path d="M16 6V2m0 0h-4m4 0-3 3a6 6 0 1 0 2 8" /></svg>
        </button>
        <details className="accountMenu">
          <summary aria-label="Account menu">Account</summary>
          <div><button onClick={() => void logout()} type="button">Sign out</button></div>
        </details>
      </div>
    </header>
  );
}
