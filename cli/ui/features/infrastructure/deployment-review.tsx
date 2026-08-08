"use client";

import { useEffect, useMemo, useState } from "react";
import { StatusBadge } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";
import { LocalAPIError, LocalClient, type ResolvedDeploymentRequest } from "@/lib/api/local-client";
import type { BuildRecord, DeploymentJob, DeploymentPolicy, DeploymentPreview, PlacementFacts, ServiceRecord, TopologyPlan } from "@/lib/contracts/registry";
import { deploymentStage, reviewFingerprint, reviewSubmissionKey, retryableReviewStates, type ReviewSubmitState } from "@/features/infrastructure/deployment-review-model";

type ReviewEntry = {
  service: ServiceRecord;
  build?: BuildRecord;
  request?: ResolvedDeploymentRequest;
  preview?: DeploymentPreview;
  state: ReviewSubmitState | "ready";
  job?: DeploymentJob;
  error?: string;
};

type Props = {
  builds: BuildRecord[];
  console: ConsoleController;
  facts: PlacementFacts;
  onLive: () => void;
  policies: DeploymentPolicy[];
  topology: TopologyPlan;
};

export function DeploymentReview({ builds, console, facts, onLive, policies, topology }: Props) {
  const client = useMemo(() => new LocalClient(), []);
  const services = console.state.services.filter((service) => service.type === "application" && facts.services.some((fact) => fact.id === service.id));
  const assignments = useMemo(() => new Map(topology.assignments.map((assignment) => [assignment.service_key, assignment])), [topology]);
  const acceptedBuilds = (service: ServiceRecord) => builds.filter((build) => build.service_id === service.id && build.service_key === service.name && build.build.status === "succeeded").sort((a, b) => b.created_at.localeCompare(a.created_at));
  const latestBuild = (service: ServiceRecord) => acceptedBuilds(service)[0];
  const placed = services.filter((service) => assignments.has(service.name));
  const [selected, setSelected] = useState<Record<string, boolean>>(() => Object.fromEntries(placed.map((service) => [service.id, true])));
  const [buildIDs, setBuildIDs] = useState<Record<string, string>>({});
  const [entries, setEntries] = useState<Record<string, ReviewEntry>>({});
  const [reviewID, setReviewID] = useState("");
  const [reviewAuthority, setReviewAuthority] = useState("");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const authorityFingerprint = reviewFingerprint([
    topology.revision, topology.plan_hash,
    ...placed.flatMap((service) => { const build = latestBuild(service); const config = service.configuration; return [service.id, build?.id, config?.revision, config?.state_hash, policies.map((policy) => `${policy.id}:${policy.revision}:${policy.policy_hash}`).join(",")]; }),
  ]);

  useEffect(() => {
    if (reviewID && reviewAuthority && authorityFingerprint !== reviewAuthority) {
      setEntries({});
      setReviewID("");
      setReviewAuthority("");
      setMessage("Topology, configuration, BuildRecord, or policy changed. Review deployment again.");
    }
  }, [authorityFingerprint, reviewAuthority, reviewID]);

  function toggle(serviceID: string) {
    setSelected((current) => ({ ...current, [serviceID]: !current[serviceID] }));
    resetReview();
  }

  function resetReview() {
    setEntries({});
    setReviewID("");
    setReviewAuthority("");
    setMessage("");
  }

  async function review() {
    const reviewKey = crypto.randomUUID();
    const chosen = placed.filter((service) => selected[service.id]);
    if (!chosen.length) { setMessage("Select at least one placed application."); return; }
    setBusy(true); setMessage(""); setEntries({}); setReviewAuthority("");
    const next: Record<string, ReviewEntry> = {};
    await Promise.all(chosen.map(async (service) => {
      const assignment = assignments.get(service.name);
      const build = builds.find((item) => item.id === buildIDs[service.id]) ?? latestBuild(service);
      const config = service.configuration;
      if (!assignment) { next[service.id] = blocked(service, "No applied topology assignment exists."); return; }
      if (!build) { next[service.id] = blocked(service, "No succeeded accepted BuildRecord is available."); return; }
      if (!config || !config.state_hash || config.state_hash.length !== 64) { next[service.id] = blocked(service, "Service configuration revision/hash is unavailable; apply configuration first."); return; }
      const request: ResolvedDeploymentRequest = { schema_version: "opsi.deployment_job/v1", build_record_id: build.id, environment_id: assignment.environment_id, expected_topology_revision: topology.revision, expected_topology_hash: topology.plan_hash, expected_configuration_revision: config.revision, expected_configuration_state_hash: config.state_hash };
      try {
        const preview = await client.deploymentPreview(facts.project_id, request);
        const authority = preview.snapshot.authority;
        const reviewedRequest = { ...request, expected_deployment_policy_revision: authority.deployment_policy_revision, expected_deployment_policy_hash: authority.deployment_policy_hash };
        next[service.id] = { service, build, request: reviewedRequest, preview, state: preview.eligible ? "ready" : "blocked", error: preview.eligible ? undefined : preview.message };
      } catch (error) {
        next[service.id] = { service, build, request, state: "blocked", error: error instanceof Error ? error.message : "Cloud rejected deployment review." };
      }
    }));
    setEntries(next); setReviewID(reviewKey); setReviewAuthority(authorityFingerprint); setBusy(false);
  }

  async function submit() {
    const reviewKey = reviewID;
    if (!reviewKey || !Object.values(entries).some((entry) => entry.state === "ready" || retryableReviewStates(entry.state))) return;
    setBusy(true); setMessage("");
    let created = 0;
    for (const entry of Object.values(entries)) {
      if (!entry.request || entry.state !== "ready" && !retryableReviewStates(entry.state)) continue;
      setEntries((current) => ({ ...current, [entry.service.id]: { ...current[entry.service.id], state: "queued", error: undefined } }));
      try {
        const job = await client.deploymentApply(facts.project_id, entry.request, reviewSubmissionKey(reviewKey, entry.service.id));
        created++;
        const state = (job.rollout_state || job.status) === "succeeded" ? "succeeded" : "queued";
        setEntries((current) => ({ ...current, [entry.service.id]: { ...current[entry.service.id], state, job } }));
      } catch (error) {
        if (error instanceof LocalAPIError && ["TOPOLOGY_REVIEW_STALE", "CONFIGURATION_REVIEW_STALE", "POLICY_REVIEW_STALE", "ROUTING_TOPOLOGY_CHANGED", "ROUTING_POLICY_CHANGED"].includes(error.code)) {
          setEntries({}); setReviewID(""); setReviewAuthority(""); setMessage("Authority changed after review. The review was closed; review again."); break;
        }
        setEntries((current) => ({ ...current, [entry.service.id]: { ...current[entry.service.id], state: "failed", error: error instanceof Error ? error.message : "Deployment submission failed." } }));
      }
    }
    if (created) {
      try { await console.actions.load(); }
      catch (error) { setMessage(error instanceof Error ? `Jobs were accepted, but refresh failed: ${error.message}` : "Jobs were accepted, but refresh failed."); }
      onLive();
    }
    setBusy(false);
  }

  if (!topology || !placed.length) return <section className="deploymentReview" aria-labelledby="deployment-review-heading"><div className="sectionHeading"><div><p className="eyebrow">Deployment authority</p><h3 id="deployment-review-heading">Review deployment</h3></div></div><p className="muted">No placed application is ready for review. Apply a TopologyPlan before creating DeploymentJobs.</p></section>;
  return <section className="deploymentReview" aria-labelledby="deployment-review-heading"><div className="sectionHeading"><div><p className="eyebrow">Deployment authority</p><h3 id="deployment-review-heading">Review deployment</h3><p>Cloud compiles immutable WorkloadSpec from this applied topology. Resources, probes, environment, and runtime are not editable here.</p></div><span>{placed.length} placed</span></div><div className="deploymentReviewToolbar"><label><input aria-label="Select all placed applications" checked={placed.every((service) => selected[service.id])} onChange={(event) => { setSelected(Object.fromEntries(placed.map((service) => [service.id, event.target.checked]))); resetReview(); }} type="checkbox" /> Select all</label><button className="secondaryAction" disabled={busy} onClick={() => void review()} type="button">{busy ? "Working…" : "Review selected"}</button><button className="primary" disabled={busy || !Object.values(entries).some((entry) => entry.state === "ready" || retryableReviewStates(entry.state))} onClick={() => void submit()} type="button">Submit missing jobs</button></div>{message ? <p className="truthCallout" role="status">{message}</p> : null}<ul className="deploymentReviewList">{placed.map((service) => <ReviewRow builds={acceptedBuilds(service)} buildID={buildIDs[service.id]} entry={entries[service.id]} key={service.id} onBuild={(id) => { setBuildIDs((current) => ({ ...current, [service.id]: id })); resetReview(); }} onToggle={() => toggle(service.id)} selected={Boolean(selected[service.id])} service={service} />)}</ul></section>;
}

function blocked(service: ServiceRecord, error: string): ReviewEntry { return { service, state: "blocked", error }; }

function ReviewRow({ builds, buildID, entry, onBuild, onToggle, selected, service }: { builds: BuildRecord[]; buildID?: string; entry?: ReviewEntry; onBuild: (id: string) => void; onToggle: () => void; selected: boolean; service: ServiceRecord }) {
  const selectedBuild = builds.find((build) => build.id === buildID) ?? builds[0];
  const snapshot = entry?.preview?.snapshot;
  const workload = snapshot?.workload;
  const localError = reviewBlockReason(service, selectedBuild);
  const fallbackState = localError ? "blocked" : "pending";
  return <li className="deploymentReviewRow" data-state={entry?.state ?? fallbackState}><label className="deploymentReviewSelect"><input aria-label={`Select ${service.name}`} checked={selected} onChange={onToggle} type="checkbox" /><strong>{service.name}</strong></label><label className="deploymentReviewBuild">Accepted BuildRecord<select aria-label={`${service.name} BuildRecord`} disabled={!builds.length} onChange={(event) => onBuild(event.target.value)} value={selectedBuild?.id ?? ""}>{builds.length ? builds.map((build) => <option key={build.id} value={build.id}>{build.id} · {build.build.oci_digest.slice(0, 18)}</option>) : <option value="">No succeeded build</option>}</select></label><span className="deploymentReviewMeta">{workload && snapshot ? <><code title={snapshot.image.digest}>{snapshot.image.digest}</code><small>{snapshot.authority.runtime_id} · {workload.replicas} replicas · {workload.resources.requests.cpu} / {workload.resources.requests.memory}</small><small>probes {workload.readiness_probe?.path || "none"} / {workload.liveness_probe?.path || "none"}</small><small>env {workload.environment?.map((item) => `${item.name}=${item.value}`).join(", ") || "none"}</small><small>configuration revision {snapshot.authority.service_configuration_revision ?? "unavailable"}</small></> : <small>{entry?.error || localError || "Review to resolve immutable workload."}</small>}</span><span className="deploymentReviewStatus"><StatusBadge value={entry?.state === "ready" ? "reviewed" : entry?.state ?? fallbackState} /><small>{entry?.job ? `${deploymentStage(entry.job)} · ${entry.job.id}` : entry?.error || localError || "Awaiting review"}</small></span></li>;
}

function reviewBlockReason(service: ServiceRecord, build?: BuildRecord) {
  if (!build) return "No succeeded accepted BuildRecord is available.";
  if (!service.configuration?.state_hash || service.configuration.state_hash.length !== 64) return "Service configuration revision/hash is unavailable; apply configuration first.";
  return "";
}
