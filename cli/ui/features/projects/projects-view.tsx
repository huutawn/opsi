"use client";

import { useMemo, useRef, useState, type MouseEvent } from "react";
import { Button, Empty, Icon, PageHeader, StatusBadge } from "@/components/ui/primitives";
import { routeHref } from "@/features/console/navigation";
import type { ConsoleController } from "@/features/console/types";
import { formatTimestamp, shortIdentifier, statusLabel, type PresentationStatus } from "@/lib/presentation/project";
import { useI18n } from "@/lib/i18n";

export function ProjectsView({ console }: { console: ConsoleController }) {
  const { t, locale } = useI18n();
  const dialog = useRef<HTMLDialogElement>(null);
  const [query, setQuery] = useState("");
  const [statusFilter, setStatusFilter] = useState("all");

  const rows = useMemo(
    () =>
      console.state.projects
        .map((project) => ({
          project,
          entry: console.projectSummaries[project.id],
        }))
        .filter(({ project, entry }) => {
          const matchesQuery = `${project.name} ${project.slug}`.toLowerCase().includes(query.trim().toLowerCase());
          const status = entry?.summary?.overall ?? (entry?.status === "error" ? "unavailable" : "unknown");
          return matchesQuery && (statusFilter === "all" || status === statusFilter);
        }),
    [console.projectSummaries, console.state.projects, query, statusFilter]
  );

  function openProject(event: MouseEvent<HTMLAnchorElement>, projectID: string) {
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    console.setProjectID(projectID);
  }

  return (
    <div className="p-4 lg:p-margin-desktop max-w-7xl mx-auto space-y-6">
      <PageHeader
        action={
          <Button onClick={() => dialog.current?.showModal()} variant="primary">
            <Icon name="add" className="text-[18px]" />
            {t("projects.new_project", "New Project")}
          </Button>
        }
        description={t("projects.description", "Factual overview of local projects and their operational health.")}
        eyebrow={t("projects.eyebrow", "Local Workspace")}
        icon="folder"
        title={t("projects.title", "Projects")}
      />

      {/* Filter and Search Bar */}
      <div className="bg-surface-container-low border border-outline-variant/20 rounded-xl p-4 shadow-sm flex flex-col md:flex-row items-center gap-4">
        <div className="relative flex-1 w-full">
          <Icon name="search" className="absolute left-3 top-1/2 -translate-y-1/2 text-on-surface-variant text-[20px] pointer-events-none" />
          <input
            aria-label={t("common.search", "Search")}
            className="w-full bg-surface-container-highest border border-outline-variant/30 rounded-lg py-2.5 pl-10 pr-4 text-xs font-body-md text-on-surface focus:outline-none focus:border-primary/50"
            onChange={(event) => setQuery(event.target.value)}
            placeholder={t("projects.search_placeholder", "Search projects by name or slug...")}
            type="search"
            value={query}
          />
        </div>

        <select
          aria-label={t("common.status", "Status")}
          className="bg-surface-container-highest border border-outline-variant/30 text-on-surface text-xs rounded-lg py-2.5 px-3 focus:outline-none focus:border-primary/50 cursor-pointer w-full md:w-auto"
          onChange={(event) => setStatusFilter(event.target.value)}
          value={statusFilter}
        >
          <option value="all">{t("projects.filter_all", "All statuses")}</option>
          <option value="healthy">{t("status.healthy", "Healthy")}</option>
          <option value="degraded">{t("status.degraded", "Degraded")}</option>
          <option value="failed">{t("status.failed", "Failed")}</option>
          <option value="unavailable">{t("status.unavailable", "Unavailable")}</option>
          <option value="unknown">{t("common.not_reported", "Not reported")}</option>
        </select>
      </div>

      {console.state.projects.length === 0 ? (
        <Empty
          action={
            <Button onClick={() => dialog.current?.showModal()} variant="primary">
              <Icon name="add" className="text-[18px]" />
              {t("projects.new_project", "New Project")}
            </Button>
          }
          text={t("projects.no_projects_desc", "Create a project with the Opsi CLI or request access from your organization admin.")}
          title={t("projects.no_projects_title", "No Projects Available")}
        />
      ) : rows.length === 0 ? (
        <Empty
          text={t("projects.no_matching_desc", "Clear the search or status filter to see the projects list.")}
          title={t("projects.no_matching_title", "No Matching Projects")}
        />
      ) : (
        <div className="bg-surface-container-low border border-outline-variant/20 rounded-xl shadow-sm overflow-hidden">
          <div className="overflow-x-auto">
            <table className="w-full text-left text-xs">
              <thead>
                <tr className="border-b border-outline-variant/20 bg-surface-container text-on-surface-variant uppercase font-label-sm text-[10px]">
                  <th className="py-3.5 px-4">{t("projects.table_project", "Project")}</th>
                  <th className="py-3.5 px-4">{t("projects.table_status", "Health")}</th>
                  <th className="py-3.5 px-4">{t("projects.table_runtime", "Runtime")}</th>
                  <th className="py-3.5 px-4">{t("observability.tab_applications", "Services")}</th>
                  <th className="py-3.5 px-4">{t("projects.table_delivery", "Latest Delivery")}</th>
                  <th className="py-3.5 px-4">{t("observability.tab_incidents", "Incidents")}</th>
                  <th className="py-3.5 px-4">{t("projects.table_updated", "Updated")}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-outline-variant/10">
                {rows.map(({ project, entry }) => {
                  const summary = entry?.summary;
                  const status = summary?.overall ?? (entry?.status === "error" ? "unavailable" : "unknown");
                  const delivery = summary?.latestBuild?.build.status ?? summary?.latestDeployment?.rollout_state ?? summary?.latestDeployment?.status;
                  const deliveryIdentity = summary?.latestBuild
                    ? `${shortIdentifier(summary.latestBuild.workload.sha, 9)} • ${shortIdentifier(summary.latestBuild.build.oci_digest, 15)}`
                    : summary?.latestDeployment
                      ? shortIdentifier(summary.latestDeployment.current_digest ?? summary.latestDeployment.desired_digest, 15)
                      : "No delivery";

                  return (
                    <tr
                      key={project.id}
                      className="projectRow hover:bg-surface-container-high/50 transition-colors cursor-pointer"
                      onClick={() => console.setProjectID(project.id)}
                    >
                      <td className="py-3.5 px-4">
                        <a
                          className="flex flex-col group font-body-md text-sm text-on-surface font-semibold hover:text-primary transition-colors cursor-pointer"
                          href={routeHref({ projectID: project.id, view: "deploy" })}
                          onClick={(event) => openProject(event, project.id)}
                        >
                          <strong className="block" title={project.name}>{project.name}</strong>
                          <span className="font-code-md text-[11px] text-on-surface-variant group-hover:text-primary/80 font-normal">
                            {project.status || "active"} • <code>{project.slug}</code>
                          </span>
                          {entry?.refreshing ? (
                            <span className="text-[10px] text-status-progress font-code-md font-semibold">{t("common.loading", "Loading…")}</span>
                          ) : entry?.stale ? (
                            <span className="text-[10px] text-status-warning font-code-md font-semibold">Stale — retry Refresh current data</span>
                          ) : null}
                        </a>
                      </td>
                      <td className="py-3.5 px-4" data-label="Health">
                        <StatusBadge label={statusLabel(status as PresentationStatus, locale)} value={status} />
                      </td>
                      <td className="py-3.5 px-4 font-code-md" data-label="Runtime">
                        {entry?.status === "loading" ? t("common.loading", "Loading…") : entry?.runtimeStatus ? (
                          <StatusBadge label={statusLabel(entry.runtimeStatus, locale)} value={entry.runtimeStatus} />
                        ) : (
                          "—"
                        )}
                      </td>
                      <td className="py-3.5 px-4 font-body-md text-on-surface font-medium" data-label="Services">
                        {entry?.status === "loading" ? "…" : summary ? summary.serviceCount : "0"}
                      </td>
                      <td className="py-3.5 px-4" data-label="Latest Delivery">
                        <div className="flex flex-col">
                          {delivery ? <StatusBadge value={delivery} /> : <span className="text-on-surface-variant">—</span>}
                          <small className="font-code-md text-[10px] text-on-surface-variant truncate max-w-[160px] mt-0.5">
                            {deliveryIdentity}
                          </small>
                        </div>
                      </td>
                      <td className="py-3.5 px-4 font-body-md text-on-surface" data-label="Incidents">
                        {summary ? (
                          <span className={summary.openIncidents > 0 ? "text-status-warning font-bold" : "text-on-surface-variant"}>
                            {summary.openIncidents} open
                          </span>
                        ) : (
                          "—"
                        )}
                      </td>
                      <td className="py-3.5 px-4 text-on-surface-variant font-code-md text-[11px]">
                        {summary?.updatedAt ? formatTimestamp(summary.updatedAt, undefined, locale) : "—"}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </div>
      )}

      {/* Modal Dialog for New Project */}
      <dialog aria-labelledby="createProjectTitle" aria-label={t("projects.create_title", "Create Project")} className="fixed inset-0 m-auto bg-surface-container-low border border-outline-variant/20 rounded-2xl p-6 shadow-2xl backdrop:bg-black/60 max-w-md w-full" ref={dialog}>
        <div className="flex items-center justify-between border-b border-outline-variant/20 pb-4 mb-4">
          <h2 className="font-headline-md text-lg font-bold text-on-surface" id="createProjectTitle">{t("projects.create_title", "Create Project")}</h2>
          <button aria-label={t("common.close", "Close")} className="text-on-surface-variant hover:text-on-surface cursor-pointer" onClick={() => dialog.current?.close()} type="button">
            <Icon name="close" className="text-[20px]" />
          </button>
        </div>

        <form
          className="space-y-4"
          onSubmit={(event) => {
            dialog.current?.close();
            void console.actions.createProject(event);
          }}
        >
          <div>
            <label className="font-label-sm text-xs text-on-surface font-semibold block mb-1.5">
              {t("projects.name_label", "Project Name")}
              <input
                autoComplete="off"
                className="w-full bg-surface-container-highest border border-outline-variant/30 rounded-lg p-2.5 text-xs text-on-surface focus:outline-none focus:border-primary/50 mt-1 font-normal"
                name="name"
                placeholder={t("projects.name_placeholder", "e.g., Checkout Service")}
                required
              />
            </label>
          </div>

          <div>
            <label className="font-label-sm text-xs text-on-surface font-semibold block mb-1.5">
              {t("projects.slug_label", "URL Slug")}
              <input
                autoComplete="off"
                className="w-full bg-surface-container-highest border border-outline-variant/30 rounded-lg p-2.5 text-xs text-on-surface focus:outline-none focus:border-primary/50 font-code-md mt-1 font-normal"
                name="slug"
                pattern="(?:[a-z0-9]|-)+"
                placeholder={t("projects.slug_placeholder", "e.g., checkout-service")}
                required
                spellCheck={false}
              />
            </label>
          </div>

          <div className="flex items-center justify-end gap-3 pt-4 border-t border-outline-variant/20">
            <Button onClick={() => dialog.current?.close()} type="button" variant="secondary">
              {t("common.cancel", "Cancel")}
            </Button>
            <Button disabled={console.state.busy === "project"} type="submit" variant="primary">
              {console.state.busy === "project" ? t("projects.creating_button", "Creating…") : t("projects.create_button", "Create Project")}
            </Button>
          </div>
        </form>
      </dialog>
    </div>
  );
}

export function WorkspaceHomeView({ console }: { console: ConsoleController }) {
  const { t } = useI18n();
  const degraded = console.session?.cloud_connected !== "ok";

  return (
    <div className="p-4 lg:p-margin-desktop max-w-7xl mx-auto space-y-6">
      <PageHeader
        action={
          <Button onClick={() => console.navigate({ view: "projects" })} variant="primary">
            <Icon name="folder" className="text-[18px]" />
            {t("projects.view_all_projects", "View All Projects")}
          </Button>
        }
        description={t("projects.workspace_desc", "Central operations hub for your local and cloud infrastructure.")}
        eyebrow={t("projects.workspace_eyebrow", "Local System")}
        icon="home"
        title={t("projects.workspace_title", "Workspace Home")}
      />

      <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
        <div className="bg-surface-container-low border border-outline-variant/20 rounded-xl p-6 shadow-sm space-y-2">
          <span className="font-label-sm text-xs text-on-surface-variant uppercase tracking-wider block">{t("projects.title", "Projects")}</span>
          <div className="font-headline-lg text-3xl font-bold text-on-surface">{console.state.projects.length}</div>
          <p className="text-xs text-on-surface-variant">{t("projects.overview_desc", "Monitor active deployments, services, and system connectivity across your projects.")}</p>
        </div>

        <div className="bg-surface-container-low border border-outline-variant/20 rounded-xl p-6 shadow-sm space-y-2">
          <span className="font-label-sm text-xs text-on-surface-variant uppercase tracking-wider block">Cloud Control Plane</span>
          <div className={`font-headline-lg text-3xl font-bold ${degraded ? "text-error" : "text-status-ready"}`}>
            {degraded ? t("status.unavailable", "Unavailable") : t("status.connected", "Connected")}
          </div>
          <p className="text-xs text-on-surface-variant">
            {degraded ? "Previously loaded factual records remain visible where available." : "Local Edge connection verified."}
          </p>
        </div>
      </div>
    </div>
  );
}
