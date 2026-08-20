"use client";

import { useEffect, useRef, useState } from "react";
import { Button, Icon, StatusBadge } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";
import { LocalClient } from "@/lib/api/local-client";
import type { BuildRecord, DeploymentPolicy, GitHubBinding, GitHubRepository, PlacementFacts, TopologyDiff, TopologyDraft, TopologyPlan, TopologyPreview, TopologyValidation } from "@/lib/contracts/registry";
import { assignmentFor } from "@/lib/presentation/infrastructure/model";

type PlacementData = { facts: PlacementFacts; topology: TopologyPlan | null; repositories: GitHubRepository[]; bindings: GitHubBinding[]; builds: BuildRecord[]; policies: DeploymentPolicy[] };
type Preview = { topology: TopologyPreview; validation: TopologyValidation; diff: TopologyDiff; policyHash: string; policyDiff: string[] };
const client = new LocalClient();

export function PlacementDialog({ console, data, onClose, onApplied }: { console: ConsoleController; data: PlacementData; onClose: () => void; onApplied: () => void }) {
  const dialog = useRef<HTMLDialogElement>(null);
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
  useEffect(() => {
    const element = dialog.current;
    element?.showModal();
    return () => { if (element?.open) element.close(); };
  }, []);

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

  return (
    <dialog
      aria-labelledby="placement-title"
      className="fixed inset-0 m-auto bg-surface-container-low border border-outline-variant/30 rounded-2xl shadow-2xl p-6 max-w-2xl w-full backdrop:bg-background/80 backdrop:backdrop-blur-sm z-50 text-on-surface flex flex-col gap-5 max-h-[90vh] overflow-y-auto"
      onCancel={(event) => {
        event.preventDefault();
        onClose();
      }}
      ref={dialog}
    >
      <div className="flex items-start justify-between border-b border-outline-variant/20 pb-4">
        <div>
          <span className="font-label-sm text-xs text-primary font-bold uppercase tracking-wider block mb-1">
            Infrastructure Action
          </span>
          <h2 id="placement-title" className="font-headline-md text-xl font-bold text-on-surface">
            Plan Placement
          </h2>
          <p className="font-body-md text-xs text-on-surface-variant mt-1">
            Review target, capacity, validation, and immutable revisions before apply.
          </p>
        </div>
        <button
          aria-label="Close placement dialog"
          autoFocus
          className="p-1.5 text-on-surface-variant hover:text-on-surface hover:bg-surface-container-highest rounded-lg transition-colors cursor-pointer"
          onClick={onClose}
          type="button"
        >
          <Icon name="close" className="text-[20px]" />
        </button>
      </div>

      <ol aria-label="Placement phases" className="grid grid-cols-4 gap-2 bg-surface-container p-2.5 rounded-xl border border-outline-variant/15 text-xs text-center">
        {["Target", "Capacity", "Validate", "Review"].map((item, index) => {
          const active = phase === ["target", "capacity", "validate", "review"][index];
          return (
            <li
              className={`py-1.5 px-2 rounded-lg font-semibold transition-colors ${
                active ? "bg-primary text-on-primary shadow-sm" : "text-on-surface-variant"
              }`}
              key={item}
            >
              {index + 1}. {item}
            </li>
          );
        })}
      </ol>

      {phase === "target" ? (
        <div className="space-y-4">
          <div className="space-y-1.5">
            <label className="text-xs font-label-sm text-on-surface-variant block">Repository</label>
            <select aria-label="Repository" className="select" onChange={(event) => { setRepositoryID(event.target.value); setServiceKey(""); setBuildID(""); }} value={repositoryID}>
              <option value="">Choose exact repository…</option>
              {repositories.map((item) => (
                <option key={item.repository_id} value={item.repository_id}>{item.full_name}</option>
              ))}
            </select>
          </div>

          <div className="space-y-1.5">
            <label className="text-xs font-label-sm text-on-surface-variant block">Service Binding</label>
            <select aria-label="Service binding" className="select" onChange={(event) => selectService(event.target.value)} value={serviceKey}>
              <option value="">Choose exact service…</option>
              {bindings.map((item) => (
                <option key={item.id} value={item.service_key}>{item.service_key}</option>
              ))}
            </select>
          </div>

          <div className="space-y-1.5">
            <label className="text-xs font-label-sm text-on-surface-variant block">BuildRecord</label>
            <select aria-label="BuildRecord" className="select" onChange={(event) => setBuildID(event.target.value)} value={buildID}>
              <option value="">Choose accepted BuildRecord…</option>
              {builds.map((item) => (
                <option key={item.id} value={item.id}>{item.id} · {item.workload.sha.slice(0, 12)}</option>
              ))}
            </select>
          </div>

          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-1.5">
              <label className="text-xs font-label-sm text-on-surface-variant block">Environment</label>
              <select aria-label="Environment" className="select" onChange={(event) => { setEnvironmentID(event.target.value); setRuntimeID(""); }} value={environmentID}>
                <option value="">Choose environment…</option>
                {environments.map((item) => (
                  <option key={item.id} value={item.id}>{item.name} · {item.status}</option>
                ))}
              </select>
            </div>
            <div className="space-y-1.5">
              <label className="text-xs font-label-sm text-on-surface-variant block">Runtime</label>
              <select aria-label="Runtime" className="select" onChange={(event) => setRuntimeID(event.target.value)} value={runtimeID}>
                <option value="">Choose runtime…</option>
                {runtimes.map((item) => (
                  <option key={item.id} value={item.id}>{item.name} · {item.status}</option>
                ))}
              </select>
            </div>
          </div>

          <div className="flex items-center justify-end gap-3 pt-3 border-t border-outline-variant/20">
            <Button disabled={!targetReady} onClick={() => setPhase("capacity")} size="sm" type="button" variant="primary">
              Next: Capacity →
            </Button>
          </div>
        </div>
      ) : null}

      {phase === "capacity" ? (
        <div className="space-y-4">
          <p className="text-xs text-on-surface-variant bg-surface-container/60 p-3 rounded-xl border border-outline-variant/15">
            {currentAssignment ? `Prefilled from current assignment ${currentAssignment.service_key}.` : "Enter desired resource capacity requirements."}
          </p>
          <div className="grid grid-cols-3 gap-3">
            <div className="space-y-1.5">
              <label className="text-xs font-label-sm text-on-surface-variant block">Replicas</label>
              <input aria-label="Replicas" className="field" inputMode="numeric" min="1" name="replicas" onChange={(event) => setReplicas(event.target.value)} type="number" value={replicas} />
            </div>
            <div className="space-y-1.5">
              <label className="text-xs font-label-sm text-on-surface-variant block">CPU (m)</label>
              <input aria-label="CPU request" className="field" inputMode="numeric" min="1" name="cpu" onChange={(event) => setCPU(event.target.value)} type="number" value={cpu} />
            </div>
            <div className="space-y-1.5">
              <label className="text-xs font-label-sm text-on-surface-variant block">Memory (MiB)</label>
              <input aria-label="Memory request" className="field" inputMode="numeric" min="1" name="memory" onChange={(event) => setMemory(event.target.value)} type="number" value={memory} />
            </div>
          </div>

          <div className="space-y-1.5">
            <label className="text-xs font-label-sm text-on-surface-variant block">Exposure Intent</label>
            <select aria-label="Exposure intent" className="select" onChange={(event) => setExposure(event.target.value)} value={exposure}>
              <option value="">Choose exposure intent…</option>
              <option value="none">None</option>
              <option value="internal">Internal intent</option>
              <option value="public">Public intent</option>
            </select>
          </div>

          <div className="space-y-1.5">
            <label className="text-xs font-label-sm text-on-surface-variant block">Rationale</label>
            <textarea aria-label="Placement rationale" className="textarea" maxLength={2048} onChange={(event) => setRationale(event.target.value)} rows={2} value={rationale} />
          </div>

          <label className="flex items-center gap-2 text-xs text-on-surface-variant cursor-pointer">
            <input checked={allowUnknown} onChange={(event) => setAllowUnknown(event.target.checked)} type="checkbox" />
            <span>Allow unknown capacity if policy explicitly grants it</span>
          </label>

          <div className="flex items-center justify-between pt-3 border-t border-outline-variant/20">
            <Button onClick={() => setPhase("target")} size="sm" type="button" variant="outline">
              ← Back
            </Button>
            <Button disabled={!capacityReady} onClick={() => setPhase("validate")} size="sm" type="button" variant="primary">
              Next: Validate →
            </Button>
          </div>
        </div>
      ) : null}

      {phase === "validate" ? (
        <div className="space-y-4">
          <div className="bg-surface-container/60 p-4 rounded-xl border border-outline-variant/15 space-y-3">
            <h4 className="font-headline-md text-sm font-bold text-on-surface">Validation Inputs</h4>
            <p className="text-xs text-on-surface-variant leading-relaxed">
              Runtime eligibility, heartbeat freshness, requested/reserved capacity, and policy matching are evaluated by Local API.
            </p>
            <Button disabled={busy} onClick={() => void validate()} size="sm" type="button" variant="primary">
              {busy ? "Validating…" : "Run Factual Validation"}
            </Button>
          </div>
          <div className="flex items-center justify-start pt-3 border-t border-outline-variant/20">
            <Button onClick={() => setPhase("capacity")} size="sm" type="button" variant="outline">
              ← Back
            </Button>
          </div>
        </div>
      ) : null}

      {phase === "review" && preview ? (
        <div className="space-y-4">
          <div className="bg-surface-container/60 p-4 rounded-xl border border-outline-variant/15 space-y-3">
            <div className="flex items-center justify-between">
              <h4 className="font-headline-md text-sm font-bold text-on-surface">Review & Apply</h4>
              <StatusBadge value={preview.validation.valid ? "ready" : "blocked"} />
            </div>
            <div className="grid grid-cols-2 gap-2 text-xs font-code-md bg-surface-container p-2.5 rounded-lg">
              <div><span className="text-on-surface-variant">Plan Hash:</span> <strong className="text-on-surface truncate block">{preview.topology.plan_hash?.slice(0, 10)}</strong></div>
              <div><span className="text-on-surface-variant">Policy Hash:</span> <strong className="text-primary truncate block">{preview.policyHash?.slice(0, 10)}</strong></div>
            </div>
            <p className="text-xs text-on-surface-variant">
              {preview.diff.changes.length} topology changes; {preview.policyDiff.length} policy changes.
            </p>
            <div className="space-y-1.5 pt-2">
              <label className="text-xs font-label-sm text-on-surface-variant block">Type APPLY to confirm</label>
              <input aria-label="Apply confirmation" className="field" onChange={(event) => setConfirm(event.target.value)} placeholder="APPLY" value={confirm} />
            </div>
          </div>
          <div className="flex items-center justify-between pt-3 border-t border-outline-variant/20">
            <Button onClick={() => setPhase("validate")} size="sm" type="button" variant="outline">
              ← Back
            </Button>
            <Button disabled={busy || confirm !== "APPLY" || (!preview.validation.valid && !(allowUnknown && onlyUnknownCapacity(preview.validation)))} onClick={() => void apply()} size="sm" type="button" variant="primary">
              {busy ? "Applying…" : "Apply Reviewed Revisions"}
            </Button>
          </div>
        </div>
      ) : null}

      {message ? (
        <p className="p-3 bg-error-container/20 text-error border border-error/30 rounded-xl text-xs" role="alert">
          {message}
        </p>
      ) : null}
    </dialog>
  );
}

function onlyUnknownCapacity(validation: TopologyValidation) { return validation.issues.length > 0 && validation.issues.every((item) => item.code === "TOPOLOGY_CAPACITY_UNKNOWN"); }

