"use client";

import { useMemo, useState, useRef, useEffect, type RefObject } from "react";
import { routeLabel, type ConsoleRoute } from "@/features/console/navigation";
import { LocalClient, type LocalSessionStatus } from "@/lib/api/local-client";
import type { Project } from "@/lib/contracts/registry";
import { Icon, IconButton } from "@/components/ui/primitives";

export function ContextHeader({
  environment,
  menuButtonRef,
  onMenu,
  onRefresh,
  project,
  route,
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
    function handleClickOutside(event: MouseEvent) {
      if (menuRef.current && !menuRef.current.contains(event.target as Node)) {
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
    <header className="fixed top-0 left-0 lg:left-72 right-0 h-16 bg-surface/80 backdrop-blur-xl border-b border-outline-variant/20 z-40 flex items-center justify-between px-4 lg:px-margin-desktop">
      {/* Left Area: Mobile Menu + Breadcrumbs */}
      <div className="flex items-center gap-3">
        <button
          aria-label="Open navigation"
          className="iconButton min-w-[40px] min-h-[40px] p-2 rounded-lg text-on-surface-variant hover:text-on-surface hover:bg-surface-container-highest lg:hidden flex items-center justify-center cursor-pointer"
          onClick={onMenu}
          ref={menuButtonRef}
          type="button"
        >
          <Icon name="menu" className="text-[22px]" />
        </button>

        <nav aria-label="Breadcrumb" className="breadcrumb flex items-center text-on-surface-variant font-breadcrumb text-sm truncate">
          <span>{project ? "Projects" : session.org_id || "Workspace"}</span>
          {project ? (
            <>
              <span>/</span>
              <span className="truncate max-w-[140px] font-medium" title={project.name}>
                {project.name}
              </span>
              <span>/</span>
              <span className="truncate max-w-[120px] font-medium" title={environment}>
                {environment}
              </span>
              <span>/</span>
              <span className="text-on-surface font-bold truncate">
                {routeLabel(route)}
              </span>
            </>
          ) : null}
        </nav>
      </div>

      {/* Right Area: Search + Indicators + Actions + Profile */}
      <div className="flex items-center gap-4 lg:gap-6">
        {/* Search Bar */}
        <div className="relative hidden md:block w-72 lg:w-96">
          <Icon
            name="search"
            className="absolute left-3 top-1/2 -translate-y-1/2 text-on-surface-variant text-[20px] pointer-events-none"
          />
          <input
            className="w-full bg-surface-container-high border border-outline-variant/30 rounded-xl py-2 pl-10 pr-4 text-sm font-body-md text-on-surface focus:outline-none focus:border-primary/50 transition-colors placeholder:text-on-surface-variant/50"
            placeholder="Search services, nodes, builds..."
            type="text"
            value={searchQuery}
            onChange={(e) => setSearchQuery(e.target.value)}
          />
        </div>

        {/* Status Indicators & Alerts */}
        {!cloudConnected ? (
          <div className="hidden sm:flex items-center gap-1.5 px-2.5 py-1 rounded-full bg-status-warning/10 text-status-warning font-label-sm text-[11px] border border-status-warning/30">
            <span className="w-1.5 h-1.5 rounded-full bg-status-warning animate-pulse"></span>
            <span>Cloud Degraded</span>
          </div>
        ) : null}

        {project && !agentConnected ? (
          <div className="hidden sm:flex items-center gap-1.5 px-2.5 py-1 rounded-full bg-status-warning/10 text-status-warning font-label-sm text-[11px] border border-status-warning/30">
            <span className="w-1.5 h-1.5 rounded-full bg-status-warning"></span>
            <span>Agent unavailable</span>
          </div>
        ) : null}

        {/* Action Controls */}
        <div className="flex items-center gap-2 border-l border-outline-variant/30 pl-4 lg:pl-6">
          <IconButton
            aria-label="Refresh current data"
            icon="refresh"
            title="Refresh current data"
            onClick={onRefresh}
          />

          {/* Account Dropdown */}
          <div className="accountMenu relative" ref={menuRef}>
            <button
              aria-expanded={accountMenuOpen}
              aria-label="Account options"
              className="iconButton min-w-[40px] min-h-[40px] w-10 h-10 rounded-full bg-surface-container-highest border border-outline-variant/40 flex items-center justify-center text-primary font-bold text-xs ring-2 ring-transparent hover:ring-primary/40 transition-all cursor-pointer overflow-hidden"
              onClick={() => setAccountMenuOpen(!accountMenuOpen)}
              type="button"
            >
              <span>{project ? project.name.slice(0, 2).toUpperCase() : "OP"}</span>
            </button>

            {accountMenuOpen ? (
              <div className="absolute right-0 top-[calc(100%+8px)] w-56 bg-surface-container border border-outline-variant/30 rounded-xl shadow-2xl p-2 z-50 flex flex-col gap-1">
                <div className="px-3 py-2 border-b border-outline-variant/20">
                  <span className="font-label-sm text-[10px] text-on-surface-variant uppercase tracking-wider block">
                    Signed in as
                  </span>
                  <span className="font-body-md text-sm font-semibold text-on-surface truncate block">
                    {session.org_id || "Opsi User"}
                  </span>
                </div>
                <button
                  className="w-full flex items-center gap-2 px-3 py-2 rounded-lg text-sm text-error hover:bg-error-container/20 transition-colors text-left cursor-pointer"
                  onClick={() => {
                    setAccountMenuOpen(false);
                    void logout();
                  }}
                  type="button"
                >
                  <Icon name="logout" className="text-[18px]" />
                  Sign Out
                </button>
              </div>
            ) : null}
          </div>
        </div>
      </div>
    </header>
  );
}
