"use client";

import { useState } from "react";
import { Empty, Surface, StatusBadge } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";
import { LocalClient } from "@/lib/api/local-client";
import type { BuildRecord, DeploymentPolicy, GitHubBinding, GitHubRepository, PlacementFacts, TopologyDiff, TopologyDraft, TopologyPlan, TopologyPreview, TopologyValidation } from "@/lib/contracts/registry";
import { assignmentFor } from "@/lib/presentation/infrastructure/model";

type PlacementData = { facts: PlacementFacts; topology: TopologyPlan | null; repositories: GitHubRepository[]; bindings: GitHubBinding[]; builds: BuildRecord[]; policies: DeploymentPolicy[] };
type Preview = { topology: TopologyPreview; validation: TopologyValidation; diff: TopologyDiff; policyHash: string; policyDiff: string[] };
const client = new LocalClient();

export function PlacementDialog({ console, data, onClose, onApplied }: { console: ConsoleController; data: PlacementData; onClose: () => void; onApplied: () => void }) {
  const project = console.state.project;
  const projectID = project?.id ?? "";
  const [phase, setPhase] = useState<"target" | "capacity" | "validate" | "review">("target");
  const [repositoryID, setRepositoryID] = useState("");
  const [serviceKey, setServiceKey] = useState("");
  const [buildID, setBuildID] = useState("");
  const [environmentID, setEnvironmentID] = useState("");
  const [runtimeID, setRuntimeID] = useState("");
  const [replicas, setReplicas] = useState("");
  const [cpu, setCPU] = useState("");
  const [memory, setMemory] = useState("");
  const [exposure, setExposure] = useState("");
  const [rationale, setRationale] = useState("");
  const [preview, setPreview] = useState<Preview | null>(null);
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const [confirm, setConfirm] = useState("");
  const [allowUnknown, setAllowUnknown] = useState(false);

  const environments = data.facts.environments;
  const runtimes = data.facts.runtimes.filter((item) => item.environment_id === environmentID);
  const repositories = data.repositories.filter((item) => item.claim_status === "active");
  const bindings = data.bindings.filter((item) => String(item.repository_id) === repositoryID && item.status === "active");
  const builds = data.builds.filter((item) => String(item.repository_id) === repositoryID && item.service_key === serviceKey);
  const build = builds.find((item) => item.id === buildID) ?? null;
  const currentAssignment = assignmentFor(data.topology, serviceKey);
  const currentPolicy = data.policies.find((item) => item.policy.enabled && item.policy.repository_id === Number(repositoryID) && item.policy.service_keys.includes(serviceKey) && item.policy.environment_id === environmentID && item.policy.allowed_runtime_ids.includes(runtimeID) && build && item.policy.allowed_config_hashes.includes(build.build.config_hash) && item.policy.allowed_oci_repositories.includes(build.build.oci_repository));

  function selectService(value: string) {
    setServiceKey(value);
    setBuildID("");
    const assignment = assignmentFor(data.topology, value);
    setReplicas(assignment ? String(assignment.replicas) : "");
    setCPU(assignment ? String(assignment.cpu_request_millicores) : "");
    setMemory(assignment ? String(Math.round(assignment.memory_request_bytes / 1024 / 1024)) : "");
    setExposure(assignment?.exposure.mode ?? "");
    setRationale(assignment?.rationale?.summary ?? "");
  }

  if (!project) return null;
  const targetReady = Boolean(repositoryID && serviceKey && build && environmentID && runtimeID);
  const capacityReady = Boolean(replicas && cpu && memory && exposure);

  function draft(): TopologyDraft {
    if (!project || !build || !targetReady || !capacityReady) throw new Error("Select every target and capacity field before validation.");
    const numeric = { replicas: Number(replicas), cpu: Number(cpu), memory: Number(memory) };
    if (![numeric.replicas, numeric.cpu, numeric.memory].every((value) => Number.isFinite(value) && value > 0)) throw new Error("Replicas, CPU, and memory must be positive factual requests.");
    const assignment = { service_key: serviceKey, environment_id: environmentID, runtime_id: runtimeID, replicas: numeric.replicas, cpu_request_millicores: numeric.cpu, memory_request_bytes: numeric.memory * 1024 * 1024, exposure: { mode: exposure as "none" | "internal" | "public" }, rationale: { summary: rationale } };
    return { schema_version: "opsi.topology_plan/v1", project_id: projectID, assignments: [...(data.topology?.assignments ?? []).filter((item) => item.service_key !== serviceKey), assignment] };
  }

  async function validate() {
    setBusy(true); setMessage("");
    try {
      const body = draft();
      if (!build?.build.plan_hash) throw new Error("The selected BuildRecord has no build plan hash; routing cannot be authorized.");
      const policy = { schema_version: "opsi.deployment_policy/v1" as const, project_id: projectID, repository_id: Number(repositoryID), service_keys: [serviceKey], workflow_refs: [build.workload.workflow_ref], job_workflow_refs: build.workload.job_workflow_ref ? [build.workload.job_workflow_ref] : [], allowed_events: [build.workload.event_name], allowed_git_refs: [build.workload.ref], environment_id: environmentID, allowed_runtime_ids: [runtimeID], allowed_oci_repositories: [build.build.oci_repository], allowed_platforms: [build.build.platform], allowed_config_hashes: [build.build.config_hash], allowed_build_plan_hashes: [build.build.plan_hash], allow_unknown_capacity: allowUnknown, enabled: true };
      const [topology, validation, diff, policyPreview, policyDiff] = await Promise.all([client.topologyPlan(projectID, body), client.topologyValidate(projectID, body, currentPolicy?.id ?? ""), client.topologyDiff(projectID, body), client.deploymentPolicyPreview(projectID, policy), client.deploymentPolicyDiff(projectID, { policy_id: currentPolicy?.id, policy })]);
      setPreview({ topology, validation, diff, policyHash: policyPreview.policy_hash, policyDiff: policyDiff.changes.map((item) => JSON.stringify(item)) });
      setPhase("review");
    } catch (error) { setMessage((error as Error).message); }
    finally { setBusy(false); }
  }

  async function apply() {
    if (!preview || confirm !== "APPLY" || !build) return;
    setBusy(true); setMessage("");
    try {
      const body = draft();
      const policy = { schema_version: "opsi.deployment_policy/v1" as const, project_id: projectID, repository_id: Number(repositoryID), service_keys: [serviceKey], workflow_refs: [build.workload.workflow_ref], job_workflow_refs: build.workload.job_workflow_ref ? [build.workload.job_workflow_ref] : [], allowed_events: [build.workload.event_name], allowed_git_refs: [build.workload.ref], environment_id: environmentID, allowed_runtime_ids: [runtimeID], allowed_oci_repositories: [build.build.oci_repository], allowed_platforms: [build.build.platform], allowed_config_hashes: [build.build.config_hash], allowed_build_plan_hashes: [build.build.plan_hash ?? ""], allow_unknown_capacity: allowUnknown, enabled: true };
      const policyResult = await client.deploymentPolicyApply(projectID, { policy_id: currentPolicy?.id, policy, expected_revision: currentPolicy?.revision ?? 0, expected_state_hash: currentPolicy?.state_hash ?? "" }, crypto.randomUUID());
      const finalValidation = await client.topologyValidate(projectID, body, policyResult.policy.id);
      if (!finalValidation.valid && !(allowUnknown && onlyUnknownCapacity(finalValidation))) throw new Error(finalValidation.issues.map((item) => `${item.code}: ${item.message}`).join("; "));
      const result = await client.topologyApply(projectID, { draft: body, expected_revision: data.topology?.revision ?? 0, expected_state_hash: data.topology?.state_hash ?? "", policy_id: policyResult.policy.id }, crypto.randomUUID());
      setConfirm("");
      onApplied();
      onClose();
      void result;
    } catch (error) { setMessage((error as Error).message); }
    finally { setBusy(false); }
  }

  return <dialog className="placementDialog" open aria-labelledby="placement-title">
    <div className="dialogHeading"><div><p className="eyebrow">Infrastructure action</p><h2 id="placement-title">Plan placement</h2><p>Review exact target, capacity, validation, and immutable revisions before apply.</p></div><button aria-label="Close placement dialog" className="iconButton" onClick={onClose} type="button">×</button></div>
    <ol className="phaseRail" aria-label="Placement phases">{["Target", "Capacity", "Validate", "Review & apply"].map((item, index) => <li className={phase === ["target", "capacity", "validate", "review"][index] ? "active" : ""} key={item}>{index + 1}. {item}</li>)}</ol>
    {phase === "target" ? <div className="form placementForm">
      <label>Repository<select aria-label="Repository" className="select" value={repositoryID} onChange={(event) => { setRepositoryID(event.target.value); setServiceKey(""); setBuildID(""); }}><option value="">Choose exact repository…</option>{repositories.map((item) => <option key={item.repository_id} value={item.repository_id}>{item.full_name}</option>)}</select></label>
      <label>Service binding<select aria-label="Service binding" className="select" value={serviceKey} onChange={(event) => selectService(event.target.value)}><option value="">Choose exact service…</option>{bindings.map((item) => <option key={item.id} value={item.service_key}>{item.service_key}</option>)}</select></label>
      <label>BuildRecord<select aria-label="BuildRecord" className="select" value={buildID} onChange={(event) => setBuildID(event.target.value)}><option value="">Choose accepted BuildRecord…</option>{builds.map((item) => <option key={item.id} value={item.id}>{item.id} · {item.workload.sha.slice(0, 12)}</option>)}</select></label>
      <label>Environment<select aria-label="Environment" className="select" value={environmentID} onChange={(event) => { setEnvironmentID(event.target.value); setRuntimeID(""); }}><option value="">Choose exact environment…</option>{environments.map((item) => <option key={item.id} value={item.id}>{item.name} · {item.status}</option>)}</select></label>
      <label>Runtime<select aria-label="Runtime" className="select" value={runtimeID} onChange={(event) => setRuntimeID(event.target.value)}><option value="">Choose exact runtime…</option>{runtimes.map((item) => <option key={item.id} value={item.id}>{item.name} · {item.status}</option>)}</select></label>
      <div className="dialogActions"><button disabled={!targetReady} onClick={() => setPhase("capacity")} type="button">Next: Capacity</button></div>
    </div> : null}
    {phase === "capacity" ? <div className="form placementForm">
      <p className="notice">{currentAssignment ? `Prefilled from current TopologyPlan assignment ${currentAssignment.service_key}.` : "No current assignment found. Enter every value; nothing is silently defaulted."}</p>
      <label>Replicas<input aria-label="Replicas" className="field" inputMode="numeric" min="1" name="replicas" onChange={(event) => setReplicas(event.target.value)} type="number" value={replicas} /></label>
      <label>CPU request (millicores)<input aria-label="CPU request" className="field" inputMode="numeric" min="1" name="cpu" onChange={(event) => setCPU(event.target.value)} type="number" value={cpu} /></label>
      <label>Memory request (MiB)<input aria-label="Memory request" className="field" inputMode="numeric" min="1" name="memory" onChange={(event) => setMemory(event.target.value)} type="number" value={memory} /></label>
      <label>Exposure intent<select aria-label="Exposure intent" className="select" value={exposure} onChange={(event) => setExposure(event.target.value)}><option value="">Choose exposure intent…</option><option value="none">None</option><option value="internal">Internal intent</option><option value="public">Public intent metadata</option></select></label>
      <label className="span2">Rationale<textarea aria-label="Placement rationale" className="textarea" maxLength={2048} onChange={(event) => setRationale(event.target.value)} value={rationale} /></label>
      <label className="span2"><input checked={allowUnknown} onChange={(event) => setAllowUnknown(event.target.checked)} type="checkbox" /> Allow unknown capacity only if the reviewed policy explicitly grants it.</label>
      <div className="dialogActions"><button onClick={() => setPhase("target")} type="button">Back</button><button disabled={!capacityReady} onClick={() => setPhase("validate")} type="button">Next: Validate</button></div>
    </div> : null}
    {phase === "validate" ? <div className="form placementForm"><Surface title="Validation inputs"><p>Runtime eligibility, heartbeat freshness, requested/reserved/available capacity, oversubscription, and policy match are evaluated by Local API.</p><button disabled={busy} onClick={() => void validate()} type="button">{busy ? "Validating…" : "Run factual validation"}</button></Surface><div className="dialogActions"><button onClick={() => setPhase("capacity")} type="button">Back</button></div></div> : null}
    {phase === "review" && preview ? <div className="form placementForm"><Surface title="Review & apply"><div className="hashPair"><div><span>Topology plan hash</span><code>{preview.topology.plan_hash}</code></div><div><span>Policy hash</span><code>{preview.policyHash}</code></div></div><StatusBadge value={preview.validation.valid ? "ready" : "blocked"} /><p>{preview.validation.issues.length ? preview.validation.issues.map((item) => `${item.code}: ${item.message}`).join(" · ") : "No deterministic validation issues."}</p><p>{preview.diff.changes.length} topology changes; {preview.policyDiff.length} policy changes. Policy and topology apply are sequential, not atomic.</p>{preview.validation.runtimes.map((runtime) => <div className="capacityCard" key={runtime.runtime_id}><b>{runtime.runtime_id}</b><p>CPU {runtime.capacity.available_cpu_millicores === undefined ? "Unknown" : `${runtime.capacity.available_cpu_millicores}m available`} · memory {runtime.capacity.available_memory_bytes === undefined ? "Unknown" : `${runtime.capacity.available_memory_bytes} bytes available`} · heartbeat {runtime.capacity.heartbeat_age_seconds === undefined ? "Unknown" : `${runtime.capacity.heartbeat_age_seconds}s`}</p></div>)}<label>Type APPLY to confirm<input aria-label="Apply confirmation" className="field" onChange={(event) => setConfirm(event.target.value)} value={confirm} /></label><div className="dialogActions"><button onClick={() => setPhase("validate")} type="button">Back</button><button disabled={busy || confirm !== "APPLY" || (!preview.validation.valid && !(allowUnknown && onlyUnknownCapacity(preview.validation)))} onClick={() => void apply()} type="button">{busy ? "Applying…" : "Apply reviewed revisions"}</button></div></Surface></div> : <Empty text="Validation preview is unavailable." />}
    {message ? <p className="inlineError" role="alert">{message}</p> : null}
  </dialog>;
}

function onlyUnknownCapacity(validation: TopologyValidation) { return validation.issues.length > 0 && validation.issues.every((item) => item.code === "TOPOLOGY_CAPACITY_UNKNOWN"); }
