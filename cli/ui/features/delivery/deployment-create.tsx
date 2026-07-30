"use client";

import { useRef, useState } from "react";
import { DeliveryStatus, short, type DeliveryViewProps } from "@/features/delivery/shared";
import { LocalClient } from "@/lib/api/local-client";
import type { DeploymentPreview, WorkloadSpec } from "@/lib/contracts/registry";

const schemaVersion = "opsi.deployment_create/v1";

export function DeploymentCreate({ console, data, selectedService }: DeliveryViewProps) {
  const dialog = useRef<HTMLDialogElement>(null);
  const client = useRef(new LocalClient()).current;
  const [serviceID, setServiceID] = useState(selectedService?.id ?? "");
  const [buildID, setBuildID] = useState(console.route.build ?? "");
  const [environmentID, setEnvironmentID] = useState("");
  const [replicas, setReplicas] = useState("");
  const [port, setPort] = useState("");
  const [cpuRequest, setCPURequest] = useState("");
  const [memoryRequest, setMemoryRequest] = useState("");
  const [cpuLimit, setCPULimit] = useState("");
  const [memoryLimit, setMemoryLimit] = useState("");
  const [readinessMode, setReadinessMode] = useState("");
  const [readinessPath, setReadinessPath] = useState("");
  const [readinessInitialDelay, setReadinessInitialDelay] = useState("");
  const [readinessPeriod, setReadinessPeriod] = useState("");
  const [readinessTimeout, setReadinessTimeout] = useState("");
  const [readinessFailures, setReadinessFailures] = useState("");
  const [termination, setTermination] = useState("");
  const [preview, setPreview] = useState<DeploymentPreview | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const builds = data.builds.filter((record) => record.service_id === serviceID && record.build.status === "succeeded");
  const selectedBuild = builds.find((record) => record.id === buildID) ?? builds[0];
  const environment = data.placement?.environments.find((item) => item.id === environmentID) ?? data.placement?.environments.find((item) => item.status === "active");
  const assignment = data.topology?.assignments.find((item) => item.service_key === selectedBuild?.service_key && item.environment_id === environment?.id);

  function open() {
    const nextService = selectedService ?? data.services.find((item) => item.type === "application");
    const nextBuild = data.builds.find((record) => record.id === console.route.build) ?? data.builds.find((record) => record.service_id === nextService?.id && record.build.status === "succeeded");
    const nextEnvironment = data.placement?.environments.find((item) => item.status === "active") ?? data.placement?.environments[0];
    const nextAssignment = data.topology?.assignments.find((item) => item.service_key === nextBuild?.service_key && item.environment_id === nextEnvironment?.id);
    setServiceID(nextService?.id ?? "");
    setBuildID(nextBuild?.id ?? "");
    setEnvironmentID(nextEnvironment?.id ?? "");
    setReplicas(nextAssignment ? String(nextAssignment.replicas) : "");
    setPort(nextService?.container_port ? String(nextService.container_port) : "");
    setCPURequest(nextAssignment ? `${nextAssignment.cpu_request_millicores}m` : "");
    setMemoryRequest(nextAssignment ? memory(nextAssignment.memory_request_bytes) : "");
    setCPULimit("");
    setMemoryLimit("");
    setReadinessMode(nextService?.health_path ? "http" : "");
    setReadinessPath(nextService?.health_path ?? "");
    setReadinessInitialDelay("");
    setReadinessPeriod("");
    setReadinessTimeout("");
    setReadinessFailures("");
    setTermination("");
    setPreview(null);
    setError("");
    dialog.current?.showModal();
  }

  const request = buildRequest();
  function buildRequest() {
    if (!selectedBuild || !environment || !positiveInteger(replicas) || !positiveInteger(port) || !cpuRequest || !memoryRequest || !cpuLimit || !memoryLimit || !positiveInteger(termination) || !readinessMode || (readinessMode === "http" && (!readinessPath.startsWith("/") || !positiveInteger(readinessInitialDelay) || !positiveInteger(readinessPeriod) || !positiveInteger(readinessTimeout) || !positiveInteger(readinessFailures)))) return null;
    const numericPort = Number(port);
    const workload: WorkloadSpec = {
      schema_version: "opsi.workload_spec/v1",
      service_key: selectedBuild.service_key,
      replicas: Number(replicas),
      application_container_name: "app",
      container_port: numericPort,
      resources: { requests: { cpu: cpuRequest, memory: memoryRequest }, limits: { cpu: cpuLimit, memory: memoryLimit } },
      termination_grace_period_seconds: Number(termination),
      exposure: { mode: assignment?.exposure.mode === "none" ? "none" : "internal" },
      ...(readinessMode === "http" ? { readiness_probe: { path: readinessPath, port: numericPort, initial_delay_seconds: Number(readinessInitialDelay), period_seconds: Number(readinessPeriod), timeout_seconds: Number(readinessTimeout), failure_threshold: Number(readinessFailures) } } : {}),
    };
    return { schema_version: schemaVersion, build_record_id: selectedBuild.id, environment_id: environment.id, workload };
  }

  async function previewDeployment() {
    if (!request) { setError("Enter every explicit deployment input. No port, replica, resource, termination, or readiness default is assumed."); return; }
    setBusy(true); setError("");
    try {
      const [decision, diff] = await Promise.all([client.deploymentPreview(console.state.project?.id ?? "", request), client.deploymentDiff(console.state.project?.id ?? "", request)]);
      setPreview({ ...decision, changes: diff.changes });
    } catch (reason) { setError(reason instanceof Error ? reason.message : "Deployment preview failed."); }
    finally { setBusy(false); }
  }

  function apply() {
    if (!request || !preview?.eligible) return;
    console.reviewMutation({ project: console.state.project?.name || console.state.project?.id || "project", targetType: "deployment", targetID: request.build_record_id, operation: "apply", diff: preview.changes.length ? preview.changes : [`replicas: ${replicas}`, `port: ${port}`, `CPU: ${cpuRequest} → ${cpuLimit}`, `memory: ${memoryRequest} → ${memoryLimit}`, `readiness: ${readinessMode === "http" ? readinessPath : "explicitly disabled"}`], risk: "Creates one canonical rollout from the reviewed immutable BuildRecord." }, async (key) => {
      const created = await client.deploymentApply(console.state.project?.id ?? "", request, key);
      data.mergeDeployment(created);
      console.navigate({ deployment: created.id, build: request.build_record_id, service: serviceID });
      dialog.current?.close();
      await data.refreshDeployments();
      return `Deployment ${created.id} returned factual status ${created.status}.`;
    });
  }

  return <><button className="primary" onClick={open} type="button">Create Deployment</button><dialog aria-labelledby="create-deployment-title" className="deliveryDialog" ref={dialog}><form method="dialog"><div className="dialogHeading"><div><p className="eyebrow">Reviewed Mutation</p><h2 id="create-deployment-title">Create Immutable Deployment</h2><p>Every workload value is authoritative or explicitly entered.</p></div><button aria-label="Close deployment creation" type="submit">Close</button></div></form><div className="mutationSteps" aria-label="Deployment creation steps"><span>1 Service</span><span>2 Artifact</span><span>3 Target</span><span>4 Workload</span><span>5 Preview</span></div><div className="deploymentForm"><label>Service<select value={serviceID} onChange={(event) => { setServiceID(event.target.value); setBuildID(""); setPreview(null); }}>{data.services.filter((item) => item.type === "application").map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label><label>Accepted BuildRecord<select value={selectedBuild?.id ?? ""} onChange={(event) => { setBuildID(event.target.value); setPreview(null); }}>{builds.map((record) => <option key={record.id} value={record.id}>{short(record.workload.sha, 12)} · {short(record.build.oci_digest, 18)}</option>)}</select></label><label>Environment<select value={environment?.id ?? ""} onChange={(event) => { setEnvironmentID(event.target.value); setPreview(null); }}>{data.placement?.environments.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label><label>Replicas<input inputMode="numeric" min="1" name="replicas" type="number" value={replicas} onChange={(event) => { setReplicas(event.target.value); setPreview(null); }} /></label><label>Container Port<input inputMode="numeric" min="1" name="container_port" type="number" value={port} onChange={(event) => { setPort(event.target.value); setPreview(null); }} /></label><label>CPU Request<input name="cpu_request" placeholder="Example: 250m…" value={cpuRequest} onChange={(event) => { setCPURequest(event.target.value); setPreview(null); }} /></label><label>CPU Limit<input name="cpu_limit" placeholder="Example: 500m…" value={cpuLimit} onChange={(event) => { setCPULimit(event.target.value); setPreview(null); }} /></label><label>Memory Request<input name="memory_request" placeholder="Example: 256Mi…" value={memoryRequest} onChange={(event) => { setMemoryRequest(event.target.value); setPreview(null); }} /></label><label>Memory Limit<input name="memory_limit" placeholder="Example: 512Mi…" value={memoryLimit} onChange={(event) => { setMemoryLimit(event.target.value); setPreview(null); }} /></label><label>Readiness<select value={readinessMode} onChange={(event) => { setReadinessMode(event.target.value); setPreview(null); }}><option value="">Choose Explicitly…</option><option value="http">HTTP Probe</option><option value="none">No HTTP Probe</option></select></label>{readinessMode === "http" ? <><label>Readiness Path<input name="readiness_path" placeholder="Example: /healthz…" value={readinessPath} onChange={(event) => { setReadinessPath(event.target.value); setPreview(null); }} /></label><label>Readiness Initial Delay Seconds<input inputMode="numeric" min="1" name="readiness_initial_delay" type="number" value={readinessInitialDelay} onChange={(event) => { setReadinessInitialDelay(event.target.value); setPreview(null); }} /></label><label>Readiness Period Seconds<input inputMode="numeric" min="1" name="readiness_period" type="number" value={readinessPeriod} onChange={(event) => { setReadinessPeriod(event.target.value); setPreview(null); }} /></label><label>Readiness Timeout Seconds<input inputMode="numeric" min="1" name="readiness_timeout" type="number" value={readinessTimeout} onChange={(event) => { setReadinessTimeout(event.target.value); setPreview(null); }} /></label><label>Readiness Failure Threshold<input inputMode="numeric" min="1" name="readiness_failures" type="number" value={readinessFailures} onChange={(event) => { setReadinessFailures(event.target.value); setPreview(null); }} /></label></> : null}<label>Termination Grace Seconds<input inputMode="numeric" min="1" name="termination_seconds" type="number" value={termination} onChange={(event) => { setTermination(event.target.value); setPreview(null); }} /></label></div>{error ? <p className="inlineError" role="alert">{error}</p> : null}<div className="buttonRow"><button disabled={!request || busy} onClick={() => void previewDeployment()} type="button">{busy ? "Previewing…" : "Preview Decision & Diff"}</button></div>{preview ? <div className="previewResult"><DeliveryStatus status={preview.eligible ? "succeeded" : "failed"} label={preview.decision_code} /><p>{preview.message}</p><ul>{preview.changes.map((change) => <li key={change}>{change}</li>)}</ul><button className="primary" disabled={!preview.eligible || busy} onClick={apply} type="button">Review & Submit Deployment</button></div> : null}</dialog></>;
}

function positiveInteger(value: string) { return Number.isInteger(Number(value)) && Number(value) > 0; }
function memory(bytes: number) { return bytes % (1024 * 1024) === 0 ? `${bytes / (1024 * 1024)}Mi` : String(bytes); }
