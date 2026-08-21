"use client";

import { useEffect } from "react";
import { Empty, StatePanel } from "@/components/ui/primitives";
import { DeliveryStatus, Evidence, ServiceFilter, displayTime, short, type DeliveryViewProps } from "@/features/delivery/shared";
import { buildFailure, buildFailureCategory, terminalBuild } from "@/lib/presentation/build";

export function BuildsView({ console, data, selectedService }: DeliveryViewProps) {
  const activeBinding = selectedService ? data.bindings.find((binding) => binding.service_id === selectedService.id && binding.status === "active") : undefined;
  const serviceKey = activeBinding?.service_key;
  const repositoryID = console.route.repository;
  const sha = console.route.sha;
  const status = console.route.status;
  const cursor = console.route.cursor;
  const filters = { serviceKey, repositoryID, sha, status, cursor };
  const loadBuilds = data.loadBuilds;

  useEffect(() => {
    void loadBuilds({ serviceKey, repositoryID, sha, status, cursor });
  }, [cursor, loadBuilds, repositoryID, serviceKey, sha, status]);

  const selected = data.buildResults.records.find((record) => record.id === console.route.build) ?? data.buildResults.records[0];
  const repository = selected ? data.repositories.find((item) => item.repository_id === selected.repository_id) : undefined;
  const activeServiceBuildJobs = selectedService ? (data.buildJobs[selectedService.id] ?? []) : [];
  const latestJob = activeServiceBuildJobs[0];
  const canCreateBuild = Boolean(selectedService && activeBinding && (!latestJob || terminalBuild(latestJob)));

  function triggerBuild(service = selectedService) {
    if (!service) return;
    console.reviewMutation(
      {
        project: console.state.project?.name || console.state.project?.id || "",
        targetType: "BuildJob",
        targetID: service.id,
        operation: "build",
        diff: [
          `Resolve exact commit from active source binding for ${service.name}`,
          "Resolve canonical build strategy in Cloud",
          "Publish and accept immutable BuildRecord only after verification",
        ],
        risk: "Creates a new canonical BuildJob intent. It does not mutate a prior failed job, place the Application, or deploy it.",
      },
      async (key) => {
        const job = await data.createBuild(service, key);
        return `BuildJob ${job.id} accepted with factual state ${job.status}.`;
      }
    );
  }

  if (data.buildState === "unavailable" && data.buildResults.records.length === 0) {
    return <StatePanel title="Build Records Unavailable" text={data.buildError} retry={() => void data.loadBuilds(filters)} />;
  }

  const failureCode = selected?.build.status === "failed" ? "USER_BUILD_FAILED" : "";
  const failureInfo = selected?.build.status === "failed" ? buildFailure(failureCode, "Build failed.") : undefined;
  const failureCat = selected?.build.status === "failed" ? buildFailureCategory(failureCode) : undefined;

  return (
    <div className="deliveryPage">
      <div className="deliveryToolbar">
        <ServiceFilter console={console} services={data.services} selected={selectedService} />
        <label>
          Repository ID
          <input
            inputMode="numeric"
            name="repository_id"
            value={console.route.repository ?? ""}
            onChange={(event) => console.navigate({ repository: event.target.value, cursor: "" })}
          />
        </label>
        <label>
          Source SHA
          <input
            name="sha"
            spellCheck={false}
            value={console.route.sha ?? ""}
            onChange={(event) => console.navigate({ sha: event.target.value, cursor: "" })}
          />
        </label>
        <label>
          Status
          <select value={console.route.status ?? ""} onChange={(event) => console.navigate({ status: event.target.value, cursor: "" })}>
            <option value="">Any</option>
            <option value="succeeded">Succeeded</option>
            <option value="failed">Failed</option>
          </select>
        </label>
        {selectedService ? (
          <button className="primary" disabled={!canCreateBuild} onClick={() => triggerBuild(selectedService)} type="button">
            Create Build
          </button>
        ) : null}
      </div>
      <div className="masterDetail">
        <section className="masterList" aria-label="Build records">
          <div className="sectionHeading">
            <div>
              <p className="eyebrow">Trusted Builds</p>
              <h2>Build Records</h2>
            </div>
            <span>{data.buildResults.records.length} shown</span>
          </div>
          {data.buildResults.records.length ? (
            <ul>
              {data.buildResults.records.map((record) => (
                <li key={record.id}>
                  <button
                    aria-pressed={selected?.id === record.id}
                    onClick={() => console.navigate({ build: record.id, service: record.service_id })}
                    type="button"
                  >
                    <span>
                      <strong>{data.services.find((service) => service.id === record.service_id)?.name ?? record.service_key}</strong>
                      <small>
                        {record.workload.ref.replace("refs/heads/", "")} · {short(record.workload.sha, 12)}
                      </small>
                    </span>
                    <span>
                      <DeliveryStatus status={record.build.status} />
                      <small>{displayTime(record.created_at)}</small>
                    </span>
                    <code title={record.build.oci_digest}>{short(record.build.oci_digest, 18)}</code>
                  </button>
                </li>
              ))}
            </ul>
          ) : (
            <Empty
              title="No trusted BuildRecord received"
              text="Cloud has no BuildRecord matching these exact filters. This does not prove that GitHub Actions failed."
            />
          )}
          {data.buildResults.next_cursor ? (
            <button onClick={() => console.navigate({ cursor: data.buildResults.next_cursor })} type="button">
              Next Page
            </button>
          ) : null}
        </section>
        <aside className="detailPanel" aria-label="Selected BuildRecord detail">
          {selected ? (
            <>
              <div className="detailHeading">
                <div>
                  <p className="eyebrow">Build Detail</p>
                  <h2>
                    {selected.service_key} · {short(selected.workload.sha, 12)}
                  </h2>
                </div>
                <div style={{ display: "flex", gap: "8px" }}>
                  {selected.build.status === "succeeded" ? (
                    <button
                      className="primary"
                      onClick={() => console.navigate({ tab: "deployments", service: selected.service_id, build: selected.id, deployment: "" })}
                      type="button"
                    >
                      Prepare Deployment
                    </button>
                  ) : selected.build.status === "failed" && selectedService ? (
                    <button onClick={() => triggerBuild(selectedService)} type="button">
                      Retry Build
                    </button>
                  ) : null}
                </div>
              </div>

              {failureInfo ? (
                <div className="truthCallout" role="alert" style={{ borderLeftColor: "var(--bad)", background: "var(--bad-bg)" }}>
                  <strong>
                    {failureCat ? `[${failureCat}] ` : ""}
                    {failureInfo.title}
                  </strong>
                  <p>{failureInfo.action}</p>
                </div>
              ) : null}

              <dl className="evidenceGrid">
                <Evidence label="BuildRecord ID" value={selected.id} mono />
                <Evidence label="Repository / owner" value={`${selected.repository_id} / ${selected.repository_owner_id}`} />
                <Evidence label="Binding ID" value={selected.active_binding_id} mono />
                <Evidence label="Full source SHA" value={selected.workload.sha} mono />
                <Evidence label="Workflow ref" value={selected.workload.workflow_ref} mono />
                <Evidence label="Run / attempt" value={`${selected.workload.run_id} / ${selected.workload.run_attempt}`} />
                <Evidence label="Full OCI reference" value={`${selected.build.oci_repository}@${selected.build.oci_digest}`} mono />
                <Evidence label="Platform" value={selected.build.platform} />
              </dl>
              <details>
                <summary>Technical Evidence</summary>
                <dl className="evidenceGrid">
                  <Evidence label="Config hash" value={selected.build.config_hash} mono />
                  <Evidence label="Plan hash" value={selected.build.plan_hash} mono />
                  <Evidence label="Provenance digest" value={selected.build.provenance_digest} mono />
                  <Evidence label="Job workflow ref" value={selected.workload.job_workflow_ref} mono />
                </dl>
              </details>
              {repository?.full_name && selected.workload.run_id ? (
                <a
                  href={`https://github.com/${repository.full_name}/actions/runs/${selected.workload.run_id}`}
                  rel="noreferrer"
                  target="_blank"
                >
                  Open Exact GitHub Workflow Run
                </a>
              ) : (
                <p className="muted">GitHub link unavailable: exact repository full name and run identity are required.</p>
              )}
            </>
          ) : (
            <Empty title="Select a build" text="Choose a BuildRecord to inspect immutable artifact and provenance evidence." />
          )}
        </aside>
      </div>
    </div>
  );
}
