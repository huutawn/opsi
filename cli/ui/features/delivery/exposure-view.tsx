"use client";

import { useEffect } from "react";
import { Empty } from "@/components/ui/primitives";
import { DeploymentDetail } from "@/features/delivery/deployment-detail";
import { ExposureConfigure } from "@/features/delivery/exposure-configure";
import { DeliveryStatus, ServiceFilter, displayTime, short, type DeliveryViewProps } from "@/features/delivery/shared";

export function ExposureView({ console, data, selectedService }: DeliveryViewProps) {
  const exposures = data.exposures.filter((job) => job.exposure_spec && (!selectedService || job.service_id === selectedService.id));
  const selected = exposures.find((job) => job.id === console.route.deployment) ?? exposures[0];
  const selectedState = selected?.rollout_state || selected?.status;
  useEffect(() => { if (!console.route.deployment && selected) console.navigate({ deployment: selected.id, service: selected.service_id }); }, [console, selected]);
  return <div className="deliveryPage"><div className="deliveryToolbar"><ServiceFilter console={console} services={data.services} selected={selectedService} /><p aria-live="polite">{data.exposureError || "Configured routing is not the same as public verification."}</p><ExposureConfigure console={console} data={data} selectedService={selectedService} /></div><div className="masterDetail"><section className="masterList" aria-label="Exposure inventory"><div className="sectionHeading"><div><p className="eyebrow">Factual Routes</p><h2>Exposure</h2></div><span>{exposures.length} configured</span></div>{exposures.length ? <ul>{exposures.map((job) => { const state = exposureState(job.rollout_state || job.status, Boolean(job.readiness_evidence_hash || job.terminal_result?.readiness_evidence_hash)); return <li key={job.id}><button aria-pressed={selected?.id === job.id} onClick={() => console.navigate({ deployment: job.id, service: job.service_id })} type="button"><span><strong>{job.exposure_spec?.hostname}{job.exposure_spec?.path}</strong><small>Port {job.exposure_spec?.service_port} · TLS {job.exposure_spec?.tls.mode}</small></span><span><DeliveryStatus label={state.label} status={state.status} /><small>{displayTime(job.updated_at || job.created_at)}</small></span><code>{short(job.current_digest || job.desired_digest, 18)}</code></button></li>; })}</ul> : <Empty title="No exposure configured" text={data.exposureState === "unavailable" ? "Exposure inventory is unavailable; no route is fabricated." : "Configure exposure only after a base deployment has factual runtime verification."} />}</section><div><section className="exposureTruth"><h2>Configured ≠ Publicly Verified</h2>{selected ? <><p>The route specification is stored. Runtime rollout state is <strong>{selectedState}</strong>.</p><p>{selectedState === "succeeded" && (selected.readiness_evidence_hash || selected.terminal_result?.readiness_evidence_hash) ? "Runtime routing is verified; external DNS/TLS verification is unavailable from this API." : "Public availability and TLS health are not claimed."}</p></> : <p>Select a factual exposure record to inspect runtime evidence.</p>}</section><DeploymentDetail console={console} data={data} selected={selected} /></div></div></div>;
}

function exposureState(state: string, evidence: boolean) {
  if (state === "rolled_back") return { status: "rolled_back", label: "Rolled Back" };
  if (["failed", "rollback_failed"].includes(state)) return { status: "failed", label: "Failed" };
  if (state === "succeeded" && evidence) return { status: "succeeded", label: "Runtime Verified" };
  if (state === "succeeded") return { status: "not_reported", label: "Verification Not Reported" };
  if (["prepared", "queued", "leased"].includes(state)) return { status: "waiting", label: "Rollout Pending" };
  return { status: "in_progress", label: "Runtime Applying" };
}
