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
      <Sidebar active={console.active} onSelect={console.setActive} />
      <main className="main">
        <Topbar
          cloudURL={console.cloudURL}
          orgID={console.orgID}
          onCloudURL={console.setCloudURL}
          onOrgID={console.setOrgID}
          onPAT={console.setPAT}
          onRefresh={() => void console.actions.load()}
          pat={console.pat}
        />
        <ProjectPicker onSelect={console.setProjectID} project={console.state.project} projects={console.state.projects} />
        <ConsoleRouter console={console} />
      </main>
    </div>
  );
}
