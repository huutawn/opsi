"use client";

import { useRef, useState } from "react";
import { DeliveryStatus, short } from "@/features/delivery/shared";
import type { DeliveryViewProps } from "@/features/delivery/shared";
import { LocalClient } from "@/lib/api/local-client";
import type { ExposurePreview, ExposureSpec } from "@/lib/contracts/registry";

export function ExposureConfigure({ console, data }: DeliveryViewProps) {
  const dialog = useRef<HTMLDialogElement>(null);
  const client = useRef(new LocalClient()).current;
  const bases = data.deployments.filter((job) => job.snapshot && ["succeeded", "rolled_back"].includes(job.rollout_state || job.status) && job.terminal_result?.application_image_id && (job.readiness_evidence_hash || job.terminal_result.readiness_evidence_hash));
  const [baseID, setBaseID] = useState("");
  const [hostname, setHostname] = useState("");
  const [path, setPath] = useState("/");
  const [tlsMode, setTLSMode] = useState<"disabled" | "secret_ref">("disabled");
  const [tlsReference, setTLSReference] = useState("");
  const [deploymentID, setDeploymentID] = useState("");
  const [preview, setPreview] = useState<ExposurePreview | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const base = bases.find((job) => job.id === baseID) ?? bases[0];

  function open() {
    setBaseID(bases[0]?.id ?? "");
    setHostname(""); setPath("/"); setTLSMode("disabled"); setTLSReference(""); setDeploymentID(`dep-ui-${crypto.randomUUID().replaceAll("-", "").slice(0, 24)}`); setPreview(null); setError("");
    dialog.current?.showModal();
  }

  async function request(includeStateHash: boolean) {
    if (!base?.snapshot || !hostname || !path.startsWith("/") || (tlsMode === "secret_ref" && !tlsReference)) throw new Error("Choose a verified base deployment and enter an exact hostname, path, and opaque TLS reference when enabled.");
    const normalizedPath = path !== "/" && path.endsWith("/") ? path.slice(0, -1) : path;
    const draft: Omit<ExposureSpec, "spec_hash"> = { schema_version: "opsi.exposure_spec/v1", project_id: console.state.project?.id ?? "", environment_id: base.environment_id ?? "", runtime_id: base.runtime_id ?? "", service_key: base.snapshot.workload.service_key, deployment_job_id: deploymentID, hostname: hostname.toLowerCase(), path: normalizedPath, service_port: base.snapshot.workload.container_port, tls: tlsMode === "disabled" ? { mode: "disabled" } : { mode: "secret_ref", secret_ref: tlsReference } };
    return { schema_version: "opsi.exposure_mutation/v1" as const, base_deployment_job_id: base.id, ...(includeStateHash && preview ? { expected_state_hash: preview.state_hash } : {}), exposure: { ...draft, spec_hash: await hashExposure(draft) } };
  }

  async function previewExposure() {
    setBusy(true); setError("");
    try {
      const body = await request(false);
      const [decision, diff] = await Promise.all([client.exposurePreview(console.state.project?.id ?? "", body), client.exposureDiff(console.state.project?.id ?? "", body)]);
      setPreview({ ...decision, changes: diff.changes });
    } catch (reason) { setError(reason instanceof Error ? reason.message : "Exposure preview failed."); }
    finally { setBusy(false); }
  }

  function apply() {
    if (!preview?.eligible) return;
    console.reviewMutation({ project: console.state.project?.name || console.state.project?.id || "project", targetType: "exposure", targetID: preview.desired.spec_hash, operation: "apply", diff: preview.changes, risk: "Changes external routing. Runtime and external verification remain separate factual states." }, async (key) => {
      const created = await client.exposureApply(console.state.project?.id ?? "", await request(true), key);
      data.mergeDeployment(created);
      console.navigate({ deployment: created.id, service: created.service_id });
      dialog.current?.close();
      await data.refreshDeployments();
      return `Exposure rollout ${created.id} returned factual state ${created.rollout_state || created.status}.`;
    });
  }

  return <><button className="primary" disabled={!bases.length} onClick={open} type="button">Configure Exposure</button><dialog aria-labelledby="configure-exposure-title" className="deliveryDialog" ref={dialog}><form method="dialog"><div className="dialogHeading"><div><p className="eyebrow">Post-Deployment Mutation</p><h2 id="configure-exposure-title">Configure Exposure</h2><p>Exposure starts from a verified deployment and never fetches a secret value.</p></div><button aria-label="Close exposure configuration" type="submit">Close</button></div></form><div className="deploymentForm"><label>Verified Base Deployment<select value={base?.id ?? ""} onChange={(event) => { setBaseID(event.target.value); setPreview(null); }}>{bases.map((job) => <option key={job.id} value={job.id}>{job.id} · {short(job.current_digest, 18)}</option>)}</select></label><label>Service Port<input disabled value={base?.snapshot?.workload.container_port ?? ""} /></label><label>Hostname<input autoComplete="off" name="hostname" placeholder="api.example.com…" value={hostname} onChange={(event) => { setHostname(event.target.value); setPreview(null); }} /></label><label>Path<input autoComplete="off" name="path" placeholder="/api…" value={path} onChange={(event) => { setPath(event.target.value); setPreview(null); }} /></label><label>TLS<select value={tlsMode} onChange={(event) => { setTLSMode(event.target.value as "disabled" | "secret_ref"); setPreview(null); }}><option value="disabled">Disabled</option><option value="secret_ref">Opaque Secret Reference</option></select></label>{tlsMode === "secret_ref" ? <label>TLS Secret Reference<input autoComplete="off" name="tls_reference" placeholder="secret reference…" value={tlsReference} onChange={(event) => { setTLSReference(event.target.value); setPreview(null); }} /></label> : null}</div>{error ? <p className="inlineError" role="alert">{error}</p> : null}<div className="buttonRow"><button disabled={!base || !hostname || busy} onClick={() => void previewExposure()} type="button">{busy ? "Previewing…" : "Preview Decision & Diff"}</button></div>{preview ? <div className="previewResult"><DeliveryStatus label={preview.decision_code} status={preview.eligible ? "succeeded" : "failed"} /><p>{preview.message}</p><ul>{preview.changes.map((change) => <li key={change}>{change}</li>)}</ul><button className="primary" disabled={!preview.eligible || busy} onClick={apply} type="button">Review & Apply Exposure</button></div> : null}</dialog></>;
}

async function hashExposure(spec: Omit<ExposureSpec, "spec_hash">) {
  const data = new TextEncoder().encode(JSON.stringify({ ...spec, spec_hash: "" }));
  const digest = await crypto.subtle.digest("SHA-256", data);
  return Array.from(new Uint8Array(digest), (value) => value.toString(16).padStart(2, "0")).join("");
}
