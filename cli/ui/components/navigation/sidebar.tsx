import { projectDestinations, routeHref, type ConsoleRoute } from "@/features/console/navigation";
import type { Project } from "@/lib/contracts/registry";
import { ProjectSwitcher } from "@/components/navigation/project-switcher";
import type { RefObject } from "react";

export function Sidebar({ agentConnected, cloudConnected, drawerRef, environment, environmentID, environments, onBrowse, onClose, onEnvironment, onNavigate, onSelectProject, open, orgID, project, projects, route }: {
  agentConnected: string;
  cloudConnected: string;
  drawerRef: RefObject<HTMLElement | null>;
  environment: string;
  environmentID: string;
  environments: Array<{ id: string; name: string }>;
  onBrowse: () => void;
  onClose: () => void;
  onEnvironment: (environmentID: string) => void;
  onNavigate: (route: Partial<ConsoleRoute>) => void;
  onSelectProject: (projectID: string) => void;
  open: boolean;
  orgID: string;
  project: Project | null;
  projects: Project[];
  route: ConsoleRoute;
}) {
  function navigate(event: React.MouseEvent<HTMLAnchorElement>, next: Partial<ConsoleRoute>) {
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    onNavigate(next);
    onClose();
  }
  const projectID = project?.id ?? "";
  const systemLive = cloudConnected === "ok" && (!project || agentConnected === "ok");
  return <>
    <button aria-label="Close navigation" className={`sidebarBackdrop ${open ? "open" : ""}`} onClick={onClose} tabIndex={-1} type="button" />
    <aside aria-label="Primary navigation" aria-modal={open ? "true" : undefined} className={`sidebar ${open ? "open" : ""}`} ref={drawerRef} role={open ? "dialog" : undefined}>
      <div className="brandRow"><a aria-label="Opsi home" className="brand" href={routeHref({ view: "home" })} onClick={(event) => navigate(event, { view: "home", projectID: "" })}><span aria-hidden="true">O</span><b>Opsi</b></a><button aria-label="Close navigation" className="iconButton mobileOnly" onClick={onClose} type="button"><Icon kind="close" /></button></div>
      <ProjectSwitcher onBrowse={onBrowse} onSelect={onSelectProject} orgID={orgID} project={project} projects={projects} />
      {project ? <label className="environmentPicker"><span>Environment</span><select aria-label="Current environment" onChange={(event) => onEnvironment(event.target.value)} value={environmentID}><option value="">Choose environment</option>{environments.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label> : null}
      <nav aria-label={project ? "Project" : "Workspace"} className="navSection">
        {!project ? <>
          <NavItem active={route.view === "home"} href={routeHref({ view: "home" })} icon="home" label="Home" onClick={(event) => navigate(event, { view: "home", projectID: "" })} />
          <NavItem active={route.view === "projects"} href={routeHref({ view: "projects" })} icon="projects" label="Projects" onClick={(event) => navigate(event, { view: "projects", projectID: "" })} />
        </> : projectDestinations.map((item) => <NavItem active={route.view === item.id} href={routeHref({ ...route, projectID, view: item.id, tab: "" })} icon={item.id} key={item.id} label={item.label} onClick={(event) => navigate(event, { projectID, view: item.id, tab: "" })} />)}
      </nav>
      <div className="sidebarBottom">
        <nav className="sidebarFooter" aria-label="Settings"><NavItem active={route.view === "settings"} href={routeHref({ ...route, projectID, view: "settings", tab: "general" })} icon="settings" label="Settings" onClick={(event) => navigate(event, { projectID, view: "settings", tab: "general" })} /></nav>
        <div className="systemFact" data-state={systemLive ? "live" : "degraded"} role="status"><i aria-hidden="true" /><span><strong>{systemLive ? "System live" : "System degraded"}</strong><small>{project ? `${environment} / ${project.id}` : orgID || "Organization unavailable"}</small></span></div>
      </div>
    </aside>
  </>;
}

function NavItem({ active, href, icon, label, onClick }: { active: boolean; href: string; icon: string; label: string; onClick: React.MouseEventHandler<HTMLAnchorElement> }) {
  return <a aria-current={active ? "page" : undefined} className={active ? "active" : ""} href={href} onClick={onClick} title={label}><Icon kind={icon} /><span>{label}</span></a>;
}

function Icon({ kind }: { kind: string }) {
  const path = kind === "home"
    ? "M3 9.5 10 3l7 6.5V17H5V9.5"
    : kind === "projects"
    ? "M3 5h14v11H3zM6 5V3h8v2"
    : kind === "settings"
    ? "M10 3v2m0 10v2M3 10h2m10 0h2M5 5l1.5 1.5m7 7L15 15m0-10-1.5 1.5m-7 7L5 15M10 7a3 3 0 1 1 0 6 3 3 0 0 1 0-6Z"
    : kind === "close"
    ? "m5 5 10 10M15 5 5 15"
    : kind === "topology"
    ? "M10 3v4m0 6v4M3 10h4m6 0h4M7 7h6v6H7z"
    : kind === "overview"
    ? "M3 4h6v6H3zm8 0h6v3h-6zm0 5h6v7h-6zM3 12h6v4H3z"
    : kind === "services"
    ? "M4 5h12v4H4zm0 6h12v4H4z"
    : kind === "delivery"
    ? "M3 10h11m-4-4 4 4-4 4m6-9v10"
    : kind === "infrastructure"
    ? "M3 4h14v3H3zm0 5h14v3H3zm0 5h14v3H3z"
    : kind === "observability"
    ? "M3 13h3l2-6 3 8 2-5h4"
    : "M10 3 4 6v4c0 4 2.5 6 6 7 3.5-1 6-3 6-7V6z";
  return <svg aria-hidden="true" viewBox="0 0 20 20"><path d={path} /></svg>;
}
