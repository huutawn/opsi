"use client";

import { Empty } from "@/components/ui/primitives";
import { DeliveryStatus, Evidence, ServiceFilter, displayTime, short, type DeliveryViewProps } from "@/features/delivery/shared";
import { deriveCurrentDeliveryState, derivePipeline, type PipelineStage } from "@/lib/presentation/delivery/model";
import { terminalBuild } from "@/lib/presentation/build";

export function PipelineView({ console, data, selectedService }: DeliveryViewProps) {
  if (!data.hasLoaded && (data.sourceState === "loading" || data.buildState === "loading" || data.deploymentState === "loading")) {
    return <Empty title="Loading delivery pipeline" text="Source, BuildRecord, and deployment facts are still loading. No empty-state conclusion is available yet." />;
  }

  if (!selectedService) {
    const applications = data.services.filter((s) => s.type === "application");
    return (
      <div className="deliveryPage">
        <div className="deliveryToolbar">
          <ServiceFilter console={console} services={data.services} selected={selectedService} />
          <p aria-live="polite">{data.sourceError || data.buildError || data.deploymentError || "Project-wide delivery overview."}</p>
        </div>
        {applications.length === 0 ? (
          <Empty title="No delivery service" text="Add an application service, then bind it to a repository before Delivery can show a causal pipeline." />
        ) : (
          <div className="deliverySplit">
            <section className="deliverySection">
              <div className="sectionHeading">
                <div>
                  <p className="eyebrow">Project Applications</p>
                  <h2>Delivery Overview</h2>
                </div>
                <span>{applications.length} applications</span>
              </div>
              <ol className="rolloutList">
                {applications.map((service) => {
                  const state = deriveCurrentDeliveryState({
                    service,
                    deployments: data.deployments,
                    builds: data.builds,
                    buildJobs: data.buildJobs[service.id] ?? [],
                    placement: data.placement,
                  });
                  return (
                    <li key={service.id}>
                      <button onClick={() => console.navigate({ service: service.id, tab: "pipeline" })} type="button">
                        <DeliveryStatus status={state.rolloutState} />
                        <span>
                          <strong>{service.name}</strong>
                          <small>
                            {state.runtimeLabel} · {state.lastSuccessfulAt ? `Last deployed ${displayTime(state.lastSuccessfulAt)}` : "Never deployed"}
                          </small>
                        </span>
                        <code>{short(state.deployedDigest || "No artifact deployed", 20)}</code>
                      </button>
                    </li>
                  );
                })}
              </ol>
            </section>
            <section className="deliverySection">
              <div className="sectionHeading">
                <div>
                  <p className="eyebrow">Operational Guidance</p>
                  <h2>Delivery Status</h2>
                </div>
              </div>
              <div className="nextAction">
                <strong>Application-centric delivery</strong>
                <p>Select any application to inspect its end-to-end source, build, immutable artifact, deployment, and live verification pipeline.</p>
              </div>
            </section>
          </div>
        )}
      </div>
    );
  }

  const pipeline = derivePipeline({
    projectID: console.state.project?.id ?? "",
    service: selectedService,
    bindings: data.bindings,
    repositories: data.repositories,
    builds: data.builds,
    deployments: data.deployments,
    availability: {
      source: data.sourceState === "unavailable" ? "unavailable" : "ready",
      builds: data.buildState === "unavailable" ? "unavailable" : "ready",
      deployments: data.deploymentState === "unavailable" ? "unavailable" : "ready",
    },
  });

  const deliveryState = deriveCurrentDeliveryState({
    service: selectedService,
    deployments: data.deployments,
    builds: data.builds,
    buildJobs: data.buildJobs[selectedService.id] ?? [],
    placement: data.placement,
  });

  const stages = Object.entries(pipeline.stages) as Array<[string, PipelineStage]>;
  const release = pipeline.currentRelease;
  const releaseDigest = release?.current_digest || release?.terminal_result?.current_digest;
  const recent = data.deployments.filter((job) => job.service_id === selectedService.id).slice(0, 5);
  const activeOp = deliveryState.activeOperation;

  const activeBinding = pipeline.activeBinding;
  const latestBuildJob = deliveryState.latestBuildJob;
  const canBuild = Boolean(activeBinding && (!latestBuildJob || terminalBuild(latestBuildJob)));

  function triggerBuild() {
    if (!selectedService || !activeBinding) return;
    console.reviewMutation(
      {
        project: console.state.project?.name || console.state.project?.id || "",
        targetType: "BuildJob",
        targetID: selectedService.id,
        operation: "build",
        diff: [
          `Resolve exact commit from the active source binding for ${selectedService.name}`,
          "Resolve the canonical build strategy in Cloud",
          "Publish and accept an immutable BuildRecord only after verification",
        ],
        risk: "Creates a new canonical BuildJob intent. It does not mutate a prior failed job, place the Application, or deploy it.",
      },
      async (key) => {
        const job = await data.createBuild(selectedService, key);
        return `BuildJob ${job.id} accepted with factual state ${job.status}.`;
      }
    );
  }

  return (
    <div className="deliveryPage">
      <div className="deliveryToolbar">
        <ServiceFilter console={console} services={data.services} selected={selectedService} />
        <p aria-live="polite">{data.sourceError || data.buildError || data.deploymentError || "Factual state loaded through the Local API."}</p>
        <div style={{ display: "flex", gap: "8px" }}>
          <button className="secondaryAction" disabled={!canBuild} onClick={triggerBuild} type="button">
            Build Application
          </button>
          {deliveryState.canDeployNewerBuild && deliveryState.newerAcceptedBuild ? (
            <button
              className="primary"
              onClick={() => console.navigate({ tab: "deployments", service: selectedService.id, build: deliveryState.newerAcceptedBuild?.id })}
              type="button"
            >
              Deploy Newer Build
            </button>
          ) : null}
          {deliveryState.rollbackCandidate?.isEligible && deliveryState.currentDeployment ? (
            <button onClick={() => console.actions.rollback(deliveryState.currentDeployment!.id)} type="button">
              Rollback
            </button>
          ) : null}
        </div>
      </div>

      {activeOp ? (
        <div className="truthCallout" role="status" style={{ borderLeftColor: "var(--blue)", background: "var(--blue-bg)" }}>
          <strong>Active {activeOp.type.toUpperCase()}: {activeOp.id}</strong>
          <p>Status: {activeOp.status}. Progress is durably recorded and monitored across page refreshes.</p>
        </div>
      ) : null}

      <section className="releaseSummary" aria-labelledby="current-release-title">
        <div>
          <p className="eyebrow">Current Release</p>
          <h2 id="current-release-title">{selectedService.name}</h2>
          <p>
            {release
              ? `${release.environment_id || "Environment not reported"} · ${release.rollout_state || release.status}`
              : "No factual current release"}
          </p>
        </div>
        <dl>
          <Evidence
            label="Source SHA"
            value={
              release?.snapshot?.authority.build_record.workload.sha ? (
                <code
                  aria-label={`Source SHA ${release.snapshot.authority.build_record.workload.sha}`}
                  title={release.snapshot.authority.build_record.workload.sha}
                >
                  {short(release.snapshot.authority.build_record.workload.sha, 16)}
                </code>
              ) : undefined
            }
            mono
          />
          <Evidence
            label="Immutable digest"
            value={
              releaseDigest ? (
                <code aria-label={`Immutable digest ${releaseDigest}`} title={releaseDigest}>
                  {short(releaseDigest, 22)}
                </code>
              ) : undefined
            }
            mono
          />
          <Evidence label="Runtime verification" value={pipeline.stages.verify.label} />
          <Evidence
            label="Exposure"
            value={release?.exposure_spec ? `${release.exposure_spec.hostname}${release.exposure_spec.path}` : "Not configured"}
          />
        </dl>
      </section>

      <ol className="stageRail" aria-label="Immutable delivery pipeline">
        {stages.map(([key, stage], index) => (
          <li data-status={stage.status} key={key}>
            <button
              onClick={() =>
                console.navigate({
                  tab: stage.targetTab,
                  service: selectedService.id,
                  build: pipeline.latestBuild?.id,
                  deployment: pipeline.linkedDeployment?.id,
                })
              }
              type="button"
            >
              <span className="stageIndex" aria-hidden="true">
                {index + 1}
              </span>
              <span>
                <small>{key}</small>
                <strong>{stage.label}</strong>
                <em>{stage.explanation}</em>
              </span>
              <DeliveryStatus status={stage.status} />
            </button>
          </li>
        ))}
      </ol>

      <div className="deliverySplit">
        <section className="deliverySection">
          <div className="sectionHeading">
            <div>
              <p className="eyebrow">Rollout Timeline</p>
              <h2>Active & Recent Rollouts</h2>
            </div>
          </div>
          {recent.length ? (
            <ol className="rolloutList">
              {recent.map((job) => (
                <li key={job.id}>
                  <button
                    aria-pressed={console.route.deployment === job.id}
                    onClick={() => console.navigate({ tab: "deployments", deployment: job.id, service: selectedService.id })}
                    type="button"
                  >
                    <DeliveryStatus status={job.rollout_state || job.status} />
                    <span>
                      <strong>{job.rollout_state || job.status}</strong>
                      <small>
                        {displayTime(job.updated_at || job.created_at)} · attempt {job.attempt_count ?? 0}
                      </small>
                    </span>
                    <code>{short(job.desired_digest || job.snapshot?.image.digest, 20)}</code>
                  </button>
                </li>
              ))}
            </ol>
          ) : (
            <Empty title="No rollout observed" text="A successful BuildRecord does not imply that a DeploymentJob exists." />
          )}
        </section>
        <section className="deliverySection">
          <div className="sectionHeading">
            <div>
              <p className="eyebrow">Next Action</p>
              <h2>Blockers & Release History</h2>
            </div>
          </div>
          <div className="nextAction">
            <DeliveryStatus status={pipeline.stages.verify.status} />
            <strong>{pipeline.stages.verify.nextAction}</strong>
            <p>{pipeline.stages.verify.explanation}</p>
          </div>
          {pipeline.unlinkedDeployments.length ? (
            <div className="truthCallout">
              <strong>Unlinked historical record</strong>
              <p>
                {pipeline.unlinkedDeployments.length} deployment record(s) lack canonical BuildRecord identity and are not correlated
                heuristically.
              </p>
            </div>
          ) : null}
          {deliveryState.canDeployNewerBuild && deliveryState.newerAcceptedBuild ? (
            <div className="truthCallout" style={{ borderLeftColor: "var(--ok)", background: "var(--ok-bg)" }}>
              <strong>Newer accepted build available</strong>
              <p>
                BuildRecord <code>{short(deliveryState.newerAcceptedBuild.id, 16)}</code> with digest{" "}
                <code>{short(deliveryState.newerAcceptedBuild.build.oci_digest, 16)}</code> is accepted and ready to deploy.
              </p>
            </div>
          ) : null}
        </section>
      </div>
    </div>
  );
}
