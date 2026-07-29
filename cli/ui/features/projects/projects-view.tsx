"use client";

import { useMemo, useRef, useState } from "react";
import { Empty, PageHeader, StatusBadge } from "@/components/ui/primitives";
import { routeHref } from "@/features/console/navigation";
import type { ConsoleController } from "@/features/console/types";
import { deriveProjectSummary, formatTimestamp, statusLabel } from "@/lib/presentation/project";

export function ProjectsView({ console }: { console: ConsoleController }) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [query, setQuery] = useState("");
  const [environmentFilter, setEnvironmentFilter] = useState("all");
  const selectedEnvironment = console.state.foundation.placement?.environments.find((item) => item.status === "active")?.name;
  const selectedSummary = console.state.project ? deriveProjectSummary({
    project: console.state.project,
    readiness: console.state.readiness,
    services: console.state.services,
    deployments: console.state.deployments,
    foundation: console.state.foundation,
  }) : null;
  const rows = useMemo(() => console.state.projects.map((project) => ({
    project,
    environment: project.id === console.state.project?.id ? selectedEnvironment ?? "Not reported" : "Not reported",
    summary: project.id === console.state.project?.id ? selectedSummary : null,
  })).filter(({ project, environment }) => {
    const matchesQuery = `${project.name} ${project.slug}`.toLowerCase().includes(query.trim().toLowerCase());
    return matchesQuery && (environmentFilter === "all" || environment === environmentFilter);
  }), [console.state.project?.id, console.state.projects, environmentFilter, query, selectedEnvironment, selectedSummary]);
  const environments = [...new Set(console.state.projects.map((project) => project.id === console.state.project?.id ? selectedEnvironment ?? "Not reported" : "Not reported"))];

  function openProject(event: React.MouseEvent<HTMLAnchorElement>, projectID: string) {
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    console.setProjectID(projectID);
  }

  return (
    <section className="page projectsPage">
      <PageHeader
        action={<button className="primary" onClick={() => dialog.current?.showModal()} type="button">New project</button>}
        eyebrow="Workspace"
        title="Projects"
        description="Choose the project that needs your attention. Missing operational sources stay explicitly unreported."
      />
      <div className="projectFilters" role="search">
        <label><span>Search projects</span><input autoComplete="off" className="field" name="project_search" onChange={(event) => setQuery(event.target.value)} placeholder="Search by name or slug…" type="search" value={query} /></label>
        <label><span>Environment</span><select className="select" name="environment_filter" onChange={(event) => setEnvironmentFilter(event.target.value)} value={environmentFilter}><option value="all">All environments</option>{environments.map((environment) => <option key={environment} value={environment}>{environment}</option>)}</select></label>
      </div>
      {console.state.projects.length === 0 ? (
        <Empty action={<button className="primary" onClick={() => dialog.current?.showModal()} type="button">Create project</button>} title="No projects yet" text="Create a project to connect services, runtime, and delivery evidence." />
      ) : rows.length === 0 ? (
        <Empty title="No matching projects" text="Clear the search or environment filter to see the workspace again." />
      ) : (
        <div className="projectList">
          <div className="projectListHeader" aria-hidden="true"><span>Project</span><span>Status</span><span>Services</span><span>Latest delivery</span><span>Open incidents</span><span>Updated</span></div>
          {rows.map(({ project, environment, summary }) => {
            const status = summary?.overall ?? "unknown";
            const delivery = summary?.latestBuild?.build.status ?? summary?.latestDeployment?.rollout_state ?? summary?.latestDeployment?.status;
            return (
              <a className="projectRow" href={routeHref({ projectID: project.id, view: "overview" })} key={project.id} onClick={(event) => openProject(event, project.id)}>
                <span className="projectIdentity"><strong title={project.name}>{project.name}</strong><small>{environment} · <code>{project.slug}</code></small></span>
                <span data-label="Status"><StatusBadge label={statusLabel(status)} value={status} /></span>
                <span data-label="Services">{summary ? summary.serviceCount : "Not reported"}</span>
                <span data-label="Latest delivery">{delivery ? <StatusBadge value={delivery} /> : "No data yet"}</span>
                <span data-label="Open incidents">{summary ? summary.openIncidents : "Not reported"}</span>
                <span data-label="Updated">{formatTimestamp(summary?.updatedAt)}</span>
              </a>
            );
          })}
        </div>
      )}
      <dialog aria-labelledby="createProjectTitle" className="nativeDialog" ref={dialog}>
        <form method="dialog"><button aria-label="Close create project dialog" className="iconButton dialogClose" type="submit"><svg aria-hidden="true" viewBox="0 0 20 20"><path d="m5 5 10 10M15 5 5 15" /></svg></button></form>
        <p className="eyebrow">Workspace</p>
        <h2 id="createProjectTitle">Create project</h2>
        <p>Review creates the durable Cloud project through the Local API.</p>
        <form className="form" onSubmit={(event) => { dialog.current?.close(); void console.actions.createProject(event); }}>
          <label>Name<input autoComplete="off" className="field" name="name" placeholder="Checkout platform…" required /></label>
          <label>Slug<input autoComplete="off" className="field" name="slug" pattern="[a-z0-9-]+" placeholder="checkout-platform…" required spellCheck={false} /></label>
          <div className="modalActions span2"><button onClick={() => dialog.current?.close()} type="button">Cancel</button><button className="primary" disabled={console.state.busy === "project"} type="submit">Review project</button></div>
        </form>
      </dialog>
    </section>
  );
}
