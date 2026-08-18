"use client";

import { projectDestinations, routeHref, type ConsoleRoute } from "@/features/console/navigation";
import type { Project } from "@/lib/contracts/registry";
import { ProjectSwitcher } from "@/components/navigation/project-switcher";
import { Icon } from "@/components/ui/primitives";
import { useEffect, type MouseEvent, type RefObject } from "react";

export function Sidebar({
  agentConnected,
  cloudConnected,
  drawerRef,
  environment,
  environmentID,
  environments,
  onBrowse,
  onClose,
  onEnvironment,
  onNavigate,
  onSelectProject,
  open,
  orgID,
  project,
  projects,
  route,
}: {
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
  useEffect(() => {
    if (open) {
      window.requestAnimationFrame(() => {
        drawerRef.current?.querySelector<HTMLButtonElement>('[aria-label="Close navigation"]')?.focus();
      });
    }
  }, [open, drawerRef]);

  function navigate(event: MouseEvent<HTMLAnchorElement>, next: Partial<ConsoleRoute>) {
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    onNavigate(next);
    onClose();
  }

  const projectID = project?.id ?? "";
  const systemLive = cloudConnected === "ok" && (!project || agentConnected === "ok");

  return (
    <>
      {open ? (
        <div
          aria-hidden="true"
          className="fixed inset-0 bg-background/80 backdrop-blur-sm z-40 lg:hidden"
          onClick={onClose}
        />
      ) : null}
      <aside
        aria-label="Primary navigation"
        aria-modal={open ? "true" : undefined}
        className={`sidebar fixed left-0 top-0 h-full w-72 bg-surface-container-low z-50 flex flex-col border-r border-outline-variant/30 shadow-2xl transition-transform lg:translate-x-0 lg:pointer-events-auto ${
          open ? "translate-x-0 pointer-events-auto" : "-translate-x-full pointer-events-none"
        }`}
        onKeyDown={(event) => {
          if (event.key === "Escape") {
            event.preventDefault();
            onClose();
          } else if (event.key === "Tab") {
            const focusables = Array.from(
              drawerRef.current?.querySelectorAll<HTMLElement>(
                'button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'
              ) ?? []
            ).filter((el) => el.offsetParent !== null);
            if (focusables.length > 0) {
              const first = focusables[0];
              const last = focusables[focusables.length - 1];
              if (event.shiftKey && document.activeElement === first) {
                event.preventDefault();
                last.focus();
              } else if (!event.shiftKey && document.activeElement === last) {
                event.preventDefault();
                first.focus();
              }
            }
          }
        }}
        ref={drawerRef}
        role={open ? "dialog" : undefined}
      >
        {/* Brand Header */}
        <div className="flex items-center justify-between px-6 py-6 border-b border-outline-variant/20">
          <a
            aria-label="Opsi Home"
            className="flex items-center gap-3 cursor-pointer min-h-[40px]"
            href={routeHref({ view: "home" })}
            onClick={(event) => navigate(event, { view: "home", projectID: "" })}
          >
            <div className="w-8 h-8 rounded-lg bg-surface-container-high border border-outline-variant/30 flex items-center justify-center text-primary font-bold font-code-md shadow-sm">
              <Icon name="terminal" className="text-[20px] text-primary" />
            </div>
            <span className="font-headline-md text-headline-md tracking-tighter text-on-surface font-semibold">
              OPSI
            </span>
          </a>
          {open ? (
            <button
              aria-label="Close navigation"
              className="iconButton min-w-[40px] min-h-[40px] p-2 rounded-lg text-on-surface-variant hover:text-on-surface lg:hidden flex items-center justify-center cursor-pointer"
              onClick={onClose}
              type="button"
            >
              <Icon name="close" className="text-[20px]" />
            </button>
          ) : null}
        </div>

        {/* Project & Environment Switcher Area */}
        <div className="p-4 flex flex-col gap-3">
          <ProjectSwitcher
            onBrowse={onBrowse}
            onSelect={onSelectProject}
            orgID={orgID}
            project={project}
            projects={projects}
          />
          {project && environments.length > 0 ? (
            <div className="environmentPicker bg-surface-container-high rounded-xl p-2 flex items-center gap-2 border border-outline-variant/20">
              <div className="flex-1 flex flex-col px-2 py-1 min-w-0">
                <span className="font-label-sm text-[11px] text-on-surface-variant uppercase tracking-wider">
                  Environment
                </span>
                <select
                  aria-label="Current environment"
                  className="min-h-[40px] w-full bg-transparent font-body-md text-sm text-on-surface font-medium border-0 p-0 focus:outline-none cursor-pointer"
                  onChange={(event) => onEnvironment(event.target.value)}
                  value={environmentID}
                >
                  <option value="" className="bg-surface-container-high text-on-surface">Choose environment</option>
                  {environments.map((item) => (
                    <option key={item.id} value={item.id} className="bg-surface-container-high text-on-surface">
                      {item.name}
                    </option>
                  ))}
                </select>
              </div>
              <Icon name="unfold_more" className="text-on-surface-variant pr-1 text-[20px]" />
            </div>
          ) : null}
        </div>

        {/* Main Navigation Links */}
        <nav aria-label={project ? "Project Navigation" : "Workspace Navigation"} className="navSection flex-1 px-3 py-2 space-y-1 overflow-y-auto">
          {!project ? (
            <>
              <NavItem
                active={route.view === "home"}
                href={routeHref({ view: "home" })}
                icon="home"
                label="Home"
                onClick={(event) => navigate(event, { view: "home", projectID: "" })}
              />
              <NavItem
                active={route.view === "projects"}
                href={routeHref({ view: "projects" })}
                icon="grid_view"
                label="Projects"
                onClick={(event) => navigate(event, { view: "projects", projectID: "" })}
              />
            </>
          ) : (
            projectDestinations.map((item) => {
              const active = route.view === item.id;
              const iconMap: Record<string, string> = {
                topology: "account_tree",
                overview: "dashboard",
                services: "layers",
                delivery: "rocket_launch",
                infrastructure: "dns",
                observability: "monitoring",
                security: "security",
              };
              return (
                <NavItem
                  active={active}
                  href={routeHref({ ...route, projectID, view: item.id, tab: "" })}
                  icon={iconMap[item.id] || "folder"}
                  key={item.id}
                  label={item.label}
                  onClick={(event) => navigate(event, { projectID, view: item.id, tab: "" })}
                />
              );
            })
          )}
        </nav>

        {/* Bottom Section */}
        <div className="mt-auto p-4 border-t border-outline-variant/20 flex flex-col gap-3">
          <NavItem
            active={route.view === "settings"}
            href={routeHref({ ...route, projectID, view: "settings", tab: "general" })}
            icon="settings"
            label="Settings"
            onClick={(event) => navigate(event, { projectID, view: "settings", tab: "general" })}
          />
          <div
            className={`flex items-center gap-3 p-3 rounded-xl border ${
              systemLive
                ? "bg-state-live-bg border-border-live/40"
                : "bg-surface-container border-status-warning/30"
            }`}
            role="status"
          >
            <div
              className={`w-2.5 h-2.5 rounded-full ${
                systemLive ? "bg-status-ready animate-pulse" : "bg-status-warning"
              }`}
            />
            <div className="flex flex-col min-w-0">
              <span className={`font-label-sm text-xs font-semibold ${systemLive ? "text-border-live" : "text-status-warning"}`}>
                {systemLive ? "SYSTEM LIVE" : "SYSTEM DEGRADED"}
              </span>
              <span className="text-[11px] text-on-surface-variant font-code-md truncate">
                {project ? `${environment || "Prod"} • ${project.slug || project.id}` : orgID || "Ready"}
              </span>
            </div>
          </div>
        </div>
      </aside>
    </>
  );
}

function NavItem({
  active,
  href,
  icon,
  label,
  onClick,
}: {
  active: boolean;
  href: string;
  icon: string;
  label: string;
  onClick: (event: MouseEvent<HTMLAnchorElement>) => void;
}) {
  return (
    <a
      aria-current={active ? "page" : undefined}
      className={`flex items-center px-4 py-3 rounded-lg font-body-md text-sm transition-all group ${
        active
          ? "bg-primary-container text-primary font-bold border-l-4 border-primary shadow-sm"
          : "text-on-surface-variant hover:bg-surface-container-highest hover:text-on-surface"
      }`}
      href={href}
      onClick={onClick}
      title={label}
    >
      <Icon
        name={icon}
        className={`mr-3.5 text-[20px] transition-opacity ${
          active ? "opacity-100 text-primary" : "opacity-70 group-hover:opacity-100"
        }`}
      />
      <span>{label}</span>
    </a>
  );
}
