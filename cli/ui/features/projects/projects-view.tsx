"use client";

import { useMemo, useRef, useState } from "react";
import { Empty, PageHeader, StatusBadge } from "@/components/ui/primitives";
import { routeHref } from "@/features/console/navigation";
import type { ConsoleController } from "@/features/console/types";
import { formatTimestamp, shortIdentifier, statusLabel, type PresentationStatus } from "@/lib/presentation/project";

export function ProjectsView({ console }: { console: ConsoleController }) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [query, setQuery] = useState("");
  const [statusFilter, setStatusFilter] = useState("all");
  const rows = useMemo(() => console.state.projects.map((project) => ({
    project,
    entry: console.projectSummaries[project.id],
  })).filter(({ project, entry }) => {
    const matchesQuery = `${project.name} ${project.slug}`.toLowerCase().includes(query.trim().toLowerCase());
    const status = entry?.summary?.overall ?? (entry?.status === "error" ? "unavailable" : "unknown");
    return matchesQuery && (statusFilter === "all" || status === statusFilter);
  }), [console.projectSummaries, console.state.projects, query, statusFilter]);

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
        <label><span>Status</span><select className="select" name="status_filter" onChange={(event) => setStatusFilter(event.target.value)} value={statusFilter}><option value="all">All statuses</option><option value="healthy">Healthy</option><option value="degraded">Degraded</option><option value="failed">Failed</option><option value="unavailable">Unavailable</option><option value="unknown">Not reported</option></select></label>
      </div>
      {console.state.projects.length === 0 ? (
        <Empty action={<button className="primary" onClick={() => dialog.current?.showModal()} type="button">Create project</button>} title="No projects yet" text="Create a project to connect services, runtime, and delivery evidence." />
      ) : rows.length === 0 ? (
        <Empty title="No matching projects" text="Clear the search or status filter to see the workspace again." />
      ) : (
        <div className="projectList">
          <div className="projectListHeader" aria-hidden="true"><span>Project</span><span>Health</span><span>Runtime</span><span>Services</span><span>Latest delivery</span><span>Incidents</span><span>Last changed</span></div>
          {rows.map(({ project, entry }) => {
            const summary = entry?.summary;
            const status = summary?.overall ?? (entry?.status === "error" ? "unavailable" : "unknown");
            const delivery = summary?.latestBuild?.build.status ?? summary?.latestDeployment?.rollout_state ?? summary?.latestDeployment?.status;
            const deliveryIdentity = summary?.latestBuild
              ? `${shortIdentifier(summary.latestBuild.workload.sha, 9)} · ${shortIdentifier(summary.latestBuild.build.oci_digest, 15)}`
              : summary?.latestDeployment ? shortIdentifier(summary.latestDeployment.current_digest ?? summary.latestDeployment.desired_digest, 15) : "Not reported by Local API";
            const freshness = entry?.refreshing
              ? "Refreshing"
              : entry?.stale ? "Stale — retry Refresh current data"
                : entry?.status === "error" ? "Source unavailable — retry Refresh current data" : "";
            return (
              <a className="projectRow" href={routeHref({ projectID: project.id })} key={project.id} onClick={(event) => openProject(event, project.id)}>
                <span className="projectIdentity"><strong title={project.name}>{project.name}</strong><small>{project.status || "Lifecycle not reported"} · {entry?.environment || "Environment not reported by Local API"} · <code>{project.slug}</code></small></span>
                <span data-label="Health"><span className="projectHealth">{entry?.status === "loading" ? <span role="status">Loading…</span> : <StatusBadge label={statusLabel(status as PresentationStatus)} value={status} />}{freshness ? <small role="status" title={entry?.error}>{freshness}</small> : null}</span></span>
                <span data-label="Runtime">{entry?.status === "loading" ? "Loading…" : entry?.runtimeStatus ? <StatusBadge label={statusLabel(entry.runtimeStatus)} value={entry.runtimeStatus} /> : "Not reported by Local API"}</span>
                <span data-label="Services">{entry?.status === "loading" ? "Loading…" : summary ? summary.serviceCount : "Not reported by Local API"}</span>
                <span data-label="Latest delivery" className="projectDelivery">{entry?.status === "loading" ? "Loading…" : <>{delivery ? <StatusBadge value={delivery} /> : null}<small>{deliveryIdentity}</small></>}</span>
                <span data-label="Incidents">{entry?.status === "loading" ? "Loading…" : summary ? `${summary.openIncidents} open` : "Not reported by Local API"}</span>
                <span data-label="Last changed">{entry?.status === "loading" ? "Loading…" : summary?.updatedAt ? formatTimestamp(summary.updatedAt) : "Not reported by Local API"}</span>
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
          <label>Slug<input autoComplete="off" className="field" name="slug" pattern="(?:[a-z0-9]|-)+" placeholder="checkout-platform…" required spellCheck={false} /></label>
          <div className="modalActions span2"><button onClick={() => dialog.current?.close()} type="button">Cancel</button><button className="primary" disabled={console.state.busy === "project"} type="submit">Review project</button></div>
        </form>
      </dialog>
    </section>
  );
}

export function WorkspaceHomeView({ console }: { console: ConsoleController }) {
  const degraded = console.session?.cloud_connected !== "ok";
  return <section className="page workspaceHome">
    <PageHeader eyebrow="Workspace" title="Home" description="Choose a project or review the Local workspace connection before making changes." action={<button className="primary" onClick={() => console.navigate({ view: "projects" })} type="button">Browse projects</button>} />
    <div className="workspaceSummary">
      <section aria-labelledby="workspace-projects"><p className="eyebrow">Projects</p><h2 id="workspace-projects">{console.state.projects.length} available</h2><p>Project navigation appears only after a project is selected.</p></section>
      <section aria-labelledby="workspace-cloud"><p className="eyebrow">Cloud source</p><h2 id="workspace-cloud">{degraded ? "Unavailable" : "Connected"}</h2><p>{degraded ? "Previously loaded factual data remains visible where available." : "Project history is available through the Local API."}</p></section>
    </div>
  </section>;
}
