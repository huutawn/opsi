"use client";

import { useEffect, useMemo, useState } from "react";
import { Empty, Panel, StatePanel, StatusBadge } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";
import { LocalClient } from "@/lib/api/local-client";
import type { BuildRecord, DeploymentPolicy, DeploymentPolicyDraft, GitHubBinding, GitHubRepository, PlacementFacts, TopologyDiff, TopologyDraft, TopologyPlan, TopologyPreview, TopologyValidation } from "@/lib/contracts/registry";

type WizardData = {
  facts: PlacementFacts;
  repositories: GitHubRepository[];
  bindings: GitHubBinding[];
  builds: BuildRecord[];
  policies: DeploymentPolicy[];
  topology: TopologyPlan | null;
};

type PreviewState = {
  topology: TopologyPreview;
  validation: TopologyValidation;
  diff: TopologyDiff;
  policy: { policy: DeploymentPolicyDraft; policy_hash: string };
  policyDiff: { changes: unknown[]; proposed_hash: string };
};

const client = new LocalClient();

export function TopologyView({ console }: { console: ConsoleController }) {
  const project = console.state.project;
  const [status, setStatus] = useState<"loading" | "ready" | "empty" | "error">("loading");
  const [message, setMessage] = useState("");
  const [data, setData] = useState<WizardData | null>(null);
  const [repositoryID, setRepositoryID] = useState("");
  const [serviceKey, setServiceKey] = useState("");
  const [buildRecordID, setBuildRecordID] = useState("");
  const [environmentID, setEnvironmentID] = useState("");
  const [runtimeID, setRuntimeID] = useState("");
  const [replicas, setReplicas] = useState(1);
  const [cpu, setCPU] = useState(250);
  const [memoryMiB, setMemoryMiB] = useState(256);
  const [exposure, setExposure] = useState<"none" | "internal" | "public">("none");
  const [rationale, setRationale] = useState("");
  const [allowUnknown, setAllowUnknown] = useState(false);
  const [preview, setPreview] = useState<PreviewState | null>(null);
  const [busy, setBusy] = useState("");
  const [confirm, setConfirm] = useState("");
  const [result, setResult] = useState<{ plan: TopologyPlan; policy: DeploymentPolicy; reused: boolean } | null>(null);
  const [mutationKeys, setMutationKeys] = useState(() => ({ policy: crypto.randomUUID(), topology: crypto.randomUUID(), disable: crypto.randomUUID() }));

  async function load() {
    if (!project) return;
    setStatus("loading"); setMessage(""); setPreview(null);
    try {
      const [facts, repositories, bindings, builds, policies] = await Promise.all([
        client.placementFacts(project.id), client.githubRepositories(project.id), client.githubBindings(project.id), client.buildRecords(project.id), client.deploymentPolicies(project.id),
      ]);
      let topology: TopologyPlan | null = null;
      try { topology = await client.topology(project.id); } catch (error) { if ((error as Error & { status?: number }).status !== 404) throw error; }
      const next = { facts, repositories: repositories.repositories ?? [], bindings: bindings.bindings ?? [], builds: builds.records ?? [], policies: policies.policies ?? [], topology };
      setData(next);
      const firstRepository = next.repositories.find((item) => item.claim_status === "active");
      const firstBinding = next.bindings.find((item) => item.repository_id === firstRepository?.repository_id && item.status === "active");
      const currentAssignment = topology?.assignments.find((item) => item.service_key === firstBinding?.service_key);
      const firstRuntime = facts.runtimes.find((item) => item.id === currentAssignment?.runtime_id) ?? facts.runtimes.find((item) => item.status === "ready" && facts.environments.some((environment) => environment.id === item.environment_id && environment.status === "active"));
      const firstEnvironment = facts.environments.find((item) => item.id === firstRuntime?.environment_id);
      setRepositoryID(String(firstRepository?.repository_id ?? "")); setServiceKey(firstBinding?.service_key ?? ""); setEnvironmentID(firstEnvironment?.id ?? ""); setRuntimeID(firstRuntime?.id ?? "");
      setStatus(firstRepository && firstBinding && firstEnvironment && firstRuntime ? "ready" : "empty");
    } catch (error) { setStatus("error"); setMessage((error as Error).message); }
  }

  useEffect(() => {
    queueMicrotask(() => void load());
    // Project selection remounts the routed view through the console controller.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [project?.id]);

  const repository = data?.repositories.find((item) => String(item.repository_id) === repositoryID);
  const bindings = data?.bindings.filter((item) => String(item.repository_id) === repositoryID && item.status === "active") ?? [];
  const environments = data?.facts.environments.filter((item) => item.status === "active") ?? [];
  const runtimes = data?.facts.runtimes.filter((item) => item.environment_id === environmentID) ?? [];
  const matchingBuilds = useMemo(() => data?.builds.filter((item) => String(item.repository_id) === repositoryID && item.service_key === serviceKey) ?? [], [data, repositoryID, serviceKey]);
  const build = matchingBuilds.find((item) => item.id === buildRecordID) ?? matchingBuilds[0];
  const matchingPolicies = data?.policies.filter((item) => item.policy.enabled && build?.build.plan_hash && item.policy.repository_id === Number(repositoryID) && item.policy.service_keys.includes(serviceKey) && item.policy.environment_id === environmentID && item.policy.allowed_runtime_ids.includes(runtimeID) && item.policy.workflow_refs.includes(build.workload.workflow_ref) && (build.workload.job_workflow_ref ? (item.policy.job_workflow_refs ?? []).includes(build.workload.job_workflow_ref) : (item.policy.job_workflow_refs ?? []).length === 0) && item.policy.allowed_events.includes(build.workload.event_name) && item.policy.allowed_git_refs.includes(build.workload.ref) && item.policy.allowed_config_hashes.includes(build.build.config_hash) && item.policy.allowed_build_plan_hashes.includes(build.build.plan_hash) && item.policy.allowed_platforms.includes(build.build.platform) && item.policy.allowed_oci_repositories.includes(build.build.oci_repository)) ?? [];
  const currentPolicy = matchingPolicies.length === 1 ? matchingPolicies[0] : undefined;
  const runtime = data?.facts.runtimes.find((item) => item.id === runtimeID);
  const node = data?.facts.nodes.find((item) => item.runtime_id === runtimeID);
  const agents = data?.facts.agents.filter((item) => item.runtime_id === runtimeID && item.status === "active" && item.capabilities.deploy === true) ?? [];
  const previewOnlyUnknownCapacity = preview && !preview.validation.valid && preview.validation.issues.length > 0 && preview.validation.issues.every((item) => item.code === "TOPOLOGY_CAPACITY_UNKNOWN") && allowUnknown;

  function topologyDraft(): TopologyDraft {
    const assignment = { service_key: serviceKey, environment_id: environmentID, runtime_id: runtimeID, replicas, cpu_request_millicores: cpu, memory_request_bytes: memoryMiB * 1024 * 1024, exposure: { mode: exposure }, rationale: { summary: rationale } };
    const retained = data?.topology?.assignments.filter((item) => item.service_key !== serviceKey) ?? [];
    return { schema_version: "opsi.topology_plan/v1", project_id: project!.id, assignments: [...retained, assignment] };
  }

  function policyDraft(): DeploymentPolicyDraft {
    if (!build?.build.plan_hash) throw new Error("The selected service needs an accepted BuildRecord with a plan hash before policy review.");
    return {
      schema_version: "opsi.deployment_policy/v1", project_id: project!.id, repository_id: Number(repositoryID), service_keys: [serviceKey], workflow_refs: [build.workload.workflow_ref],
      job_workflow_refs: build.workload.job_workflow_ref ? [build.workload.job_workflow_ref] : [], allowed_events: [build.workload.event_name], allowed_git_refs: [build.workload.ref],
      environment_id: environmentID, allowed_runtime_ids: [runtimeID], allowed_oci_repositories: [build.build.oci_repository], allowed_oci_prefixes: [], allowed_platforms: [build.build.platform],
      allowed_config_hashes: [build.build.config_hash], allowed_build_plan_hashes: [build.build.plan_hash], allow_unknown_capacity: allowUnknown, enabled: true,
    };
  }

  async function createPreview() {
    if (!project) return;
    setBusy("preview"); setMessage(""); setResult(null);
    try {
      if (matchingPolicies.length > 1) throw new Error("Multiple active policies match this BuildRecord. Disable the ambiguity before previewing a replacement.");
      const draft = topologyDraft(); const policy = policyDraft();
      const [topology, validation, diff, policyPreview, policyDiff] = await Promise.all([
        client.topologyPlan(project.id, draft), client.topologyValidate(project.id, draft, currentPolicy?.id ?? ""), client.topologyDiff(project.id, draft),
        client.deploymentPolicyPreview(project.id, policy), client.deploymentPolicyDiff(project.id, { policy_id: currentPolicy?.id, policy }),
      ]);
      setPreview({ topology, validation, diff, policy: policyPreview, policyDiff });
    } catch (error) { setMessage((error as Error).message); }
    finally { setBusy(""); }
  }

  async function apply() {
    if (!project || !preview || confirm !== "APPLY") return;
    setBusy("apply"); setMessage("");
    try {
      const policyResult = await client.deploymentPolicyApply(project.id, {
        policy_id: currentPolicy?.id, policy: preview.policy.policy, expected_revision: currentPolicy?.revision ?? 0, expected_state_hash: currentPolicy?.state_hash ?? "",
      }, mutationKeys.policy);
      const overridePolicyID = preview.policy.policy.allow_unknown_capacity ? policyResult.policy.id : "";
      const validation = await client.topologyValidate(project.id, preview.topology.draft, overridePolicyID);
      if (!validation.valid) throw new Error(validation.issues.map((item) => `${item.code}: ${item.message}`).join("; "));
      const planResult = await client.topologyApply(project.id, {
        draft: preview.topology.draft, expected_revision: data?.topology?.revision ?? 0, expected_state_hash: data?.topology?.state_hash ?? "", policy_id: overridePolicyID,
      }, mutationKeys.topology);
      setResult({ plan: planResult.plan, policy: policyResult.policy, reused: planResult.reused || policyResult.reused }); setConfirm("");
      setMutationKeys((value) => ({ ...value, policy: crypto.randomUUID(), topology: crypto.randomUUID() }));
      await load();
    } catch (error) { setMessage((error as Error).message); }
    finally { setBusy(""); }
  }

  async function disablePolicy(policy: DeploymentPolicy) {
    if (!project || confirm !== `DISABLE ${policy.id}`) return;
    setBusy(`disable-${policy.id}`); setMessage("");
    try { await client.disableDeploymentPolicy(project.id, policy.id, { expected_revision: policy.revision, expected_state_hash: policy.state_hash }, mutationKeys.disable); setMutationKeys((value) => ({ ...value, disable: crypto.randomUUID() })); setConfirm(""); await load(); }
    catch (error) { setMessage((error as Error).message); }
    finally { setBusy(""); }
  }

  if (!project) return <StatePanel title="Manual placement" text="Select a project first." />;
  if (status === "loading") return <StatePanel title="Manual placement" text="Loading factual runtime, repository, BuildRecord, and policy state..." />;
  if (status === "error") return <StatePanel title="Manual placement" text={message} retry={() => void load()} />;
  if (status === "empty" || !data) return <Panel title="Manual placement"><Empty text="An active repository binding, service, environment, and runtime are required before placement can be planned." /><button onClick={() => void load()} type="button">Retry</button></Panel>;

  return (
    <div className="placementStack">
      <Panel title="Manual placement wizard">
        <div className="wizardRail"><b>1 Project</b><b>2 Repository</b><b>3 Service</b><b>4 Environment</b><b>5 Runtime</b><b>6 Resources</b><b>7 Exposure</b><b>8 Validate</b><b>9 Diff</b><b>10 Policy</b><b>11 Confirm</b><b>12 Audit</b></div>
        <div className="placementGrid">
          <label>Project<input className="field" value={`${project.name} · ${project.id}`} disabled /></label>
          <label>Repository<select className="select" value={repositoryID} onChange={(event) => { setRepositoryID(event.target.value); setServiceKey(""); setBuildRecordID(""); setPreview(null); }}>{data.repositories.filter((item) => item.claim_status === "active").map((item) => <option key={item.repository_id} value={item.repository_id}>{item.full_name}</option>)}</select></label>
          <label>Service<select className="select" value={serviceKey} onChange={(event) => { setServiceKey(event.target.value); setBuildRecordID(""); setPreview(null); }}>{bindings.map((item) => <option key={item.id} value={item.service_key}>{item.service_key}</option>)}</select></label>
          <label>BuildRecord<select className="select" value={build?.id ?? ""} onChange={(event) => { setBuildRecordID(event.target.value); setPreview(null); }}>{matchingBuilds.map((item) => <option key={item.id} value={item.id}>{item.id} · {item.workload.ref}</option>)}</select></label>
          <label>Environment<select className="select" value={environmentID} onChange={(event) => { setEnvironmentID(event.target.value); setRuntimeID(""); setPreview(null); }}>{environments.map((item) => <option key={item.id} value={item.id}>{item.name} · {item.type}</option>)}</select></label>
          <label>Runtime<select className="select" value={runtimeID} onChange={(event) => { setRuntimeID(event.target.value); setPreview(null); }}>{runtimes.map((item) => <option key={item.id} value={item.id}>{item.name} · {item.status}</option>)}</select></label>
          <label>Replicas<input className="field" type="number" min="1" max="100" value={replicas} onChange={(event) => setReplicas(Number(event.target.value))} /></label>
          <label>CPU request (millicores)<input className="field" type="number" min="1" value={cpu} onChange={(event) => setCPU(Number(event.target.value))} /></label>
          <label>Memory request (MiB)<input className="field" type="number" min="1" value={memoryMiB} onChange={(event) => setMemoryMiB(Number(event.target.value))} /></label>
          <label>Exposure intent<select className="select" value={exposure} onChange={(event) => setExposure(event.target.value as typeof exposure)}><option value="none">None</option><option value="internal">Internal intent</option><option value="public">Public intent metadata</option></select></label>
          <label className="wide">Display rationale (never authority)<textarea className="textarea" maxLength={2048} value={rationale} onChange={(event) => setRationale(event.target.value)} /></label>
        </div>
        <div className="runtimeEvidence">
          <div><span>Runtime</span><b>{runtime?.status ?? "unknown"}</b></div><div><span>Node health</span><b>{node?.status ?? "missing"}</b></div><div><span>Heartbeat</span><b>{node?.last_seen_at ?? "unknown"}</b></div><div><span>Deploy Agents</span><b>{agents.length}</b></div><div><span>Observed capacity</span><b>{node?.cpu_cores && node?.memory_mb ? `${node.cpu_cores} cores · ${node.memory_mb} MiB` : "unknown"}</b></div>
        </div>
        <label className="override"><input type="checkbox" checked={allowUnknown} onChange={(event) => setAllowUnknown(event.target.checked)} /> Explicit DeploymentPolicy override: allow unknown capacity</label>
        <p className="muted">Exposure is metadata only. This wizard creates no DNS, route, certificate, port mapping, DeploymentJob, or Agent command.</p>
        <button disabled={busy !== "" || !repository || !serviceKey || !runtimeID || !build || matchingPolicies.length > 1} onClick={() => void createPreview()} type="button">{busy === "preview" ? "Validating..." : "Preview topology and policy"}</button>
        {!build ? <p className="placementError">No accepted BuildRecord matches this repository and service.</p> : null}
        {matchingPolicies.length > 1 ? <p className="placementError">Multiple active DeploymentPolicies match the selected BuildRecord. Routing is ambiguous until all but one are disabled.</p> : null}
        {message ? <p className="placementError">{message}</p> : null}
      </Panel>

      {preview ? <Panel title="Deterministic review">
        <div className="hashPair"><div><span>Topology hash</span><code>{preview.topology.plan_hash}</code></div><div><span>Policy hash</span><code>{preview.policy.policy_hash}</code></div></div>
        <div className="reviewGrid"><div><b>Validation</b><StatusBadge value={preview.validation.valid ? "ready" : "blocked"} /><p>{preview.validation.issues.length ? preview.validation.issues.map((item) => `${item.code}: ${item.message}`).join(" · ") : "No deterministic errors."}</p></div><div><b>Topology diff</b><p>{preview.diff.changes.length} service changes from revision {preview.diff.current_revision}; other service assignments are retained.</p></div><div><b>DeploymentPolicy</b><p>BuildRecord {build?.id}: exact repo/ref/workflow/event/config/plan/platform/OCI match. Unknown capacity override: <b>{String(preview.policy.policy.allow_unknown_capacity)}</b>.</p></div></div>
        {preview.validation.runtimes.map((item) => <div className="capacityCard" key={item.runtime_id}><div><b>{item.runtime_id}</b><StatusBadge value={item.eligible ? "ready" : "blocked"} /></div><p>Heartbeat {item.capacity.heartbeat_age_seconds ?? 0}s · source {item.capacity.source} · CPU {item.capacity.requested_cpu_millicores}/{item.capacity.available_cpu_millicores ?? 0}m · memory {item.capacity.requested_memory_bytes}/{item.capacity.available_memory_bytes ?? 0} bytes · reserved {item.capacity.reserved_cpu_millicores}m/{item.capacity.reserved_memory_bytes} bytes</p></div>)}
        <label>Type APPLY to create immutable policy/topology revisions<input className="field" value={confirm} onChange={(event) => setConfirm(event.target.value)} /></label>
        <button disabled={confirm !== "APPLY" || busy !== "" || (!!preview && !preview.validation.valid && !previewOnlyUnknownCapacity)} onClick={() => void apply()} type="button">{busy === "apply" ? "Applying revisions..." : "Apply policy then topology"}</button>
      </Panel> : null}

      {result ? <Panel title="Audit result"><div className="hashPair"><div><span>Topology</span><code>{result.plan.id} · r{result.plan.revision} · {result.plan.state_hash}</code></div><div><span>Policy</span><code>{result.policy.id} · r{result.policy.revision} · {result.policy.state_hash}</code></div></div><p>Idempotent replay reused: <b>{String(result.reused)}</b>. Audit actor is derived from the authenticated Cloud identity.</p></Panel> : null}

      <Panel title="DeploymentPolicy revisions">
        {data.policies.length === 0 ? <Empty text="No DeploymentPolicy revisions exist yet." /> : data.policies.map((policy) => <div className="policyRow" key={policy.id}><div><b>{policy.id}</b><span>r{policy.revision}</span><StatusBadge value={policy.policy.enabled ? "active" : "disabled"} /><code>{policy.policy_hash}</code></div>{policy.policy.enabled ? <div className="disableRow"><input className="field" placeholder={`DISABLE ${policy.id}`} value={confirm} onChange={(event) => setConfirm(event.target.value)} /><button disabled={confirm !== `DISABLE ${policy.id}` || busy !== ""} onClick={() => void disablePolicy(policy)} type="button">Disable</button></div> : null}</div>)}
      </Panel>
    </div>
  );
}
