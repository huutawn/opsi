"use client";

import { routeHref } from "@/features/console/navigation";
import type { Project } from "@/lib/contracts/registry";
import { Icon } from "@/components/ui/primitives";
import { useRef, MouseEvent } from "react";

export function ProjectSwitcher({
  orgID,
  project,
  projects,
  onBrowse,
  onSelect,
}: {
  orgID: string;
  project: Project | null;
  projects: Project[];
  onBrowse: () => void;
  onSelect: (projectID: string) => void;
}) {
  const menu = useRef<HTMLDetailsElement>(null);

  function choose(event: MouseEvent<HTMLAnchorElement>, projectID?: string) {
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    if (menu.current) menu.current.open = false;
    if (projectID) onSelect(projectID);
    else onBrowse();
  }

  return (
    <details
      className="projectSwitcher relative z-50 group"
      onKeyDown={(e) => {
        if (e.key === "Escape" && menu.current?.open) {
          menu.current.open = false;
          menu.current.querySelector("summary")?.focus();
        }
      }}
      ref={menu}
    >
      <summary
        aria-label="Switch project"
        className="bg-surface-container-high rounded-xl p-2 flex items-center justify-between gap-2 border border-outline-variant/20 cursor-pointer list-none hover:border-outline-variant/50 transition-colors min-h-[40px]"
      >
        <div className="flex-1 flex flex-col min-w-0 px-2 py-1">
          <span className="text-[11px] font-medium text-on-surface-variant uppercase tracking-wider truncate">
            {project ? "Project" : "Workspace"}
          </span>
          <span className="text-sm font-semibold text-on-surface truncate">
            {project?.name || "All Projects"}
          </span>
          <span className="text-[10px] font-mono text-on-surface-variant/70 truncate">
            {project?.slug || orgID || "Organization"}
          </span>
        </div>
        <Icon name="unfold_more" className="text-on-surface-variant text-[20px] shrink-0 pr-1" />
      </summary>

      <div className="absolute top-[calc(100%+6px)] left-0 w-full bg-surface-container border border-outline-variant/30 rounded-xl shadow-2xl p-2 z-50 flex flex-col gap-1 max-h-72 overflow-y-auto">
        <div className="px-3 py-1.5 text-[10px] text-on-surface-variant uppercase tracking-wider border-b border-outline-variant/20 mb-1 font-medium">
          Projects
        </div>
        {projects.map((item) => (
          <a
            key={item.id}
            href={routeHref({ projectID: item.id })}
            onClick={(e) => choose(e, item.id)}
            className={`flex flex-col px-3 py-2 rounded-lg text-sm transition-colors ${
              project?.id === item.id
                ? "bg-primary-container text-primary font-bold"
                : "text-on-surface hover:bg-surface-container-highest"
            }`}
          >
            <span className="truncate">{item.name}</span>
            <span className="font-mono text-[10px] opacity-70 truncate">{item.slug || item.id}</span>
          </a>
        ))}
        <div className="pt-1 mt-1 border-t border-outline-variant/20">
          <a
            href={routeHref({ view: "projects" })}
            onClick={(e) => choose(e)}
            className="flex items-center gap-2 px-3 py-2 rounded-lg text-xs font-medium text-on-surface-variant hover:text-on-surface hover:bg-surface-container-highest transition-colors"
          >
            <Icon name="grid_view" className="text-[16px]" />
            Browse all projects
          </a>
        </div>
      </div>
    </details>
  );
}
