"use client";

import { Empty } from "@/components/ui/primitives";
import { DeliveryStatus, Evidence, ServiceFilter, displayTime, short, type DeliveryViewProps } from "@/features/delivery/shared";
import { derivePipeline, type PipelineStage } from "@/lib/presentation/delivery/model";

export function PipelineView({ console, data, selectedService }: DeliveryViewProps) {
  if (!selectedService) return <Empty title="No delivery service" text="Add an application service, then bind it to a repository before Delivery can show a causal pipeline." />;
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
  const stages = Object.entries(pipeline.stages) as Array<[string, PipelineStage]>;
  const release = pipeline.currentRelease;
  const releaseDigest = release?.current_digest || release?.terminal_result?.current_digest;
  const recent = data.deployments.filter((job) => job.service_id === selectedService.id).slice(0, 5);

  return <div className="deliveryPage">
    <div className="deliveryToolbar"><ServiceFilter console={console} services={data.services} selected={selectedService} /><p aria-live="polite">{data.sourceError || data.buildError || data.deploymentError || "Factual state loaded through the Local API."}</p></div>
    <section className="releaseSummary" aria-labelledby="current-release-title">
      <div><p className="eyebrow">Current Release</p><h2 id="current-release-title">{selectedService.name}</h2><p>{release ? `${release.environment_id || "Environment not reported"} · ${release.rollout_state || release.status}` : "No factual current release"}</p></div>
      <dl>
        <Evidence label="Source SHA" value={release?.snapshot?.authority.build_record.workload.sha ? <code aria-label={`Source SHA ${release.snapshot.authority.build_record.workload.sha}`} title={release.snapshot.authority.build_record.workload.sha}>{short(release.snapshot.authority.build_record.workload.sha, 16)}</code> : undefined} mono />
        <Evidence label="Immutable digest" value={releaseDigest ? <code aria-label={`Immutable digest ${releaseDigest}`} title={releaseDigest}>{short(releaseDigest, 22)}</code> : undefined} mono />
        <Evidence label="Runtime verification" value={pipeline.stages.verify.label} />
        <Evidence label="Exposure" value={release?.exposure_spec ? `${release.exposure_spec.hostname}${release.exposure_spec.path}` : "Not configured"} />
      </dl>
    </section>
    <ol className="stageRail" aria-label="Immutable delivery pipeline">
      {stages.map(([key, stage], index) => <li data-status={stage.status} key={key}>
        <button onClick={() => console.navigate({ tab: stage.targetTab, service: selectedService.id, build: pipeline.latestBuild?.id, deployment: pipeline.linkedDeployment?.id })} type="button">
          <span className="stageIndex" aria-hidden="true">{index + 1}</span><span><small>{key}</small><strong>{stage.label}</strong><em>{stage.explanation}</em></span><DeliveryStatus status={stage.status} />
        </button>
      </li>)}
    </ol>
    <div className="deliverySplit">
      <section className="deliverySection"><div className="sectionHeading"><div><p className="eyebrow">Rollout Timeline</p><h2>Active & Recent Rollouts</h2></div></div>{recent.length ? <ol className="rolloutList">{recent.map((job) => <li key={job.id}><button aria-pressed={console.route.deployment === job.id} onClick={() => console.navigate({ tab: "deployments", deployment: job.id, service: selectedService.id })} type="button"><DeliveryStatus status={job.rollout_state || job.status} /><span><strong>{job.rollout_state || job.status}</strong><small>{displayTime(job.updated_at || job.created_at)} · attempt {job.attempt_count ?? 0}</small></span><code>{short(job.desired_digest || job.snapshot?.image.digest, 20)}</code></button></li>)}</ol> : <Empty title="No rollout observed" text="A successful BuildRecord does not imply that a DeploymentJob exists." />}</section>
      <section className="deliverySection"><div className="sectionHeading"><div><p className="eyebrow">Next Action</p><h2>Blockers & Release History</h2></div></div><div className="nextAction"><DeliveryStatus status={pipeline.stages.verify.status} /><strong>{pipeline.stages.verify.nextAction}</strong><p>{pipeline.stages.verify.explanation}</p></div>{pipeline.unlinkedDeployments.length ? <div className="truthCallout"><strong>Unlinked historical record</strong><p>{pipeline.unlinkedDeployments.length} deployment record(s) lack canonical BuildRecord identity and are not correlated heuristically.</p></div> : null}</section>
    </div>
  </div>;
}
