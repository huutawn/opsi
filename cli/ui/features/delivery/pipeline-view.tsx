"use client";

import { Button, Empty, Icon } from "@/components/ui/primitives";
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
      <div className="space-y-6">
        <div className="flex items-center justify-between gap-4 bg-surface-container-low p-4 rounded-xl border border-outline-variant/20">
          <ServiceFilter console={console} selected={selectedService} services={data.services} />
          <p className="text-xs text-on-surface-variant">{data.sourceError || data.buildError || data.deploymentError || "Project-wide delivery overview."}</p>
        </div>
        {applications.length === 0 ? (
          <Empty title="No delivery service" text="Add an application service, then bind it to a repository before Delivery can show a causal pipeline." />
        ) : (
          <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-6">
            {applications.map((service) => {
              const state = deriveCurrentDeliveryState({
                service,
                deployments: data.deployments,
                builds: data.builds,
                buildJobs: data.buildJobs[service.id] ?? [],
                placement: data.placement,
              });
              return (
                <div
                  key={service.id}
                  className="bg-surface-container-low border border-outline-variant/20 rounded-xl p-6 shadow-sm hover:shadow-md transition-all cursor-pointer flex flex-col justify-between space-y-4"
                  onClick={() => console.navigate({ service: service.id, tab: "pipeline" })}
                >
                  <div className="flex items-start justify-between">
                    <div className="flex items-center gap-3">
                      <div className="w-10 h-10 rounded-lg bg-surface-container-high flex items-center justify-center text-primary border border-outline-variant/20">
                        <Icon name="rocket_launch" className="text-[20px]" />
                      </div>
                      <div>
                        <h3 className="font-headline-md text-base font-bold text-on-surface">{service.name}</h3>
                        <span className="font-code-md text-[11px] text-on-surface-variant">{state.runtimeLabel}</span>
                      </div>
                    </div>
                    <DeliveryStatus status={state.rolloutState} />
                  </div>

                  <div className="bg-surface-container-highest/60 p-3.5 rounded-lg border border-outline-variant/20 space-y-1 text-xs font-code-md">
                    <div className="text-on-surface-variant truncate">Digest: {short(state.deployedDigest || "No artifact deployed", 24)}</div>
                    <div className="text-[11px] text-on-surface-variant/80">
                      {state.lastSuccessfulAt ? `Last deployed: ${displayTime(state.lastSuccessfulAt)}` : "Never deployed"}
                    </div>
                  </div>

                  <Button size="sm" variant="outline" className="w-full">
                    View Pipeline →
                  </Button>
                </div>
              );
            })}
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
    <div className="space-y-6">
      {/* Service Filter & Quick Action Bar */}
      <div className="flex flex-col md:flex-row items-center justify-between gap-4 bg-surface-container-low p-4 rounded-xl border border-outline-variant/20 shadow-sm">
        <ServiceFilter console={console} selected={selectedService} services={data.services} />
        <div className="flex flex-wrap items-center gap-3">
          <Button disabled={!canBuild} onClick={triggerBuild} size="sm" variant="secondary">
            <Icon name="build" className="text-[16px]" />
            Build Application
          </Button>
          {deliveryState.canDeployNewerBuild && deliveryState.newerAcceptedBuild ? (
            <Button
              onClick={() =>
                console.navigate({
                  tab: "deployments",
                  service: selectedService.id,
                  build: deliveryState.newerAcceptedBuild?.id,
                })
              }
              size="sm"
              variant="primary"
            >
              <Icon name="rocket_launch" className="text-[16px]" />
              Deploy Newer Build
            </Button>
          ) : null}
          {deliveryState.rollbackCandidate?.isEligible && deliveryState.currentDeployment ? (
            <Button onClick={() => console.actions.rollback(deliveryState.currentDeployment!.id)} size="sm" variant="danger">
              <Icon name="undo" className="text-[16px]" />
              Rollback
            </Button>
          ) : null}
        </div>
      </div>

      {activeOp ? (
        <div className="bg-status-progress/10 border border-status-progress/30 p-4 rounded-xl text-status-progress text-xs flex items-center gap-3" role="status">
          <Icon name="sync" className="animate-spin text-[20px] shrink-0" />
          <div>
            <strong>Active {activeOp.type.toUpperCase()}: {activeOp.id}</strong>
            <p className="text-on-surface-variant mt-0.5">Status: {activeOp.status}. Execution is monitored and durable across reloads.</p>
          </div>
        </div>
      ) : null}

      {/* Current Release Card */}
      <div className="bg-surface-container-low border border-outline-variant/20 rounded-xl p-6 shadow-sm space-y-4">
        <div className="flex items-start justify-between border-b border-outline-variant/20 pb-4">
          <div className="flex items-center gap-3">
            <div className="w-10 h-10 rounded-lg bg-surface-container-high flex items-center justify-center text-primary border border-outline-variant/20">
              <Icon name="check_circle" className="text-[22px] text-status-ready" />
            </div>
            <div>
              <span className="font-label-sm text-[10px] text-on-surface-variant uppercase tracking-wider block">Current Release</span>
              <h2 className="font-headline-md text-xl font-bold text-on-surface">{selectedService.name}</h2>
            </div>
          </div>
          <span className="font-code-md text-xs text-on-surface-variant">
            {release ? `${release.environment_id || "Production"} • ${release.rollout_state || release.status}` : "No factual release"}
          </span>
        </div>

        <dl className="grid grid-cols-2 md:grid-cols-4 gap-4 text-xs">
          <Evidence
            label="Source SHA"
            mono
            value={
              release?.snapshot?.authority.build_record.workload.sha ? (
                <code>{short(release.snapshot.authority.build_record.workload.sha, 16)}</code>
              ) : undefined
            }
          />
          <Evidence
            label="Immutable Digest"
            mono
            value={releaseDigest ? <code>{short(releaseDigest, 22)}</code> : undefined}
          />
          <Evidence label="Runtime Verification" value={pipeline.stages.verify.label} />
          <Evidence
            label="Exposure"
            value={release?.exposure_spec ? `${release.exposure_spec.hostname}${release.exposure_spec.path}` : "Internal Only"}
          />
        </dl>
      </div>

      {/* 5-Step Pipeline Stage Rail */}
      <div className="bg-surface-container-low border border-outline-variant/20 rounded-xl p-6 shadow-sm space-y-4">
        <span className="font-label-sm text-xs text-on-surface-variant uppercase tracking-wider block">Delivery Pipeline Track</span>
        <div className="grid grid-cols-1 md:grid-cols-5 gap-3">
          {stages.map(([key, stage], index) => (
            <button
              key={key}
              onClick={() =>
                console.navigate({
                  tab: stage.targetTab,
                  service: selectedService.id,
                  build: pipeline.latestBuild?.id,
                  deployment: pipeline.linkedDeployment?.id,
                })
              }
              className="p-4 rounded-xl bg-surface-container border border-outline-variant/20 hover:border-outline-variant/50 transition-all text-left flex flex-col justify-between space-y-3 cursor-pointer"
              type="button"
            >
              <div className="flex items-center justify-between">
                <span className="w-6 h-6 rounded-full bg-surface-container-high text-primary flex items-center justify-center font-bold text-xs">
                  {index + 1}
                </span>
                <DeliveryStatus status={stage.status} />
              </div>
              <div>
                <span className="font-label-sm text-[10px] text-on-surface-variant uppercase tracking-wider block">{key}</span>
                <strong className="font-body-md text-sm font-semibold text-on-surface block mt-0.5">{stage.label}</strong>
                <p className="text-[11px] text-on-surface-variant line-clamp-2 mt-1">{stage.explanation}</p>
              </div>
            </button>
          ))}
        </div>
      </div>

      {/* Rollout History & Next Action Split */}
      <div className="grid grid-cols-1 lg:grid-cols-12 gap-6">
        <div className="lg:col-span-7 bg-surface-container-low border border-outline-variant/20 rounded-xl p-6 shadow-sm space-y-4">
          <div className="flex items-center justify-between border-b border-outline-variant/20 pb-3">
            <span className="font-headline-md text-sm font-bold text-on-surface">Recent Rollouts</span>
            <span className="text-xs text-on-surface-variant">{recent.length} deployments</span>
          </div>

          {recent.length ? (
            <div className="space-y-3">
              {recent.map((job) => (
                <div
                  key={job.id}
                  onClick={() => console.navigate({ tab: "deployments", deployment: job.id, service: selectedService.id })}
                  className="p-3.5 bg-surface-container rounded-xl border border-outline-variant/20 hover:border-outline-variant/50 transition-all cursor-pointer flex items-center justify-between gap-3"
                >
                  <div className="flex items-center gap-3 min-w-0">
                    <DeliveryStatus status={job.rollout_state || job.status} />
                    <div className="min-w-0">
                      <strong className="font-body-md text-sm text-on-surface block truncate">{job.rollout_state || job.status}</strong>
                      <span className="text-[11px] text-on-surface-variant">
                        {displayTime(job.updated_at || job.created_at)} • Attempt {job.attempt_count ?? 1}
                      </span>
                    </div>
                  </div>
                  <code className="font-code-md text-[11px] text-on-surface-variant bg-surface-container-highest px-2 py-1 rounded">
                    {short(job.desired_digest || job.snapshot?.image.digest, 16)}
                  </code>
                </div>
              ))}
            </div>
          ) : (
            <Empty text="No rollouts observed yet for this service." title="No Rollouts" />
          )}
        </div>

        <div className="lg:col-span-5 bg-surface-container-low border border-outline-variant/20 rounded-xl p-6 shadow-sm space-y-4">
          <span className="font-headline-md text-sm font-bold text-on-surface">Action Guidance</span>
          <div className="bg-surface-container p-4 rounded-xl border border-outline-variant/20 space-y-2">
            <div className="flex items-center gap-2">
              <DeliveryStatus status={pipeline.stages.verify.status} />
              <strong className="font-body-md text-sm text-on-surface">{pipeline.stages.verify.nextAction}</strong>
            </div>
            <p className="text-xs text-on-surface-variant leading-relaxed">{pipeline.stages.verify.explanation}</p>
          </div>

          {deliveryState.canDeployNewerBuild && deliveryState.newerAcceptedBuild ? (
            <div className="bg-status-ready/10 border border-status-ready/30 p-4 rounded-xl space-y-2">
              <div className="flex items-center gap-2 text-status-ready font-bold text-xs">
                <Icon name="check_circle" className="text-[16px]" />
                <span>Newer Accepted Build Available</span>
              </div>
              <p className="text-xs text-on-surface-variant font-code-md">
                BuildRecord {short(deliveryState.newerAcceptedBuild.id, 16)} with digest{" "}
                {short(deliveryState.newerAcceptedBuild.build.oci_digest, 16)} is ready to deploy.
              </p>
            </div>
          ) : null}
        </div>
      </div>
    </div>
  );
}
