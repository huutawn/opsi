"use client";

import { type RefObject, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Button, ErrorState, Icon, PageHeader, StatusBadge } from "@/components/ui/primitives";
import { DeploymentTimeline } from "@/features/deploy/deployment-timeline";
import { PlanReview } from "@/features/deploy/plan-review";
import { SourceStep } from "@/features/deploy/source-step";
import type { ConsoleController } from "@/features/console/types";
import { BootstrapCommand, BootstrapDialog } from "@/features/deploy/target-bootstrap";
import { LocalAPIError, LocalClient } from "@/lib/api/local-client";
import type { DeploymentPlan, DeploymentRun, DeploymentRunEvent, DeploymentRunResult, GitHubRepository, WorkloadSecretMetadata } from "@/lib/contracts/registry";

export function DeployView({ console }: { console: ConsoleController }) {
  const client = useMemo(() => new LocalClient(), []);
  const projectID = console.state.project?.id || "";
  const [repositories, setRepositories] = useState<GitHubRepository[]>([]);
  const [runs, setRuns] = useState<DeploymentRun[]>([]);
  const [run, setRun] = useState<DeploymentRun | null>(null);
  const [events, setEvents] = useState<DeploymentRunEvent[]>([]);
  const [result, setResult] = useState<DeploymentRunResult | null>(null);
  const [draftPlan, setDraftPlan] = useState<DeploymentPlan | null>(null);
  const [repositoryID, setRepositoryID] = useState(0);
  const [refName, setRefName] = useState("main");
  const [hostname, setHostname] = useState("");
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [showNew, setShowNew] = useState(false);
	const [showConnect, setShowConnect] = useState(false);
	const targetResume = useRef(false);
	const connectTrigger = useRef<HTMLButtonElement>(null);
  const canMutate = ["owner", "admin", "developer"].includes(console.session?.role || "");
	const needsServer = Boolean(run?.plan.issues.some((issue) => issue.code === "TARGET_SERVER_REQUIRED" && issue.blocking));

  const load = useCallback(async (selectLatest = false) => {
    if (!projectID) return;
    try {
      const [repositoryResult, runResult] = await Promise.all([client.githubRepositories(projectID), client.deploymentRuns(projectID)]);
      setRepositories(repositoryResult.repositories || []);
      const nextRuns = runResult.deployment_runs || [];
      setRuns(nextRuns);
      const selected = (selectLatest ? nextRuns[0] : nextRuns.find((item) => item.id === run?.id)) || nextRuns[0] || null;
      setRun(selected);
      if (selected) {
        setHostname(selected.plan.target.hostname || "");
		setDraftPlan(structuredClone(selected.plan));
		const [timeline, projection] = await Promise.all([client.deploymentRunEvents(projectID, selected.id), client.deploymentRunResult(projectID, selected.id)]);
        setEvents(timeline.events || []);
        setResult(projection);
      } else { setEvents([]); setResult(null); setDraftPlan(null); }
      setError("");
    } catch (cause) { setError((cause as Error).message); }
  }, [client, projectID, run?.id]);

  useEffect(() => { void load(true); }, [projectID]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (!run || ["succeeded", "rolled_back", "cancelled", "awaiting_approval", "awaiting_input", "awaiting_warning_ack", "failed", "stale"].includes(run.state)) return;
    const timer = window.setInterval(() => void load(), 2500);
    return () => window.clearInterval(timer);
  }, [load, run]);

  async function start() { await mutate("create", async () => (await client.createDeploymentRun(projectID, { repository_id: repositoryID, selected_ref: refName.trim(), target: { hostname: hostname.trim() || undefined } }, crypto.randomUUID())).deployment_run); setShowNew(false); }
  async function action(name: "analyze" | "approve" | "acknowledge" | "retry" | "cancel", body: Record<string, unknown> = {}) { if (!run) return; await mutate(name, () => client.deploymentRunAction(projectID, run.id, name, body, crypto.randomUUID())); }
  async function saveDraft() { if (!run || !draftPlan) return; await mutate("plan", () => client.updateDeploymentPlan(projectID, run.id, run.revision, run.plan.hash, draftPlan, crypto.randomUUID())); }
  async function resolveSecret(applicationID: string, logicalName: string, value: string): Promise<WorkloadSecretMetadata> { const response = await client.upsertWorkloadSecret(projectID, applicationID, logicalName, value, crypto.randomUUID()); return response.workload_secret; }
  async function mutate(name: string, operation: () => Promise<DeploymentRun>) { setBusy(name); setError(""); try { const next = await operation(); setRun(next); setDraftPlan(structuredClone(next.plan)); setRuns((current) => [next, ...current.filter((item) => item.id !== next.id)]); const [timeline, projection] = await Promise.all([client.deploymentRunEvents(projectID, next.id), client.deploymentRunResult(projectID, next.id)]); setEvents(timeline.events || []); setResult(projection); } catch (cause) { const apiError = cause as LocalAPIError; setError(apiError.nextAction && apiError.nextAction !== "Retry after checking Local backend connectivity." ? `${apiError.message} ${apiError.nextAction}` : apiError.message); } finally { setBusy(""); } }

	useEffect(() => {
		if (!needsServer || !canMutate || !run) { targetResume.current = false; return; }
		const inspect = async () => {
			try {
				const facts = await client.placementFacts(projectID);
				if (!targetResume.current && facts.runtimes.some((runtime) => runtime.status === "ready")) {
					targetResume.current = true;
					await mutate("analyze", () => client.deploymentRunAction(projectID, run.id, "analyze", {}, crypto.randomUUID()));
				}
			} catch { targetResume.current = false; /* The visible run issue remains the factual next action. */ }
		};
		void inspect();
		const timer = window.setInterval(() => void inspect(), 2500);
		return () => window.clearInterval(timer);
	}, [canMutate, client, needsServer, projectID, run?.id]); // eslint-disable-line react-hooks/exhaustive-deps

  if (error && !run && runs.length === 0) return <div className="p-4 lg:p-margin-desktop"><ErrorState retry={() => void load(true)} text={error} title="Deployment workflow unavailable" /></div>;
  const sourceOnly = showNew || !run;
  return (
    <main className="mx-auto max-w-7xl space-y-6 p-4 lg:p-margin-desktop">
      <PageHeader action={!sourceOnly && <Button onClick={() => { setShowNew(true); setError(""); }} variant="outline"><Icon name="add" />New deployment</Button>} description="One reviewable path from an exact repository commit to verified runtime." eyebrow={console.state.project?.name} icon="rocket_launch" title="Deploy" />
      {error && <div className="border border-status-failed/40 bg-error-container/10 p-3 text-sm text-error" role="alert">{error}</div>}
      {sourceOnly ? <SourceStep busy={busy === "create"} canMutate={canMutate} hostname={hostname} onHostname={setHostname} onRef={setRefName} onRepository={setRepositoryID} onStart={() => void start()} refName={refName} repositories={repositories} repositoryID={repositoryID} /> : run && <>
		<RunHeader onSelect={(id) => { const selected = runs.find((item) => item.id === id) || null; setRun(selected); setDraftPlan(selected ? structuredClone(selected.plan) : null); setHostname(selected?.plan.target.hostname || ""); if (selected) void Promise.all([client.deploymentRunEvents(projectID, selected.id), client.deploymentRunResult(projectID, selected.id)]).then(([timeline, projection]) => { setEvents(timeline.events || []); setResult(projection); }); }} run={run} runs={runs} />
        {(run.state === "awaiting_input" || run.state === "awaiting_approval") && draftPlan && <div className="border border-outline-variant/30 bg-surface-container p-4 sm:p-6"><PlanReview canEdit={canMutate} dirty={JSON.stringify(draftPlan) !== JSON.stringify(run.plan)} onPlan={setDraftPlan} onResolveSecret={resolveSecret} onSave={() => void saveDraft()} plan={draftPlan} saving={busy === "plan"} services={console.state.services} /></div>}
		{needsServer && console.state.bootstrapCommand && <BootstrapCommand command={console.state.bootstrapCommand} />}
        {!['awaiting_input','awaiting_approval'].includes(run.state) && <div className="border border-outline-variant/30 bg-surface-container p-4 sm:p-6"><DeploymentTimeline events={events} run={run} /></div>}
        {run.state === "succeeded" && result && <DeploymentResult result={result} />}
        <PrimaryAction busy={busy} canMutate={canMutate} connectTrigger={connectTrigger} needsServer={needsServer} onAction={action} onConnect={() => setShowConnect(true)} run={run} />
        <TechnicalDetails events={events} result={result} run={run} />
		{showConnect && <BootstrapDialog console={console} onClose={() => { setShowConnect(false); window.requestAnimationFrame(() => connectTrigger.current?.focus()); }} onCreated={async () => { await console.actions.load(); }} />}
      </>}
    </main>
  );
}

function RunHeader({ onSelect, run, runs }: { onSelect: (id: string) => void; run: DeploymentRun; runs: DeploymentRun[] }) { return <div className="flex flex-col gap-3 border-y border-outline-variant/30 py-4 sm:flex-row sm:items-center sm:justify-between"><div><p className="text-xs uppercase tracking-wider text-on-surface-variant">Exact source</p><p className="mt-1 font-medium">{run.plan.source.repository} · {run.plan.source.selected_ref}</p><p className="mt-1 font-mono text-xs text-on-surface-variant">{run.plan.source.commit_sha.slice(0, 12)}</p></div><div className="flex items-center gap-3"><StatusBadge label={run.state.replaceAll("_", " ")} status={run.state === "succeeded" ? "ready" : run.state === "failed" || run.state === "stale" ? "failed" : run.state.startsWith("awaiting") ? "pending" : "in_progress"} /><label className="sr-only" htmlFor="deployment-run-select">Deployment run</label><select className="min-h-10 border border-outline-variant/30 bg-surface-container-low px-3 text-sm" id="deployment-run-select" onChange={(event) => onSelect(event.target.value)} value={run.id}>{runs.map((item) => <option key={item.id} value={item.id}>{new Date(item.created_at).toLocaleString()} · {item.state}</option>)}</select></div></div>; }

function PrimaryAction({ busy, canMutate, connectTrigger, needsServer, onAction, onConnect, run }: { busy: string; canMutate: boolean; connectTrigger: RefObject<HTMLButtonElement | null>; needsServer: boolean; onAction: (name: "analyze" | "approve" | "acknowledge" | "retry" | "cancel", body?: Record<string, unknown>) => void; onConnect: () => void; run: DeploymentRun }) { let label="", action: Parameters<typeof onAction>[0]|""="", body:Record<string,unknown>={};if(run.state==="awaiting_input"&&needsServer){label="Connect server"}else if(run.state==="awaiting_input"||run.state==="stale"){label="Analyze again";action="analyze"}else if(run.state==="awaiting_approval"){label="Approve & Deploy";action="approve";body={plan_hash:run.plan.hash}}else if(run.state==="awaiting_warning_ack"){label="Acknowledge & Continue";action="acknowledge";body={preflight_hash:run.preflight_hash}}else if(run.state==="failed"&&run.failure?.retryable){label="Retry failed step";action="retry"}else if(["analyzing","provisioning","building","preflighting","deploying","verifying","cleaning_up"].includes(run.state)){label="Cancel run";action="cancel"}if(!label)return null;if(!canMutate)return <div className="border border-outline-variant/30 bg-surface-container-low p-4 text-sm text-on-surface-variant" role="status">Your role has read-only access to this run.</div>;return <div className="sticky bottom-4 z-10 flex items-center justify-between gap-4 border border-outline-variant/40 bg-surface-container-high p-4 shadow-lg"><p className="text-sm text-on-surface-variant">This is the only action needed for the current state.</p><Button disabled={Boolean(busy)} onClick={() => action ? onAction(action,body) : onConnect()} ref={!action ? connectTrigger : undefined} size="lg" variant={action === "cancel" ? "outline" : "primary"}>{busy===action?"Working…":label}</Button></div>; }

function DeploymentResult({ result }: { result: DeploymentRunResult }) { return <section aria-labelledby="deployment-result-title" className="border border-status-ready/40 bg-state-live-bg p-4 sm:p-6"><p className="text-xs font-medium uppercase tracking-wider text-status-ready">Verified result</p><h2 className="mt-2 text-xl font-semibold" id="deployment-result-title">Repository is running</h2>{result.public_url && <a className="mt-3 inline-flex min-h-11 items-center gap-2 text-primary underline underline-offset-4" href={result.public_url} rel="noreferrer" target="_blank"><Icon name="open_in_new" />{result.public_url}</a>}<ul className="mt-4 grid gap-2 sm:grid-cols-2">{result.applications.map((application) => <li className="border border-status-ready/30 bg-surface-container p-3 text-sm" key={application.service_id}><p className="font-medium">{application.service_key}</p><p className="mt-1 text-on-surface-variant">{application.deployment_status} · {application.available_replicas || 0} replica(s) ready</p><p className={application.digest_matches_image_id ? "mt-1 text-status-ready" : "mt-1 text-error"}>{application.digest_matches_image_id ? "K3s imageID matches the immutable build digest" : "Image digest evidence does not match"}</p>{application.build_log_url && <a className="mt-2 inline-flex min-h-10 items-center text-primary underline" href={application.build_log_url} rel="noreferrer" target="_blank">Raw build log</a>}</li>)}</ul>{result.verifications.length > 0 && <ul className="mt-4 grid gap-2 sm:grid-cols-2">{result.verifications.map((verification) => <li className="border border-status-ready/30 bg-surface-container p-3 text-sm" key={verification.id}><p className="font-medium">{verification.dependency_logical_name}</p><p className="mt-1 text-on-surface-variant">{verification.overall_status} · {verification.connection.protocol || verification.provider_health.provider_kind} {verification.connection.latency_ms ? `· ${verification.connection.latency_ms} ms` : ""}</p></li>)}</ul>}</section>; }

function TechnicalDetails({ events, result, run }: { events: DeploymentRunEvent[]; result: DeploymentRunResult | null; run: DeploymentRun }) { return <details className="border border-outline-variant/30 bg-surface-container-low"><summary className="cursor-pointer px-4 py-3 text-sm font-medium">Technical details</summary><div className="space-y-4 border-t border-outline-variant/30 p-4 text-xs"><dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-2"><dt className="text-on-surface-variant">Run ID</dt><dd className="break-all font-mono">{run.id}</dd><dt className="text-on-surface-variant">Source SHA</dt><dd className="break-all font-mono">{run.plan.source.commit_sha}</dd><dt className="text-on-surface-variant">Plan hash</dt><dd className="break-all font-mono">{run.plan.hash}</dd><dt className="text-on-surface-variant">Preflight hash</dt><dd className="break-all font-mono">{run.preflight_hash || "Not available"}</dd><dt className="text-on-surface-variant">Build records</dt><dd className="break-all font-mono">{checkpointIDs(run, "build_record").join(", ") || "Not available"}</dd><dt className="text-on-surface-variant">Deployments</dt><dd className="break-all font-mono">{checkpointIDs(run, "deployment_job").join(", ") || "Not available"}</dd><dt className="text-on-surface-variant">Image digests</dt><dd className="break-all font-mono">{result?.applications.map((item) => item.build_digest).join(", ") || "Not available"}</dd></dl>{result?.capacity.map((capacity) => <dl className="grid grid-cols-2 gap-2 border border-outline-variant/30 p-3 sm:grid-cols-4" key={capacity.runtime_id}><CapacityFact label="Requested" value={`${capacity.requested_cpu_millicores}m · ${formatBytes(capacity.requested_memory_bytes)}`} /><CapacityFact label="Assigned" value={`${capacity.assigned_cpu_millicores}m · ${formatBytes(capacity.assigned_memory_bytes)}`} /><CapacityFact label="Reserved" value={`${capacity.reserved_cpu_millicores}m · ${formatBytes(capacity.reserved_memory_bytes)}`} /><CapacityFact label="Available" value={`${capacity.available_cpu_millicores ?? 0}m · ${formatBytes(capacity.available_memory_bytes || 0)}`} /></dl>)}<pre className="max-h-64 overflow-auto bg-surface-container-lowest p-3 text-on-surface-variant">{events.map((event) => `${event.created_at} ${event.level.toUpperCase()} ${event.message}`).join("\n")}</pre></div></details>; }

function CapacityFact({ label, value }: { label: string; value: string }) { return <div><dt className="text-on-surface-variant">{label}</dt><dd className="mt-1 font-mono">{value}</dd></div>; }
function formatBytes(bytes: number) { return bytes ? `${Math.round(bytes / 1024 / 1024)} MiB` : "0 MiB"; }

function checkpointIDs(run: DeploymentRun, kind: string) { return (run.authority_refs.checkpoints || []).filter((checkpoint) => checkpoint.kind === kind).map((checkpoint) => checkpoint.id); }
