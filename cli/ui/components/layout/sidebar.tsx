import { projectDestinations, routeHref, type ConsoleRoute } from "@/features/console/navigation";
import type { Project } from "@/lib/contracts/registry";
import { ProjectPicker } from "@/components/layout/project-picker";

export function Sidebar({
  environment,
  onBrowse,
  onClose,
  onNavigate,
  onSelectProject,
  open,
  orgID,
  project,
  projects,
  route,
}: {
  environment: string;
  onBrowse: () => void;
  onClose: () => void;
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
  return (
    <>
      <button aria-label="Close navigation" className={`sidebarBackdrop ${open ? "open" : ""}`} onClick={onClose} tabIndex={open ? 0 : -1} type="button" />
      <aside aria-label="Primary navigation" className={`sidebar ${open ? "open" : ""}`}>
        <div className="brandRow">
          <a className="brand" href={routeHref({ view: "projects" })} onClick={(event) => navigate(event, { view: "projects", projectID: "" })}>Opsi</a>
          <button aria-label="Close navigation" className="iconButton mobileOnly" onClick={onClose} type="button">
            <svg aria-hidden="true" viewBox="0 0 20 20"><path d="m5 5 10 10M15 5 5 15" /></svg>
          </button>
        </div>
        <ProjectPicker environment={environment} onBrowse={onBrowse} onSelect={onSelectProject} orgID={orgID} project={project} projects={projects} />
        <nav>
          {!project ? (
            <div className="navSection">
              <a aria-current={route.view === "projects" ? "page" : undefined} className={route.view === "projects" ? "active" : ""} href={routeHref({ view: "projects" })} onClick={(event) => navigate(event, { view: "projects", projectID: "" })}>Projects</a>
            </div>
          ) : (
            <div className="navSection">
              <p>Project</p>
              {projectDestinations.map((item) => (
                <a
                  aria-current={route.view === item.id ? "page" : undefined}
                  className={route.view === item.id ? "active" : ""}
                  href={routeHref({ projectID, view: item.id })}
                  key={item.id}
                  onClick={(event) => navigate(event, { projectID, view: item.id, tab: "" })}
                >
                  {item.label}
                </a>
              ))}
            </div>
          )}
        </nav>
        <nav className="sidebarFooter" aria-label="Workspace navigation">
          {project ? <a href={routeHref({ view: "projects" })} onClick={(event) => navigate(event, { view: "projects", projectID: "" })}>All projects</a> : null}
          <a aria-current={route.view === "settings" ? "page" : undefined} className={route.view === "settings" ? "active" : ""} href={routeHref({ projectID, view: "settings" })} onClick={(event) => navigate(event, { projectID, view: "settings", tab: "" })}>Settings</a>
        </nav>
      </aside>
    </>
  );
}
