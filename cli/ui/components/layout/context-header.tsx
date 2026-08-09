import { useMemo, type RefObject } from "react";
import { routeLabel, type ConsoleRoute } from "@/features/console/navigation";
import { LocalClient, type LocalSessionStatus } from "@/lib/api/local-client";
import type { Project } from "@/lib/contracts/registry";
import { ConnectionPopover } from "@/components/layout/connection-popover";

export function ContextHeader({ environment, environmentID, environments, lastUpdated, menuButtonRef, onEnvironment, onMenu, onRefresh, project, route, serviceScope, session }: {
  environment: string;
  environmentID: string;
  environments: Array<{ id: string; name: string }>;
  lastUpdated?: string;
  menuButtonRef: RefObject<HTMLButtonElement | null>;
  onEnvironment: (environmentID: string) => void;
  onMenu: () => void;
  onRefresh: () => void;
  project: Project | null;
  route: ConsoleRoute;
  serviceScope?: string;
  session: LocalSessionStatus;
}) {
  const client = useMemo(() => new LocalClient(), []);
  async function logout() { try { await client.logout(); } finally { onRefresh(); } }
  return <header className="contextHeader">
    <button aria-label="Open navigation" className="iconButton mobileOnly" onClick={onMenu} ref={menuButtonRef} type="button"><svg aria-hidden="true" viewBox="0 0 20 20"><path d="M3 5h14M3 10h14M3 15h14" /></svg></button>
    <div className="contextIdentity" aria-label="Current context">
      <div className="breadcrumb" aria-label="Breadcrumb"><span>{project ? "Projects" : session.org_id || "Workspace unavailable"}</span>{project ? <><i aria-hidden="true">/</i><strong title={project.name}>{project.name}</strong><i aria-hidden="true">/</i><span title={environment}>{environment}</span><i aria-hidden="true">/</i><span>{routeLabel(route)}</span></> : null}</div>
      <p>{routeLabel(route)}{project ? ` · ${environment}` : " · Workspace"}{serviceScope ? ` · Service ${serviceScope}` : ""}{lastUpdated ? ` · Updated ${formatUpdated(lastUpdated)}` : ""}</p>
    </div>
    <div className="headerActions">
      {project && environments.length > 1 ? <label className="environmentPicker"><span>Environment</span><select aria-label="Current environment" onChange={(event) => onEnvironment(event.target.value)} value={environmentID}><option value="">Choose…</option>{environments.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label> : null}
      {session.cloud_connected !== "ok" ? <span className="sourceWarning">Cloud unavailable</span> : null}
      {project && session.agent_connected !== "ok" ? <span className="sourceWarning">{session.agent_connected === "not connected" ? "Agent not connected" : "Agent unavailable"}</span> : null}
      <ConnectionPopover session={session} />
      <button aria-label="Refresh current data" className="iconButton" onClick={onRefresh} type="button"><svg aria-hidden="true" viewBox="0 0 20 20"><path d="M16 6V2m0 0h-4m4 0-3 3a6 6 0 1 0 2 8" /></svg></button>
      <details className="accountMenu"><summary aria-label="Account menu">Account</summary><div><button onClick={() => void logout()} type="button">Sign out</button></div></details>
    </div>
  </header>;
}

function formatUpdated(value: string) {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(date);
}
