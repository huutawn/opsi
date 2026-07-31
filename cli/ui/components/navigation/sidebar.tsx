import { projectDestinations, routeHref, type ConsoleRoute } from "@/features/console/navigation";
import type { Project } from "@/lib/contracts/registry";
import { ProjectSwitcher } from "@/components/navigation/project-switcher";

export function Sidebar({ collapsed, environment, onBrowse, onClose, onCollapse, onNavigate, onSelectProject, open, orgID, project, projects, route }: {
  collapsed: boolean;
  environment: string;
  onBrowse: () => void;
  onClose: () => void;
  onCollapse: () => void;
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
  return <>
    <button aria-label="Close navigation" className={`sidebarBackdrop ${open ? "open" : ""}`} onClick={onClose} tabIndex={open ? 0 : -1} type="button" />
    <aside aria-label="Primary navigation" className={`sidebar ${open ? "open" : ""} ${collapsed ? "collapsed" : ""}`}>
      <div className="brandRow"><a aria-label="Opsi home" className="brand" href={routeHref({ view: "home" })} onClick={(event) => navigate(event, { view: "home", projectID: "" })}><span aria-hidden="true">O</span><b>Opsi</b></a><button aria-label="Close navigation" className="iconButton mobileOnly" onClick={onClose} type="button"><Icon kind="close" /></button></div>
      <ProjectSwitcher environment={environment} onBrowse={onBrowse} onSelect={onSelectProject} orgID={orgID} project={project} projects={projects} />
      <nav aria-label={project ? "Project" : "Workspace"} className="navSection">
        {!project ? <>
          <NavItem active={route.view === "home"} href={routeHref({ view: "home" })} icon="home" label="Home" onClick={(event) => navigate(event, { view: "home", projectID: "" })} />
          <NavItem active={route.view === "projects"} href={routeHref({ view: "projects" })} icon="projects" label="Projects" onClick={(event) => navigate(event, { view: "projects", projectID: "" })} />
        </> : projectDestinations.map((item) => <NavItem active={route.view === item.id} href={routeHref({ projectID, view: item.id })} icon={item.id} key={item.id} label={item.label} onClick={(event) => navigate(event, { projectID, view: item.id, tab: "" })} />)}
      </nav>
      <nav className="sidebarFooter" aria-label="Settings"><NavItem active={route.view === "settings"} href={routeHref({ projectID, view: "settings" })} icon="settings" label="Settings" onClick={(event) => navigate(event, { projectID, view: "settings", tab: "general" })} /></nav>
      <button aria-label={collapsed ? "Expand sidebar" : "Collapse sidebar"} aria-pressed={collapsed} className="sidebarCollapse" onClick={onCollapse} type="button"><Icon kind="collapse" /><span>{collapsed ? "Expand" : "Collapse"}</span></button>
    </aside>
  </>;
}

function NavItem({ active, href, icon, label, onClick }: { active: boolean; href: string; icon: string; label: string; onClick: React.MouseEventHandler<HTMLAnchorElement> }) {
  return <a aria-current={active ? "page" : undefined} className={active ? "active" : ""} href={href} onClick={onClick} title={label}><Icon kind={icon} /><span>{label}</span></a>;
}

function Icon({ kind }: { kind: string }) {
  const path = kind === "home" ? "M3 9.5 10 3l7 6.5V17H5V9.5" : kind === "projects" ? "M3 5h14v11H3zM6 5V3h8v2" : kind === "settings" ? "M10 3v2m0 10v2M3 10h2m10 0h2M5 5l1.5 1.5m7 7L15 15m0-10-1.5 1.5m-7 7L5 15M10 7a3 3 0 1 1 0 6 3 3 0 0 1 0-6Z" : kind === "close" ? "m5 5 10 10M15 5 5 15" : kind === "collapse" ? "m12 5-5 5 5 5" : kind === "overview" ? "M3 4h6v6H3zm8 0h6v3h-6zm0 5h6v7h-6zM3 12h6v4H3z" : kind === "services" ? "M4 5h12v4H4zm0 6h12v4H4z" : kind === "delivery" ? "M3 10h11m-4-4 4 4-4 4m6-9v10" : kind === "infrastructure" ? "M10 3v4m0 6v4M3 10h4m6 0h4M7 7h6v6H7z" : kind === "observability" ? "M3 13h3l2-6 3 8 2-5h4" : "M10 3 4 6v4c0 4 2.5 6 6 7 3.5-1 6-3 6-7V6z";
  return <svg aria-hidden="true" viewBox="0 0 20 20"><path d={path} /></svg>;
}
