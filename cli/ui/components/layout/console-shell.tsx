"use client";

import { ConsoleRouter } from "@/features/console/console-router";
import { ProjectPicker } from "@/components/layout/project-picker";
import { Sidebar } from "@/components/layout/sidebar";
import { Topbar } from "@/components/layout/topbar";
import { useConsoleState } from "@/hooks/use-console-state";

export function ConsoleShell() {
  const console = useConsoleState();

  return (
    <div className="app">
      <a className="skipLink" href="#main">
        Skip to content
      </a>
      <Sidebar active={console.active} onSelect={console.setActive} />
      <main className="main" id="main">
        <Topbar
          orgID={console.orgID}
          onOrgID={console.setOrgID}
          onRefresh={() => void console.actions.load()}
        />
        <ProjectPicker onSelect={console.setProjectID} project={console.state.project} projects={console.state.projects} />
        <ConsoleRouter console={console} />
      </main>
    </div>
  );
}
