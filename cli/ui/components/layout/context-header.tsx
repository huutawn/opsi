"use client";

import { useMemo, useState, useRef, useEffect, RefObject } from "react";
import { routeLabel, type ConsoleRoute } from "@/features/console/navigation";
import { LocalClient, type LocalSessionStatus } from "@/lib/api/local-client";
import type { Project } from "@/lib/contracts/registry";
import { Icon, IconButton } from "@/components/ui/primitives";

export function ContextHeader({
  environment,
  lastUpdated,
  menuButtonRef,
  onMenu,
  onRefresh,
  project,
  route,
  serviceScope,
  session,
}: {
  environment: string;
  lastUpdated?: string;
  menuButtonRef: RefObject<HTMLButtonElement | null>;
  onMenu: () => void;
  onRefresh: () => void;
  project: Project | null;
  route: ConsoleRoute;
  serviceScope?: string;
  session: LocalSessionStatus;
}) {
  const client = useMemo(() => new LocalClient(), []);
  const [accountMenuOpen, setAccountMenuOpen] = useState(false);
  const [searchQuery, setSearchQuery] = useState("");
  const menuRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    function handleClickOutside(e: MouseEvent) {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) {
        setAccountMenuOpen(false);
      }
    }
    document.addEventListener("mousedown", handleClickOutside);
    return () => document.removeEventListener("mousedown", handleClickOutside);
  }, []);

  async function logout() {
    try {
      await client.logout();
    } finally {
      onRefresh();
    }
  }

  const cloudConnected = session.cloud_connected === "ok";
  const agentConnected = session.agent_connected === "ok";

  return (
    <header className="fixed top-0 left-0 lg:left-72 right-0 h-16 bg-surface/80 backdrop-blur-xl border-b border-outline-variant/20 z-30 flex items-center justify-between px-4 lg:px-8">
      <div className="flex items-center gap-3">
        <button
          className="lg:hidden p-2 text-on-surface-variant hover:text-on-surface rounded-lg hover:bg-surface-container-highest transition-colors"
          onClick={onMenu}
          ref={menuButtonRef}
        >
          <Icon name="menu" className="text-[22px]" />
        </button>

        <nav aria-label="Breadcrumb" className="flex items-center text-sm font-medium text-on-surface-variant truncate gap-2 font-breadcrumb">
          <span className="hover:text-on-surface transition-colors cursor-pointer">{project ? "Projects" : session.org_id || "Workspace"}</span>
          {project && (
            <>
              <Icon name="chevron_right" className="text-[16px] text-on-surface-variant/70 shrink-0" />
              <span className="truncate max-w-[140px] hover:text-on-surface transition-colors cursor-pointer font-medium" title={project.name}>{project.name}</span>
              <Icon name="chevron_right" className="text-[16px] text-on-surface-variant/70 shrink-0" />
              <span className="truncate max-w-[120px] hover:text-on-surface transition-colors cursor-pointer font-medium" title={environment}>{environment}</span>
              <Icon name="chevron_right" className="text-[16px] text-on-surface-variant/70 shrink-0" />
              <span className="text-on-surface font-bold truncate">{routeLabel(route)}</span>
            </>
          )}
        </nav>
      </div>

      <div className="flex items-center gap-4 lg:gap-6">
        <div className="relative hidden md:block w-72 lg:w-96">
          <Icon name="search" className="absolute left-3 top-1/2 -translate-y-1/2 text-on-surface-variant text-[20px] pointer-events-none" />
          <input
            className="w-full bg-surface-container-high border border-outline-variant/30 rounded-xl py-2 pl-10 pr-4 text-sm text-on-surface focus:outline-none focus:border-primary/50 transition-colors placeholder:text-on-surface-variant/50"
            placeholder="Search services, nodes, builds..."
            value={searchQuery}
            onChange={(e) => setSearchQuery(e.target.value)}
          />
        </div>

        {!cloudConnected && (
          <div className="hidden sm:flex items-center gap-1.5 px-2.5 py-1 rounded-full bg-status-warning/10 text-status-warning text-[11px] font-medium border border-status-warning/30">
            <span className="w-1.5 h-1.5 rounded-full bg-status-warning animate-pulse" />
            <span>Cloud Degraded</span>
          </div>
        )}

        {project && !agentConnected && (
          <div className="hidden sm:flex items-center gap-1.5 px-2.5 py-1 rounded-full bg-status-warning/10 text-status-warning text-[11px] font-medium border border-status-warning/30">
            <span className="w-1.5 h-1.5 rounded-full bg-status-warning" />
            <span>Agent unavailable</span>
          </div>
        )}

        <div className="flex items-center gap-2 border-l border-outline-variant/30 pl-4 lg:pl-6">
          <IconButton icon="refresh" title="Refresh data" onClick={onRefresh} />
          <IconButton icon="notifications" title="Notifications" />

          <div className="relative ml-2" ref={menuRef}>
            <button
              className="w-8 h-8 rounded-full bg-surface-container-highest border border-outline-variant/40 flex items-center justify-center text-primary font-bold text-xs ring-2 ring-transparent hover:ring-primary/40 transition-all cursor-pointer"
              onClick={() => setAccountMenuOpen(!accountMenuOpen)}
            >
              {project ? project.name.slice(0, 2).toUpperCase() : "OP"}
            </button>

            {accountMenuOpen && (
              <div className="absolute right-0 top-full mt-2 w-56 bg-surface-container border border-outline-variant/30 rounded-xl shadow-xl p-2 z-50">
                <div className="px-3 py-2 border-b border-outline-variant/20 mb-1">
                  <span className="text-[10px] text-on-surface-variant uppercase tracking-wider block">Signed in as</span>
                  <span className="text-sm font-semibold text-on-surface block truncate">{session.org_id || "Opsi User"}</span>
                </div>
                <button
                  className="w-full flex items-center gap-2 px-3 py-2 rounded-lg text-sm text-error hover:bg-error-container/20 transition-colors text-left"
                  onClick={() => { setAccountMenuOpen(false); void logout(); }}
                >
                  <Icon name="logout" className="text-[18px]" />
                  Sign Out
                </button>
              </div>
            )}
          </div>
        </div>
      </div>
    </header>
  );
}
