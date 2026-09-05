"use client";

import { type RefObject, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Button, Icon, PageHeader, StatusBadge } from "@/components/ui/primitives";
import { DeploymentTimeline } from "@/features/deploy/deployment-timeline";
import { ApprovalPlanSummary, PlanReview } from "@/features/deploy/plan-review";
import { getUnreviewedApplications } from "@/features/deploy/runtime-config";
import { SourceStep } from "@/features/deploy/source-step";
import type { ConsoleController } from "@/features/console/types";
import { BootstrapCommand, BootstrapDialog, BootstrapProgress } from "@/features/deploy/target-bootstrap";
import { RefineAnalysis, RepositoryExport } from "@/features/deploy/analysis-tools";
import { PublicRoute } from "@/features/deploy/public-route";
import { PublicHostnameQuotaPanel } from "@/features/deploy/public-hostname-quota";
import { publicHostname, publicSubdomainFromHostname } from "@/features/deploy/public-subdomain";
import { ResourceProposalDialog } from "@/features/deploy/resource-proposal-dialog";
import { LocalAPIError, LocalClient } from "@/lib/api/local-client";
import type { AnalysisScope, DeploymentPlan, DeploymentRun, DeploymentRunEvent, DeploymentRunResult, GitHubInstallation, GitHubRepository, PublicHostnameAllocation, PublicHostnameQuota, RepositoryExportPreview, RepositoryExportResult, ResourceRecommendation, WorkloadSecretMetadata } from "@/lib/contracts/registry";
import { terminalBootstrap } from "@/lib/presentation/infrastructure/model";

export function DeployView({ console }: { console: ConsoleController }) {
  const client = useMemo(() => new LocalClient(), []);
  const projectID = console.state.project?.id || "";
  const [installations, setInstallations] = useState<GitHubInstallation[]>([]);
  const [linkedInstallationIDs, setLinkedInstallationIDs] = useState<number[]>([]);
  const [repositories, setRepositories] = useState<GitHubRepository[]>([]);
  const [runs, setRuns] = useState<DeploymentRun[]>([]);
  const [run, setRun] = useState<DeploymentRun | null>(null);
  const [events, setEvents] = useState<DeploymentRunEvent[]>([]);
  const [result, setResult] = useState<DeploymentRunResult | null>(null);
  const [hostnameQuota, setHostnameQuota] = useState<PublicHostnameQuota | null>(null);
  const [draftPlan, setDraftPlan] = useState<DeploymentPlan | null>(null);
  const [installationID, setInstallationID] = useState(0);
  const [repositoryID, setRepositoryID] = useState(0);
  const [refName, setRefName] = useState("main");
  const [hostname, setHostname] = useState("");
  const [busy, setBusy] = useState("");
  const [error, setError] = useState<DeployFailure | null>(null);
  const [loadFailure, setLoadFailure] = useState<DeployFailure | null>(null);
  const [showNew, setShowNew] = useState(false);
  const [exportResult, setExportResult] = useState<RepositoryExportResult | null>(null);
  const [showConnect, setShowConnect] = useState(false);
  const targetResume = useRef(false);
  const connectTrigger = useRef<HTMLButtonElement>(null);
  const [showProposal, setShowProposal] = useState(false);
  const [recommendation, setRecommendation] = useState<ResourceRecommendation | null>(null);
  const [recLoading, setRecLoading] = useState(false);
  const [recError, setRecError] = useState<string | null>(null);
  const [applyingProposal, setApplyingProposal] = useState(false);
  const canMutate = ["owner", "admin", "developer"].includes(console.session?.role || "");
  const needsServer = Boolean(run?.plan.issues.some((issue) => issue.code === "TARGET_SERVER_REQUIRED" && issue.blocking));
  const bootstrapSession = useMemo(() => [...console.state.sessions].sort((a, b) => b.created_at.localeCompare(a.created_at))[0], [console.state.sessions]);
  const bootstrapActive = Boolean(needsServer && bootstrapSession && !terminalBootstrap(bootstrapSession));
  const bootstrapEvents = bootstrapSession && console.state.bootstrapEventsSessionID === bootstrapSession.id ? console.state.bootstrapEvents : [];

  const load = useCallback(async (selectLatest = false) => {
    if (!projectID) return;
    try {
      const [installationResult, discoveryResult, repositoryResult, runResult, quotaResult] = await Promise.all([
        client.githubInstallations(projectID),
        client.githubInstallationDiscovery(projectID),
        client.githubRepositories(projectID),
        client.deploymentRuns(projectID),
        client.publicHostnameQuota(projectID),
      ]);
	  setHostnameQuota(quotaResult);
      const linked = installationResult.installations || [];
      const availableByID = new Map((discoveryResult.installations || []).map((item) => [item.installation_id, item]));
      for (const installation of linked) availableByID.set(installation.installation_id, installation);
      const available = [...availableByID.values()];
      setInstallations(available);
      setLinkedInstallationIDs(linked.map((item) => item.installation_id));
      if (!installationID && available[0]) setInstallationID(available[0].installation_id);
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
      setError(null);
      setLoadFailure(null);
    } catch (cause) { setLoadFailure(deployFailure(cause)); }
  }, [client, installationID, projectID, run?.id]);

  useEffect(() => { void load(true); }, [projectID]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    const saved = readSourceDraft(projectID);
    setInstallationID(saved?.installationID || 0);
    setRepositoryID(saved?.repositoryID || 0);
    setRefName(saved?.refName || "main");
    setHostname(saved?.hostname || "");
  }, [projectID]);
  useEffect(() => {
    if (!run || ["succeeded", "rolled_back", "cancelled", "awaiting_approval", "awaiting_input", "awaiting_warning_ack", "failed", "stale"].includes(run.state)) return;
    const timer = window.setInterval(() => void load(), 2500);
    return () => window.clearInterval(timer);
  }, [load, run]);
  useEffect(() => {
    if (!run || run.state !== "succeeded" || !result?.public_endpoints?.some((endpoint) => endpoint.status === "publishing")) return;
    const timer = window.setInterval(() => void client.deploymentRunResult(projectID, run.id).then(setResult).catch(() => undefined), 2500);
    return () => window.clearInterval(timer);
  }, [client, projectID, result?.public_endpoints, run]);

  async function start() { const next = await mutate("create", async () => (await client.createDeploymentRun(projectID, { repository_id: repositoryID, selected_ref: refName.trim(), target: { hostname: hostname.trim() || undefined } }, crypto.randomUUID())).deployment_run); if (next) { clearSourceDraft(projectID); setExportResult(null); setShowNew(false); } }
  async function discoverGitHub() { setBusy("github-discover"); setError(null); try { const started = await client.startGitHubInstallationDiscovery(projectID, crypto.randomUUID()); window.location.assign(started.authorization_url); } catch (cause) { setError(deployFailure(cause)); setBusy(""); } }
  async function connectInstallation() { if (!installationID) return; setBusy("github-connect"); setError(null); try { const started = await client.startGitHubInstallationClaim(projectID, installationID, crypto.randomUUID()); window.location.assign(started.authorization_url); } catch (cause) { setError(deployFailure(cause)); setBusy(""); } }
  async function sourceAction() {
    const repository = repositories.find((item) => item.repository_id === repositoryID);
    if (!repository) return;
    if (repository.claim_status === "conflict") {
      setError({ code: "GITHUB_REPOSITORY_ALREADY_CLAIMED", message: "This repository is claimed by another project. Select a different repository or release it from the owning project.", nextAction: "Select a repository available to this project.", status: 409 });
      return;
    }
    if (repository.claim_status === "available") {
      setBusy("repository-claim");
      setError(null);
      try {
        await client.claimGitHubRepository(projectID, repository.repository_id, crypto.randomUUID());
        const inventory = await client.githubRepositories(projectID);
        const refreshed = inventory.repositories || [];
        setRepositories(refreshed);
        if (!refreshed.some((item) => item.repository_id === repository.repository_id && item.claim_status === "active")) {
          throw new Error("Cloud accepted the claim but factual repository inventory has not converged. Retry claim and analysis.");
        }
        await start();
      } catch (cause) { setError(deployFailure(cause)); }
      finally { setBusy(""); }
      return;
    }
    await start();
  }
  async function login(setFailure: (failure: DeployFailure) => void) { setBusy("login"); try { const started = await client.startLogin(projectID, window.location.search); window.location.assign(started.auth_url); } catch (cause) { setFailure(deployFailure(cause)); setBusy(""); } }
  async function action(name: "analyze" | "approve" | "acknowledge" | "retry" | "cancel", body: Record<string, unknown> = {}) { if (!run) return; await mutate(name, () => client.deploymentRunAction(projectID, run.id, name, body, crypto.randomUUID())); }
  async function refine(scope: AnalysisScope) { await action("analyze", { scope }); }
  async function previewExport() { if (!run) return null; try { setError(null); return await client.repositoryExportPreview(projectID, run.id); } catch (cause) { setError(deployFailure(cause)); return null; } }
  async function createExport(preview: RepositoryExportPreview) {
    try {
      setError(null);
      const response = await client.repositoryExport(projectID, preview, crypto.randomUUID());
      setExportResult(response.repository_export);
      return true;
    } catch (cause) {
      setError(deployFailure(cause));
      return false;
    }
  }
  async function saveDraft() { if (!run || !draftPlan) return; await mutate("plan", () => client.updateDeploymentPlan(projectID, run.id, run.revision, run.plan.hash, draftPlan, crypto.randomUUID())); }
  const loadRecommendation = useCallback(async (openDialog = true) => {
    if (!projectID || !run) return null;
    setRecLoading(true);
    setRecError(null);
    if (openDialog) setShowProposal(true);
    try {
      const rec = await client.resourceRecommendation(projectID, run.id);
      setRecommendation(rec);
      return rec;
    } catch (cause) {
      const msg = (cause as Error).message || "Failed to load resource recommendation";
      setRecError(msg);
      return null;
    } finally {
      setRecLoading(false);
    }
  }, [client, projectID, run]);

  // Proposal is prompted after bootstrap completes/resumes analysis or when requested via Resource proposal action
  async function applyProposal(rec: ResourceRecommendation) {
    if (!run || !draftPlan) return;
    setApplyingProposal(true);
    try {
      const updatedPlan = structuredClone(draftPlan);
      for (const appRec of rec.applications) {
        const index = updatedPlan.applications.findIndex((a) => a.key === appRec.key);
        if (index >= 0) {
          updatedPlan.applications[index].capacity = {
            ...updatedPlan.applications[index].capacity,
            replicas: appRec.replicas,
            cpu_milli: appRec.proposed.cpu_request_milli,
            cpu_limit_milli: appRec.proposed.cpu_limit_milli,
            memory_bytes: appRec.proposed.memory_request_bytes,
            memory_limit_bytes: appRec.proposed.memory_limit_bytes,
          };
        }
      }
      setDraftPlan(updatedPlan);
      const updatedRun = await client.updateDeploymentPlan(
        projectID,
        run.id,
        run.revision,
        run.plan.hash,
        updatedPlan,
        crypto.randomUUID(),
        rec.basis?.basis_hash
      );
      setRun(updatedRun);
      setDraftPlan(structuredClone(updatedRun.plan));
      setRuns((current) => [updatedRun, ...current.filter((item) => item.id !== updatedRun.id)]);
      setShowProposal(false);
    } catch (cause) {
      const failure = deployFailure(cause);
      if (failure.code === "RESOURCE_RECOMMENDATION_STALE") {
        setRecError("Resource recommendation is stale because cluster topology or capacity changed. Loading refreshed proposal…");
        void loadRecommendation(true);
      } else {
        setError(failure);
      }
    } finally {
      setApplyingProposal(false);
    }
  }
  async function hostnameAction(allocation: PublicHostnameAllocation, actionName: "release" | "retry") { const name = `hostname-${allocation.id}`; setBusy(name); setError(null); try { await client.publicHostnameAction(projectID, allocation.id, actionName, crypto.randomUUID()); const quota = await client.publicHostnameQuota(projectID); setHostnameQuota(quota); if (run) setResult(await client.deploymentRunResult(projectID, run.id)); } catch (cause) { setError(deployFailure(cause)); } finally { setBusy(""); } }
  async function resolveSecret(applicationID: string, logicalName: string, value: string): Promise<WorkloadSecretMetadata> { const response = await client.upsertWorkloadSecret(projectID, applicationID, logicalName, value, crypto.randomUUID()); return response.workload_secret; }
  async function listSecrets(applicationID: string): Promise<WorkloadSecretMetadata[]> { const response = await client.workloadSecrets(projectID, applicationID); return response.workload_secrets || []; }
  async function mutate(name: string, operation: () => Promise<DeploymentRun>) { setBusy(name); setError(null); try { const next = await operation(); setRun(next); setDraftPlan(structuredClone(next.plan)); setRuns((current) => [next, ...current.filter((item) => item.id !== next.id)]); const [timeline, projection, quota] = await Promise.all([client.deploymentRunEvents(projectID, next.id), client.deploymentRunResult(projectID, next.id), client.publicHostnameQuota(projectID)]); setEvents(timeline.events || []); setResult(projection); setHostnameQuota(quota); return next; } catch (cause) { setError(deployFailure(cause)); return null; } finally { setBusy(""); } }

	useEffect(() => {
		if (!needsServer || !canMutate || !run) { targetResume.current = false; return; }
		let checking = false;
		const inspect = async () => {
			if (checking) return;
			checking = true;
			try {
				if (bootstrapSession) await console.actions.refreshBootstrap(bootstrapSession.id);
				const facts = await client.placementFacts(projectID);
				if (!targetResume.current && facts.runtimes.some((runtime) => runtime.status === "ready")) {
					targetResume.current = true;
					const resumed = await mutate("analyze", () => client.deploymentRunAction(projectID, run.id, "analyze", {}, crypto.randomUUID()));
					if (!resumed) targetResume.current = false;
					else void loadRecommendation(true);
				}
			} catch { targetResume.current = false; /* The visible run issue remains the factual next action. */ }
			finally { checking = false; }
		};
		void inspect();
		const timer = window.setInterval(() => void inspect(), 2500);
		return () => window.clearInterval(timer);
	}, [bootstrapSession?.id, canMutate, client, needsServer, projectID, run?.id]); // eslint-disable-line react-hooks/exhaustive-deps

  if (loadFailure && !run && runs.length === 0) return <DeployLoadFailure busy={busy} failure={loadFailure} onLogin={() => void login(setLoadFailure)} onRetry={() => void load(true)} projectName={console.state.project?.name} />;
  const sourceOnly = showNew || !run;
  const draftDirty = Boolean(draftPlan && run && JSON.stringify(draftPlan) !== JSON.stringify(run.plan));
  const hostnameQuotaBlocked = (value: string) => {
    if (!hostnameQuota || hostnameQuota.remaining > 0) return false;
    const label = publicSubdomainFromHostname(value) || value;
    const fqdn = label ? publicHostname(label) : "";
    return ![...(hostnameQuota.allocations || []), ...(hostnameQuota.project_allocations || [])].some((allocation) => allocation.status !== "released" && allocation.project_id === projectID && allocation.hostname === fqdn);
  };
  return (
    <main className="mx-auto max-w-7xl space-y-6 p-4 lg:p-margin-desktop">
      <PageHeader action={!sourceOnly && <Button onClick={() => { setShowNew(true); setHostname(""); setError(null); }} variant="outline"><Icon name="add" />New deployment</Button>} description="One reviewable path from an exact repository commit to verified runtime." eyebrow={console.state.project?.name} icon="rocket_launch" title="Deploy" />
      {error && <DeployActionFailure busy={busy} failure={error} onLogin={() => void login(setError)} />}
      <PublicHostnameQuotaPanel busy={busy} canMutate={canMutate} onAction={(allocation, actionName) => void hostnameAction(allocation, actionName)} projectID={projectID} quota={hostnameQuota} />
      {sourceOnly ? <SourceStep busy={busy} canMutate={canMutate && !isAuthFailure(error)} hostname={hostname} installationID={installationID} installations={installations} linkedInstallationIDs={linkedInstallationIDs} onConnectInstallation={() => void connectInstallation()} onDiscover={() => void discoverGitHub()} onHostname={(value) => { setHostname(value); updateSourceDraft(projectID, { hostname: value }); }} onInstallation={(value) => { setInstallationID(value); updateSourceDraft(projectID, { installationID: value }); }} onRef={(value) => { setRefName(value); updateSourceDraft(projectID, { refName: value }); }} onRepository={(value) => { setRepositoryID(value); updateSourceDraft(projectID, { repositoryID: value }); }} onStart={() => void sourceAction()} quotaBlocked={hostnameQuotaBlocked(hostname)} refName={refName} repositories={repositories} repositoryID={repositoryID} /> : run && <>
        <RunHeader onSelect={(id) => { const selected = runs.find((item) => item.id === id) || null; setRun(selected); setExportResult(null); setDraftPlan(selected ? structuredClone(selected.plan) : null); setHostname(selected?.plan.target.hostname || ""); if (selected) void Promise.all([client.deploymentRunEvents(projectID, selected.id), client.deploymentRunResult(projectID, selected.id)]).then(([timeline, projection]) => { setEvents(timeline.events || []); setResult(projection); }); }} run={run} runs={runs} />
        {run.state === "awaiting_input" && draftPlan && <div className="border border-outline-variant/30 bg-surface-container p-4 sm:p-6"><PlanReview canEdit={canMutate} dirty={draftDirty} onListSecrets={listSecrets} onPlan={setDraftPlan} onProposal={() => void loadRecommendation(true)} onResolveSecret={resolveSecret} onSave={() => void saveDraft()} plan={draftPlan} quotaBlocked={hostnameQuotaBlocked(draftPlan.target.hostname || "")} saving={busy === "plan"} services={console.state.services} /></div>}
        {run.state === "awaiting_approval" && draftPlan && <ApprovalPlanSummary onProposal={() => void loadRecommendation(true)} plan={draftPlan}><PlanReview canEdit={canMutate} dirty={draftDirty} onListSecrets={listSecrets} onPlan={setDraftPlan} onResolveSecret={resolveSecret} onSave={() => void saveDraft()} plan={draftPlan} quotaBlocked={hostnameQuotaBlocked(draftPlan.target.hostname || "")} saving={busy === "plan"} services={console.state.services} /><RepositoryExport canCreate={canMutate} onCreate={createExport} onPreview={previewExport} result={exportResult} /></ApprovalPlanSummary>}
        {run.plan.issues.some((issue) => issue.code === "ANALYSIS_TRUNCATED") && <RefineAnalysis
          busy={busy === "analyze"}
          initialScope={run.plan.analysis_scope || { application_roots: [], exclude_paths: [] }}
          onRefine={(scope) => void refine(scope)}
        />}
        {run.state === "awaiting_input" && <RepositoryExport canCreate={canMutate} onCreate={createExport} onPreview={previewExport} result={exportResult} />}
		{needsServer && bootstrapSession && <BootstrapProgress console={console} events={bootstrapEvents} session={bootstrapSession} />}
		{needsServer && console.state.bootstrapCommand && <BootstrapCommand command={console.state.bootstrapCommand} />}
        {!['awaiting_input','awaiting_approval'].includes(run.state) && <div className="border border-outline-variant/30 bg-surface-container p-4 sm:p-6"><DeploymentTimeline events={events} run={run} /></div>}
        {run.state === "succeeded" && result && <DeploymentResult canMutate={canMutate} client={client} plan={run.plan} projectID={projectID} result={result} />}
        {(() => {
          const unreviewedApps = draftPlan || run.plan ? getUnreviewedApplications((draftPlan || run.plan)!) : [];
          return <PrimaryAction bootstrapActive={bootstrapActive} busy={busy} canMutate={canMutate} connectTrigger={connectTrigger} draftDirty={draftDirty} hasUnreviewed={unreviewedApps.length > 0} needsServer={needsServer} onAction={action} onConnect={() => setShowConnect(true)} onNew={() => { setShowNew(true); setHostname(""); setError(null); }} onSaveDraft={() => void saveDraft()} run={run} unreviewedAppName={unreviewedApps[0]?.name} />;
        })()}
        <TechnicalDetails events={events} result={result} run={run} />
		{showConnect && <BootstrapDialog console={console} onClose={() => { setShowConnect(false); window.requestAnimationFrame(() => connectTrigger.current?.focus()); }} onCreated={async () => { await console.actions.load(); }} />}
		{showProposal && (
			<ResourceProposalDialog
				applying={applyingProposal}
				error={recError}
				loading={recLoading}
				onApply={applyProposal}
				onClose={() => setShowProposal(false)}
				recommendation={recommendation}
			/>
		)}
      </>}
    </main>
  );
}

type DeployFailure = { code: string; message: string; nextAction: string; status: number };

function deployFailure(cause: unknown): DeployFailure {
  const error = cause as LocalAPIError;
  const nextAction = error.nextAction && error.nextAction !== "Retry after checking Local backend connectivity." ? error.nextAction : "";
  return { code: error.code || "LOCAL_REQUEST_FAILED", message: error.message || "Deployment request failed.", nextAction, status: error.status || 0 };
}

function isAuthFailure(failure: DeployFailure | null) {
  return Boolean(failure && (failure.status === 401 || failure.code === "CLOUD_AUTH_REQUIRED" || failure.code === "CLOUD_PAT_REQUIRED" || failure.code === "LOCAL_CREDENTIAL_MISSING"));
}

type SourceDraft = { installationID: number; repositoryID: number; refName: string; hostname: string };

function readSourceDraft(projectID: string): SourceDraft | null {
  if (!projectID) return null;
  const query = new URLSearchParams(window.location.search);
  if (query.get("source_project") !== projectID) return null;
  return {
    installationID: Number(query.get("source_installation")) || 0,
    repositoryID: Number(query.get("source_repository")) || 0,
    refName: query.get("source_ref") || "main",
    hostname: query.get("source_hostname") || "",
  };
}

function writeSourceDraft(projectID: string, draft: SourceDraft) {
  if (!projectID) return;
  const url = new URL(window.location.href);
  url.searchParams.set("source_project", projectID);
  url.searchParams.set("source_installation", String(draft.installationID));
  url.searchParams.set("source_repository", String(draft.repositoryID));
  url.searchParams.set("source_ref", draft.refName);
  if (draft.hostname) url.searchParams.set("source_hostname", draft.hostname);
  else url.searchParams.delete("source_hostname");
  window.history.replaceState(null, "", url);
}

function updateSourceDraft(projectID: string, patch: Partial<SourceDraft>) {
	const current = readSourceDraft(projectID) || { installationID: 0, repositoryID: 0, refName: "main", hostname: "" };
	writeSourceDraft(projectID, { ...current, ...patch });
}

function clearSourceDraft(projectID: string) {
  if (!projectID) return;
  const url = new URL(window.location.href);
  for (const name of ["source_project", "source_installation", "source_repository", "source_ref", "source_hostname"]) {
    url.searchParams.delete(name);
  }
  window.history.replaceState(null, "", url);
}

function DeployActionFailure({ busy, failure, onLogin }: { busy: string; failure: DeployFailure; onLogin: () => void }) {
  return <div className="flex flex-col gap-3 border border-status-failed/40 bg-error-container/10 p-4 text-sm text-error sm:flex-row sm:items-center sm:justify-between" role="alert"><div><p>{failure.message}</p>{failure.nextAction && <p className="mt-1 text-error/80">{failure.nextAction}</p>}</div>{isAuthFailure(failure) && <Button disabled={Boolean(busy)} onClick={onLogin} variant="danger">{busy === "login" ? "Opening GitHub…" : "Sign in again"}</Button>}</div>;
}

function DeployLoadFailure({ busy, failure, onLogin, onRetry, projectName }: { busy: string; failure: DeployFailure; onLogin: () => void; onRetry: () => void; projectName?: string }) {
  const auth = isAuthFailure(failure);
  return <main className="mx-auto max-w-7xl space-y-6 p-4 lg:p-margin-desktop"><PageHeader description="One reviewable path from an exact repository commit to verified runtime." eyebrow={projectName} icon="rocket_launch" title="Deploy" /><section className="flex flex-col items-center justify-center border border-status-failed/40 bg-error-container/10 p-8 text-center" role="alert"><Icon className="mb-3 text-[36px] text-error" name="error" /><h2 className="text-lg font-semibold text-error">{auth ? "Cloud sign-in required" : "Deployment data unavailable"}</h2><p className="mt-2 max-w-lg text-sm text-error/80">{failure.message}</p>{failure.nextAction && <p className="mt-1 max-w-lg text-sm text-error/80">{failure.nextAction}</p>}<Button className="mt-5" disabled={Boolean(busy)} onClick={auth ? onLogin : onRetry} variant="danger">{busy === "login" ? "Opening GitHub…" : auth ? "Sign in again" : "Retry"}</Button></section></main>;
}

function RunHeader({ onSelect, run, runs }: { onSelect: (id: string) => void; run: DeploymentRun; runs: DeploymentRun[] }) { return <div className="flex flex-col gap-3 border-y border-outline-variant/30 py-4 sm:flex-row sm:items-center sm:justify-between"><div><p className="text-xs uppercase tracking-wider text-on-surface-variant">Exact source</p><p className="mt-1 font-medium">{run.plan.source.repository} · {run.plan.source.selected_ref}</p><p className="mt-1 font-mono text-xs text-on-surface-variant">{run.plan.source.commit_sha.slice(0, 12)}</p></div><div className="flex items-center gap-3"><StatusBadge label={run.state.replaceAll("_", " ")} status={run.state === "succeeded" ? "ready" : run.state === "failed" || run.state === "stale" ? "failed" : run.state.startsWith("awaiting") ? "pending" : "in_progress"} /><label className="sr-only" htmlFor="deployment-run-select">Deployment run</label><select className="min-h-10 border border-outline-variant/30 bg-surface-container-low px-3 text-sm" id="deployment-run-select" onChange={(event) => onSelect(event.target.value)} value={run.id}>{runs.map((item) => <option key={item.id} value={item.id}>{new Date(item.created_at).toLocaleString()} · {item.state}</option>)}</select></div></div>; }

function PrimaryAction({ bootstrapActive, busy, canMutate, connectTrigger, draftDirty, hasUnreviewed = false, needsServer, onAction, onConnect, onNew, onSaveDraft, run, unreviewedAppName }: { bootstrapActive: boolean; busy: string; canMutate: boolean; connectTrigger: RefObject<HTMLButtonElement | null>; draftDirty: boolean; hasUnreviewed?: boolean; needsServer: boolean; onAction: (name: "analyze" | "approve" | "acknowledge" | "retry" | "cancel", body?: Record<string, unknown>) => void; onConnect: () => void; onNew: () => void; onSaveDraft: () => void; run: DeploymentRun; unreviewedAppName?: string }) {
  let label = "";
  let action: Parameters<typeof onAction>[0] | "" = "";
  let body: Record<string, unknown> = {};
  let newDeployment = false;
  let saveDraftFirst = false;
  if (run.state === "awaiting_input" && needsServer) label = bootstrapActive ? "Connecting server…" : "Connect server";
  else if (run.state === "awaiting_input" || run.state === "stale") { label = "Analyze again"; action = "analyze"; }
  else if (run.state === "awaiting_approval" && draftDirty) { label = "Save changes before approval"; saveDraftFirst = true; }
  else if (run.state === "awaiting_approval") { label = "Approve & Deploy"; action = "approve"; body = { plan_hash: run.plan.hash }; }
  else if (run.state === "awaiting_warning_ack") { label = "Acknowledge & Continue"; action = "acknowledge"; body = { preflight_hash: run.preflight_hash }; }
  else if (run.state === "failed" && run.failure?.retryable && run.attempt < run.plan.failure_policy.max_attempts) { label = "Retry failed step"; action = "retry"; }
  else if (run.state === "failed") { label = "New deployment"; newDeployment = true; }
  else if (["analyzing", "provisioning", "building", "preflighting", "deploying", "verifying", "cleaning_up"].includes(run.state)) { label = "Cancel run"; action = "cancel"; }
  if (!label) return null;
  if (!canMutate) return <div className="border border-outline-variant/30 bg-surface-container-low p-4 text-sm text-on-surface-variant" role="status">Your role has read-only access to this run.</div>;
  const working = (Boolean(action) && busy === action) || (saveDraftFirst && busy === "plan");
  const approvalBlockedByReview = action === "approve" && hasUnreviewed;
  const disabled = Boolean(busy) || bootstrapActive || (saveDraftFirst && hasUnreviewed) || approvalBlockedByReview;
  const statusMessage = bootstrapActive
    ? "Opsi is connecting and verifying the server."
    : newDeployment
    ? "This run reached its bounded retry limit."
    : approvalBlockedByReview
    ? `Review required: "${unreviewedAppName || "application"}" requires runtime configuration or confirmation.`
    : saveDraftFirst
    ? "Save the edited draft before approving the immutable deployment plan."
    : "This is the only action needed for the current state.";
  return <div className="sticky bottom-4 z-10 flex items-center justify-between gap-4 border border-outline-variant/40 bg-surface-container-high p-4 shadow-lg"><p className="text-sm text-on-surface-variant">{statusMessage}</p><Button disabled={disabled || working} onClick={() => saveDraftFirst ? onSaveDraft() : action ? onAction(action, body) : newDeployment ? onNew() : onConnect()} ref={!action && !newDeployment && !saveDraftFirst ? connectTrigger : undefined} size="lg" variant={action === "cancel" ? "danger" : "primary"}>{working ? "Working…" : label}</Button></div>;
}
function DeploymentResult({ canMutate, client, plan, projectID, result }: { canMutate: boolean; client: LocalClient; plan: DeploymentPlan; projectID: string; result: DeploymentRunResult }) { const automatic = plan.target.public_routes === "automatic"; return <section aria-labelledby="deployment-result-title" className="border border-status-ready/40 bg-state-live-bg p-4 sm:p-6"><p className="text-xs font-medium uppercase tracking-wider text-status-ready">Verified result</p><h2 className="mt-2 text-xl font-semibold" id="deployment-result-title">Repository is running</h2>{result.public_url && <a className="mt-3 inline-flex min-h-11 items-center gap-2 text-primary underline underline-offset-4" href={result.public_url} rel="noreferrer" target="_blank"><Icon name="open_in_new" />{result.public_url}</a>}<ul className="mt-4 grid gap-2 sm:grid-cols-2">{result.applications.map((application) => <li className="border border-status-ready/30 bg-surface-container p-3 text-sm" key={application.service_id}><p className="font-medium">{application.service_key}</p><p className="mt-1 text-on-surface-variant">{application.deployment_status} · {application.available_replicas || 0} replica(s) ready</p><p className={application.digest_matches_image_id ? "mt-1 text-status-ready" : "mt-1 text-error"}>{application.digest_matches_image_id ? "K3s imageID matches the immutable build digest" : "Image digest evidence does not match"}</p>{application.build_log_url && <a className="mt-2 inline-flex min-h-10 items-center text-primary underline" href={application.build_log_url} rel="noreferrer" target="_blank">Raw build log</a>}</li>)}</ul>{automatic && <PublicEndpoints endpoints={result.public_endpoints || []} />}{!automatic && <PublicRoute canMutate={canMutate} client={client} plan={plan} projectID={projectID} result={result} />}{result.verifications.length > 0 && <ul className="mt-4 grid gap-2 sm:grid-cols-2">{result.verifications.map((verification) => <li className="border border-status-ready/30 bg-surface-container p-3 text-sm" key={verification.id}><p className="font-medium">{verification.dependency_logical_name}</p><p className="mt-1 text-on-surface-variant">{verification.overall_status} · {verification.connection.protocol || verification.provider_health.provider_kind} {verification.connection.latency_ms ? `· ${verification.connection.latency_ms} ms` : ""}</p></li>)}</ul>}</section>; }

function PublicEndpoints({ endpoints }: { endpoints: NonNullable<DeploymentRunResult["public_endpoints"]> }) { if (endpoints.length === 0) return <p className="mt-4 text-sm text-on-surface-variant" role="status">Public endpoints are being prepared.</p>; return <section aria-labelledby="public-endpoints-title" className="mt-4 border border-outline-variant/30 bg-surface-container p-4"><div><p className="text-xs font-medium uppercase tracking-wider text-secondary">Public endpoints</p><h3 className="mt-1 text-base font-semibold" id="public-endpoints-title">HTTPS routes</h3></div><ul className="mt-3 grid gap-2 sm:grid-cols-2" aria-live="polite">{endpoints.map((endpoint) => { const ready = endpoint.status === "ready"; const label = endpoint.status === "manual_preserved" ? "Manual route preserved" : endpoint.status === "ready" ? "Ready" : endpoint.status === "failed" ? "Failed" : "Publishing"; return <li className="border border-outline-variant/30 bg-surface-container-low p-3 text-sm" key={endpoint.service_id}><div className="flex items-start justify-between gap-3"><div><p className="font-medium">{endpoint.service_key} · port {endpoint.port}</p><p className="mt-1 break-all font-mono text-xs text-on-surface-variant">{endpoint.url}</p></div><span className={endpoint.status === "failed" ? "text-error" : endpoint.status === "ready" ? "text-status-ready" : "text-on-surface-variant"}>{label}</span></div>{ready || endpoint.status === "manual_preserved" ? <a className="mt-3 inline-flex min-h-10 items-center gap-2 text-primary underline" href={endpoint.url} rel="noreferrer" target="_blank"><Icon name="open_in_new" />Open HTTPS URL</a> : endpoint.message ? <p className="mt-2 text-error">{endpoint.message}</p> : <p className="mt-2 text-on-surface-variant">Cloudflare route is applying. This page updates automatically.</p>}</li>; })}</ul></section>; }

function TechnicalDetails({ events, result, run }: { events: DeploymentRunEvent[]; result: DeploymentRunResult | null; run: DeploymentRun }) { return <details className="border border-outline-variant/30 bg-surface-container-low"><summary className="cursor-pointer px-4 py-3 text-sm font-medium">Technical details</summary><div className="space-y-4 border-t border-outline-variant/30 p-4 text-xs"><dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-2"><dt className="text-on-surface-variant">Run ID</dt><dd className="break-all font-mono">{run.id}</dd><dt className="text-on-surface-variant">Source SHA</dt><dd className="break-all font-mono">{run.plan.source.commit_sha}</dd><dt className="text-on-surface-variant">Plan hash</dt><dd className="break-all font-mono">{run.plan.hash}</dd><dt className="text-on-surface-variant">Preflight hash</dt><dd className="break-all font-mono">{run.preflight_hash || "Not available"}</dd><dt className="text-on-surface-variant">Build records</dt><dd className="break-all font-mono">{checkpointIDs(run, "build_record").join(", ") || "Not available"}</dd><dt className="text-on-surface-variant">Deployments</dt><dd className="break-all font-mono">{checkpointIDs(run, "deployment_job").join(", ") || "Not available"}</dd><dt className="text-on-surface-variant">Image digests</dt><dd className="break-all font-mono">{result?.applications.map((item) => item.build_digest).join(", ") || "Not available"}</dd></dl>{result?.capacity.map((capacity) => <dl className="grid grid-cols-2 gap-2 border border-outline-variant/30 p-3 sm:grid-cols-4" key={capacity.runtime_id}><CapacityFact label="Requested" value={`${capacity.requested_cpu_millicores}m · ${formatBytes(capacity.requested_memory_bytes)}`} /><CapacityFact label="Assigned" value={`${capacity.assigned_cpu_millicores}m · ${formatBytes(capacity.assigned_memory_bytes)}`} /><CapacityFact label="Reserved" value={`${capacity.reserved_cpu_millicores}m · ${formatBytes(capacity.reserved_memory_bytes)}`} /><CapacityFact label="Available" value={`${capacity.available_cpu_millicores ?? 0}m · ${formatBytes(capacity.available_memory_bytes || 0)}`} /></dl>)}<pre className="max-h-64 overflow-auto bg-surface-container-lowest p-3 text-on-surface-variant">{events.map((event) => `${event.created_at} ${event.level.toUpperCase()} ${event.message}`).join("\n")}</pre></div></details>; }

function CapacityFact({ label, value }: { label: string; value: string }) { return <div><dt className="text-on-surface-variant">{label}</dt><dd className="mt-1 font-mono">{value}</dd></div>; }
function formatBytes(bytes: number) { return bytes ? `${Math.round(bytes / 1024 / 1024)} MiB` : "0 MiB"; }

function checkpointIDs(run: DeploymentRun, kind: string) { return (run.authority_refs.checkpoints || []).filter((checkpoint) => checkpoint.kind === kind).map((checkpoint) => checkpoint.id); }
