"use client";

import { MouseEvent } from "react";
import { routeHref, type ConsoleRoute } from "@/features/console/navigation";
import type { Project } from "@/lib/contracts/registry";
import { ProjectSwitcher } from "./project-switcher";
import { Icon } from "@/components/ui/primitives";
import { useI18n } from "@/lib/i18n";
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
  drawerRef: React.RefObject<HTMLElement | null>;
  environment: string;
  environmentID: string;
  environments: { id: string; name: string }[];
  onBrowse: () => void;
  onClose: () => void;
  onEnvironment: (id: string) => void;
  onNavigate: (route: Partial<ConsoleRoute>) => void;
  onSelectProject: (id: string) => void;
  open: boolean;
  orgID: string;
  project: Project | null;
  projects: Project[];
  route: ConsoleRoute;
}) {
  const { t } = useI18n();
  const projectID = project?.id ?? "";
  const navigate = (event: MouseEvent<HTMLAnchorElement>, target: Partial<ConsoleRoute>) => {
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    onNavigate(target);
    onClose();
  };

  const projectDestinations = [
    { id: "deploy", label: t("nav.deploy", "Deploy"), icon: "rocket_launch" },
    { id: "assistant", label: t("nav.assistant", "AI Assistant"), icon: "hub" },
    { id: "observability", label: t("nav.observability", "Observability"), icon: "monitoring" },
    { id: "security", label: t("nav.security", "Security"), icon: "security" },
  ] as const;

  const systemLive = cloudConnected === "ok" && (!project || agentConnected === "ok");

  return (
    <>
      {open && (
        <div 
          className="fixed inset-0 z-40 bg-surface/80 backdrop-blur-sm lg:hidden"
          onClick={onClose}
        />
      )}
      
      <aside
        className={`
          sidebar fixed top-0 bottom-0 left-0 z-50 w-72 bg-surface-container-low border-r border-outline-variant/30 flex flex-col shadow-2xl transition-transform duration-300 ease-in-out
          ${open ? "translate-x-0" : "-translate-x-full lg:translate-x-0"}
        `}
        ref={drawerRef}
      >
        <div className="flex items-center justify-between px-6 py-6 border-b border-outline-variant/20">
          <a
            className="flex items-center gap-3 cursor-pointer min-h-[40px]"
            href={routeHref({ view: "home" })}
            onClick={(e) => navigate(e, { view: "home", projectID: "" })}
          >
            <div className="w-8 h-8 rounded-lg bg-white flex flex-col items-center justify-center shadow-sm p-1">
              <svg className="w-5 h-5 text-[#00a6e0]" viewBox="0 0 48 48" fill="none" xmlns="http://www.w3.org/2000/svg">
                <path d="M24 4L40 13.24V31.76L24 41L8 31.76V13.24L24 4Z" stroke="#00354a" strokeWidth="3.5" fill="#c4e7ff" />
                <path d="M24 4V22.5M24 22.5L40 13.24M24 22.5L8 13.24" stroke="#00354a" strokeWidth="3.5" />
                <path d="M24 22.5V41" stroke="#00354a" strokeWidth="3.5" />
                <circle cx="24" cy="22.5" r="3.5" fill="#00a6e0" />
              </svg>
            </div>
            <span className="text-xl font-bold tracking-tight text-on-surface font-headline-md">OPSI</span>
          </a>
          {open && (
            <button
              aria-label={t("nav.close_navigation", "Close navigation")}
              className="lg:hidden p-2 text-on-surface-variant hover:text-on-surface rounded-lg hover:bg-surface-container-highest transition-colors min-h-[40px] min-w-[40px]"
              onClick={onClose}
              type="button"
            >
              <Icon name="close" className="text-[20px]" />
            </button>
          )}
        </div>

        <div className="p-4 flex flex-col gap-3">
          <ProjectSwitcher
            onBrowse={onBrowse}
            onSelect={onSelectProject}
            orgID={orgID}
            project={project}
            projects={projects}
          />
          {project && environments.length > 0 && (
            <div className="bg-surface-container-high rounded-xl p-2 flex items-center border border-outline-variant/20 relative">
              <div className="flex-1 px-2 py-1">
                <span className="text-[11px] text-on-surface-variant uppercase tracking-wider block font-medium">{t("nav.environment", "Environment")}</span>
                <select
                  aria-label={t("nav.current_environment", "Current environment")}
                  className="environmentPicker w-full bg-transparent text-sm text-on-surface font-medium border-0 p-0 focus:outline-none appearance-none cursor-pointer min-h-[40px]"
                  onChange={(e) => onEnvironment(e.target.value)}
                  value={environmentID}
                >
                  <option value="" className="bg-surface-container-high text-on-surface">{t("nav.choose_environment", "Choose environment")}</option>
                  {environments.map((item) => (
                    <option key={item.id} value={item.id} className="bg-surface-container-high text-on-surface">{item.name}</option>
                  ))}
                </select>
              </div>
              <Icon name="unfold_more" className="text-on-surface-variant text-[20px] pointer-events-none absolute right-2" />
            </div>
          )}
        </div>

        <nav className="navSection flex-1 px-3 py-2 space-y-1 overflow-y-auto">
          {!project ? (
            <>
              <NavItem active={route.view === "home"} href={routeHref({ view: "home" })} icon="home" label={t("nav.home", "Home")} onClick={(e) => navigate(e, { view: "home", projectID: "" })} />
              <NavItem active={route.view === "projects"} href={routeHref({ view: "projects" })} icon="grid_view" label={t("nav.projects", "Projects")} onClick={(e) => navigate(e, { view: "projects", projectID: "" })} />
            </>
          ) : (
            projectDestinations.map((item) => {
              const iconMap: Record<string, string> = {
                deploy: "rocket_launch",
                assistant: "hub",
                observability: "monitoring",
                security: "security",
              };
              return (
                <NavItem
                  key={item.id}
                  active={route.view === item.id}
                  href={routeHref({ ...route, projectID, view: item.id, tab: "" })}
                  icon={iconMap[item.id] || "folder"}
                  label={item.label}
                  onClick={(e) => navigate(e, { projectID, view: item.id, tab: "" })}
                />
              );
            })
          )}
        </nav>

        <div className="p-4 border-t border-outline-variant/20 flex flex-col gap-3">
          <NavItem
            active={route.view === "settings"}
            href={routeHref({ ...route, projectID, view: "settings", tab: "general" })}
            icon="settings"
            label={t("nav.settings", "Settings")}
            onClick={(e) => navigate(e, { projectID, view: "settings", tab: "general" })}
          />
          <div className={`flex items-center gap-3 p-3 rounded-xl border ${systemLive ? "bg-state-live-bg border-border-live/40" : "bg-surface-container border-status-warning/30"}`}>
            <div className={`w-2.5 h-2.5 rounded-full ${systemLive ? "bg-status-ready animate-pulse" : "bg-status-warning"}`} />
            <div className="flex flex-col min-w-0">
              <span className={`text-xs font-semibold ${systemLive ? "text-border-live" : "text-status-warning"}`}>
                {systemLive ? t("status.system_live", "SYSTEM LIVE") : t("status.system_degraded", "SYSTEM DEGRADED")}
              </span>
              <span className="text-[11px] text-on-surface-variant font-mono truncate">
                {project ? `${environment || "Prod"} • ${project.slug || project.id}` : orgID || "Ready"}
              </span>
            </div>
          </div>
        </div>
      </aside>
    </>
  );
}

function NavItem({ active, href, icon, label, onClick }: { active: boolean; href: string; icon: string; label: string; onClick: (e: MouseEvent<HTMLAnchorElement>) => void }) {
  return (
    <a
      aria-current={active ? "page" : undefined}
      href={href}
      onClick={onClick}
      className={`flex items-center px-4 py-3 rounded-lg text-sm transition-all group ${
        active 
          ? "bg-primary-container text-primary font-bold border-l-4 border-primary" 
          : "text-on-surface-variant hover:bg-surface-container-highest hover:text-on-surface font-medium"
      }`}
    >
      <Icon name={icon} className={`mr-3 text-[20px] ${active ? "text-primary" : "text-on-surface-variant group-hover:text-on-surface"}`} />
      <span>{label}</span>
    </a>
  );
}
