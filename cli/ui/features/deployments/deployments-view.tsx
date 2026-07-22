"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Empty, Panel, StatePanel, StatusBadge } from "@/components/ui/primitives";
import { DeploymentsTable, EventsList } from "@/features/console/shared";
import { LocalClient } from "@/lib/api/local-client";
import type { BuildRecord, DeploymentJob, DeploymentPreview, ExposureMutationRequest, ExposurePreview, ExposureSpec, PlacementFacts, ServiceRecord, TopologyPlan, WorkloadSpec } from "@/lib/contracts/registry";
import type { ConsoleController } from "@/features/console/types";

const schemaVersion = "opsi.deployment_job/v1" as const;
const rolloutStateLabels: Record<string, string> = {
  rolling_back: "Rolling back",
  rolled_back: "Rolled back",
  rollback_failed: "Rollback failed",
};

export function DeploymentsView({ console }: { console: ConsoleController }) {
  const projectID = console.state.project?.id ?? "";
  const client = useMemo(() => new LocalClient(), []);
  const [services, setServices] = useState<ServiceRecord[]>([]);
  const [records, setRecords] = useState<BuildRecord[]>([]);
  const [facts, setFacts] = useState<PlacementFacts | null>(null);
  const [topology, setTopology] = useState<TopologyPlan | null>(null);
  const [serviceID, setServiceID] = useState("");
  const [repositoryID, setRepositoryID] = useState("");
  const [recordID, setRecordID] = useState("");
  const [environmentID, setEnvironmentID] = useState("");
  const [replicas, setReplicas] = useState(1);
  const [port, setPort] = useState(8080);
  const [readinessPath, setReadinessPath] = useState("");
  const [preview, setPreview] = useState<DeploymentPreview | null>(null);
  const [selectedJobID, setSelectedJobID] = useState("");
  const [job, setJob] = useState<DeploymentJob | null>(null);
  const [events, setEvents] = useState(console.state.deploymentEvents);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [disconnected, setDisconnected] = useState(false);
  const [baseDeploymentID, setBaseDeploymentID] = useState("");
  const [exposureJobID, setExposureJobID] = useState("");
  const [hostname, setHostname] = useState("");
  const [exposurePath, setExposurePath] = useState("/");
  const [tlsMode, setTLSMode] = useState<"disabled" | "secret_ref">("disabled");
  const [tlsReference, setTLSReference] = useState("");
  const [exposurePreview, setExposurePreview] = useState<ExposurePreview | null>(null);
  const [confirmExposure, setConfirmExposure] = useState(false);
  const [confirmRollback, setConfirmRollback] = useState(false);
  const selectionOverride = useRef("");

  const loadOptions = useCallback(async () => {
    if (!projectID) return;
    setLoading(true);
    setError("");
    try {
      const [serviceResult, buildResult, placementResult, topologyResult] = await Promise.all([client.services(projectID), client.buildRecords(projectID, { status: "succeeded" }), client.placementFacts(projectID), client.topology(projectID)]);
      setServices(serviceResult.services ?? []);
      setRecords(buildResult.records ?? []);
      setFacts(placementResult);
      setTopology(topologyResult);
      setServiceID((current) => current || serviceResult.services?.find((item) => item.type === "application")?.id || "");
      setEnvironmentID((current) => current || placementResult.environments.find((item) => item.status === "active")?.id || "");
      setDisconnected(false);
    } catch (reason) {
      setError((reason as Error).message);
      setDisconnected(true);
    } finally {
      setLoading(false);
    }
  }, [client, projectID]);

  useEffect(() => {
    queueMicrotask(() => void loadOptions());
  }, [loadOptions]);

  useEffect(() => {
    if (selectedJobID || selectionOverride.current || !projectID) return;
    const recoverable = console.state.deployments.find((item) => item.mode === "immutable_image");
    if (!recoverable) return;
    queueMicrotask(() => {
      if (selectionOverride.current) return;
      setSelectedJobID(recoverable.id);
      setJob(recoverable);
    });
  }, [console.state.deployments, projectID, selectedJobID]);

  // BuildRecord.service_key is the authority; service.name is only a display label.
  const serviceRecords = records.filter((item) => item.service_id === serviceID && item.build.status === "succeeded");
  const repositoryIDs = Array.from(new Set(serviceRecords.map((item) => String(item.repository_id))));
  const selectedRepositoryID = repositoryIDs.includes(repositoryID) ? repositoryID : repositoryIDs[0] ?? "";
  const acceptedRecords = serviceRecords.filter((item) => String(item.repository_id) === selectedRepositoryID);
  const selectedRecord = acceptedRecords.find((item) => item.id === recordID) ?? acceptedRecords[0];
  const serviceKey = selectedRecord?.service_key ?? "";
  const assignment = topology?.assignments.find((item) => item.service_key === serviceKey && item.environment_id === environmentID);
  const resolvedRuntimeID = preview?.snapshot.authority.runtime_id;
  const resolvedNodeID = preview?.snapshot.authority.node_id;
  const resolvedAgentID = preview?.snapshot.authority.agent_id;
  const targetRuntime = resolvedRuntimeID ? facts?.runtimes.find((item) => item.id === resolvedRuntimeID) : undefined;

  const workload: WorkloadSpec = {
    schema_version: "opsi.workload_spec/v1",
    service_key: serviceKey,
    replicas: assignment?.replicas ?? replicas,
    application_container_name: "app",
    container_port: port,
    readiness_probe: readinessPath ? { path: readinessPath, port, initial_delay_seconds: 2, period_seconds: 5, timeout_seconds: 2, failure_threshold: 6 } : undefined,
    resources: {
      requests: { cpu: assignment ? `${assignment.cpu_request_millicores}m` : "100m", memory: assignment ? formatMemory(assignment.memory_request_bytes) : "128Mi" },
      limits: { cpu: assignment ? `${Math.max(assignment.cpu_request_millicores, 500)}m` : "500m", memory: assignment ? formatMemory(Math.max(assignment.memory_request_bytes, 512 * 1024 * 1024)) : "512Mi" },
    },
    termination_grace_period_seconds: 30,
    exposure: { mode: assignment?.exposure.mode === "none" ? "none" : "internal" },
  };
  const request = selectedRecord && environmentID ? { schema_version: schemaVersion, build_record_id: selectedRecord.id, environment_id: environmentID, workload } : null;

  async function previewDeployment() {
    if (!request) return;
    setLoading(true);
    setError("");
    try {
      setPreview(await client.deploymentPreview(projectID, request));
      setDisconnected(false);
    } catch (reason) {
      setError((reason as Error).message);
      setDisconnected(true);
    } finally {
      setLoading(false);
    }
  }

  async function applyDeployment() {
    if (!request || !preview?.eligible) return;
    setLoading(true);
    setError("");
    try {
      const key = `ui-${selectedRecord?.id}-${environmentID}-${preview.snapshot.spec_hash.slice(0, 24)}`;
      const created = await client.deploymentApply(projectID, request, key);
      selectionOverride.current = created.id;
      setJob(created);
      setSelectedJobID(created.id);
      await refreshJob(created.id, created.reused ?? false);
      await console.actions.load();
      setDisconnected(false);
    } catch (reason) {
      setError((reason as Error).message);
      setDisconnected(true);
    } finally {
      setLoading(false);
    }
  }

  const refreshJob = useCallback(async (jobID: string, reused?: boolean) => {
    const [current, result] = await Promise.all([client.deployment(projectID, jobID), client.deploymentEvents(projectID, jobID)]);
    setJob(reused === undefined ? current : { ...current, reused });
    setEvents(result.events ?? []);
    setDisconnected(false);
  }, [client, projectID]);

  async function cancelDeployment() {
    if (!job || job.status !== "queued") return;
    setLoading(true);
    try {
      const current = await client.deploymentCancel(projectID, job.id, `ui-cancel-${job.id}`);
      setJob(current);
      await refreshJob(job.id);
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      setLoading(false);
    }
  }

  async function retryDeployment() {
    if (!job || job.status !== "failed" || job.failure_code !== "DEPLOYMENT_LEASE_ATTEMPTS_EXHAUSTED" || job.terminal_result) return;
    setLoading(true);
    try {
      const current = await client.deploymentRetry(projectID, job.id, `ui-retry-${job.id}-${job.attempt_count ?? 0}`);
      setJob(current);
      await refreshJob(job.id);
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      setLoading(false);
    }
  }

  const baseDeployments = console.state.deployments.filter((item) => item.snapshot && ["succeeded", "rolled_back"].includes(item.status));
  const selectedBase = baseDeployments.find((item) => item.id === baseDeploymentID) ?? baseDeployments[0];

  async function exposureRequest(): Promise<ExposureMutationRequest> {
    if (!selectedBase?.snapshot || !hostname) throw new Error("Choose a successful deployment and enter a hostname.");
    const deploymentJobID = exposureJobID || `dep-ui-${crypto.randomUUID().replaceAll("-", "").slice(0, 24)}`;
    if (!exposureJobID) setExposureJobID(deploymentJobID);
    const normalizedPath = exposurePath !== "/" && exposurePath.endsWith("/") ? exposurePath.slice(0, -1) : exposurePath;
    const draft: Omit<ExposureSpec, "spec_hash"> = { schema_version: "opsi.exposure_spec/v1", project_id: projectID, environment_id: selectedBase.environment_id ?? "", runtime_id: selectedBase.runtime_id ?? "", service_key: selectedBase.snapshot.workload.service_key, deployment_job_id: deploymentJobID, hostname: hostname.toLowerCase(), path: normalizedPath, service_port: selectedBase.snapshot.workload.container_port, tls: tlsMode === "disabled" ? { mode: "disabled" } : { mode: "secret_ref", secret_ref: tlsReference } };
    const exposure = { ...draft, spec_hash: await hashExposure(draft) } as ExposureSpec;
    return { schema_version: "opsi.exposure_mutation/v1", base_deployment_job_id: selectedBase.id, expected_state_hash: exposurePreview?.state_hash, exposure };
  }

  async function previewExposure() {
    setLoading(true); setError("");
    try { const request = await exposureRequest(); delete request.expected_state_hash; setExposurePreview(await client.exposurePreview(projectID, request)); setConfirmExposure(false); }
    catch (reason) { setError((reason as Error).message); }
    finally { setLoading(false); }
  }

  async function applyExposure() {
    if (!exposurePreview?.eligible || !confirmExposure) return;
    setLoading(true); setError("");
    try { const request = await exposureRequest(); const created = await client.exposureApply(projectID, request, `ui-exposure-${request.exposure.deployment_job_id}`); selectionOverride.current = created.id; setJob(created); setSelectedJobID(created.id); await refreshJob(created.id, created.reused ?? false); await console.actions.load(); }
    catch (reason) { setError((reason as Error).message); }
    finally { setLoading(false); }
  }

  async function explicitRollback() {
    if (!job?.rollback_eligible || !confirmRollback) return;
    setLoading(true); setError("");
    try { const created = await client.rollback(projectID, job.id, `ui-rollback-${job.id}-${job.rollout_state_hash ?? "current"}`); selectionOverride.current = created.id; setJob(created); setSelectedJobID(created.id); setConfirmRollback(false); await refreshJob(created.id); await console.actions.load(); }
    catch (reason) { setError((reason as Error).message); }
    finally { setLoading(false); }
  }

  useEffect(() => {
    if (!selectedJobID || !projectID) return;
    if (job && ["succeeded", "failed", "rolled_back", "rollback_failed", "cancelled"].includes(job.status) && Boolean(job.terminal_result)) return;
    const timer = window.setInterval(() => void refreshJob(selectedJobID).catch(() => setDisconnected(true)), 3000);
    return () => window.clearInterval(timer);
  }, [job, projectID, selectedJobID, refreshJob]);

  if (!projectID) return <StatePanel title="Choose a project" text="Select a project before creating a manual deployment." />;
  if (disconnected) return <StatePanel title="Cloud disconnected" text={error || "The loopback Local API could not reach Cloud."} retry={() => void loadOptions()} />;

  return (
    <section className="grid">
      <Panel title="Manual immutable deployment">
        {loading && <p className="muted">Loading Cloud placement and accepted BuildRecords...</p>}
        <div className="formGrid">
          <label>Service<select value={serviceID} onChange={(event) => { setServiceID(event.target.value); setRepositoryID(""); setRecordID(""); setPreview(null); }}><option value="">Choose service</option>{services.filter((item) => item.type === "application").map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>
          <label>Repository<select value={selectedRepositoryID} onChange={(event) => { setRepositoryID(event.target.value); setRecordID(""); setPreview(null); }}><option value="">Choose repository</option>{repositoryIDs.map((item) => <option key={item} value={item}>{item}</option>)}</select></label>
          <label>Accepted BuildRecord<select value={selectedRecord?.id ?? ""} onChange={(event) => { setRecordID(event.target.value); setPreview(null); }}><option value="">Choose BuildRecord</option>{acceptedRecords.map((item) => <option key={item.id} value={item.id}>{item.id} — {item.build.oci_digest.slice(0, 18)}</option>)}</select></label>
          <label>Environment<select value={environmentID} onChange={(event) => { setEnvironmentID(event.target.value); setPreview(null); }}><option value="">Choose environment</option>{facts?.environments.filter((item) => item.status === "active").map((item) => <option key={item.id} value={item.id}>{item.name} ({item.id})</option>)}</select></label>
          <label>Replicas<input disabled={Boolean(assignment)} min={1} max={20} type="number" value={assignment?.replicas ?? replicas} onChange={(event) => { setReplicas(Number(event.target.value)); setPreview(null); }} /></label>
          <label>Container port<input min={1} max={65535} type="number" value={port} onChange={(event) => { setPort(Number(event.target.value)); setPreview(null); }} /></label>
          <label>Readiness path<input placeholder="/health" value={readinessPath} onChange={(event) => { setReadinessPath(event.target.value); setPreview(null); }} /></label>
        </div>
        <div className="specList compact">
          <div><span>Exact digest</span><b>{selectedRecord ? `${selectedRecord.build.oci_repository}@${selectedRecord.build.oci_digest}` : "-"}</b></div>
          <div><span>Resolved target</span><b>{preview ? `${targetRuntime?.name ?? resolvedRuntimeID} / ${resolvedNodeID} / ${resolvedAgentID}` : "run dry-run to resolve exact route"}</b></div>
          <div><span>Topology</span><b>{preview ? `${preview.snapshot.authority.topology_plan_id} rev ${preview.snapshot.authority.topology_revision}` : topology ? `${topology.id} rev ${topology.revision}` : "-"}</b></div>
          <div><span>DeploymentPolicy</span><b>{preview ? `${preview.snapshot.authority.deployment_policy_id} rev ${preview.snapshot.authority.deployment_policy_revision}` : "run dry-run"}</b></div>
          <div><span>WorkloadSpec</span><b>{preview?.snapshot.spec_hash ?? "not previewed"}</b></div>
        </div>
        <div className="buttonRow">
          <button disabled={!request || loading} onClick={() => void previewDeployment()} type="button">Dry-run / review diff</button>
          <button className="primary" disabled={!preview?.eligible || loading} onClick={() => void applyDeployment()} type="button">Confirm immutable deploy</button>
        </div>
        {preview && <div className="callout"><StatusBadge value={preview.eligible ? "ready" : "failed"} /><span>{preview.message || preview.decision_code} — {preview.changes.join(", ") || "no changes"}</span></div>}
        {error && <p className="errorText">{error}</p>}
      </Panel>
      <Panel title="External exposure rollout">
        <p className="muted">This creates a durable DeploymentJob. Cloud stores intent and sanitized history; the Agent remains runtime truth.</p>
        <div className="formGrid">
          <label>Base deployment<select value={selectedBase?.id ?? ""} onChange={(event) => { setBaseDeploymentID(event.target.value); setExposureJobID(""); setExposurePreview(null); }}><option value="">Choose successful deployment</option>{baseDeployments.map((item) => <option key={item.id} value={item.id}>{item.id} — {item.snapshot?.image.digest.slice(0, 18)}</option>)}</select></label>
          <label>Hostname<input placeholder="api.example.com" value={hostname} onChange={(event) => { setHostname(event.target.value); setExposurePreview(null); }} /></label>
          <label>Prefix path<input value={exposurePath} onChange={(event) => { setExposurePath(event.target.value); setExposurePreview(null); }} /></label>
          <label>Service port<input disabled value={selectedBase?.snapshot?.workload.container_port ?? ""} /></label>
          <label>TLS mode<select value={tlsMode} onChange={(event) => { setTLSMode(event.target.value as "disabled" | "secret_ref"); setExposurePreview(null); }}><option value="disabled">Disabled</option><option value="secret_ref">Opaque secret reference</option></select></label>
          {tlsMode === "secret_ref" && <label>TLS reference<input value={tlsReference} onChange={(event) => { setTLSReference(event.target.value); setExposurePreview(null); }} /></label>}
        </div>
        <div className="buttonRow"><button disabled={!selectedBase || !hostname || loading} onClick={() => void previewExposure()} type="button">Preview deterministic diff</button></div>
        {exposurePreview && <><div className="callout"><StatusBadge value={exposurePreview.eligible ? "ready" : "failed"} /><span>{exposurePreview.message} — {exposurePreview.changes.join(", ")}</span></div><div className="specList compact"><div><span>Exposure hash</span><b>{exposurePreview.desired.spec_hash}</b></div><div><span>State hash</span><b>{exposurePreview.state_hash}</b></div><div><span>Route</span><b>{exposurePreview.desired.hostname}{exposurePreview.desired.path}</b></div></div><label><input checked={confirmExposure} onChange={(event) => setConfirmExposure(event.target.checked)} type="checkbox" /> I confirm this may change external routing and trigger automatic rollback.</label><div className="buttonRow"><button className="primary" disabled={!exposurePreview.eligible || !confirmExposure || loading} onClick={() => void applyExposure()} type="button">Apply through Agent rollout</button></div></>}
      </Panel>
      <Panel title="Deployment progress">
        {selectedJobID ? <>
          {job && <div className="specList compact">
            <div><span>Job / state</span><b>{job.id} / {job.status}</b></div>
            <div><span>Idempotency</span><b>{job.reused === true ? "reused" : job.reused === false ? "new job" : "not reported"}</b></div>
            <div><span>Attempt</span><b>{job.attempt_count ?? 0}/{job.max_attempts ?? 0}{job.retry_after ? ` — retry after ${job.retry_after}` : ""}</b></div>
            <div><span>Final digest</span><b>{job.terminal_result?.application_image ?? job.snapshot?.image.reference ?? "-"}</b></div>
            <div><span>Readiness</span><b>{job.terminal_result ? `${job.terminal_result.available_replicas} replicas / ${job.terminal_result.application_image_id}` : "pending"}</b></div>
            <div><span>Result</span><b>{job.failure_code ? `${job.failure_code}: ${job.failure_message_redacted ?? "failed"}` : job.status}</b></div>
            <div><span>Rollout state</span><b>{rolloutStateLabels[job.rollout_state ?? job.status] ?? job.rollout_state ?? job.status}</b></div>
            <div><span>Desired / current / previous</span><b>{job.desired_digest ?? "-"} / {job.current_digest ?? "-"} / {job.previous_digest ?? "-"}</b></div>
            <div><span>External route</span><b>{job.exposure_spec ? `${job.exposure_spec.hostname}${job.exposure_spec.path}` : "-"}</b></div>
            <div><span>Readiness evidence</span><b>{job.readiness_evidence_hash ?? "pending"}</b></div>
          </div>}
          <div className="buttonRow">
            <button disabled={loading || job?.status !== "queued"} onClick={() => void cancelDeployment()} type="button">Cancel before mutation</button>
            <button disabled={loading || job?.status !== "failed" || job?.failure_code !== "DEPLOYMENT_LEASE_ATTEMPTS_EXHAUSTED" || Boolean(job?.terminal_result)} onClick={() => void retryDeployment()} type="button">Retry same job</button>
          </div>
          {job?.rollback_eligible && <div className="callout"><span>Explicit rollback restores the exact previous Agent known-good snapshot and can reduce availability while readiness is rechecked.</span><label><input checked={confirmRollback} onChange={(event) => setConfirmRollback(event.target.checked)} type="checkbox" /> Confirm rollback consequence</label><button disabled={!confirmRollback || loading} onClick={() => void explicitRollback()} type="button">Rollback exact known-good</button></div>}
          <EventsList events={events} />
        </> : console.state.deployments.length ? <DeploymentsTable console={console} /> : <Empty text="No DeploymentJob selected. Preview an accepted BuildRecord to begin." />}
      </Panel>
    </section>
  );
}

function formatMemory(bytes: number) {
  if (bytes % (1024 * 1024 * 1024) === 0) return `${bytes / (1024 * 1024 * 1024)}Gi`;
  return `${Math.max(1, Math.round(bytes / (1024 * 1024)))}Mi`;
}

async function hashExposure(spec: Omit<ExposureSpec, "spec_hash">) {
  const data = new TextEncoder().encode(JSON.stringify({ ...spec, spec_hash: "" }));
  const digest = await crypto.subtle.digest("SHA-256", data);
  return Array.from(new Uint8Array(digest), (value) => value.toString(16).padStart(2, "0")).join("");
}
