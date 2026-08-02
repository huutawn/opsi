import { routeHref } from "@/features/console/navigation";
import type { Project } from "@/lib/contracts/registry";
import { useRef } from "react";

export function ProjectSwitcher({ environment, orgID, project, projects, onBrowse, onSelect }: {
  environment: string;
  orgID: string;
  project: Project | null;
  projects: Project[];
  onBrowse: () => void;
  onSelect: (projectID: string) => void;
}) {
  const menu = useRef<HTMLDetailsElement>(null);

  function choose(event: React.MouseEvent<HTMLAnchorElement>, projectID?: string) {
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    menu.current?.removeAttribute("open");
    if (projectID) onSelect(projectID);
    else onBrowse();
  }

  return <details className="projectSwitcher" onKeyDown={(event) => {
    if (event.key === "Escape" && menu.current?.open) {
      menu.current.open = false;
      menu.current.querySelector("summary")?.focus();
    }
  }} ref={menu}>
    <summary aria-label="Switch project">
      <span className="switcherContext">{project ? "Project" : "Workspace"}</span>
      <strong title={project?.name}>{project?.name || "All projects"}</strong>
      <span title={project ? environment : orgID}>{project ? environment : orgID || "Organization unavailable"}</span>
      <svg aria-hidden="true" viewBox="0 0 16 16"><path d="m4 6 4 4 4-4" /></svg>
    </summary>
    <div className="switcherMenu">
      <p>Projects</p>
      {projects.map((item) => <a aria-current={project?.id === item.id ? "page" : undefined} href={routeHref({ projectID: item.id, view: "overview" })} key={item.id} onClick={(event) => choose(event, item.id)}><span>{item.name}</span><small>{item.slug}</small></a>)}
      <a href={routeHref({ view: "projects" })} onClick={(event) => choose(event)}>Browse all projects</a>
    </div>
  </details>;
}
